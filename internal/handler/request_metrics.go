package handler

import (
	"net/http"
	"time"

	chimw "github.com/go-chi/chi/v5/middleware"

	"github.com/tonghaoch/copilot-proxy-go/internal/state"
)

type trackedRequest struct {
	Writer chimw.WrapResponseWriter
	Record *state.RequestRecord
	start  time.Time
}

func trackRequest(w http.ResponseWriter, r *http.Request, endpoint string) *trackedRequest {
	start := time.Now()
	return &trackedRequest{
		Writer: chimw.NewWrapResponseWriter(w, r.ProtoMajor),
		Record: &state.RequestRecord{Timestamp: start, Endpoint: endpoint},
		start:  start,
	}
}

func (t *trackedRequest) Finish() {
	t.Record.LatencyMs = time.Since(t.start).Milliseconds()
	t.Record.StatusCode = t.Writer.Status()
	if t.Record.StatusCode == 0 {
		t.Record.StatusCode = http.StatusOK
	}
	state.Metrics.RecordRequest(*t.Record)
}
