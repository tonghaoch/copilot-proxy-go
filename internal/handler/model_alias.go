package handler

import (
	"encoding/json"
	"regexp"
	"strings"

	"github.com/tonghaoch/copilot-proxy-go/internal/state"
)

// claudeDotRe matches Copilot's dotted Claude IDs.
//
// Examples:
//
//	claude-opus-4.7
//	claude-opus-4.7-1m-internal
//	claude-opus-4.6-1m
//	claude-haiku-4.5
//	claude-sonnet-4              (no minor version)
//	claude-opus-4.7-high         (reasoning-tier suffix)
//
// Capture groups: family, major, minor (optional), suffix (optional).
var claudeDotRe = regexp.MustCompile(`^(claude-(?:opus|sonnet|haiku))-(\d+)(?:\.(\d+))?(?:-(.+))?$`)

// claudeDashRe matches Claude Code's dashed Claude IDs.
//
// Examples:
//
//	claude-opus-4-7
//	claude-opus-4-7-high
//	claude-sonnet-4
//
// Capture groups: family, major, minor (optional), suffix (optional).
var claudeDashRe = regexp.MustCompile(`^(claude-(?:opus|sonnet|haiku))-(\d+)(?:-(\d+))?(?:-(.+))?$`)

// standardClaudeContextTokens is the default Claude context window (200K). A
// model whose reported context window exceeds this is an "extended context"
// (1M) model, which Claude Code only budgets correctly when the model name
// carries the [1m] suffix (see ToClaudeCodeName).
const standardClaudeContextTokens = 200000

// is1MContextModel reports whether a model advertises an extended (>200K)
// context window in its capabilities.
func is1MContextModel(m state.Model) bool {
	return m.Capabilities.Limits.MaxContextWindowTokens > standardClaudeContextTokens
}

// ToClaudeCodeName converts a Copilot model into the Claude Code-friendly
// dashed form. Non-Claude IDs pass through unchanged.
//
// Extended-context Claude models get a "[1m]" suffix. Claude Code treats our
// proxy as an LLM gateway and can't verify 1M support, so it budgets a 200K
// window unless the model name carries [1m]; it strips the suffix locally
// before sending, so the proxy still receives a clean ID.
//
//	claude-opus-4.8   (1M limits) → claude-opus-4-8[1m]
//	claude-sonnet-4.5 (200K limits) → claude-sonnet-4-5
//	claude-opus-4.7-high          → claude-opus-4-7-high
func ToClaudeCodeName(m state.Model) string {
	match := claudeDotRe.FindStringSubmatch(m.ID)
	if match == nil {
		return m.ID
	}
	family, major, minor, suffix := match[1], match[2], match[3], match[4]

	base := family + "-" + major
	if minor != "" {
		base += "-" + minor
	}
	if suffix != "" {
		base += "-" + suffix
	}
	if is1MContextModel(m) {
		base += "[1m]"
	}
	return base
}

// ResolveCopilotModel converts an inbound model ID (whatever Claude Code or
// any other client sends) into the actual Copilot model ID to call, by
// translating Claude Code's dashed form back to Copilot's dotted form.
//
// Copilot no longer ships separate 1M-context model variants — context size is
// now a per-model capability (capabilities.limits.max_context_window_tokens),
// so there is nothing to route to. A trailing "[1m]" is stripped defensively
// (Claude Code normally strips it itself; forwarding it to Copilot causes a
// 400). The "context-1m-2025-08-07" beta header is likewise stripped from
// forwarded requests by filterBetaHeader, but neither affects model selection.
func ResolveCopilotModel(model string) string {
	model = strings.TrimSuffix(model, "[1m]")
	return dashVersionToDot(model)
}

// dashVersionToDot turns "claude-opus-4-7" into "claude-opus-4.7". Idempotent
// for non-Claude IDs and for Claude IDs that are already dotted or have no
// minor version (e.g. "claude-sonnet-4").
func dashVersionToDot(model string) string {
	m := claudeDashRe.FindStringSubmatch(model)
	if m == nil {
		return model
	}
	family, major, minor, suffix := m[1], m[2], m[3], m[4]

	out := family + "-" + major
	if minor != "" {
		out += "." + minor
	}
	if suffix != "" {
		out += "-" + suffix
	}
	return out
}

// RewriteModelInBody resolves the "model" field of a JSON object body via
// ResolveCopilotModel and rewrites the body in place. Returns the (possibly
// new) body and the resolved model name. If parsing fails or the body has no
// "model" field, returns (body, "") so callers can detect the no-op.
func RewriteModelInBody(body []byte) ([]byte, string) {
	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		return body, ""
	}
	orig, _ := payload["model"].(string)
	if orig == "" {
		return body, ""
	}
	resolved := ResolveCopilotModel(orig)
	if resolved == orig {
		return body, orig
	}
	payload["model"] = resolved
	out, err := json.Marshal(payload)
	if err != nil {
		return body, orig
	}
	return out, resolved
}
