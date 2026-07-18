package auth

import (
	"os"
	"strings"

	"github.com/tonghaoch/copilot-proxy-go/internal/state"
)

func SaveToken(token string) error {
	return os.WriteFile(state.TokenPath(), []byte(token), 0600)
}

func LoadToken() (string, error) {
	data, err := os.ReadFile(state.TokenPath())
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(data)), nil
}
