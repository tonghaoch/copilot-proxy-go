package shell

import (
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
)

// ShellType represents a detected shell.
type ShellType string

const (
	Bash       ShellType = "bash"
	Zsh        ShellType = "zsh"
	Fish       ShellType = "fish"
	PowerShell ShellType = "powershell"
	Cmd        ShellType = "cmd"
	Sh         ShellType = "sh"
)

// Detect determines the current shell type.
func Detect() ShellType {
	if runtime.GOOS == "windows" {
		return detectWindows()
	}
	return detectUnix()
}

func detectUnix() ShellType {
	shell := os.Getenv("SHELL")
	if shell == "" {
		return Sh
	}
	shell = strings.ToLower(shell)
	switch {
	case strings.Contains(shell, "zsh"):
		return Zsh
	case strings.Contains(shell, "fish"):
		return Fish
	case strings.Contains(shell, "bash"):
		return Bash
	default:
		return Sh
	}
}

func detectWindows() ShellType {
	// Try to detect parent process via wmic
	ppid := os.Getppid()
	out, err := exec.Command("wmic", "process", "where",
		fmt.Sprintf("ProcessId=%d", ppid),
		"get", "Name", "/value").Output()
	if err == nil {
		name := strings.ToLower(string(out))
		switch {
		case strings.Contains(name, "powershell") || strings.Contains(name, "pwsh"):
			return PowerShell
		case strings.Contains(name, "cmd"):
			return Cmd
		case strings.Contains(name, "bash"):
			return Bash
		case strings.Contains(name, "zsh"):
			return Zsh
		}
	}

	// Fallback: check PSModulePath (PowerShell sets this)
	if os.Getenv("PSModulePath") != "" {
		return PowerShell
	}

	return Cmd
}

// EnvVar represents a key-value environment variable.
type EnvVar struct {
	Key   string
	Value string
}

// GenerateExportScript generates a shell command string that exports the given
// environment variables and then runs the specified command.
func GenerateExportScript(shellType ShellType, vars []EnvVar, command string) string {
	switch shellType {
	case PowerShell:
		return generatePowerShell(vars, command)
	case Cmd:
		return generateCmd(vars, command)
	case Fish:
		return generateFish(vars, command)
	default:
		return generateBashZsh(vars, command)
	}
}

func generatePowerShell(vars []EnvVar, command string) string {
	var parts []string
	for _, v := range vars {
		parts = append(parts, fmt.Sprintf(`$env:%s = "%s"`, v.Key, v.Value))
	}
	if command != "" {
		parts = append(parts, command)
	}
	return strings.Join(parts, "; ")
}

func generateCmd(vars []EnvVar, command string) string {
	var parts []string
	for _, v := range vars {
		parts = append(parts, fmt.Sprintf("set %s=%s", v.Key, v.Value))
	}
	if command != "" {
		parts = append(parts, command)
	}
	return strings.Join(parts, " & ")
}

func generateFish(vars []EnvVar, command string) string {
	var parts []string
	for _, v := range vars {
		parts = append(parts, fmt.Sprintf("set -gx %s %s", v.Key, v.Value))
	}
	if command != "" {
		parts = append(parts, command)
	}
	return strings.Join(parts, "; ")
}

func generateBashZsh(vars []EnvVar, command string) string {
	var exports []string
	for _, v := range vars {
		exports = append(exports, fmt.Sprintf(`%s="%s"`, v.Key, v.Value))
	}
	result := "export " + strings.Join(exports, " ")
	if command != "" {
		result += " && " + command
	}
	return result
}
