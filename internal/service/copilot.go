package service

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/tonghaoch/copilot-proxy-go/internal/api"
	"github.com/tonghaoch/copilot-proxy-go/internal/auth"
	"github.com/tonghaoch/copilot-proxy-go/internal/state"
)

// HTTPDoer is the small portion of http.Client needed by the Copilot client.
// Keeping this boundary narrow makes upstream behavior testable without a
// process-wide HTTP client replacement.
type HTTPDoer interface {
	Do(*http.Request) (*http.Response, error)
}

// Client owns the dependencies required to call Copilot APIs.
type Client struct {
	httpClient   func() HTTPDoer
	refreshToken func() error
	buildHeaders func() http.Header
	buildURL     func(string) string
	retry        RetryPolicy
}

type RetryPolicy struct {
	MaxAttempts int
	BaseDelay   time.Duration
	MaxDelay    time.Duration
	Wait        func(context.Context, time.Duration) error
}

type ClientOptions struct {
	HTTPClient   HTTPDoer
	RefreshToken func() error
	BuildHeaders func() http.Header
	BuildURL     func(string) string
	Retry        RetryPolicy
}

// NewClient creates an independently testable Copilot client.
func NewClient(httpClient HTTPDoer, refreshToken func() error) *Client {
	return NewClientWithOptions(ClientOptions{HTTPClient: httpClient, RefreshToken: refreshToken})
}

func NewClientWithOptions(opts ClientOptions) *Client {
	if opts.BuildHeaders == nil {
		opts.BuildHeaders = api.BuildCopilotHeadersFromState
	}
	if opts.BuildURL == nil {
		opts.BuildURL = api.CopilotURL
	}
	opts.Retry = normalizeRetryPolicy(opts.Retry)
	return &Client{
		httpClient: func() HTTPDoer { return opts.HTTPClient }, refreshToken: opts.RefreshToken,
		buildHeaders: opts.BuildHeaders, buildURL: opts.BuildURL, retry: opts.Retry,
	}
}

var defaultClient = &Client{
	httpClient:   func() HTTPDoer { return api.HTTPClient() },
	refreshToken: auth.RefreshCopilotTokenNow,
	buildHeaders: api.BuildCopilotHeadersFromState,
	buildURL:     api.CopilotURL,
	retry:        normalizeRetryPolicy(RetryPolicy{}),
}

// DefaultClient returns the process-wide Copilot client used by the CLI.
func DefaultClient() *Client { return defaultClient }

// isTokenExpired checks if the response indicates an expired/invalid token.
func isTokenExpired(statusCode int) bool {
	return statusCode == http.StatusUnauthorized || statusCode == http.StatusForbidden
}

// FetchModels retrieves available models from the Copilot API.
func FetchModels(ctx context.Context) ([]state.Model, error) {
	return defaultClient.FetchModels(ctx)
}

