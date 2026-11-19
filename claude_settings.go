package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/tonghaoch/copilot-proxy-go/internal/shell"
)

func saveClaudeCodeSettings(vars []shell.EnvVar) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home directory: %w", err)
	}
	path := filepath.Join(home, ".claude", "settings.json")
	if err := saveClaudeCodeSettingsAt(path, vars); err != nil {
		return "", err
	}
	return path, nil
}

func saveClaudeCodeSettingsAt(path string, vars []shell.EnvVar) error {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return fmt.Errorf("create settings directory: %w", err)
	}

	target := path
	info, err := os.Lstat(path)
	if err == nil && info.Mode()&os.ModeSymlink != 0 {
		target, err = filepath.EvalSymlinks(path)
		if err != nil {
			return fmt.Errorf("resolve settings symlink: %w", err)
		}
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("inspect settings: %w", err)
	}
	targetInfo, targetErr := os.Stat(target)
	if targetErr != nil && !errors.Is(targetErr, os.ErrNotExist) {
		return fmt.Errorf("inspect settings target: %w", targetErr)
	}
	backupInfo, backupErr := os.Stat(path + ".bak")
	if backupErr != nil && !errors.Is(backupErr, os.ErrNotExist) {
		return fmt.Errorf("inspect settings backup: %w", backupErr)
	}
	if targetErr == nil && backupErr == nil && os.SameFile(targetInfo, backupInfo) {
		return errors.New("settings target cannot also be its backup")
	}

	root := make(map[string]json.RawMessage)
	env := make(map[string]json.RawMessage)
	existing, err := os.ReadFile(target)
	exists := err == nil
	switch {
	case err == nil:
		if len(existing) > 0 {
			if err := json.Unmarshal(existing, &root); err != nil {
				return fmt.Errorf("existing settings are not a JSON object: %w", err)
			}
			if root == nil {
				return errors.New("existing settings must be a JSON object")
			}
			if raw, ok := root["env"]; ok {
				if err := json.Unmarshal(raw, &env); err != nil {
					return fmt.Errorf("existing settings env must be an object: %w", err)
				}
				if env == nil {
					env = make(map[string]json.RawMessage)
				}
			}
		}
	case errors.Is(err, os.ErrNotExist):
		existing = nil
	default:
		return fmt.Errorf("read existing settings: %w", err)
	}

	for _, v := range vars {
		value, err := json.Marshal(v.Value)
		if err != nil {
			return fmt.Errorf("encode environment value: %w", err)
		}
		env[v.Key] = value
	}
	envJSON, err := json.Marshal(env)
	if err != nil {
		return fmt.Errorf("encode settings env: %w", err)
	}
	root["env"] = envJSON
	output, err := json.MarshalIndent(root, "", "  ")
	if err != nil {
		return fmt.Errorf("encode settings: %w", err)
	}
	output = append(output, '\n')

	if exists {
		if err := writeSettingsAtomically(path+".bak", existing); err != nil {
			return fmt.Errorf("write settings backup: %w", err)
		}
	}
	if err := writeSettingsAtomically(target, output); err != nil {
		return fmt.Errorf("write settings: %w", err)
	}
	return nil
}

func writeSettingsAtomically(path string, data []byte) error {
	file, err := os.CreateTemp(filepath.Dir(path), ".copilot-proxy-settings-*")
	if err != nil {
		return err
	}
	defer func() {
		if err := os.Remove(file.Name()); err != nil && !errors.Is(err, os.ErrNotExist) {
			slog.Warn("failed to remove temporary settings file", "error", err)
		}
	}()
	if _, err := file.Write(data); err != nil {
		return errors.Join(err, file.Close())
	}
	if err := file.Sync(); err != nil {
		return errors.Join(err, file.Close())
	}
	if err := file.Close(); err != nil {
		return err
	}
	return os.Rename(file.Name(), path)
}
