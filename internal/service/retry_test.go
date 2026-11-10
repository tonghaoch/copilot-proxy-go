package service

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/tonghaoch/copilot-proxy-go/internal/api"
)

// countingTransport fails the first failures attempts with err, then succeeds.
type countingTransport struct {
	attempts atomic.Int32
	failures int32
	err      error
}

func (t *countingTransport) Do(req *http.Request) (*http.Response, error) {
	n := t.attempts.Add(1)
	if n <= t.failures {
		return nil, t.err
	}
	return &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(strings.NewReader(`{"ok":true}`)),
		Header:     http.Header{},
	}, nil
}

func noWaitPolicy() RetryPolicy {
	return RetryPolicy{Wait: func(context.Context, time.Duration) error { return nil }}
}

func TestTransportErrorIsRetried(t *testing.T) {
	// A dropped connection is the most common upstream failure; before this was
	// retried it surfaced to the caller while 502s were retried happily.
	transport := &countingTransport{failures: 2, err: errors.New("connection reset by peer")}
	client := NewClientWithOptions(ClientOptions{
		HTTPClient:   transport,
		BuildHeaders: func() http.Header { return http.Header{} },
		BuildURL:     func(path string) string { return "https://example.invalid" + path },
		Retry:        noWaitPolicy(),
	})

	resp, err := client.ProxyResponses(context.Background(), []byte(`{}`), false, false)
	if err != nil {
		t.Fatalf("want success after retries, got %v", err)
	}
	defer resp.Body.Close()
	if got := transport.attempts.Load(); got != 3 {
		t.Errorf("attempts = %d, want 3", got)
	}
}

func TestRetryTrackerCountsOnlyAttemptsActuallyRetried(t *testing.T) {
	for _, tt := range []struct {
		name     string
		waitErr  error
		want     int64
		attempts int32
	}{
		{"successful retry", nil, 2, 3},
		{"retry canceled before another attempt", context.Canceled, 0, 1},
	} {
		t.Run(tt.name, func(t *testing.T) {
			transport := &countingTransport{failures: 2, err: errors.New("connection reset")}
			client := NewClientWithOptions(ClientOptions{
				HTTPClient: transport, BuildHeaders: func() http.Header { return http.Header{} },
				BuildURL: func(path string) string { return "https://example.invalid" + path },
				Retry:    RetryPolicy{Wait: func(context.Context, time.Duration) error { return tt.waitErr }},
			})
			ctx, tracker := WithRetryTracker(context.Background())
			resp, _ := client.ProxyResponses(ctx, []byte(`{}`), false, false)
			if resp != nil {
				resp.Body.Close()
			}
			if tracker.Uncertain() != tt.want || transport.attempts.Load() != tt.attempts {
				t.Fatalf("uncertain retries=%d attempts=%d", tracker.Uncertain(), transport.attempts.Load())
			}
		})
	}
}

func TestTransportErrorGivesUpAfterMaxAttempts(t *testing.T) {
	transport := &countingTransport{failures: 99, err: errors.New("connection reset by peer")}
	client := NewClientWithOptions(ClientOptions{
		HTTPClient:   transport,
		BuildHeaders: func() http.Header { return http.Header{} },
		BuildURL:     func(path string) string { return "https://example.invalid" + path },
		Retry:        noWaitPolicy(),
	})

	if _, err := client.ProxyResponses(context.Background(), []byte(`{}`), false, false); err == nil {
		t.Fatal("want an error once attempts are exhausted")
	}
	if got := transport.attempts.Load(); got != 3 {
		t.Errorf("attempts = %d, want 3 (MaxAttempts)", got)
	}
}

func TestCancelledContextIsNotRetried(t *testing.T) {
	// The caller is gone; retrying only wastes an upstream request.
	transport := &countingTransport{failures: 99, err: errors.New("context canceled")}
	client := NewClientWithOptions(ClientOptions{
		HTTPClient:   transport,
		BuildHeaders: func() http.Header { return http.Header{} },
		BuildURL:     func(path string) string { return "https://example.invalid" + path },
		Retry:        noWaitPolicy(),
	})

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if _, err := client.ProxyResponses(ctx, []byte(`{}`), false, false); err == nil {
		t.Fatal("want an error for a cancelled context")
	}
	if got := transport.attempts.Load(); got != 1 {
		t.Errorf("attempts = %d, want 1 (no retry after cancellation)", got)
	}
}

// rateLimitTransport always answers 429 with the given Retry-After.
type rateLimitTransport struct {
	attempts   atomic.Int32
	retryAfter string
}

func (t *rateLimitTransport) Do(*http.Request) (*http.Response, error) {
	t.attempts.Add(1)
	header := http.Header{}
	if t.retryAfter != "" {
		header.Set("Retry-After", t.retryAfter)
	}
	return &http.Response{
		StatusCode: http.StatusTooManyRequests,
		Body:       io.NopCloser(strings.NewReader(`{"error":"rate limited"}`)),
		Header:     header,
	}, nil
}

