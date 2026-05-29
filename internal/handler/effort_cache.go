package handler

import (
	"encoding/json"
	"regexp"
	"slices"
	"strings"
	"sync"
)

// effortRank lists Anthropic output_config.effort values from strongest to
// weakest. Used to pick the closest supported effort when the requested one
// is rejected by Copilot's upstream.
var effortRank = []string{"max", "high", "medium", "low", "minimal"}

// effortErrRe parses Copilot's effort-rejection error message. Example:
//
//	output_config.effort "high" is not supported by model claude-opus-4.8; supported values: [medium]
//
// Capture groups: requested effort, model ID, comma-separated supported list.
var effortErrRe = regexp.MustCompile(
	`output_config\.effort\s+"([^"]+)"\s+is not supported by model\s+([^;]+);\s*supported values:\s*\[([^\]]+)\]`)

// effortSupportCache holds the per-session record of which efforts a given
// model accepts. Populated lazily from Copilot 400 responses; cleared on
// process restart.
var effortSupportCache = &supportCache{entries: map[string][]string{}}

type supportCache struct {
	mu      sync.RWMutex
	entries map[string][]string
}

// Get returns the cached supported-effort list for a model, or nil if unknown.
func (c *supportCache) Get(model string) []string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	v, ok := c.entries[model]
	if !ok {
		return nil
	}
	out := make([]string, len(v))
	copy(out, v)
	return out
}

// Set records the supported-effort list for a model.
func (c *supportCache) Set(model string, supported []string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]string, len(supported))
	copy(out, supported)
	c.entries[model] = out
}

// parseEffortError extracts (model, supported) from a Copilot 400 body when it
// matches the effort-rejection format. Handles both raw error strings and
// JSON-wrapped bodies (where the message lives at error.message or message).
// Returns ("", nil) on no match.
func parseEffortError(body string) (model string, supported []string) {
	if m, s := matchEffortError(body); m != "" {
		return m, s
	}
	// Try unwrapping common JSON error envelopes — the inner message has
	// unescaped quotes that the regex needs.
	var env struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
		Message string `json:"message"`
	}
	if json.Unmarshal([]byte(body), &env) == nil {
		for _, candidate := range []string{env.Error.Message, env.Message} {
			if candidate == "" {
				continue
			}
			if m, s := matchEffortError(candidate); m != "" {
				return m, s
			}
		}
	}
	return "", nil
}

func matchEffortError(s string) (model string, supported []string) {
	m := effortErrRe.FindStringSubmatch(s)
	if m == nil {
		return "", nil
	}
	model = strings.TrimSpace(m[2])
	for _, part := range strings.Split(m[3], ",") {
		if p := strings.TrimSpace(part); p != "" {
			supported = append(supported, p)
		}
	}
	return model, supported
}

// pickClosestEffort picks the supported effort closest to the requested one.
// Preference order: same value if supported; else first weaker value (toward
// minimal) present in supported; else first stronger value (toward max);
// else the first entry in supported; else "".
func pickClosestEffort(requested string, supported []string) string {
	if len(supported) == 0 {
		return ""
	}
	allowed := make(map[string]bool, len(supported))
	for _, s := range supported {
		allowed[s] = true
	}
	if allowed[requested] {
		return requested
	}
	idx := slices.Index(effortRank, requested)
	if idx < 0 {
		// Unknown requested value — return first supported.
		return supported[0]
	}
	// Walk downward (weaker) first.
	for i := idx + 1; i < len(effortRank); i++ {
		if allowed[effortRank[i]] {
			return effortRank[i]
		}
	}
	// Then upward (stronger).
	for i := idx - 1; i >= 0; i-- {
		if allowed[effortRank[i]] {
			return effortRank[i]
		}
	}
	return supported[0]
}

// clampEffort returns the effort to actually send for a given model, given
// what the user asked for. Falls back to requested when nothing is cached.
func clampEffort(model, requested string) string {
	supported := effortSupportCache.Get(model)
	if supported == nil {
		return requested
	}
	return pickClosestEffort(requested, supported)
}
