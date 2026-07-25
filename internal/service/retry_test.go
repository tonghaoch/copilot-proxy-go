package service

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"
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
