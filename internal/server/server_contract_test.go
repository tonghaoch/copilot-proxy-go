package server_test

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/tonghaoch/copilot-proxy-go/internal/api"
	"github.com/tonghaoch/copilot-proxy-go/internal/config"
	"github.com/tonghaoch/copilot-proxy-go/internal/handler"
	"github.com/tonghaoch/copilot-proxy-go/internal/server"
	"github.com/tonghaoch/copilot-proxy-go/internal/state"
)

type contractCopilot struct {
	mu       sync.Mutex
	requests int
	err      error
}

func TestServerTimeoutsAllowLongLivedStreams(t *testing.T) {
	srv := server.NewWithHandler(server.Options{}, handler.New(handler.Dependencies{}))
	if srv.WriteTimeout != 0 {
		t.Fatalf("streaming server must not have a global write timeout: %s", srv.WriteTimeout)
	}
	if srv.ReadHeaderTimeout <= 0 {
		t.Fatal("expected a read-header timeout")
	}
}

func TestResponsesContractIsConcurrencySafe(t *testing.T) {
	appState := &state.State{}
	appState.SetModels([]state.Model{{ID: "test-model", SupportedEndpoints: []string{"/responses"}}})
	upstream := &contractCopilot{}
	metrics := &contractMetrics{}
	endpoints := handler.New(handler.Dependencies{
		State: appState, Metrics: metrics, Copilot: upstream, HTTP: http.DefaultClient,
	})
	srv := server.NewWithHandler(server.Options{}, endpoints)

	const concurrency = 32
	var wg sync.WaitGroup
	errs := make(chan string, concurrency)
	for range concurrency {
		wg.Add(1)
		go func() {
			defer wg.Done()
			req := httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewBufferString(
				`{"model":"test-model","input":"hello"}`,
			))
			req.Header.Set("Content-Type", "application/json")
			setTestAuthorization(req)
			recorder := httptest.NewRecorder()
			srv.Handler.ServeHTTP(recorder, req)
			if recorder.Code != http.StatusOK {
				errs <- recorder.Body.String()
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("concurrent request failed: %s", err)
	}
	upstream.mu.Lock()
	requests := upstream.requests
	upstream.mu.Unlock()
	metrics.mu.Lock()
	records := len(metrics.records)
	metrics.mu.Unlock()
	if requests != concurrency || records != concurrency {
		t.Fatalf("requests=%d records=%d", requests, records)
	}
}

func (*contractCopilot) FetchModels(context.Context) ([]state.Model, error) { return nil, nil }
func (*contractCopilot) ProxyChatCompletionEx(context.Context, []byte, bool, bool) (*http.Response, error) {
	panic("unexpected chat request")
}
func (*contractCopilot) ProxyMessages(context.Context, []byte, string, bool, bool) (*http.Response, error) {
	panic("unexpected messages request")
}
func (c *contractCopilot) ProxyResponses(_ context.Context, _ []byte, _, _ bool) (*http.Response, error) {
	c.mu.Lock()
	c.requests++
	c.mu.Unlock()
	if c.err != nil {
		return nil, c.err
	}
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     make(http.Header),
		Body: io.NopCloser(bytes.NewBufferString(
			`{"id":"resp_contract","object":"response","status":"completed","model":"test-model","output":[],"usage":{"input_tokens":3,"output_tokens":2,"total_tokens":5}}`,
		)),
	}, nil
}

