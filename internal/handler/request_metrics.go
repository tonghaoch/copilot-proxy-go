package handler

import (
	"errors"
	"net/http"
	"time"

	chimw "github.com/go-chi/chi/v5/middleware"

	"github.com/tonghaoch/copilot-proxy-go/internal/api"
	"github.com/tonghaoch/copilot-proxy-go/internal/state"
)

type trackedRequest struct {
	Writer    chimw.WrapResponseWriter
	Record    *state.RequestRecord
	start     time.Time
	metrics   MetricsStore
	budget    *state.TokenBudget
	admission *state.BudgetAdmission
}

func trackRequest(w http.ResponseWriter, r *http.Request, endpoint string, metrics MetricsStore) *trackedRequest {
	start := time.Now()
	return &trackedRequest{
		Writer:  chimw.NewWrapResponseWriter(w, r.ProtoMajor),
		Record:  &state.RequestRecord{Timestamp: start, Endpoint: endpoint},
		start:   start,
		metrics: metrics,
	}
}

func (t *trackedRequest) Finish() {
	t.Record.LatencyMs = time.Since(t.start).Milliseconds()
	t.Record.StatusCode = t.Writer.Status()
	if t.Record.StatusCode == 0 {
		t.Record.StatusCode = http.StatusOK
	}
	if t.admission != nil {
		t.budget.Complete(t.admission, *t.Record)
	}
	t.metrics.RecordRequest(*t.Record)
}

func (h *Handler) admitBudget(w http.ResponseWriter, tracked *trackedRequest, model *state.Model, backend string) bool {
	if h.budget == nil {
		return true
	}
	admission, err := h.budget.Admit(model, backend)
	if err != nil {
		status := http.StatusServiceUnavailable
		if errors.Is(err, state.ErrBudgetExceeded) {
			status = http.StatusTooManyRequests
		}
		api.ForwardError(w, &api.HTTPError{Message: err.Error(), StatusCode: status})
		return false
	}
	tracked.budget, tracked.admission = h.budget, admission
	return true
}

func markUpstreamFailure(rec *state.RequestRecord, err error) {
	var upstream *api.HTTPError
	if errors.As(err, &upstream) && upstream.StatusCode >= 400 && upstream.StatusCode < 500 && upstream.StatusCode != http.StatusRequestTimeout {
		rec.UpstreamRejected = true
	}
}
