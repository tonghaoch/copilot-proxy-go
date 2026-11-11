package api

import "testing"

func TestBuildCopilotHeadersMatchesOfficialInteraction(t *testing.T) {
	h := BuildCopilotHeaders("tok", "1.109.3")
	if got := h.Get("X-Interaction-Type"); got != "conversation-agent" {
		t.Fatalf("X-Interaction-Type = %q", got)
	}
	if h.Get("X-Agent-Task-Id") == "" || h.Get("X-Agent-Task-Id") != h.Get("X-Request-Id") {
		t.Fatalf("X-Agent-Task-Id %q must equal X-Request-Id %q", h.Get("X-Agent-Task-Id"), h.Get("X-Request-Id"))
	}
	if got := h.Get("Editor-Plugin-Version"); got != "copilot-chat/0.44.0" {
		t.Fatalf("Editor-Plugin-Version = %q", got)
	}
}
