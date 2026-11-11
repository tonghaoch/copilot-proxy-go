package service

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/tonghaoch/copilot-proxy-go/internal/api"
)

func TestAnthropicCatalogPaginationAndAliases(t *testing.T) {
	calls := 0
	client, err := NewAnthropicClient(&http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		calls++
		if req.URL.Host != "api.anthropic.com" || req.URL.Path != "/v1/models" ||
			req.Header.Get("Authorization") != "Bearer test-key" || req.Header.Get("Anthropic-Workspace-Id") != "workspace" {
			t.Fatalf("unexpected model request: %s headers=%v", req.URL, req.Header)
		}
		body := `{"data":[{"id":"claude-opus-5-5","display_name":"Opus","max_input_tokens":1000000,"max_tokens":128000,"capabilities":{"thinking":{"types":{"adaptive":{"supported":true}}}}}],"has_more":true,"last_id":"first"}`
		if req.URL.Query().Get("after_id") == "first" {
			body = `{"data":[{"id":"claude-haiku-4-5-20251001","max_input_tokens":200000,"max_tokens":64000}],"has_more":false}`
		}
		return anthropicTestResponse(http.StatusOK, body), nil
	})}, "test-key", "workspace")
	if err != nil {
		t.Fatal(err)
	}
	if err := client.RefreshModels(context.Background()); err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Fatalf("catalog calls = %d, want 2", calls)
	}
	if model := client.FindModel("claude-opus-5-5"); model == nil || !model.Capabilities.Supports.AdaptiveThinking {
		t.Fatalf("adaptive thinking capability = %+v", model)
	}
	model := client.FindModel("claude-haiku-4-5")
	if model == nil || model.ID != "claude-haiku-4-5-20251001" || model.Capabilities.Limits.MaxOutputTokens != 64_000 {
		t.Fatalf("alias lookup = %+v", model)
	}
	model.SupportedEndpoints[0] = "changed"
	if got := client.FindModel("claude-haiku-4-5").SupportedEndpoints[0]; got != "/v1/messages" {
		t.Fatalf("catalog was mutated through a returned model: %q", got)
	}
}

func TestAnthropicMessagesUseConfiguredCredentialAndPreserveProtocolHeaders(t *testing.T) {
	client, err := NewAnthropicClient(&http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.URL.String() != "https://api.anthropic.com/v1/messages" ||
			req.Header.Get("Authorization") != "Bearer server-test-key" ||
			req.Header.Get("X-Api-Key") != "" ||
			req.Header.Get("Anthropic-Version") != "2023-06-01" ||
			req.Header.Get("Anthropic-Beta") != "example-beta,second-beta" ||
			req.Header.Get("Anthropic-User-Profile-Id") != "profile-1" ||
			req.Header.Get("Anthropic-Workspace-Id") != "default-workspace" {
			t.Fatalf("unexpected direct request: %s headers=%v", req.URL, req.Header)
		}
		body, err := io.ReadAll(req.Body)
		if err != nil || string(body) != `{"model":"claude-opus-5-5"}` {
			t.Fatalf("body = %q, err = %v", body, err)
		}
		return anthropicTestResponse(http.StatusOK, `{}`), nil
	})}, "server-test-key", "default-workspace")
	if err != nil {
		t.Fatal(err)
	}
	headers := http.Header{
		"Authorization":             {"Bearer incoming-client-key"},
		"X-Api-Key":                 {"incoming-client-key"},
		"Anthropic-Version":         {"2023-06-01"},
		"Anthropic-Beta":            {"example-beta", "second-beta"},
		"Anthropic-Workspace-Id":    {"requested-workspace"},
		"Anthropic-User-Profile-Id": {"profile-1"},
	}
	resp, err := client.ProxyMessages(context.Background(), []byte(`{"model":"claude-opus-5-5"}`), headers)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
}

