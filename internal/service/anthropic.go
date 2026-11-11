package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"

	"github.com/tonghaoch/copilot-proxy-go/internal/api"
	"github.com/tonghaoch/copilot-proxy-go/internal/state"
)

const (
	anthropicBaseURL = "https://api.anthropic.com"
	anthropicVersion = "2023-06-01"
)

var directSnapshotSuffix = regexp.MustCompile(`-\d{8}$`)
var directClaudeVersion = regexp.MustCompile(`^(claude-[a-z]+-\d+)(?:-(\d+))?$`)

// AnthropicClient sends first-party requests without exposing the configured key.
type AnthropicClient struct {
	httpClient  *http.Client
	apiKey      string
	workspaceID string
	mu          sync.RWMutex
	models      []state.Model
}

type redactedAnthropicError struct {
	err error
	key string
}

func (e redactedAnthropicError) Error() string {
	return strings.ReplaceAll(e.err.Error(), e.key, "[redacted]")
}

func (e redactedAnthropicError) Is(target error) bool { return errors.Is(e.err, target) }

func NewAnthropicClient(client *http.Client, apiKey, workspaceID string) (*AnthropicClient, error) {
	apiKey = strings.TrimSpace(apiKey)
	if apiKey == "" || strings.ContainsAny(apiKey, "\r\n") {
		return nil, fmt.Errorf("invalid Anthropic API key")
	}
	if client == nil {
		client = api.HTTPClient()
	}
	// Never forward the credential through an upstream redirect.
	directClient := *client
	directClient.CheckRedirect = func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}
	return &AnthropicClient{httpClient: &directClient, apiKey: apiKey, workspaceID: workspaceID}, nil
}

// RefreshModels replaces the direct catalog only after all pages are retrieved.
func (c *AnthropicClient) RefreshModels(ctx context.Context) error {
	var models []state.Model
	seenModels := make(map[string]bool)
	seenCursors := make(map[string]bool)
	after := ""
	for {
		query := url.Values{"limit": {"1000"}}
		if after != "" {
			query.Set("after_id", after)
		}
		resp, err := c.request(ctx, http.MethodGet, "/v1/models?"+query.Encode(), nil, nil)
		if err != nil {
			return fmt.Errorf("fetching Anthropic models: %w", err)
		}
		var page struct {
			Data []struct {
				ID           string `json:"id"`
				DisplayName  string `json:"display_name"`
				Lifecycle    string `json:"lifecycle"`
				MaxInput     int    `json:"max_input_tokens"`
				MaxOutput    int    `json:"max_tokens"`
				Capabilities struct {
					Thinking struct {
						Types struct {
							Adaptive struct {
								Supported bool `json:"supported"`
							} `json:"adaptive"`
						} `json:"types"`
					} `json:"thinking"`
				} `json:"capabilities"`
			} `json:"data"`
			HasMore bool   `json:"has_more"`
			LastID  string `json:"last_id"`
		}
		err = json.NewDecoder(io.LimitReader(resp.Body, 16<<20)).Decode(&page)
		resp.Body.Close()
		if err != nil {
			return fmt.Errorf("decoding Anthropic models: %w", err)
		}
		for _, item := range page.Data {
			if item.ID == "" || item.Lifecycle == "retired" || seenModels[item.ID] {
				continue
			}
			seenModels[item.ID] = true
			name := item.DisplayName
			if name == "" {
				name = item.ID
			}
			models = append(models, state.Model{
				ID: item.ID, Name: name, Vendor: "Anthropic", ModelPickerEnabled: true,
				Policy: state.ModelPolicy{State: "enabled"},
				Capabilities: state.ModelCapabilities{
					Type: "chat",
					Supports: state.ModelSupports{
						AdaptiveThinking: item.Capabilities.Thinking.Types.Adaptive.Supported,
					},
					Limits: state.ModelLimits{
						MaxContextWindowTokens: item.MaxInput, MaxPromptTokens: item.MaxInput,
						MaxOutputTokens: item.MaxOutput, MaxNonStreamingOutput: item.MaxOutput,
					},
				},
				SupportedEndpoints: []string{"/v1/messages"},
			})
		}
		if !page.HasMore {
			break
		}
		if page.LastID == "" || seenCursors[page.LastID] {
			return fmt.Errorf("Anthropic model catalog returned an invalid pagination cursor")
		}
		after = page.LastID
		seenCursors[after] = true
	}
	if len(models) == 0 {
		return fmt.Errorf("Anthropic model catalog is empty")
	}
	c.mu.Lock()
	c.models = models
	c.mu.Unlock()
	return nil
}