// FetchModels retrieves available models using this client's dependencies.
func (c *Client) FetchModels(ctx context.Context) ([]state.Model, error) {
	resp, err := c.doCopilotRequest(ctx, http.MethodGet, "/models", nil, nil)
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
// Used by the /chat/completions passthrough endpoint.
func ProxyChatCompletion(ctx context.Context, body []byte, isAgent bool) (*http.Response, error) {
	return ProxyChatCompletionEx(ctx, body, isAgent, false)
}

// ProxyChatCompletionEx forwards a chat completion request with vision support.
// Used by the Messages handler when routing through Chat Completions backend.
func ProxyChatCompletionEx(ctx context.Context, body []byte, isAgent, vision bool) (*http.Response, error) {
	return defaultClient.ProxyChatCompletionEx(ctx, body, isAgent, vision)
}

func (c *Client) ProxyChatCompletionEx(ctx context.Context, body []byte, isAgent, vision bool) (*http.Response, error) {
	return c.doCopilotRequest(ctx, http.MethodPost, "/chat/completions", body, requestHeaders(isAgent, vision, ""))
}

// ProxyMessages forwards a request to the Copilot native Messages API.
func ProxyMessages(ctx context.Context, body []byte, betaHeader string, vision, isAgent bool) (*http.Response, error) {
	return defaultClient.ProxyMessages(ctx, body, betaHeader, vision, isAgent)
}

func (c *Client) ProxyMessages(ctx context.Context, body []byte, betaHeader string, vision, isAgent bool) (*http.Response, error) {
	return c.doCopilotRequest(ctx, http.MethodPost, "/v1/messages", body, requestHeaders(isAgent, vision, betaHeader))
}

// ProxyResponses forwards a request to the Copilot Responses API.
func ProxyResponses(ctx context.Context, body []byte, isAgent, vision bool) (*http.Response, error) {
	return defaultClient.ProxyResponses(ctx, body, isAgent, vision)
}

func (c *Client) ProxyResponses(ctx context.Context, body []byte, isAgent, vision bool) (*http.Response, error) {
	return c.doCopilotRequest(ctx, http.MethodPost, "/responses", body, requestHeaders(isAgent, vision, ""))
}

// ProxyEmbeddings forwards a request to the Copilot Embeddings API.
func ProxyEmbeddings(ctx context.Context, body []byte) (*http.Response, error) {
	return defaultClient.ProxyEmbeddings(ctx, body)
}

func (c *Client) ProxyEmbeddings(ctx context.Context, body []byte) (*http.Response, error) {
	return c.doCopilotRequest(ctx, http.MethodPost, "/embeddings", body, nil)
}

func requestHeaders(isAgent, vision bool, betaHeader string) func(http.Header) {
	return func(header http.Header) {
		api.SetInitiatorHeader(header, isAgent)
		if vision {
			header.Set("Copilot-Vision-Request", "true")
		}
		if betaHeader != "" {
			header.Set("Anthropic-Beta", betaHeader)
		}
	}
}

func (c *Client) doCopilotRequest(ctx context.Context, method, path string, body []byte, configure func(http.Header)) (*http.Response, error) {
	buildRequest := func() (*http.Request, error) {
		var reader io.Reader
		if body != nil {
			reader = bytes.NewReader(body)
		}
		req, err := http.NewRequestWithContext(ctx, method, c.buildURL(path), reader)
		if err != nil {
			return nil, err
		}
		req.Header = c.buildHeaders()
		if configure != nil {
			configure(req.Header)
		}
		return req, nil
	}

	refreshedToken := false
	for attempt := 0; attempt < c.retry.MaxAttempts; attempt++ {
		req, err := buildRequest()
		if err != nil {
			return nil, fmt.Errorf("creating upstream request: %w", err)
		}
		started := time.Now()
		resp, err := c.httpClient().Do(req)
		if err != nil {
			return nil, fmt.Errorf("sending upstream request: %w", err)
		}
		slog.Debug("upstream response", "method", method, "path", path, "status", resp.StatusCode,
			"attempt", attempt+1, "latency", time.Since(started).Round(time.Millisecond),
			"request_id", resp.Header.Get("X-Request-ID"))
		if resp.StatusCode >= http.StatusOK && resp.StatusCode < http.StatusMultipleChoices {
			return resp, nil
		}
		if isTokenExpired(resp.StatusCode) && !refreshedToken {
			drainAndClose(resp.Body)
			refreshedToken = true
			slog.Warn("upstream auth error; refreshing token", "path", path, "status", resp.StatusCode)
			if c.refreshToken == nil {
				return nil, fmt.Errorf("token refresh is not configured")
			}
			if err := c.refreshToken(); err != nil {
				return nil, fmt.Errorf("token refresh failed: %w", err)
			}
			continue
		}
		if isRetryableStatus(resp.StatusCode) && attempt+1 < c.retry.MaxAttempts {
			delay := retryDelay(resp, attempt, c.retry)
			drainAndClose(resp.Body)
			slog.Warn("retrying transient upstream response", "path", path, "status", resp.StatusCode,
				"attempt", attempt+1, "delay", delay)
			if err := c.retry.Wait(ctx, delay); err != nil {
				return nil, fmt.Errorf("waiting to retry upstream request: %w", err)
			}
			continue
		}
		httpErr := api.NewHTTPError(resp)
		resp.Body.Close()
		return nil, httpErr
	}
	return nil, fmt.Errorf("upstream request retry exhausted")
}

func normalizeRetryPolicy(policy RetryPolicy) RetryPolicy {
	if policy.MaxAttempts <= 0 {
		policy.MaxAttempts = 3
	}
	if policy.BaseDelay <= 0 {
		policy.BaseDelay = 200 * time.Millisecond
	}
	if policy.MaxDelay <= 0 {
		policy.MaxDelay = 5 * time.Second
	}
	if policy.Wait == nil {
		policy.Wait = func(ctx context.Context, delay time.Duration) error {
			timer := time.NewTimer(delay)
			defer timer.Stop()
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-timer.C:
				return nil
			}
		}
	}
	return policy
}

func isRetryableStatus(status int) bool {
	return status == http.StatusTooManyRequests || status == http.StatusBadGateway ||
		status == http.StatusServiceUnavailable || status == http.StatusGatewayTimeout
}

func retryDelay(resp *http.Response, attempt int, policy RetryPolicy) time.Duration {
	if value := strings.TrimSpace(resp.Header.Get("Retry-After")); value != "" {
		if seconds, err := strconv.Atoi(value); err == nil && seconds >= 0 {
			return min(time.Duration(seconds)*time.Second, policy.MaxDelay)
		}
		if when, err := http.ParseTime(value); err == nil {
			return min(max(time.Until(when), 0), policy.MaxDelay)
		}
	}
	delay := policy.BaseDelay << attempt
	return min(delay, policy.MaxDelay)
}

func drainAndClose(body io.ReadCloser) {
	_, _ = io.Copy(io.Discard, io.LimitReader(body, 64<<10))
	_ = body.Close()
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
	return ParseAndPatchChatCompletionWithModels(body, state.Global)
}

// ModelFinder supplies model capabilities without coupling parsing to global state.
type ModelFinder interface {
	FindModel(string) *state.Model
}

func ParseAndPatchChatCompletionWithModels(body io.Reader, models ModelFinder) ([]byte, bool, bool, error) {
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
		if model := models.FindModel(parsed.Model); model != nil {
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