func TestResponsesContractMapsUpstreamRateLimit(t *testing.T) {
	appState := &state.State{}
	appState.SetModels([]state.Model{{ID: "test-model", SupportedEndpoints: []string{"/responses"}}})
	upstream := &contractCopilot{err: &api.HTTPError{
		Message: "slow down", StatusCode: http.StatusTooManyRequests,
		Header: http.Header{"Retry-After": []string{"5"}},
	}}
	metrics := &contractMetrics{}
	endpoints := handler.New(handler.Dependencies{
		State: appState, Metrics: metrics, Copilot: upstream, HTTP: http.DefaultClient,
	})
	srv := server.NewWithHandler(server.Options{}, endpoints)
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewBufferString(
		`{"model":"test-model","input":"hello"}`,
	))
	req.Header.Set("Content-Type", "application/json")
	setTestAuthorization(req)
	recorder := httptest.NewRecorder()
	srv.Handler.ServeHTTP(recorder, req)
	if recorder.Code != http.StatusTooManyRequests {
		t.Fatalf("expected 429, got %d: %s", recorder.Code, recorder.Body.String())
	}
	if recorder.Header().Get("Retry-After") != "5" {
		t.Fatal("Retry-After header was not forwarded")
	}
	if !bytes.Contains(recorder.Body.Bytes(), []byte(`"type":"rate_limit_error"`)) {
		t.Fatalf("unexpected error body: %s", recorder.Body.String())
	}
}
func (*contractCopilot) ProxyEmbeddings(context.Context, []byte) (*http.Response, error) {
	panic("unexpected embeddings request")
}

type contractMetrics struct {
	mu      sync.Mutex
	records []state.RequestRecord
}

func (m *contractMetrics) RecordRequest(record state.RequestRecord) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.records = append(m.records, record)
}
func (*contractMetrics) UpdateSession(state.SessionSnapshot) {}
func (*contractMetrics) Snapshot() state.MetricsSnapshot     { return state.MetricsSnapshot{} }

func TestResponsesContractUsesInjectedDependencies(t *testing.T) {
	appState := &state.State{}
	appState.SetModels([]state.Model{{ID: "test-model", SupportedEndpoints: []string{"/responses"}}})
	upstream := &contractCopilot{}
	metrics := &contractMetrics{}
	endpoints := handler.New(handler.Dependencies{
		State: appState, Metrics: metrics, Copilot: upstream, HTTP: http.DefaultClient,
	})
	srv := server.NewWithHandler(server.Options{}, endpoints)

	req, err := http.NewRequest(http.MethodPost, "/v1/responses", bytes.NewBufferString(
		`{"model":"test-model","input":"hello"}`,
	))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	setTestAuthorization(req)
	recorder := httptest.NewRecorder()
	srv.Handler.ServeHTTP(recorder, req)
	if recorder.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", recorder.Code, recorder.Body.String())
	}
	if recorder.Header().Get("X-Request-ID") == "" {
		t.Fatal("expected response request ID")
	}

	upstream.mu.Lock()
	requests := upstream.requests
	upstream.mu.Unlock()
	if requests != 1 {
		t.Fatalf("expected one injected upstream request, got %d", requests)
	}
	metrics.mu.Lock()
	defer metrics.mu.Unlock()
	if len(metrics.records) != 1 || metrics.records[0].InputTokens != 3 || metrics.records[0].StatusCode != http.StatusOK {
		t.Fatalf("unexpected metrics: %+v", metrics.records)
	}
}

func TestResponsesContractRejectsMalformedJSONBeforeUpstream(t *testing.T) {
	appState := &state.State{}
	upstream := &contractCopilot{}
	metrics := &contractMetrics{}
	endpoints := handler.New(handler.Dependencies{
		State: appState, Metrics: metrics, Copilot: upstream, HTTP: http.DefaultClient,
	})
	srv := server.NewWithHandler(server.Options{}, endpoints)

	req, _ := http.NewRequest(http.MethodPost, "/v1/responses", bytes.NewBufferString(`{"model":`))
	req.Header.Set("Content-Type", "application/json")
	setTestAuthorization(req)
	recorder := httptest.NewRecorder()
	srv.Handler.ServeHTTP(recorder, req)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", recorder.Code)
	}
	upstream.mu.Lock()
	defer upstream.mu.Unlock()
	if upstream.requests != 0 {
		t.Fatal("malformed request reached upstream")
	}
}

func setTestAuthorization(req *http.Request) {
	if keys := config.GetAPIKeys(); len(keys) > 0 {
		req.Header.Set("Authorization", "Bearer "+keys[0])
	}
}
