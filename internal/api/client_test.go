package api

import (
	"net/http"
	"testing"
)

func TestHTTPClientConnectionPool(t *testing.T) {
	client := newHTTPClient(false)
	transport, ok := client.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("unexpected transport %T", client.Transport)
	}
	if transport.MaxIdleConns != maxIdleConnections {
		t.Fatalf("MaxIdleConns=%d", transport.MaxIdleConns)
	}
	if transport.MaxIdleConnsPerHost != maxIdleConnectionsPerHost {
		t.Fatalf("MaxIdleConnsPerHost=%d", transport.MaxIdleConnsPerHost)
	}
	if transport.MaxConnsPerHost != 0 {
		t.Fatalf("MaxConnsPerHost should remain unlimited, got %d", transport.MaxConnsPerHost)
	}
	if !transport.ForceAttemptHTTP2 {
		t.Fatal("HTTP/2 should remain enabled")
	}
}
