# Generating visual assets

## Demo GIF (doc/demo.gif)

Record the agent-swarm demo using [VHS](https://github.com/charmbracelet/vhs):

```bash
brew install vhs        # macOS
make build              # build tailbus binaries
vhs demo.tape           # generates doc/demo.gif
```

The tape file is at the repo root: `demo.tape`.

Alternative: use `asciinema` or screen-record `./run.sh demo` in `examples/agent-swarm/`.

## Web UI screenshot (doc/web-ui.png)

1. Start the Docker demo: `docker compose up --build`
2. Open http://localhost:8080
3. Send a test message to see the chat interface populated
4. Screenshot the browser window (720px wide recommended)
5. Save as `doc/web-ui.png`
