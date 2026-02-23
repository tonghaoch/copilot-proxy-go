package api

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"regexp"
	"time"

	"github.com/google/uuid"

	"github.com/tonghaoch/copilot-proxy-go/internal/state"
)

const (
	GitHubClientID        = "Iv1.b507a08c87ecfe98"
	GitHubScope           = "read:user"
	FallbackVSCodeVersion = "1.109.3"
	CopilotChatVersion    = "0.37.6"
	GitHubAPIVersion      = "2025-10-01"
)

// GetBaseURL returns the Copilot API base URL for the given account type.
func GetBaseURL(accountType string) string {
	switch accountType {
	case "business":
		return "https://api.business.githubcopilot.com"
	case "enterprise":
		return "https://api.enterprise.githubcopilot.com"
	default:
		return "https://api.githubcopilot.com"
	}
}

// FetchVSCodeVersion scrapes the AUR PKGBUILD for the latest VS Code version.
// Falls back to FallbackVSCodeVersion on any error.
func FetchVSCodeVersion() string {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	url := "https://aur.archlinux.org/cgit/aur.git/plain/PKGBUILD?h=visual-studio-code-bin"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		slog.Warn("failed to create VS Code version request", "error", err)
		return FallbackVSCodeVersion
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		slog.Warn("failed to fetch VS Code version", "error", err)
		return FallbackVSCodeVersion
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		slog.Warn("failed to read VS Code version response", "error", err)
		return FallbackVSCodeVersion
	}

	re := regexp.MustCompile(`pkgver=(\d+\.\d+\.\d+)`)
	matches := re.FindSubmatch(body)
	if len(matches) < 2 {
		slog.Warn("failed to parse VS Code version from PKGBUILD")
		return FallbackVSCodeVersion
	}

	version := string(matches[1])
	return version
}

// BuildCopilotHeaders builds the standard headers for Copilot API requests.
func BuildCopilotHeaders(copilotToken, vsCodeVersion string) http.Header {
	h := http.Header{}
	h.Set("Authorization", "Bearer "+copilotToken)
	h.Set("Content-Type", "application/json")
	h.Set("Copilot-Integration-Id", "vscode-chat")
	h.Set("Editor-Version", "vscode/"+vsCodeVersion)
	h.Set("Editor-Plugin-Version", "copilot-chat/"+CopilotChatVersion)
	h.Set("User-Agent", "GitHubCopilotChat/"+CopilotChatVersion)
	h.Set("Openai-Intent", "conversation-agent")
	h.Set("X-Github-Api-Version", GitHubAPIVersion)
	h.Set("X-Request-Id", uuid.New().String())
	h.Set("X-Vscode-User-Agent-Library-Version", "electron-fetch")
	return h
}

// BuildCopilotHeadersFromState builds headers using global state.
func BuildCopilotHeadersFromState() http.Header {
	return BuildCopilotHeaders(
		state.Global.GetCopilotToken(),
		state.Global.GetVSCodeVersion(),
	)
}

// BuildGitHubHeaders builds the standard headers for GitHub API requests.
func BuildGitHubHeaders(githubToken, vsCodeVersion string) http.Header {
	h := http.Header{}
	h.Set("Authorization", "token "+githubToken)
	h.Set("Accept", "application/json")
	h.Set("Content-Type", "application/json")
	h.Set("Editor-Version", "vscode/"+vsCodeVersion)
	h.Set("Editor-Plugin-Version", "copilot-chat/"+CopilotChatVersion)
	h.Set("User-Agent", "GitHubCopilotChat/"+CopilotChatVersion)
	h.Set("X-Github-Api-Version", GitHubAPIVersion)
	h.Set("X-Vscode-User-Agent-Library-Version", "electron-fetch")
	return h
}

// BuildGitHubHeadersFromState builds GitHub headers using global state.
func BuildGitHubHeadersFromState() http.Header {
	return BuildGitHubHeaders(
		state.Global.GetGithubToken(),
		state.Global.GetVSCodeVersion(),
	)
}

// SetInitiatorHeader sets the X-Initiator header based on whether the request
// is user-initiated or agent-initiated.
func SetInitiatorHeader(h http.Header, isAgent bool) {
	if isAgent {
		h.Set("X-Initiator", "agent")
	} else {
		h.Set("X-Initiator", "user")
	}
}

// CopilotURL builds a full Copilot API URL.
func CopilotURL(path string) string {
	return fmt.Sprintf("%s%s", GetBaseURL(state.Global.GetAccountType()), path)
}
