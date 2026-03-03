.PHONY: proto build test test-all clean

GOBIN := $(shell go env GOPATH)/bin
export PATH := $(GOBIN):/opt/homebrew/bin:$(PATH)

proto:
	protoc \
		--proto_path=proto \
		--go_out=. --go_opt=module=github.com/alexanderfrey/tailbus \
		--go-grpc_out=. --go-grpc_opt=module=github.com/alexanderfrey/tailbus \
		proto/tailbus/v1/messages.proto \
		proto/tailbus/v1/coord.proto \
		proto/tailbus/v1/agent.proto \
		proto/tailbus/v1/transport.proto

build:
	go build -o bin/tailbus-coord ./cmd/tailbus-coord
	go build -o bin/tailbusd ./cmd/tailbusd
	go build -o bin/tailbus ./cmd/tailbus
	go build -o bin/tailbus-relay ./cmd/tailbus-relay

test:
	go test -race ./internal/... -count=1

test-all:
	go test -race ./... -v -count=1

clean:
	rm -rf bin/
	rm -rf api/coordpb/*.go api/agentpb/*.go api/messagepb/*.go api/transportpb/*.go
