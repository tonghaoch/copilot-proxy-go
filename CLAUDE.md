# CLAUDE.md — copilot-proxy-go

## Project Overview

A Go proxy server that turns a GitHub Copilot subscription into OpenAI and Anthropic-compatible API endpoints. Allows tools like Claude Code, Cursor, and any OpenAI/Anthropic client to use Copilot as the backend LLM provider. Go rewrite of [ericc-ch/copilot-api](https://github.com/ericc-ch/copilot-api).

## Build & Run

```bash
# Build
go build -o copilot-proxy-go .

# Authenticate (GitHub OAuth device-code flow)
./copilot-proxy-go auth

# Start server (default port 4141)
./copilot-proxy-go start

# Start with Claude Code interactive setup
./copilot-proxy-go start --claude-code

# Check Copilot usage quota
./copilot-proxy-go check-usage

# Debug info
./copilot-proxy-go debug [--json]
```

Go version: 1.25 (per go.mod)

## Testing

```bash
go test -v ./...
```

Note: No test files exist yet. CI runs build + test.

## Project Structure

```
main.go                              # Entry point, cobra CLI commands (start/auth/check-usage/debug)
internal/
  api/
    config.go                        # API constants, headers, VS Code version fetcher
    errors.go                        # HTTP error types and JSON error responses
  auth/auth.go                       # GitHub OAuth device-code flow, token management, auto-refresh
  config/config.go                   # JSON config file (per-model settings, API keys, defaults)
  handler/
    messages.go                      # POST /v1/messages — core Anthropic-compatible handler (3-tier routing)
    messages_native.go               # Native Messages API backend
    messages_utils.go                # SSE helpers, model checks, vision detection, CLAUDE.md extraction
    chat_completions.go              # POST /chat/completions (OpenAI passthrough)
    responses.go                     # POST /responses (Responses API passthrough)
    translate_chat.go                # Anthropic <-> Chat Completions translation
    translate_chat_stream.go         # Streaming: Chat Completions -> Anthropic SSE
    translate_responses.go           # Anthropic <-> Responses API translation
    translate_responses_stream.go    # Streaming: Responses API -> Anthropic SSE
    responses_stream_sync.go         # Stream ID sync for Responses passthrough
    types_anthropic.go               # Anthropic request/response/stream types
    types_openai.go                  # OpenAI Chat Completions types
    types_responses.go               # OpenAI Responses API types
    quota.go                         # Compact/warmup detection, small model routing
    count_tokens.go                  # POST /v1/messages/count_tokens (estimation)
    models.go                        # GET /models
    health.go, token.go, usage.go    # Utility endpoints
    stats.go                         # GET /api/stats — aggregated metrics JSON endpoint
    dashboard.go, dashboard.html     # Embedded HTML dashboard (go:embed)
    embeddings.go                    # POST /embeddings passthrough
  logger/logger.go                   # Per-handler file logging with daily rotation (7-day retention)
  middleware/
    auth.go                          # API key auth (x-api-key / Bearer)
    ratelimit.go                     # Rate limiting (reject or wait mode)
    approval.go                      # Manual CLI approval per request
  server/server.go                   # chi router setup, all routes, middleware chain
  service/copilot.go                 # Copilot API proxy functions (all backend HTTP calls)
  shell/
    shell.go                         # Shell detection, export script generation
    clipboard.go                     # Cross-platform clipboard
  state/
    state.go                         # Thread-safe global state singleton (tokens, models, paths)
    metrics.go                       # In-memory metrics store (ring buffer, aggregates, session snapshots)
pages/index.html                     # Standalone usage dashboard
```

## Architecture

### Request Flow (Messages endpoint — most complex)

1. Parse Anthropic request → apply quota optimizations (compact/warmup → small model)
2. Detect subagent markers, merge tool result blocks
3. Update session snapshot (CLAUDE.md extraction, tools, thinking config)
4. Route to best backend based on model capabilities:
   - **Native Messages API** (`/v1/messages`) — passthrough with thinking/vision adjustments
   - **Responses API** (`/responses`) — translate Anthropic ↔ Responses format
   - **Chat Completions** (`/chat/completions`) — translate Anthropic ↔ Chat Completions format
5. Handle streaming (SSE event translation) or non-streaming (JSON translation)
6. Record request metrics (tokens, latency, backend, model) to `state.Metrics`

### Routes (chi router)

```
GET  /                              → Health
GET  /token                         → Token
GET  /usage                         → Usage
GET  /dashboard                     → Dashboard (embedded HTML)
GET  /api/stats                     → Stats (aggregated metrics JSON)
GET  /models, /v1/models            → Models
POST /chat/completions, /v1/chat/completions → ChatCompletions
POST /v1/messages                   → Messages (Anthropic-compatible)
POST /v1/messages/count_tokens      → CountTokens
POST /responses, /v1/responses      → Responses
POST /embeddings, /v1/embeddings    → Embeddings
```

### Middleware Chain

RealIP → RequestID → requestLogger → CORS → Recoverer → Auth → [RateLimit] → [ManualApproval]

## Key Dependencies

- `github.com/go-chi/chi/v5` — HTTP router
- `github.com/go-chi/cors` — CORS middleware
- `github.com/google/uuid` — UUID generation
- `github.com/spf13/cobra` — CLI framework

## Configuration

### CLI Flags (start command)

| Flag | Default | Description |
|------|---------|-------------|
| `-p, --port` | 4141 | Listen port |
| `-g, --github-token` | — | GitHub token (skips device-code flow) |
| `-a, --account-type` | "individual" | individual/business/enterprise |
| `-c, --claude-code` | false | Interactive Claude Code model selection |
| `-v, --verbose` | false | Debug logging |
| `-r, --rate-limit` | 0 | Min seconds between requests |
| `-w, --wait` | false | Wait instead of rejecting rate-limited requests |
| `--manual` | false | Require CLI approval per request |
| `--proxy-env` | false | Use HTTP proxy from env vars |
| `--show-token` | false | Print tokens to console |

### Config File (JSON)

Location: `~/.local/share/copilot-proxy-go/config.json` (Linux)

Fields: `auth.apiKeys`, `smallModel` (default: "gpt-5-mini"), `compactUseSmallModel`, `useFunctionApplyPatch`, `modelReasoningEfforts`, `extraPrompts`

### Token Storage

GitHub token: `~/.local/share/copilot-proxy-go/github_token`

## Key Patterns

- **In-memory metrics**: `state.Metrics` singleton with ring buffer (last 200 requests), incremental aggregates, and session snapshot — all behind `sync.RWMutex`; exposed via `GET /api/stats`
- **Session intelligence**: Extracts CLAUDE.md files, tool inventory, thinking config, beta features, and subagent info from each Messages request system prompt
- **Thread-safe global state**: `state.Global` singleton with `sync.RWMutex`
- **Token auto-refresh**: Background goroutine refreshes Copilot token 60s before expiry
- **Three-tier backend routing**: Native Messages > Responses > Chat Completions (based on model's `supported_endpoints`)
- **Format translation**: Full bidirectional Anthropic ↔ OpenAI translation including streaming SSE
- **Thinking/reasoning blocks**: Maps between Claude extended thinking and OpenAI reasoning formats (with signatures)
- **Quota optimization**: Detects compact/warmup requests → routes to cheaper small model
- **Tool result merging**: Merges standalone text blocks into adjacent tool_result blocks
- **API masquerading**: Mimics VS Code Copilot Chat extension via specific headers
- **Embedded assets**: Dashboard HTML via `go:embed`
- **Dual logging**: `slog` for console + per-handler file logging with rotation
- **Stream ID sync**: Fixes Copilot ID inconsistencies that crash `@ai-sdk/openai`
- **Infinite whitespace detection**: Aborts streams with >20 consecutive whitespace chars (Copilot bug workaround)
