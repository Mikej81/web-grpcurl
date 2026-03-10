# web-grpcurl

A web-based gRPC client built on top of the [grpcurl](https://github.com/fullstorydev/grpcurl) library by FullStory. It provides a browser UI for interacting with gRPC servers -- service discovery, method browsing, request editing, and RPC invocation -- without needing a terminal.

## Features

- **Service discovery** via gRPC server reflection
- **Proto file upload** for servers that do not support reflection (`.proto` and `.protoset` files)
- **Method browsing** with an expandable service tree
- **Request editing** with auto-generated JSON templates from protobuf schemas
- **RPC invocation** with syntax-highlighted JSON responses
- **Custom metadata headers** on outgoing requests
- **Plaintext and TLS** connections
- **Connection pooling** with automatic stale-connection eviction
- **Single binary** with the frontend embedded (no external files needed)

## Installation

### From Source

Requires [Go 1.24+](https://golang.org/doc/install).

```shell
go install github.com/fullstorydev/grpcurl/cmd/web-grpcurl@latest
```

Or clone and build locally:

```shell
git clone https://github.com/Mikej81/web-grpcurl.git
cd web-grpcurl
go build -o web-grpcurl ./cmd/web-grpcurl
```

### Docker

```shell
# Build
docker build -f Dockerfile.web -t web-grpcurl .

# Run
docker run -p 8080:8080 web-grpcurl

# With custom listen address
docker run -p 9090:9090 web-grpcurl -listen :9090
```

## Usage

```shell
# Start on default port 8080
web-grpcurl

# Start on a custom port
web-grpcurl -listen :9090
```

Then open your browser to `http://localhost:8080`.

### Connecting to a gRPC Server

1. Enter the server address (e.g. `localhost:50051`) in the address bar.
2. Toggle **Plaintext** if the server does not use TLS.
3. Click **Connect**.

If the server supports reflection, services and methods will populate automatically.

### Uploading Proto Files

For servers without reflection support:

1. Connect to the server first.
2. Use the proto upload area in the sidebar to drag-and-drop or select `.proto` or `.protoset` files.
3. Services defined in the uploaded files will appear in the sidebar.

### Invoking RPCs

1. Select a method from the service tree.
2. Edit the JSON request body (a template is generated automatically).
3. Optionally add metadata headers.
4. Click **Send** (or press `Ctrl+Enter`).

## CLI grpcurl

This repository is a fork of [fullstorydev/grpcurl](https://github.com/fullstorydev/grpcurl). The original CLI tool is still available under `cmd/grpcurl` and works exactly as documented upstream.

```shell
# Install the CLI
go install github.com/fullstorydev/grpcurl/cmd/grpcurl@latest

# Example usage
grpcurl -plaintext localhost:50051 list
```

See the [upstream grpcurl documentation](https://github.com/fullstorydev/grpcurl#readme) for full CLI usage details.

## Credits

This project is built on top of [grpcurl](https://github.com/fullstorydev/grpcurl) by [FullStory](https://www.fullstory.com/), which provides the core gRPC introspection and invocation library. The original grpcurl is licensed under the MIT License. See [LICENSE](LICENSE) for details.

Key upstream dependencies:
- [grpcurl](https://github.com/fullstorydev/grpcurl) -- gRPC CLI and library (MIT License)
- [protoreflect](https://github.com/jhump/protoreflect) -- protobuf reflection library
- [grpc-go](https://google.golang.org/grpc) -- Go gRPC implementation

## License

MIT License -- see [LICENSE](LICENSE) for the full text. The original copyright belongs to FullStory, Inc.
