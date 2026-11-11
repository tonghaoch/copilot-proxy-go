package service

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/tonghaoch/copilot-proxy-go/internal/api"
)

func TestCanceledRetryPreservesKnownRejection(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusRequestTimeout)
		io.WriteString(w, `{"error":{"message":"Timed out reading request body. Try again, or use a smaller request size."}}`)
	}))
	defer server.Close()
	client := NewClientWithOptions(ClientOptions{
		HTTPClient: server.Client(), BuildHeaders: func() http.Header { return make(http.Header) },
		BuildURL: func(path string) string { return server.URL + path },
		Retry:    RetryPolicy{Wait: func(context.Context, time.Duration) error { return context.Canceled }},
	})
	_, err := client.ProxyMessages(context.Background(), []byte(`{}`), "", false, false)
	var upstream *api.HTTPError
	if !errors.Is(err, context.Canceled) || !errors.As(err, &upstream) || !upstream.IncompleteRequestBody() {
		t.Fatalf("lost cancellation or definite upload rejection: %v", err)
	}
}

func TestIncompleteUploadDoesNotRetryWithoutFreshTransport(t *testing.T) {
	var calls atomic.Int32
	client := NewClientWithOptions(ClientOptions{
		HTTPClient: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			calls.Add(1)
			return &http.Response{StatusCode: http.StatusRequestTimeout, Header: make(http.Header),
				Body: io.NopCloser(strings.NewReader(`{"error":{"message":"Timed out reading request body. Try again, or use a smaller request size."}}`))}, nil
		})},
		BuildHeaders: func() http.Header { return make(http.Header) },
		BuildURL:     func(path string) string { return "https://example.test" + path },
		Retry:        noWaitPolicy(),
	})
	_, err := client.ProxyMessages(context.Background(), []byte(`{}`), "", false, false)
	var upstream *api.HTTPError
	if !errors.As(err, &upstream) || !upstream.IncompleteRequestBody() || calls.Load() != 1 {
		t.Fatalf("error=%v attempts=%d", err, calls.Load())
	}
}
