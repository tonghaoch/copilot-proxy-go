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
var claudeDotRe = regexp.MustCompile(`^(claude-[a-z]+)-(\d+)(?:\.(\d+))?(?:-(.+))?$`)

// claudeDashRe matches Claude Code's dashed Claude IDs.
//
// Examples:
//
//	claude-opus-4-7
//	claude-opus-4-7-high
//	claude-sonnet-4
//
// Capture groups: family, major, minor (optional), suffix (optional).
var claudeDashRe = regexp.MustCompile(`^(claude-[a-z]+)-(\d+)(?:-(\d+))?(?:-(.+))?$`)

// Claude Code recognises [1m] as a context-window hint. Both suffixes are
// stripped before routing requests upstream.
const (
	claudeLongSuffix = "[1m]"
	otherLongSuffix  = "[long]"
)

// ModelFlavour is one context-size variant of a Copilot model as clients see it.
type ModelFlavour struct {
	ID            string // public ID, e.g. claude-haiku-5-5 or claude-haiku-5-5[1m]
	ContextWindow int    // prompt budget for this flavour
	Long          bool   // true for the long-context (opt-in, pricier) tier
}

// ModelFlavours lists the variants a model is offered as. A model priced in
// two tiers yields a default entry capped at the cheaper tier's prompt budget
// and a long-context entry with the full window, mirroring the official
// clients' context-size picker. Copilot bills by actual prompt size, so the
// default entry exists to give clients the smaller budget to stay within.
func ModelFlavours(m state.Model) []ModelFlavour {
	base := m.ID
	if match := claudeDotRe.FindStringSubmatch(m.ID); match != nil {
		base = dottedToDashed(match)
	}
	limits := m.Capabilities.Limits
	suffix := otherLongSuffix
	if limits.MaxContextWindowTokens >= 1000000 {
		suffix = claudeLongSuffix
	}
	if def := m.DefaultPromptTokens(); def > 0 {
		long := m.Billing.TokenPrices["long_context"].MaxPromptTokens
		return []ModelFlavour{
			{ID: base, ContextWindow: def},
			{ID: base + suffix, ContextWindow: long, Long: true},
		}
	}
	if suffix == claudeLongSuffix {
		return []ModelFlavour{{ID: base + suffix, ContextWindow: promptBudget(limits), Long: true}}
	}
	return []ModelFlavour{{ID: base, ContextWindow: promptBudget(limits)}}
}

// promptBudget prefers max_prompt_tokens, the input a client may send.
func promptBudget(l state.ModelLimits) int {
	if l.MaxPromptTokens > 0 {
		return l.MaxPromptTokens
	}
	return l.MaxContextWindowTokens
}

// ToClaudeCodeName returns a model's primary public ID: the long-context
// flavour when there is one, since that is what the old single-entry list
// exposed.
func ToClaudeCodeName(m state.Model) string {
	flavours := ModelFlavours(m)
	return flavours[len(flavours)-1].ID
}

// dottedToDashed rebuilds a claudeDotRe match in Claude Code's dashed form.
func dottedToDashed(match []string) string {
	family, major, minor, suffix := match[1], match[2], match[3], match[4]
	base := family + "-" + major
	if minor != "" {
		base += "-" + minor
	}
	if suffix != "" {
		base += "-" + suffix
	}
	return base
}

// ResolveCopilotModel converts an inbound model ID (whatever Claude Code or
// any other client sends) into the actual Copilot model ID to call, by
// translating Claude Code's dashed form back to Copilot's dotted form.
//
// Context flavours share one upstream model: Copilot bills the long-context
// tier by prompt size, not by a flag, so a trailing [1m] or [long] is simply
// stripped (Claude Code strips [1m] itself; forwarding it to Copilot causes a
// 400). The "context-1m-2025-08-07" beta header is likewise stripped from
// forwarded requests by filterBetaHeader, but neither affects model selection.
func ResolveCopilotModel(model string) string {
	model = strings.TrimSuffix(model, claudeLongSuffix)
	model = strings.TrimSuffix(model, otherLongSuffix)
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
