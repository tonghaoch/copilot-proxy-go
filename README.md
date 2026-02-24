# copilot-proxy-go

[![Go](https://github.com/tonghaoch/copilot-proxy-go/actions/workflows/go.yml/badge.svg?branch=master)](https://github.com/tonghaoch/copilot-proxy-go/actions/workflows/go.yml)

Turn your **GitHub Copilot** subscription into a fully compatible **OpenAI** and **Anthropic** API server.

A single Go binary that authenticates with GitHub Copilot and exposes standard API endpoints — use it with Claude Code, OpenCode, Cursor, or any tool that speaks OpenAI/Anthropic.

> Go rewrite of [ericc-ch/copilot-api](https://github.com/ericc-ch/copilot-api).

## Features

- **Multi-API support** — Chat Completions, Messages (Anthropic), Responses, Embeddings
- **Automatic translation** — routes Anthropic requests to the best available backend (native Messages API → Responses API → Chat Completions)
- **Extended thinking** — full support for Claude thinking/reasoning blocks with interleaved thinking protocol
- **Streaming** — SSE streaming with proper event translation across all API formats
- **Quota optimization** — auto-routes compact/warmup requests to smaller models to save premium quota
- **Claude Code integration** — one-command setup with `--claude-code` flag
- **Token management** — GitHub OAuth device-code flow with automatic Copilot token refresh
## Quick Start

### 1. Build

```bash
go build -o copilot-proxy-go .
```

### 2. Authenticate

```bash
./copilot-proxy-go auth
```

This opens a GitHub device-code flow in your browser. The token is saved to `~/.local/share/copilot-api/github_token`.

### 3. Start the server

```bash
./copilot-proxy-go start
```

The proxy is now running on `http://localhost:4141`.

## Usage with Claude Code

```bash
./copilot-proxy-go start --claude-code
```

This interactively selects models and generates the environment variables for Claude Code. Or set them manually:

```bash
export ANTHROPIC_BASE_URL=http://localhost:4141
export ANTHROPIC_AUTH_TOKEN=copilot-proxy
export ANTHROPIC_MODEL=claude-sonnet-4
export ANTHROPIC_SMALL_FAST_MODEL=gpt-5-mini
claude
```

## API Endpoints

| Endpoint | Method | Description |
|----------|--------|-------------|
| `/v1/messages` | POST | Anthropic Messages API |
| `/v1/messages/count_tokens` | POST | Token counting |
| `/chat/completions` | POST | OpenAI Chat Completions |
| `/v1/chat/completions` | POST | OpenAI Chat Completions |
| `/responses` | POST | OpenAI Responses API |
| `/v1/responses` | POST | OpenAI Responses API |
| `/embeddings` | POST | Embeddings |
| `/models` | GET | List available models |
| `/v1/models` | GET | List available models |
| `/dashboard` | GET | Usage dashboard (web UI) |

## CLI Reference

### `start` — Run the proxy server

```
copilot-proxy-go start [flags]

Flags:
  -p, --port int              port to listen on (default 4141)
  -g, --github-token string   GitHub OAuth token (skips device code flow)
  -a, --account-type string   individual, business, or enterprise (default "individual")
  -c, --claude-code           interactive model selection for Claude Code
  -v, --verbose               enable verbose/debug logging
  -r, --rate-limit int        minimum seconds between requests (0 = disabled)
  -w, --wait                  wait instead of rejecting on rate limit
      --manual                require manual CLI approval for each request
      --proxy-env             enable HTTP proxy from environment variables
      --show-token            print tokens to console
```

### `auth` — Authenticate with GitHub

```
copilot-proxy-go auth [flags]

Flags:
      --force        force re-authentication
      --show-token   print token to console
```

### `check-usage` — Show Copilot quota

```
copilot-proxy-go check-usage
```

### `debug` — Print diagnostics

```
copilot-proxy-go debug [--json]
```

## Configuration

Config file location (run `copilot-proxy-go debug` to see yours):
- **macOS:** `~/Library/Application Support/copilot-proxy-go/config.json`
- **Linux:** `~/.local/share/copilot-proxy-go/config.json`
- **Windows:** `%LOCALAPPDATA%\copilot-proxy-go\config.json`

```jsonc
{
  "auth": {
    "apiKeys": []              // API keys for request authentication (empty = no auth)
  },
  "smallModel": "gpt-5-mini", // Model used for compact/warmup requests
  "compactUseSmallModel": true,
  "useFunctionApplyPatch": true,
  "modelReasoningEfforts": {
    "gpt-5-mini": "low"       // Per-model reasoning effort override
  },
  "extraPrompts": {
    "gpt-5-mini": "..."       // Per-model system prompt additions
  }
}
```

## How It Works

```
Client (Claude Code / OpenCode / etc.)
  │
  ▼
copilot-proxy-go (localhost:4141)
  │
  ├─ /v1/messages ──► Translates Anthropic → best available backend:
  │                     1. Native Messages API (if model supports /v1/messages)
  │                     2. Responses API (if model supports /responses)
  │                     3. Chat Completions (fallback)
  │
  ├─ /chat/completions ──► Direct passthrough to Copilot
  │
  ├─ /responses ──► Direct passthrough with tool conversion
  │
  └─ /embeddings ──► Direct passthrough
  │
  ▼
GitHub Copilot API
```

## License

MIT
