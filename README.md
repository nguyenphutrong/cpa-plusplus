# cpa-plusplus

`cpa-plusplus` is the `nguyenphutrong/cpa-plusplus` fork of
[router-for-me/CLIProxyAPI](https://github.com/router-for-me/CLIProxyAPI). It keeps upstream API
compatibility while publishing its own release artifacts and Docker image.

- Repository: <https://github.com/nguyenphutrong/cpa-plusplus>
- Releases: <https://github.com/nguyenphutrong/cpa-plusplus/releases>
- Docker image: `ghcr.io/nguyenphutrong/cpa-plusplus`
- Go module: `github.com/router-for-me/CLIProxyAPI/v7`

## Features

- OpenAI/Gemini/Claude/Codex/Grok-compatible API surfaces for CLI and SDK clients.
- OAuth-backed provider accounts with multi-account round-robin routing.
- Streaming, non-streaming, WebSocket, tools/function calling, and multimodal request support.
- Amp CLI/IDE provider routes under `/api/provider/{provider}/...`.
- File storage by default, with optional Postgres, git, and object-store backends.
- Embeddable Go SDK under `sdk/`.

## Quick Start

```bash
cp config.example.yaml config.yaml
go build -o cpa-plusplus ./cmd/server
./cpa-plusplus --config config.yaml
```

The default API port is `8317`. `.env` is loaded from the working directory when present.

## Docker

```bash
docker run --rm \
  -p 8317:8317 \
  -v "$PWD/config.yaml:/cpa-plusplus/config.yaml" \
  -v "$PWD/auths:/root/.cli-proxy-api" \
  -v "$PWD/logs:/cpa-plusplus/logs" \
  ghcr.io/nguyenphutrong/cpa-plusplus:latest
```

Compose is also available:

```bash
docker compose up -d
```

## Configuration

Start from `config.example.yaml`.

Key fields:

- `port`: main HTTP API port.
- `api-keys`: client-facing access keys.
- `auth-dir`: local credential directory.
- `remote-management`: management API settings.
- Provider sections such as `codex-api-key`, `claude-api-key`, `openai-compatibility`,
  `vertex-api-key`, and `ampcode`.

## SDK

```bash
go get github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy
```

Docs:

- [SDK usage](docs/sdk-usage.md)
- [SDK advanced topics](docs/sdk-advanced.md)
- [SDK access control](docs/sdk-access.md)
- [SDK watcher integration](docs/sdk-watcher.md)

## Releases

Tags matching `v*` publish GitHub release artifacts through GoReleaser and multi-architecture
container images to GitHub Container Registry.

GoReleaser builds the `cpa-plusplus` binary for Linux, Windows, macOS, and FreeBSD on `amd64` and
`arm64`.

## Development

```bash
gofmt -w .
go build -o test-output ./cmd/server && rm test-output
go test ./...
```

## Compatibility

This fork follows upstream CLIProxyAPI architecture and config concepts unless a fork release note
states otherwise. Use upstream docs for behavior that has not diverged:

<https://help.router-for.me/>

## License

MIT. See [LICENSE](LICENSE).
