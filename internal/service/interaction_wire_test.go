package service

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/tonghaoch/copilot-proxy-go/internal/api"
)

// TestProxyMessagesSendsInteractionTypeOnWire runs the real upstream request
// path and checks the headers that reach the server, not just the builder.
func TestProxyMessagesSendsInteractionTypeOnWire(t *testing.T) {
	var got []map[string]string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = append(got, map[string]string{
			"type":  r.Header.Get("X-Interaction-Type"),
			"task":  r.Header.Get("X-Agent-Task-Id"),
			"reqid": r.Header.Get("X-Request-Id"),
		})
		io.Copy(io.Discard, r.Body)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	client := NewClientWithOptions(ClientOptions{
		HTTPClient:   srv.Client(),
		BuildHeaders: func() http.Header { return api.BuildCopilotHeaders("tok", "1.109.3") },
		BuildURL:     func(path string) string { return srv.URL + path },
	})

	ctxs := []context.Context{
		context.Background(),
		WithSubagentInteraction(context.Background()),
	}
	for _, ctx := range ctxs {
		resp, err := client.ProxyMessages(ctx, []byte(`{}`), "", false, true)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
	}

	if len(got) != 2 {
		t.Fatalf("want 2 upstream requests, got %d", len(got))
	}
	for i, want := range []string{api.InteractionAgent, api.InteractionSubagent} {
		if got[i]["type"] != want {
			t.Fatalf("request %d X-Interaction-Type = %q, want %q", i, got[i]["type"], want)
		}
		if got[i]["task"] == "" || got[i]["task"] != got[i]["reqid"] {
			t.Fatalf("request %d X-Agent-Task-Id %q must equal X-Request-Id %q", i, got[i]["task"], got[i]["reqid"])
		}
	}
}