func TestAnthropicDefaultVersionSupportsTranslatedSampling(t *testing.T) {
	client, err := NewAnthropicClient(&http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if got := req.Header.Get("Anthropic-Version"); got != "2023-06-01" {
			t.Fatalf("default Anthropic version = %q", got)
		}
		return anthropicTestResponse(http.StatusOK, `{}`), nil
	})}, "test-key", "")
	if err != nil {
		t.Fatal(err)
	}
	resp, err := client.ProxyMessages(context.Background(), []byte(`{"temperature":0.5}`), nil)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
}

func TestAnthropicClientRefusesRedirectAndRedactsErrors(t *testing.T) {
	calls := 0
	client, err := NewAnthropicClient(&http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		calls++
		resp := anthropicTestResponse(http.StatusTemporaryRedirect, `{"error":{"message":"test-key"}}`)
		resp.Header.Set("Location", "https://untrusted.example/path")
		resp.Header.Set("X-Request-Id", "test-key")
		return resp, nil
	})}, "test-key", "")
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.ProxyMessages(context.Background(), []byte(`{}`), nil)
	var upstream *api.HTTPError
	if !errors.As(err, &upstream) || upstream.StatusCode != http.StatusTemporaryRedirect || calls != 1 {
		t.Fatalf("redirect was not rejected: calls=%d err=%v", calls, err)
	}
	if strings.Contains(err.Error(), "test-key") || strings.Contains(upstream.Header.Get("X-Request-Id"), "test-key") {
		t.Fatal("upstream error exposed the configured credential")
	}
}

func TestAnthropicCatalogSelectsNewestMajorAliasAndKeepsLastGoodRefresh(t *testing.T) {
	failedRefresh := false
	client, err := NewAnthropicClient(&http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if failedRefresh {
			return anthropicTestResponse(http.StatusServiceUnavailable, `{"error":{"message":"unavailable"}}`), nil
		}
		return anthropicTestResponse(http.StatusOK, `{"data":[
			{"id":"claude-sonnet-5-2"},
			{"id":"claude-sonnet-5-10-20260101"},
			{"id":"claude-sonnet-5-10-20260909"},
			{"id":"claude-opus-5-20"}]}`), nil
	})}, "test-key", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := client.RefreshModels(context.Background()); err != nil {
		t.Fatal(err)
	}
	for _, alias := range []string{"claude-sonnet-5", "claude-sonnet-5-10"} {
		if model := client.FindModel(alias); model == nil || model.ID != "claude-sonnet-5-10-20260909" {
			t.Fatalf("model for %s = %+v", alias, model)
		}
	}
	failedRefresh = true
	if err := client.RefreshModels(context.Background()); err == nil {
		t.Fatal("expected catalog refresh error")
	}
	if model := client.FindModel("claude-sonnet-5"); model == nil || model.ID != "claude-sonnet-5-10-20260909" {
		t.Fatalf("failed refresh discarded catalog: %+v", model)
	}
}

func TestAnthropicClientRedactsTransportErrorsAndPreservesCause(t *testing.T) {
	sentinel := errors.New("connection failed")
	client, err := NewAnthropicClient(&http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		return nil, fmt.Errorf("credential %s: %w", req.Header.Get("Authorization"), sentinel)
	})}, "test-key", "")
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.ProxyMessages(context.Background(), []byte(`{}`), nil)
	if !errors.Is(err, sentinel) || strings.Contains(err.Error(), "test-key") {
		t.Fatalf("transport error lost its cause or exposed the credential: %v", err)
	}
}

func TestAnthropicTransportErrorDoesNotExposeWrappedHTTPError(t *testing.T) {
	client, err := NewAnthropicClient(&http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return nil, &api.HTTPError{StatusCode: http.StatusBadRequest, Body: `{"error":{"message":"test-key"}}`}
	})}, "test-key", "")
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.ProxyMessages(context.Background(), []byte(`{}`), nil)
	var upstream *api.HTTPError
	if errors.As(err, &upstream) {
		t.Fatal("transport error exposed an unredacted upstream error")
	}
	w := httptest.NewRecorder()
	api.ForwardError(w, err)
	if strings.Contains(w.Body.String(), "test-key") {
		t.Fatalf("transport error exposed the credential: %s", w.Body)
	}
}

func anthropicTestResponse(status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Status:     http.StatusText(status),
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}
