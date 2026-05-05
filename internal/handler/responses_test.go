package handler

import "testing"

func TestConvertLocalShellTools(t *testing.T) {
	tools := []any{
		map[string]any{"type": "local_shell"},
		map[string]any{"type": "function", "name": "existing"},
	}

	got := convertLocalShellTools(tools)
	if len(got) != 2 {
		t.Fatalf("expected 2 tools, got %d", len(got))
	}

	converted, ok := got[0].(map[string]any)
	if !ok {
		t.Fatalf("expected converted tool to be an object, got %T", got[0])
	}
	if converted["type"] != "function" {
		t.Fatalf("expected local_shell to become function, got %v", converted["type"])
	}
	if converted["name"] != "local_shell" {
		t.Fatalf("expected function name local_shell, got %v", converted["name"])
	}
	params, ok := converted["parameters"].(map[string]any)
	if !ok {
		t.Fatalf("expected parameters object, got %T", converted["parameters"])
	}
	props, ok := params["properties"].(map[string]any)
	if !ok {
		t.Fatalf("expected properties object, got %T", params["properties"])
	}
	if _, ok := props["command"]; !ok {
		t.Fatalf("expected command property in local_shell schema")
	}
}
