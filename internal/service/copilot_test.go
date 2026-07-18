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

	"github.com/tonghaoch/copilot-proxy-go/internal/api"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

func TestProxyRequestPropagatesCancellation(t *testing.T) {
	requestStarted := make(chan struct{})
	client := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		close(requestStarted)
		<-req.Context().Done()
		return nil, req.Context().Err()
	})}
	restore := api.SetHTTPClient(client)
	defer restore()

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := ProxyResponses(ctx, []byte(`{}`), false, false)
		done <- err
	}()
	<-requestStarted
	cancel()

	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("expected context cancellation, got %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("upstream request did not stop after cancellation")
	}
}

func TestClientRefreshesAndReplaysRequestOnce(t *testing.T) {
	var attempts atomic.Int32
	var refreshes atomic.Int32
	client := NewClient(&http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		attempt := attempts.Add(1)
		body, err := io.ReadAll(req.Body)
		if err != nil {
			t.Fatal(err)
		}
		if string(body) != `{"model":"test"}` {
			t.Fatalf("request body was not replayed: %s", body)
		}
		status := http.StatusOK
		if attempt == 1 {
			status = http.StatusUnauthorized
		}
		return &http.Response{
			StatusCode: status,
			Status:     http.StatusText(status),
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(`{}`)),
		}, nil
	})}, func() error {
		refreshes.Add(1)
		return nil
	})

	resp, err := client.ProxyResponses(context.Background(), []byte(`{"model":"test"}`), false, false)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if attempts.Load() != 2 || refreshes.Load() != 1 {
		t.Fatalf("expected two attempts and one refresh, got attempts=%d refreshes=%d", attempts.Load(), refreshes.Load())
	}
}
