package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"time"

	agentpb "github.com/alexanderfrey/tailbus/api/agentpb"
	messagepb "github.com/alexanderfrey/tailbus/api/messagepb"
)

type stringSliceFlag []string

func (s *stringSliceFlag) String() string {
	return strings.Join(*s, ",")
}

func (s *stringSliceFlag) Set(value string) error {
	*s = append(*s, value)
	return nil
}

func loadAttachManifest(path string) (*manifestCmd, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var manifest manifestCmd
	if err := json.Unmarshal(data, &manifest); err != nil {
		return nil, fmt.Errorf("parse manifest %s: %w", path, err)
	}
	return &manifest, nil
}

func hasInlineManifestFields(description, version string, tags, capabilities, domains, inputTypes, outputTypes stringSliceFlag) bool {
	return description != "" ||
		version != "" ||
		len(tags) > 0 ||
		len(capabilities) > 0 ||
		len(domains) > 0 ||
		len(inputTypes) > 0 ||
		len(outputTypes) > 0
}

func buildInlineManifest(
	description, version string,
	tags, capabilities, domains, inputTypes, outputTypes stringSliceFlag,
) *manifestCmd {
	if !hasInlineManifestFields(description, version, tags, capabilities, domains, inputTypes, outputTypes) {
		return nil
	}
	return &manifestCmd{
		Description:  description,
		Version:      version,
		Tags:         append([]string(nil), tags...),
		Capabilities: append([]string(nil), capabilities...),
		Domains:      append([]string(nil), domains...),
		InputTypes:   append([]string(nil), inputTypes...),
		OutputTypes:  append([]string(nil), outputTypes...),
	}
}

func buildAttachInitialCommands(handle string, manifest *manifestCmd, joinRooms []string) []inboundCmd {
	cmds := []inboundCmd{{
		Type:      "register",
		RequestID: "attach-register",
		Handle:    handle,
		Manifest:  manifest,
	}}
	for idx, roomID := range joinRooms {
		cmds = append(cmds, inboundCmd{
			Type:      "join_room",
			RequestID: fmt.Sprintf("attach-join-%d", idx+1),
			RoomID:    roomID,
		})
	}
	return cmds
}

func runAttach(client bridgeClient, logger *slog.Logger, args []string) error {
	attachFlags := flag.NewFlagSet("attach", flag.ContinueOnError)
	attachFlags.SetOutput(os.Stderr)

	handle := attachFlags.String("handle", "", "agent handle to register")
	manifestPath := attachFlags.String("manifest", "", "path to manifest JSON file")
	description := attachFlags.String("description", "", "service description")
	version := attachFlags.String("version", "", "service version")
	cwd := attachFlags.String("cwd", "", "child process working directory")
	execMode := attachFlags.Bool("exec", false, "exec mode: run command per request, payload becomes argument")
	execTimeout := attachFlags.Duration("exec-timeout", 30*time.Second, "timeout for each exec invocation")

	var tags stringSliceFlag
	var capabilities stringSliceFlag
	var domains stringSliceFlag
	var inputTypes stringSliceFlag
	var outputTypes stringSliceFlag
	var joinRooms stringSliceFlag
	var envVars stringSliceFlag

	attachFlags.Var(&tags, "tag", "service tag (repeatable)")
	attachFlags.Var(&capabilities, "capability", "service capability (repeatable)")
	attachFlags.Var(&domains, "domain", "service domain (repeatable)")
	attachFlags.Var(&inputTypes, "input-type", "supported input content type (repeatable)")
	attachFlags.Var(&outputTypes, "output-type", "supported output content type (repeatable)")
	attachFlags.Var(&joinRooms, "join-room", "existing room ID to join after register (repeatable)")
	attachFlags.Var(&envVars, "env", "extra child environment entry KEY=VALUE (repeatable)")

	if err := attachFlags.Parse(args); err != nil {
		return err
	}
	if *handle == "" {
		return fmt.Errorf("attach requires --handle")
	}
	if attachFlags.NArg() == 0 {
		return fmt.Errorf("attach requires a child command after flags")
	}

	inlineManifest := buildInlineManifest(*description, *version, tags, capabilities, domains, inputTypes, outputTypes)
	if *manifestPath != "" && inlineManifest != nil {
		return fmt.Errorf("--manifest cannot be combined with inline manifest flags")
	}

	var manifest *manifestCmd
	if *manifestPath != "" {
		loaded, err := loadAttachManifest(*manifestPath)
		if err != nil {
			return err
		}
		manifest = loaded
	} else {
		manifest = inlineManifest
	}

	for _, envVar := range envVars {
		if !strings.Contains(envVar, "=") {
			return fmt.Errorf("invalid --env value %q; expected KEY=VALUE", envVar)
		}
	}

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()

	childArgs := attachFlags.Args()

	if *execMode {
		return runExecAttach(ctx, client, logger, *handle, manifest, joinRooms, childArgs, *cwd, envVars, *execTimeout)
	}

	cmd := exec.CommandContext(ctx, childArgs[0], childArgs[1:]...)
	cmd.Stdout = nil
	cmd.Stderr = os.Stderr
	if *cwd != "" {
		cmd.Dir = *cwd
	}
	cmd.Env = append(os.Environ(), envVars...)

	childStdout, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("child stdout pipe: %w", err)
	}
	childStdin, err := cmd.StdinPipe()
	if err != nil {
		return fmt.Errorf("child stdin pipe: %w", err)
	}

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start child: %w", err)
	}

	initial := buildAttachInitialCommands(*handle, manifest, joinRooms)
	bridgeErrCh := make(chan error, 1)
	childErrCh := make(chan error, 1)

	go func() {
		bridgeErrCh <- runBridge(ctx, client, logger, childStdout, childStdin, initial)
	}()
	go func() {
		childErrCh <- cmd.Wait()
	}()

	var bridgeErr error
	var childErr error
	select {
	case bridgeErr = <-bridgeErrCh:
		cancel()
		_ = childStdin.Close()
		childErr = <-childErrCh
	case childErr = <-childErrCh:
		cancel()
		_ = childStdin.Close()
		bridgeErr = <-bridgeErrCh
	}

	if bridgeErr != nil {
		return bridgeErr
	}
	if childErr != nil {
		return childErr
	}
	return nil
}

