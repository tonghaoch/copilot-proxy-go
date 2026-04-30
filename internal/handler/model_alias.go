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

// ToClaudeCodeName converts a Copilot model ID into the Claude Code-friendly
// dashed form. 1M variants are collapsed to the [1m] suffix that Claude Code
// recognizes (Claude Code strips [1m] before sending and adds the
// "context-1m-2025-08-07" beta header). Non-Claude IDs pass through.
//
//	claude-opus-4.7              → claude-opus-4-7
//	claude-opus-4.7-1m-internal  → claude-opus-4-7[1m]
//	claude-opus-4.6-1m           → claude-opus-4-6[1m]
//	claude-sonnet-4              → claude-sonnet-4
//	claude-opus-4.7-high         → claude-opus-4-7-high
func ToClaudeCodeName(copilotID string) string {
	m := claudeDotRe.FindStringSubmatch(copilotID)
	if m == nil {
		return copilotID
	}
	family, major, minor, suffix := m[1], m[2], m[3], m[4]

	base := family + "-" + major
	if minor != "" {
		base += "-" + minor
	}
	if suffix == "1m" || strings.HasPrefix(suffix, "1m-") {
		return base + "[1m]"
	}
	if suffix != "" {
		return base + "-" + suffix
	}
	return base
}

// ResolveCopilotModel converts an inbound model ID (whatever Claude Code or
// any other client sends) into the actual Copilot model ID to call. Two
// signals trigger 1M-variant routing:
//   - a literal "[1m]" suffix on the model (defensive: Claude Code normally
//     strips this before sending)
//   - the Anthropic-Beta header containing "context-1m-2025-08-07"
//
// On either signal we look for a cached model whose ID starts with
// "<canonical>-1m" and route there. Otherwise we just translate the dashed
// version back to dotted form.
func ResolveCopilotModel(model, betaHeader string) string {
	want1M := false
	if strings.HasSuffix(model, "[1m]") {
		want1M = true
		model = strings.TrimSuffix(model, "[1m]")
	}
	if strings.Contains(betaHeader, "context-1m-2025-08-07") {
		want1M = true
	}

	canonical := dashVersionToDot(model)

	if want1M {
		if oneM := find1MVariant(canonical); oneM != "" {
			return oneM
		}
	}
	return canonical
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

// find1MVariant returns the first cached model whose ID begins with
// "<canonical>-1m" — covers both "claude-opus-4.6-1m" and
// "claude-opus-4.7-1m-internal". Returns "" when no variant is registered.
func find1MVariant(canonical string) string {
	prefix := canonical + "-1m"
	for _, m := range state.Global.GetModels() {
		if strings.HasPrefix(m.ID, prefix) {
			return m.ID
		}
	}
	return ""
}

// RewriteModelInBody resolves the "model" field of a JSON object body via
// ResolveCopilotModel and rewrites the body in place. Returns the (possibly
// new) body and the resolved model name. If parsing fails or the body has no
// "model" field, returns (body, "") so callers can detect the no-op.
func RewriteModelInBody(body []byte, betaHeader string) ([]byte, string) {
	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		return body, ""
	}
	orig, _ := payload["model"].(string)
	if orig == "" {
		return body, ""
	}
	resolved := ResolveCopilotModel(orig, betaHeader)
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