func (c *AnthropicClient) Models() []state.Model {
	c.mu.RLock()
	defer c.mu.RUnlock()
	models := make([]state.Model, len(c.models))
	for i, model := range c.models {
		models[i] = copyAnthropicModel(model)
	}
	return models
}

func (c *AnthropicClient) FindModel(id string) *state.Model {
	c.mu.RLock()
	defer c.mu.RUnlock()
	for _, model := range c.models {
		if model.ID == id {
			copy := copyAnthropicModel(model)
			return &copy
		}
	}
	var latest *state.Model
	for i := range c.models {
		model := &c.models[i]
		if directSnapshotSuffix.ReplaceAllString(model.ID, "") == id && (latest == nil || model.ID > latest.ID) {
			latest = model
		}
	}
	if latest != nil {
		copy := copyAnthropicModel(*latest)
		return &copy
	}
	requested := directClaudeVersion.FindStringSubmatch(id)
	if requested == nil || requested[2] != "" {
		return nil
	}
	var best *state.Model
	bestMinor := -1
	for i := range c.models {
		candidate := &c.models[i]
		version := directClaudeVersion.FindStringSubmatch(directSnapshotSuffix.ReplaceAllString(candidate.ID, ""))
		if version == nil || version[1] != requested[1] || version[2] == "" {
			continue
		}
		minor, err := strconv.Atoi(version[2])
		if err == nil && (minor > bestMinor || (minor == bestMinor && best != nil && candidate.ID > best.ID)) {
			best, bestMinor = candidate, minor
		}
	}
	if best != nil {
		copy := copyAnthropicModel(*best)
		return &copy
	}
	return nil
}

func copyAnthropicModel(model state.Model) state.Model {
	model.SupportedEndpoints = append([]string(nil), model.SupportedEndpoints...)
	return model
}

func (c *AnthropicClient) ProxyMessages(ctx context.Context, body []byte, headers http.Header) (*http.Response, error) {
	return c.request(ctx, http.MethodPost, "/v1/messages", body, headers)
}

func (c *AnthropicClient) ProxyCountTokens(ctx context.Context, body []byte, headers http.Header) (*http.Response, error) {
	return c.request(ctx, http.MethodPost, "/v1/messages/count_tokens", body, headers)
}

func (c *AnthropicClient) request(ctx context.Context, method, path string, body []byte, headers http.Header) (*http.Response, error) {
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, anthropicBaseURL+path, reader)
	if err != nil {
		return nil, fmt.Errorf("creating Anthropic request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	version := headers.Get("Anthropic-Version")
	if version == "" {
		version = anthropicVersion
	}
	req.Header.Set("Anthropic-Version", version)
	if beta := strings.Join(headers.Values("Anthropic-Beta"), ","); beta != "" {
		req.Header.Set("Anthropic-Beta", beta)
	}
	if profile := headers.Get("Anthropic-User-Profile-Id"); profile != "" {
		req.Header.Set("Anthropic-User-Profile-Id", profile)
	}
	workspace := c.workspaceID
	if workspace == "" {
		workspace = headers.Get("Anthropic-Workspace-Id")
	}
	if workspace != "" {
		req.Header.Set("Anthropic-Workspace-Id", workspace)
	}
	if userAgent := headers.Get("User-Agent"); userAgent != "" {
		req.Header.Set("User-Agent", userAgent)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("sending Anthropic request: %w", redactedAnthropicError{err: err, key: c.apiKey})
	}
	if resp.StatusCode >= http.StatusOK && resp.StatusCode < http.StatusMultipleChoices {
		return resp, nil
	}
	httpErr := api.NewHTTPError(resp)
	resp.Body.Close()
	httpErr.Message = strings.ReplaceAll(httpErr.Message, c.apiKey, "[redacted]")
	httpErr.Body = strings.ReplaceAll(httpErr.Body, c.apiKey, "[redacted]")
	for key, values := range httpErr.Header {
		for i, value := range values {
			httpErr.Header[key][i] = strings.ReplaceAll(value, c.apiKey, "[redacted]")
		}
	}
	return nil, httpErr
}
