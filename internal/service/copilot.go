package service

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"github.com/tonghaoch/copilot-proxy-go/internal/api"
	"github.com/tonghaoch/copilot-proxy-go/internal/state"
)

// FetchModels retrieves available models from the Copilot API.
func FetchModels() ([]state.Model, error) {
	req, err := http.NewRequest(http.MethodGet, api.CopilotURL("/models"), nil)
	if err != nil {
		return nil, fmt.Errorf("creating models request: %w", err)
	}
	req.Header = api.BuildCopilotHeadersFromState()

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetching models: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, api.NewHTTPError(resp)
	}

	var result state.ModelsResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decoding models response: %w", err)
	}
	return result.Data, nil
}

// ProxyChatCompletion forwards a chat completion request to the Copilot API.
// It returns the raw HTTP response for the caller to handle (streaming or not).
func ProxyChatCompletion(body []byte, isAgent bool) (*http.Response, error) {
	req, err := http.NewRequest(http.MethodPost, api.CopilotURL("/chat/completions"), bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("creating chat completion request: %w", err)
	}

	req.Header = api.BuildCopilotHeadersFromState()
	api.SetInitiatorHeader(req.Header, isAgent)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("proxying chat completion: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		defer resp.Body.Close()
		return nil, api.NewHTTPError(resp)
	}

	return resp, nil
}

// ChatCompletionPayload contains the fields we need to inspect/modify
// in a chat completion request. We use a partial struct to avoid
// defining the entire OpenAI spec.
type ChatCompletionPayload struct {
	Model     string           `json:"model"`
	Stream    bool             `json:"stream"`
	MaxTokens *int             `json:"max_tokens,omitempty"`
	Messages  []map[string]any `json:"messages"`
}

// ParseAndPatchChatCompletion reads the request body, patches max_tokens if
// missing, and determines the initiator. Returns the patched body bytes,
// whether streaming is requested, and whether this is an agent-initiated request.
func ParseAndPatchChatCompletion(body io.Reader) ([]byte, bool, bool, error) {
	raw, err := io.ReadAll(body)
	if err != nil {
		return nil, false, false, fmt.Errorf("reading request body: %w", err)
	}

	// Parse into a generic map so we can patch without losing fields
	var payload map[string]any
	if err := json.Unmarshal(raw, &payload); err != nil {
		return nil, false, false, fmt.Errorf("parsing request body: %w", err)
	}

	// Parse the fields we care about
	var parsed ChatCompletionPayload
	json.Unmarshal(raw, &parsed)

	isStream := parsed.Stream

	// Auto-fill max_tokens from model capabilities if missing
	if parsed.MaxTokens == nil {
		if model := state.Global.FindModel(parsed.Model); model != nil {
			maxOut := model.Capabilities.Limits.MaxOutputTokens
			if maxOut > 0 {
				payload["max_tokens"] = maxOut
			}
		}
	}

	// Detect initiator: if last message is from assistant or tool, it's agent-initiated
	isAgent := false
	if len(parsed.Messages) > 0 {
		lastMsg := parsed.Messages[len(parsed.Messages)-1]
		if role, ok := lastMsg["role"].(string); ok {
			isAgent = role == "assistant" || role == "tool"
		}
	}

	// Re-marshal the patched payload
	patched, err := json.Marshal(payload)
	if err != nil {
		return nil, false, false, fmt.Errorf("marshaling patched payload: %w", err)
	}

	return patched, isStream, isAgent, nil
}
