package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/tonghaoch/copilot-proxy-go/internal/shell"
	"github.com/tonghaoch/copilot-proxy-go/internal/state"
)

func TestResponsesCapableModelIDs(t *testing.T) {
	models := []state.Model{
		{ID: "z-model", SupportedEndpoints: []string{"/responses"}},
		{ID: "chat-only", SupportedEndpoints: []string{"/chat/completions"}},
		{ID: "a-model", SupportedEndpoints: []string{"/v1/messages", "/responses"}},
		{ID: "z-model", SupportedEndpoints: []string{"/responses"}},
	}

	got := responsesCapableModelIDs(models)
	want := []string{"a-model", "z-model"}
	if len(got) != len(want) {
		t.Fatalf("expected %d models, got %d: %v", len(want), len(got), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("expected %v, got %v", want, got)
		}
	}
}

// useTempHome redirects os.UserHomeDir() to a fresh temp dir for the duration
// of the test by overriding HOME (unix) and USERPROFILE (windows).
func useTempHome(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if runtime.GOOS == "windows" {
		t.Setenv("USERPROFILE", dir)
	} else {
		t.Setenv("HOME", dir)
	}
	return dir
}

func sampleEnvVars() []shell.EnvVar {
	return []shell.EnvVar{
		{Key: "ANTHROPIC_BASE_URL", Value: "http://localhost:4141"},
		{Key: "ANTHROPIC_AUTH_TOKEN", Value: "copilot-proxy"},
		{Key: "ANTHROPIC_MODEL", Value: "claude-sonnet-4.5"},
	}
}

func readSettings(t *testing.T, path string) (map[string]json.RawMessage, map[string]string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	root := map[string]json.RawMessage{}
	if err := json.Unmarshal(data, &root); err != nil {
		t.Fatalf("unmarshal %s: %v", path, err)
	}
	env := map[string]string{}
	if raw, ok := root["env"]; ok {
		if err := json.Unmarshal(raw, &env); err != nil {
			t.Fatalf("env in %s is not string map: %v", path, err)
		}
	}
	return root, env
}

func TestSaveClaudeCodeSettings_NewFile(t *testing.T) {
	home := useTempHome(t)
	path, err := saveClaudeCodeSettings(sampleEnvVars())
	if err != nil {
		t.Fatalf("saveClaudeCodeSettings: %v", err)
	}
	want := filepath.Join(home, ".claude", "settings.json")
	if path != want {
		t.Fatalf("path = %q, want %q", path, want)
	}
	if _, err := os.Stat(path + ".bak"); !os.IsNotExist(err) {
		t.Fatalf("expected no .bak for new file, got err=%v", err)
	}
	_, env := readSettings(t, path)
	for _, v := range sampleEnvVars() {
		if got := env[v.Key]; got != v.Value {
			t.Fatalf("env[%s] = %q, want %q", v.Key, got, v.Value)
		}
	}
}

func TestSaveClaudeCodeSettings_MergePreservesOtherFields(t *testing.T) {
	home := useTempHome(t)
	dir := filepath.Join(home, ".claude")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "settings.json")
	original := `{
  "permissions": {"allow": ["Read", "Edit"]},
  "env": {
    "MY_CUSTOM_VAR": "keep-me",
    "ANTHROPIC_BASE_URL": "http://old-proxy:9999"
  },
  "hooks": {"PreToolUse": []}
}`
	if err := os.WriteFile(path, []byte(original), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := saveClaudeCodeSettings(sampleEnvVars()); err != nil {
		t.Fatalf("saveClaudeCodeSettings: %v", err)
	}

	// Backup must exist with exact original bytes.
	bak, err := os.ReadFile(path + ".bak")
	if err != nil {
		t.Fatalf("read backup: %v", err)
	}
	if string(bak) != original {
		t.Fatalf("backup mismatch:\nwant: %s\ngot:  %s", original, string(bak))
	}

	root, env := readSettings(t, path)
	if _, ok := root["permissions"]; !ok {
		t.Fatal("top-level permissions field was dropped")
	}
	if _, ok := root["hooks"]; !ok {
		t.Fatal("top-level hooks field was dropped")
	}
	if env["MY_CUSTOM_VAR"] != "keep-me" {
		t.Fatalf("user's MY_CUSTOM_VAR was dropped: %q", env["MY_CUSTOM_VAR"])
	}
	if env["ANTHROPIC_BASE_URL"] != "http://localhost:4141" {
		t.Fatalf("ANTHROPIC_BASE_URL not overwritten: %q", env["ANTHROPIC_BASE_URL"])
	}
	if env["ANTHROPIC_MODEL"] != "claude-sonnet-4.5" {
		t.Fatalf("ANTHROPIC_MODEL not set: %q", env["ANTHROPIC_MODEL"])
	}
}

func TestSaveClaudeCodeSettings_InvalidJSONRefuses(t *testing.T) {
	home := useTempHome(t)
	dir := filepath.Join(home, ".claude")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "settings.json")
	junk := []byte("{ this is not json")
	if err := os.WriteFile(path, junk, 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := saveClaudeCodeSettings(sampleEnvVars()); err == nil {
		t.Fatal("expected error on invalid JSON, got nil")
	}
	// Original file must be untouched (we refused, did not back up, did not overwrite).
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read original: %v", err)
	}
	if string(got) != string(junk) {
		t.Fatalf("invalid file was modified: %q", string(got))
	}
}

func TestSaveClaudeCodeSettings_EnvFieldWrongTypeRefuses(t *testing.T) {
	home := useTempHome(t)
	dir := filepath.Join(home, ".claude")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "settings.json")
	original := []byte(`{"env": "not-an-object"}`)
	if err := os.WriteFile(path, original, 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := saveClaudeCodeSettings(sampleEnvVars()); err == nil {
		t.Fatal("expected error when env field is a string, got nil")
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read original: %v", err)
	}
	if string(got) != string(original) {
		t.Fatal("malformed file was modified")
	}
}

func TestBuildCodexCommand(t *testing.T) {
	cmd := buildCodexCommand(shell.Bash, "gpt-5.3-codex", "http://127.0.0.1:4141/v1")

	checks := []string{
		"codex",
		"model=\"gpt-5.3-codex\"",
		"model_provider=\"copilot-proxy\"",
		"model_providers.copilot-proxy.name=\"Copilot Proxy\"",
		"model_providers.copilot-proxy.base_url=\"http://127.0.0.1:4141/v1\"",
		"model_providers.copilot-proxy.env_key=\"CODEX_API_KEY\"",
		"model_providers.copilot-proxy.wire_api=\"responses\"",
	}
	for _, check := range checks {
		if !strings.Contains(cmd, check) {
			t.Fatalf("expected command to contain %q, got %q", check, cmd)
		}
	}
}
