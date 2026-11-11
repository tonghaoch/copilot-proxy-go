package server_test

import (
	"context"
	"errors"
	"io"
	"math"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/tonghaoch/copilot-proxy-go/internal/handler"
	"github.com/tonghaoch/copilot-proxy-go/internal/server"
	"github.com/tonghaoch/copilot-proxy-go/internal/service"
	"github.com/tonghaoch/copilot-proxy-go/internal/state"
)

type retryBudgetTransport struct {
	calls       int
	failures    int
	finalStatus int
}

func (c *retryBudgetTransport) Do(*http.Request) (*http.Response, error) {
	c.calls++
	if c.calls <= c.failures {
		return nil, errors.New("connection reset")
	}
	body := `{"id":"m","type":"message","role":"assistant","model":"test-model","content":[],"stop_reason":"end_turn","usage":{"input_tokens":10,"output_tokens":5,"cache_creation_input_tokens":4,"cache_creation":{"ephemeral_5m_input_tokens":0,"ephemeral_1h_input_tokens":4}}}`
	if c.finalStatus == http.StatusRequestTimeout {
		body = `{"error":{"message":"Timed out reading request body. Try again, or use a smaller request size."}}`
	}
	return &http.Response{StatusCode: c.finalStatus, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}, nil
}

func TestBudgetAccountsEarlierUncertainAttempts(t *testing.T) {
	for _, tt := range []struct {
		name        string
		failures    int
		finalStatus int
		reported    float64
		reserved    float64
	}{
		{"success after two lost responses", 2, http.StatusOK, 0.028, 4.4},
		{"rejected upload after a lost response", 1, http.StatusRequestTimeout, 0, 2.2},
	} {
		t.Run(tt.name, func(t *testing.T) {
			transport := &retryBudgetTransport{failures: tt.failures, finalStatus: tt.finalStatus}
			upstream := service.NewClientWithOptions(service.ClientOptions{
				HTTPClient:   transport,
				BuildHeaders: func() http.Header { return make(http.Header) },
				BuildURL:     func(path string) string { return "https://example.test" + path },
				Retry:        service.RetryPolicy{Wait: func(context.Context, time.Duration) error { return nil }},
			})
			model := pricedBudgetModel(t, []string{"/v1/messages"})
			model.Capabilities.Limits = state.ModelLimits{MaxContextWindowTokens: 1000, MaxOutputTokens: 100}
			models := &state.State{}
			models.SetModels([]state.Model{model})
			budget, err := state.NewTokenBudget(100)
			if err != nil {
				t.Fatal(err)
			}
			h := handler.New(handler.Dependencies{State: models, Metrics: &contractMetrics{}, Copilot: upstream, Budget: budget})
			srv := server.NewWithHandler(server.Options{}, h)
			response := serveBudgetRequest(srv, http.MethodPost, "/v1/messages",
				`{"model":"test-model","max_tokens":20,"messages":[{"role":"user","content":"hello"}]}`)
			got := budget.Snapshot()
			if response.Code != tt.finalStatus || transport.calls != tt.failures+1 ||
				math.Abs(got.ReportedUsageUSD-tt.reported) > 1e-9 ||
				math.Abs(got.ReservedUSD-tt.reserved) > 1e-9 || got.ConservativeEstimates != int64(tt.failures) {
				t.Fatalf("status=%d calls=%d budget=%+v body=%s", response.Code, transport.calls, got, response.Body.String())
			}
		})
	}
}
