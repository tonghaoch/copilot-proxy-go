package server_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/tonghaoch/copilot-proxy-go/internal/handler"
	"github.com/tonghaoch/copilot-proxy-go/internal/server"
	"github.com/tonghaoch/copilot-proxy-go/internal/service"
	"github.com/tonghaoch/copilot-proxy-go/internal/state"
)

func TestBudgetDoesNotReserveRejectedAttemptsWhenRetryAborts(t *testing.T) {
	for _, status := range []int{http.StatusRequestTimeout, http.StatusTooManyRequests, http.StatusUnauthorized} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			var calls atomic.Int32
			upstreamServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				calls.Add(1)
				w.WriteHeader(status)
				if status == http.StatusRequestTimeout {
					io.WriteString(w, `{"error":{"message":"Timed out reading request body. Try again, or use a smaller request size."}}`)
				}
			}))
			defer upstreamServer.Close()
			upstream := service.NewClientWithOptions(service.ClientOptions{
				HTTPClient:   upstreamServer.Client(),
				BuildHeaders: func() http.Header { return make(http.Header) },
				BuildURL:     func(path string) string { return upstreamServer.URL + path },
				RefreshToken: func() error { return errors.New("refresh failed") },
				Retry:        service.RetryPolicy{Wait: func(context.Context, time.Duration) error { return context.Canceled }},
			})
			models := &state.State{}
			models.SetModels([]state.Model{pricedBudgetModel(t, []string{"/v1/messages"})})
			budget, err := state.NewTokenBudget(100)
			if err != nil {
				t.Fatal(err)
			}
			h := handler.New(handler.Dependencies{State: models, Metrics: &contractMetrics{}, Copilot: upstream, Budget: budget})
			srv := server.NewWithHandler(server.Options{}, h)
			response := serveBudgetRequest(srv, http.MethodPost, "/v1/messages",
				`{"model":"test-model","max_tokens":20,"messages":[{"role":"user","content":"hello"}]}`)
			got := budget.Snapshot()
			if response.Code != status || calls.Load() != 1 || got.Blocked || got.EstimatedSpentUSD != 0 || got.ReservedUSD != 0 {
				t.Fatalf("status=%d calls=%d budget=%+v body=%s", response.Code, calls.Load(), got, response.Body.String())
			}
		})
	}
}