func TestLongRateLimitIsReturnedNotRetried(t *testing.T) {
	// Retrying inside the rate-limit window spends another premium request on a
	// response that is bound to be another 429.
	transport := &rateLimitTransport{retryAfter: "60"}
	client := NewClientWithOptions(ClientOptions{
		HTTPClient:   transport,
		BuildHeaders: func() http.Header { return http.Header{} },
		BuildURL:     func(path string) string { return "https://example.invalid" + path },
		Retry:        noWaitPolicy(),
	})

	if _, err := client.ProxyResponses(context.Background(), []byte(`{}`), false, false); err == nil {
		t.Fatal("want the 429 surfaced to the caller")
	}
	if got := transport.attempts.Load(); got != 1 {
		t.Errorf("attempts = %d, want 1 (no retry beyond MaxRetryAfter)", got)
	}
}

func TestShortRateLimitIsRetried(t *testing.T) {
	transport := &rateLimitTransport{retryAfter: "1"}
	client := NewClientWithOptions(ClientOptions{
		HTTPClient:   transport,
		BuildHeaders: func() http.Header { return http.Header{} },
		BuildURL:     func(path string) string { return "https://example.invalid" + path },
		Retry:        noWaitPolicy(),
	})

	if _, err := client.ProxyResponses(context.Background(), []byte(`{}`), false, false); err == nil {
		t.Fatal("want an error after retries are exhausted")
	}
	if got := transport.attempts.Load(); got != 3 {
		t.Errorf("attempts = %d, want 3 (a short window is worth waiting out)", got)
	}
}

func TestRetryAfterIsNotClampedToMaxDelay(t *testing.T) {
	// Clamping to MaxDelay would retry while the window is still open.
	resp := &http.Response{Header: http.Header{}}
	resp.Header.Set("Retry-After", "60")
	policy := normalizeRetryPolicy(RetryPolicy{})

	if got := retryDelay(resp, 0, policy); got != 60*time.Second {
		t.Errorf("retryDelay = %v, want 60s", got)
	}
}

func TestIncompleteUploadRetriesOnFreshConnection(t *testing.T) {
	var mu sync.Mutex
	var addresses, bodies []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read request: %v", err)
		}
		mu.Lock()
		addresses = append(addresses, r.RemoteAddr)
		bodies = append(bodies, string(body))
		attempt := len(addresses)
		mu.Unlock()
		if attempt == 1 {
			w.WriteHeader(http.StatusRequestTimeout)
			io.WriteString(w, `{"error":{"message":"Timed out reading request body. Try again, or use a smaller request size."}}`)
			return
		}
		io.WriteString(w, `{"ok":true}`)
	}))
	defer srv.Close()
	client := NewClientWithOptions(ClientOptions{
		HTTPClient: srv.Client(), BuildHeaders: func() http.Header { return http.Header{} },
		BuildURL: func(path string) string { return srv.URL + path }, Retry: noWaitPolicy(),
	})
	resp, err := client.ProxyMessages(context.Background(), []byte(`{"model":"test"}`), "", false, false)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	mu.Lock()
	defer mu.Unlock()
	if len(addresses) != 2 || addresses[0] == addresses[1] {
		t.Fatalf("expected a fresh connection, got %v", addresses)
	}
	if bodies[0] != `{"model":"test"}` || bodies[1] != bodies[0] {
		t.Fatalf("request body was not replayed: %v", bodies)
	}
}

func TestGenericTimeoutIsNotRetried(t *testing.T) {
	var attempts atomic.Int32
	client := NewClientWithOptions(ClientOptions{
		HTTPClient: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			attempts.Add(1)
			return &http.Response{StatusCode: http.StatusRequestTimeout, Header: http.Header{},
				Body: io.NopCloser(strings.NewReader(`{"error":{"message":"model timed out"}}`))}, nil
		})},
		BuildHeaders: func() http.Header { return http.Header{} },
		BuildURL:     func(path string) string { return "https://example.invalid" + path },
		Retry:        noWaitPolicy(),
	})
	_, err := client.ProxyMessages(context.Background(), []byte(`{}`), "", false, false)
	var upstream *api.HTTPError
	if !errors.As(err, &upstream) || upstream.StatusCode != http.StatusRequestTimeout || attempts.Load() != 1 {
		t.Fatalf("got error %v and %d attempts", err, attempts.Load())
	}
}

func TestRejectedUploadAfterUncertainAttemptRemainsUncertain(t *testing.T) {
	var attempts atomic.Int32
	client := NewClientWithOptions(ClientOptions{
		HTTPClient: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			if attempts.Add(1) == 1 {
				return nil, errors.New("connection reset")
			}
			return &http.Response{StatusCode: http.StatusRequestTimeout, Header: http.Header{},
				Body: io.NopCloser(strings.NewReader(`{"error":{"message":"Timed out reading request body. Try again, or use a smaller request size."}}`))}, nil
		})},
		BuildHeaders: func() http.Header { return http.Header{} },
		BuildURL:     func(path string) string { return "https://example.invalid" + path },
		Retry:        noWaitPolicy(),
	})
	_, err := client.ProxyMessages(context.Background(), []byte(`{}`), "", false, false)
	var upstream *api.HTTPError
	if !errors.As(err, &upstream) || !upstream.PriorAttemptUncertain || attempts.Load() != 2 {
		t.Fatalf("got error %v and %d attempts", err, attempts.Load())
	}
}
