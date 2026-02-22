# Copilot Proxy Go — Implementation Plan

> A Go rewrite of [caozhiyuan/copilot-api](https://github.com/caozhiyuan/copilot-api/tree/all).
> Turns GitHub Copilot into an OpenAI / Anthropic compatible API server.

## Status Legend

- ⬜ Not started
- 🔨 In progress
- ✅ Completed
- ⏭️ Skipped

---

## Phase 1 — Core Foundation

> Goal: A working server that can authenticate with GitHub, fetch models, and proxy
> one translation path (Chat Completions passthrough) end-to-end.

| # | Feature | Status | Notes |
|---|---------|--------|-------|
| 1.1 | Project scaffolding (`go.mod`, directory structure, `main.go`) | ✅ | chi + cobra |
| 1.2 | Global state management (tokens, models, config, flags) | ✅ | Singleton with sync.RWMutex |
| 1.3 | File system paths (`~/.local/share/copilot-api/`) | ✅ | `EnsurePaths()` |
| 1.4 | GitHub OAuth device-code flow (client ID, scope `read:user`) | ✅ | |
| 1.5 | Device code polling with interval | ✅ | With slow_down handling |
| 1.6 | GitHub token persistence to disk | ✅ | `0600` permissions |
| 1.7 | Copilot token fetch (`GET /copilot_internal/v2/token`) | ✅ | |
| 1.8 | Copilot token auto-refresh timer | ✅ | Goroutine with `refresh_in - 60s` |
| 1.9 | User identity logging (`GET /user`) | ✅ | |
| 1.10 | Dynamic Copilot base URL per account type | ✅ | individual/business/enterprise |
| 1.11 | VS Code version fetching (AUR scrape + hardcoded fallback) | ✅ | 5s timeout, regex parse |
| 1.12 | Copilot request headers builder (User-Agent, editor-version, etc.) | ✅ | |
| 1.13 | `X-Initiator` header (agent/user) | ✅ | |
| 1.14 | `x-request-id: {uuid}` per request | ✅ | google/uuid |
| 1.15 | HTTP server setup (e.g. `net/http` + router like `chi` or `echo`) | ✅ | go-chi/chi v5 |
| 1.16 | Request logging middleware | ✅ | slog-based |
| 1.17 | CORS middleware | ✅ | go-chi/cors |
| 1.18 | Health check `GET /` → "Server running" | ✅ | |
| 1.19 | `GET /models` + `/v1/models` — model listing | ✅ | |
| 1.20 | Model capabilities parsing & caching at startup | ✅ | |
| 1.21 | `GET /models` service — fetch from Copilot API | ✅ | |
| 1.22 | `POST /chat/completions` + `/v1/chat/completions` — passthrough | ✅ | |
| 1.23 | `max_tokens` auto-fill from model capabilities | ✅ | |
| 1.24 | Agent/user initiator detection (chat completions) | ✅ | Last message role check |
| 1.25 | Non-streaming response passthrough | ✅ | |
| 1.26 | SSE streaming passthrough | ✅ | bufio.Scanner + http.Flusher |
| 1.27 | `HTTPError` type + `forwardError` utility | ✅ | JSON error parsing |
| 1.28 | `GET /token` — expose current Copilot bearer token | ✅ | |
| 1.29 | Basic `start` command (port, github-token, account-type flags only) | ✅ | +verbose, +show-token |

**Milestone**: Can authenticate, list models, and proxy OpenAI Chat Completions requests.

---

## Phase 2 — Full API Translation

> Goal: Support all 3 backend routing paths for the Anthropic Messages endpoint,
> plus the OpenAI Responses and Embeddings endpoints.

### 2A — Anthropic Messages → Chat Completions Backend

| # | Feature | Status | Notes |
|---|---------|--------|-------|
| 2.1 | `POST /v1/messages` route registration | ⬜ | |
| 2.2 | 3-way backend routing logic (messages/responses/chat-completions) | ⬜ | Based on `supported_endpoints` |
| 2.3 | System prompt translation (string or array → system message) | ⬜ | |
| 2.4 | Extra prompt injection from config per model | ⬜ | |
| 2.5 | User message translation (split tool_result into tool role) | ⬜ | |
| 2.6 | Assistant message translation (tool_use → tool_calls, thinking → reasoning) | ⬜ | |
| 2.7 | Image content handling (base64 → data URI) | ⬜ | |
| 2.8 | Tool definition translation (input_schema → parameters) | ⬜ | |
| 2.9 | Tool choice translation (auto/any/tool/none) | ⬜ | |
| 2.10 | Model name normalization (strip version suffixes) | ⬜ | |
| 2.11 | Non-streaming response translation (OpenAI → Anthropic) | ⬜ | |
| 2.12 | Stop reason mapping (stop→end_turn, etc.) | ⬜ | |
| 2.13 | Streaming translation — state machine (SSE chunks → Anthropic events) | ⬜ | Most complex piece |
| 2.14 | Thinking text streaming as thinking blocks | ⬜ | |
| 2.15 | Reasoning opaque streaming with placeholder + signature | ⬜ | |
| 2.16 | Tool call streaming with `input_json_delta` | ⬜ | |
| 2.17 | Multi-tool-call streaming state | ⬜ | |
| 2.18 | Usage / cache token passthrough | ⬜ | |
| 2.19 | Error event translation | ⬜ | |
| 2.20 | Interleaved thinking protocol injection | ⬜ | XML system prompt + system-reminder |
| 2.21 | Thinking budget calculation (clamp min/max) | ⬜ | |
| 2.22 | Thinking block filtering for Claude models | ⬜ | Empty, "Thinking...", `@` in signature |
| 2.23 | Edge cases: content after thinking, reasoning_text during content block | ⬜ | |
| 2.24 | `copilot-vision-request: true` header when images detected | ⬜ | |
| 2.25 | `"Thinking..."` placeholder for opencode compatibility | ⬜ | |
| 2.26 | Cache read token separation (Anthropic billing model) | ⬜ | |

### 2B — Anthropic Messages → Responses API Backend

| # | Feature | Status | Notes |
|---|---------|--------|-------|
| 2.27 | Full message/tool/system → Responses format translation | ⬜ | |
| 2.28 | Temperature forced to 1 for reasoning models | ⬜ | |
| 2.29 | `max_output_tokens` minimum 12800 | ⬜ | |
| 2.30 | Reasoning effort from config | ⬜ | |
| 2.31 | Reasoning config (`include`, `store`, `parallel_tool_calls`) | ⬜ | |
| 2.32 | User ID parsing for `safety_identifier` + `prompt_cache_key` | ⬜ | |
| 2.33 | Codex phase assignment (`commentary`/`final_answer` for gpt-5.3-codex) | ⬜ | |
| 2.34 | Thinking block → reasoning item conversion (signature `@` encoding) | ⬜ | |
| 2.35 | Tool result → `function_call_output` conversion | ⬜ | |
| 2.36 | Image content → `input_image` conversion | ⬜ | |
| 2.37 | Non-streaming Responses → Anthropic translation | ⬜ | |
| 2.38 | Streaming Responses → Anthropic SSE translation | ⬜ | |
| 2.39 | Infinite whitespace detection guard (20 char limit) | ⬜ | |
| 2.40 | Stream completion detection | ⬜ | |
| 2.41 | Function call argument parsing with fallback | ⬜ | |

### 2C — Native Messages API Passthrough

| # | Feature | Status | Notes |
|---|---------|--------|-------|
| 2.42 | Direct forwarding for models supporting `/v1/messages` | ⬜ | |
| 2.43 | Thinking block filtering before forwarding | ⬜ | |
| 2.44 | Adaptive thinking support with effort mapping | ⬜ | |
| 2.45 | `anthropic-beta` header filtering (remove `claude-code-20250219`) | ⬜ | |
| 2.46 | `anthropic-beta` auto-injection for thinking | ⬜ | |
| 2.47 | Vision detection + header | ⬜ | |
| 2.48 | Streaming / non-streaming passthrough | ⬜ | |

### 2D — OpenAI Responses Endpoint (Passthrough)

| # | Feature | Status | Notes |
|---|---------|--------|-------|
| 2.49 | `POST /responses` + `/v1/responses` route | ⬜ | |
| 2.50 | Model support validation (400 if unsupported) | ⬜ | |
| 2.51 | `apply_patch` custom tool → function tool conversion | ⬜ | |
| 2.52 | `web_search` tool removal | ⬜ | |
| 2.53 | Stream ID synchronization (fix `@ai-sdk/openai` crashes) | ⬜ | |
| 2.54 | `service_tier` nullification | ⬜ | |
| 2.55 | Vision detection in Responses payloads | ⬜ | |
| 2.56 | Agent initiator detection (Responses) | ⬜ | |
| 2.57 | Non-streaming / streaming passthrough | ⬜ | |

### 2E — Embeddings Endpoint

| # | Feature | Status | Notes |
|---|---------|--------|-------|
| 2.58 | `POST /embeddings` + `/v1/embeddings` route | ⬜ | |
| 2.59 | Embeddings passthrough to Copilot | ⬜ | |

**Milestone**: Full Anthropic Messages API compatibility with all 3 backends,
plus OpenAI Responses and Embeddings passthrough.

---

## Phase 3 — Optimizations & Utilities

> Goal: Quota-saving logic, rate limiting, token counting, logging, and config system.

### 3A — Configuration System

| # | Feature | Status | Notes |
|---|---------|--------|-------|
| 3.1 | JSON config file (`config.json`) auto-creation with defaults | ⬜ | |
| 3.2 | `auth.apiKeys` config option | ⬜ | |
| 3.3 | `extraPrompts` per-model config | ⬜ | |
| 3.4 | `smallModel` config (default `gpt-5-mini`) | ⬜ | |
| 3.5 | `modelReasoningEfforts` config | ⬜ | |
| 3.6 | `useFunctionApplyPatch` config toggle | ⬜ | |
| 3.7 | `compactUseSmallModel` config toggle | ⬜ | |
| 3.8 | Default `extraPrompts` auto-merge on startup | ⬜ | |

### 3B — Inbound API Key Auth

| # | Feature | Status | Notes |
|---|---------|--------|-------|
| 3.9 | Auth middleware (x-api-key / Bearer) | ⬜ | |
| 3.10 | API key normalization (trim, dedup, filter) | ⬜ | |
| 3.11 | `WWW-Authenticate` header on 401 | ⬜ | |
| 3.12 | OPTIONS / root bypass | ⬜ | |

### 3C — Quota Optimizations

| # | Feature | Status | Notes |
|---|---------|--------|-------|
| 3.13 | Compact request detection → small model routing | ⬜ | |
| 3.14 | Warmup/probe request detection → small model | ⬜ | |
| 3.15 | Tool result + text block merging (avoid premium billing) | ⬜ | |
| 3.16 | Subagent marker detection → force `X-Initiator: agent` | ⬜ | |

### 3D — Rate Limiting & Manual Approval

| # | Feature | Status | Notes |
|---|---------|--------|-------|
| 3.17 | Time-based rate limiter (reject 429 or wait) | ⬜ | |
| 3.18 | Interactive CLI approval prompt (403 on reject) | ⬜ | |

### 3E — Token Counting

| # | Feature | Status | Notes |
|---|---------|--------|-------|
| 3.19 | `POST /v1/messages/count_tokens` route | ⬜ | |
| 3.20 | Multi-encoding tokenizer (o200k_base, cl100k_base, etc.) | ⬜ | Use Go tokenizer lib |
| 3.21 | Model-specific tokenizer selection | ⬜ | |
| 3.22 | Tool definition token counting | ⬜ | |
| 3.23 | Image token estimation (85 per image) | ⬜ | |
| 3.24 | Claude token count 15% inflation | ⬜ | |
| 3.25 | Fallback to `input_tokens: 1` on error | ⬜ | |

### 3F — Logging System

| # | Feature | Status | Notes |
|---|---------|--------|-------|
| 3.26 | Per-handler daily log files | ⬜ | |
| 3.27 | Log rotation (delete after 7 days) | ⬜ | |
| 3.28 | Buffered writing (flush interval / buffer size) | ⬜ | |
| 3.29 | Process cleanup handlers (flush on exit/SIGINT/SIGTERM) | ⬜ | |

### 3G — Usage Endpoint

| # | Feature | Status | Notes |
|---|---------|--------|-------|
| 3.30 | `GET /usage` route | ⬜ | |
| 3.31 | Copilot usage fetch (`GET /copilot_internal/user`) | ⬜ | |

**Milestone**: Full config system, quota optimizations, rate limiting, logging, and token counting.

---

## Phase 4 — CLI Flags & Shell Integration

> Goal: Full CLI experience with all flags and Claude Code launch support.

### 4A — CLI Flags

| # | Feature | Status | Notes |
|---|---------|--------|-------|
| 4.1 | `start` command with all flags (`--port`, `--verbose`, `--account-type`, `--manual`, `--rate-limit`, `--wait`, `--github-token`, `--claude-code`, `--show-token`, `--proxy-env`) | ⬜ | Use `cobra` or `urfave/cli` |
| 4.2 | `auth` command (standalone device-code flow) | ⬜ | |
| 4.3 | `check-usage` command (formatted terminal output) | ⬜ | |
| 4.4 | `debug` command (diagnostic info) | ⬜ | |
| 4.5 | `debug --json` flag | ⬜ | |

### 4B — Shell Integration

| # | Feature | Status | Notes |
|---|---------|--------|-------|
| 4.6 | Cross-platform shell detection (bash/zsh/fish/powershell/cmd) | ⬜ | |
| 4.7 | Env var export script generation per shell syntax | ⬜ | |
| 4.8 | Claude Code env vars generation (ANTHROPIC_BASE_URL, etc.) | ⬜ | |
| 4.9 | Clipboard auto-copy (fallback to print) | ⬜ | |
| 4.10 | Interactive model selection for `--claude-code` | ⬜ | |

### 4C — Proxy Support

| # | Feature | Status | Notes |
|---|---------|--------|-------|
| 4.11 | Per-URL proxy routing from env vars (HTTP_PROXY, etc.) | ⬜ | Go's `http.ProxyFromEnvironment` |
| 4.12 | `--proxy-env` flag to enable | ⬜ | |

**Milestone**: Complete CLI with all subcommands, flags, and Claude Code integration.

---

## Phase 5 — Deployment & Extras

> Goal: Docker support, web UI dashboard, and remaining polish.

### 5A — Docker

| # | Feature | Status | Notes |
|---|---------|--------|-------|
| 5.1 | Multi-stage Dockerfile (Go build) | ⬜ | |
| 5.2 | Health check | ⬜ | |
| 5.3 | `GH_TOKEN` env var support | ⬜ | |
| 5.4 | Volume mount for token persistence | ⬜ | |
| 5.5 | Entrypoint script (`--auth` flag handling) | ⬜ | |
| 5.6 | Docker Compose example | ⬜ | |

### 5B — Web UI Dashboard

| # | Feature | Status | Notes |
|---|---------|--------|-------|
| 5.7 | Standalone HTML usage dashboard (embed or serve) | ⬜ | |
| 5.8 | Quota progress bars with color thresholds | ⬜ | |
| 5.9 | Detailed JSON tree view | ⬜ | |
| 5.10 | URL query parameter configuration | ⬜ | |
| 5.11 | Usage viewer URL printed at startup | ⬜ | |

### 5C — Remaining Polish

| # | Feature | Status | Notes |
|---|---------|--------|-------|
| 5.12 | `auth` command `--verbose` and `--show-token` flags | ⬜ | |
| 5.13 | Force re-authentication support | ⬜ | |
| 5.14 | Token count calculation + logging in chat completions | ⬜ | |

**Milestone**: Production-ready with Docker deployment and monitoring dashboard.

---

## Architecture Decisions (Go-specific)

| Decision | Choice | Rationale |
|----------|--------|-----------|
| HTTP framework | `go-chi/chi` v5 | Lightweight, idiomatic, great middleware |
| CLI framework | `spf13/cobra` | Most popular Go CLI, subcommand support |
| SSE streaming | `bufio.Scanner` + `http.Flusher` | Native Go streaming |
| JSON handling | `encoding/json` | Standard library |
| Tokenizer | TBD (`tiktoken-go` or similar) | Need multi-encoding support |
| Config | `encoding/json` file read/write | Match original behavior |
| Logging | `log/slog` (stdlib) | Structured logging, zero dependencies |
| UUID | `google/uuid` | For `x-request-id` |
| CORS | `go-chi/cors` | Pairs with chi router |

---

## How to Resume Work

1. Open this file and find the first ⬜ item in the current phase
2. Update its status to 🔨 when starting
3. Update to ✅ when done
4. Commit at the end of each phase
5. Each phase commit should be verified before pushing
