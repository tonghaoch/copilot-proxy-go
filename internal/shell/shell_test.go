package shell

import "testing"

func TestQuoteArgBash(t *testing.T) {
	got := QuoteArg(Bash, `model="gpt's"`)
	want := `'model="gpt'"'"'s"'`
	if got != want {
		t.Fatalf("expected %q, got %q", want, got)
	}
}

func TestQuoteArgPowerShell(t *testing.T) {
	got := QuoteArg(PowerShell, `model="gpt's"`)
	want := `'model="gpt''s"'`
	if got != want {
		t.Fatalf("expected %q, got %q", want, got)
	}
}

func TestGenerateExportScriptQuotesBashEnvValues(t *testing.T) {
	got := GenerateExportScript(Bash, []EnvVar{{Key: "CODEX_API_KEY", Value: `sk-$HOME "quote"`}}, "codex")
	want := `export CODEX_API_KEY='sk-$HOME "quote"' && codex`
	if got != want {
		t.Fatalf("expected %q, got %q", want, got)
	}
}

func TestGenerateExportScriptQuotesPowerShellEnvValues(t *testing.T) {
	got := GenerateExportScript(PowerShell, []EnvVar{{Key: "CODEX_API_KEY", Value: `sk-'x'`}}, "codex")
	want := `$env:CODEX_API_KEY = 'sk-''x'''; codex`
	if got != want {
		t.Fatalf("expected %q, got %q", want, got)
	}
}

func TestGenerateExportScriptQuotesCmdEnvValues(t *testing.T) {
	got := GenerateExportScript(Cmd, []EnvVar{{Key: "CODEX_API_KEY", Value: `sk-&%PATH%`}}, "codex")
	want := `set "CODEX_API_KEY=sk-^&^%PATH^%" & codex`
	if got != want {
		t.Fatalf("expected %q, got %q", want, got)
	}
}
