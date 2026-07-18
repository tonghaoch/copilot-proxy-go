package service

import (
	"context"
	"errors"
	"net/http"
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