// runExecAttach registers the handle, subscribes for messages, and for each
// incoming session_open spawns the command with the payload as an argument.
// The command's stdout becomes the session response.
//
// If any argument is "{}", it is replaced with the payload. Otherwise the
// payload is appended as the last argument.
func runExecAttach(
	ctx context.Context,
	client bridgeClient,
	logger *slog.Logger,
	handle string,
	manifest *manifestCmd,
	joinRooms []string,
	baseArgs []string,
	cwdPath string,
	envVars stringSliceFlag,
	timeout time.Duration,
) error {
	// Register.
	regReq := buildRegisterRequest(inboundCmd{
		Type:     "register",
		Handle:   handle,
		Manifest: manifest,
	})
	resp, err := client.Register(ctx, regReq)
	if err != nil {
		return fmt.Errorf("register: %w", err)
	}
	if !resp.Ok {
		return fmt.Errorf("register: %s", resp.Error)
	}
	logger.Info("registered", "handle", handle, "mode", "exec")

	// Join rooms.
	for _, roomID := range joinRooms {
		if _, err := client.JoinRoom(ctx, &agentpb.JoinRoomRequest{RoomId: roomID, Handle: handle}); err != nil {
			logger.Warn("join room failed", "room", roomID, "error", err)
		}
	}

	// Subscribe.
	stream, err := client.Subscribe(ctx, &agentpb.SubscribeRequest{Handle: handle})
	if err != nil {
		return fmt.Errorf("subscribe: %w", err)
	}

	logger.Info("listening", "handle", handle)

	for {
		msg, err := stream.Recv()
		if err != nil {
			if ctx.Err() != nil || err == io.EOF {
				return nil
			}
			return fmt.Errorf("subscribe recv: %w", err)
		}

		env := msg.Envelope
		if env == nil || env.Type != messagepb.EnvelopeType_ENVELOPE_TYPE_SESSION_OPEN {
			continue
		}

		payload := string(env.Payload)
		output, execErr := execCommand(ctx, baseArgs, payload, cwdPath, envVars, timeout)

		responsePayload := output
		if execErr != nil {
			if len(output) > 0 {
				responsePayload = fmt.Sprintf("error: %s\n%s", execErr, output)
			} else {
				responsePayload = fmt.Sprintf("error: %s", execErr)
			}
		}

		if _, err := client.ResolveSession(ctx, &agentpb.ResolveSessionRequest{
			SessionId:   env.SessionId,
			FromHandle:  handle,
			Payload:     []byte(responsePayload),
			ContentType: "text/plain",
		}); err != nil {
			logger.Error("resolve failed", "session", env.SessionId, "error", err)
		} else {
			logger.Info("handled", "from", env.FromHandle, "bytes", len(output))
		}
	}
}

// execCommand runs the base command with payload substituted. If any arg is
// "{}", it is replaced with payload; otherwise payload is appended as the
// last argument. Environment variables TAILBUS_PAYLOAD, TAILBUS_SESSION,
// and TAILBUS_FROM are set for the child process.
func execCommand(ctx context.Context, baseArgs []string, payload, cwdPath string, envVars []string, timeout time.Duration) (string, error) {
	// Build args: replace {} or append payload.
	args := make([]string, len(baseArgs)-1)
	copy(args, baseArgs[1:])
	replaced := false
	for i, arg := range args {
		if arg == "{}" {
			args[i] = payload
			replaced = true
		}
	}
	if !replaced && payload != "" {
		args = append(args, payload)
	}

	execCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	cmd := exec.CommandContext(execCtx, baseArgs[0], args...)
	if cwdPath != "" {
		cmd.Dir = cwdPath
	}
	cmd.Env = append(os.Environ(), envVars...)
	cmd.Env = append(cmd.Env, "TAILBUS_PAYLOAD="+payload)

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	output := stdout.String()
	if output == "" && stderr.Len() > 0 {
		output = stderr.String()
	}
	return output, err
}
