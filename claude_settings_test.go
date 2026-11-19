package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/tonghaoch/copilot-proxy-go/internal/shell"
)

func TestSaveClaudeSettingsCreatesAndMerges(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".claude", "settings.json")
	vars := claudeCodeEnv(4141, "primary", "small", "configured-key")
	if err := saveClaudeCodeSettingsAt(path, vars); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path + ".bak"); !os.IsNotExist(err) {
		t.Fatalf("new file unexpectedly has a backup: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0600 {
		t.Fatalf("settings mode = %o", info.Mode().Perm())
	}

	original := []byte(`{"permissions":{"allow":["Read"]},"env":{"CUSTOM":"keep","NUMBER":12,"NULL":null,"ANTHROPIC_MODEL":"old"},"hooks":{"PreToolUse":[]}}`)
	if err := os.WriteFile(path, original, 0600); err != nil {
		t.Fatal(err)
	}
	if err := saveClaudeCodeSettingsAt(path, vars); err != nil {
		t.Fatal(err)
	}
	backup, err := os.ReadFile(path + ".bak")
	if err != nil || !bytes.Equal(backup, original) {
		t.Fatalf("backup = %q, err=%v", backup, err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var result struct {
		Permissions json.RawMessage            `json:"permissions"`
		Hooks       json.RawMessage            `json:"hooks"`
		Env         map[string]json.RawMessage `json:"env"`
	}
	if err := json.Unmarshal(data, &result); err != nil {
		t.Fatal(err)
	}
	if string(result.Permissions) == "" || string(result.Hooks) == "" ||
		string(result.Env["CUSTOM"]) != `"keep"` || string(result.Env["NUMBER"]) != `12` ||
		string(result.Env["NULL"]) != `null` || string(result.Env["ANTHROPIC_MODEL"]) != `"primary"` ||
		string(result.Env["ANTHROPIC_AUTH_TOKEN"]) != `"configured-key"` {
		t.Fatalf("merged settings = %s", data)
	}
}

func TestSaveClaudeSettingsHandlesNullAndRejectsInvalidObjects(t *testing.T) {
	vars := []shell.EnvVar{{Key: "ANTHROPIC_MODEL", Value: "test"}}
	for _, tt := range []struct {
		name, input string
		valid       bool
	}{
		{"null-env", `{"env":null,"keep":true}`, true},
		{"null-root", `null`, false},
		{"array-root", `[]`, false},
		{"invalid-json", `{broken`, false},
		{"string-env", `{"env":"no"}`, false},
		{"array-env", `{"env":[]}`, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "settings.json")
			if err := os.WriteFile(path, []byte(tt.input), 0600); err != nil {
				t.Fatal(err)
			}
			err := saveClaudeCodeSettingsAt(path, vars)
			if (err == nil) != tt.valid {
				t.Fatalf("save error = %v", err)
			}
			data, readErr := os.ReadFile(path)
			if readErr != nil {
				t.Fatal(readErr)
			}
			if !tt.valid && string(data) != tt.input {
				t.Fatalf("invalid settings were modified: %s", data)
			}
			if tt.valid && (!json.Valid(data) || !bytes.Contains(data, []byte(`"ANTHROPIC_MODEL": "test"`))) {
				t.Fatalf("null env was not replaced: %s", data)
			}
		})
	}
}

func TestSaveClaudeSettingsLeavesOriginalWhenBackupFails(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "settings.json")
	original := []byte(`{"env":{"CUSTOM":"keep"}}`)
	if err := os.WriteFile(path, original, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(path+".bak", 0700); err != nil {
		t.Fatal(err)
	}
	if err := saveClaudeCodeSettingsAt(path, []shell.EnvVar{{Key: "ANTHROPIC_MODEL", Value: "test"}}); err == nil {
		t.Fatal("expected backup failure")
	}
	got, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(got, original) {
		t.Fatalf("original = %q, err=%v", got, err)
	}
	leftovers, err := filepath.Glob(filepath.Join(dir, ".copilot-proxy-settings-*"))
	if err != nil || len(leftovers) != 0 {
		t.Fatalf("temporary files remain: %v, %v", leftovers, err)
	}
}

func TestSaveClaudeSettingsPreservesSymlink(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation may require elevated Windows privileges")
	}
	dir := t.TempDir()
	target := filepath.Join(dir, "managed.json")
	path := filepath.Join(dir, "settings.json")
	original := []byte(`{"env":{"CUSTOM":"keep"}}`)
	if err := os.WriteFile(target, original, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, path); err != nil {
		t.Fatal(err)
	}
	if err := saveClaudeCodeSettingsAt(path, []shell.EnvVar{{Key: "ANTHROPIC_MODEL", Value: "test"}}); err != nil {
		t.Fatal(err)
	}
	info, err := os.Lstat(path)
	if err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("settings symlink was replaced: %v, %v", info, err)
	}
	updated, err := os.ReadFile(target)
	if err != nil || !bytes.Contains(updated, []byte(`"ANTHROPIC_MODEL": "test"`)) {
		t.Fatalf("target = %s, err=%v", updated, err)
	}
	backup, err := os.ReadFile(path + ".bak")
	if err != nil || !bytes.Equal(backup, original) {
		t.Fatalf("backup = %s, err=%v", backup, err)
	}
}

func TestSaveSettingsRequiresClaudeCode(t *testing.T) {
	cmd := startCmd()
	cmd.SilenceErrors = true
	cmd.SilenceUsage = true
	cmd.SetArgs([]string{"--save-settings"})
	if err := cmd.Execute(); err == nil || !strings.Contains(err.Error(), "requires --claude-code") {
		t.Fatalf("expected flag validation error, got %v", err)
	}
}

func TestSaveClaudeSettingsRejectsBackupAsSymlinkTarget(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation may require elevated Windows privileges")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "settings.json")
	original := []byte(`{"env":{}}`)
	if err := os.WriteFile(path+".bak", original, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(path+".bak", path); err != nil {
		t.Fatal(err)
	}
	if err := saveClaudeCodeSettingsAt(path, []shell.EnvVar{{Key: "ANTHROPIC_MODEL", Value: "test"}}); err == nil {
		t.Fatal("expected backup-target collision to be rejected")
	}
	got, err := os.ReadFile(path + ".bak")
	if err != nil || !bytes.Equal(got, original) {
		t.Fatalf("backup target = %q, err=%v", got, err)
	}
}
