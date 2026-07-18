package api

import (
	"crypto/tls"
	"net/http"
	"sync"
	"time"
)

const (
	maxIdleConnections        = 256
	maxIdleConnectionsPerHost = 64
)

var (
	clientMu   sync.RWMutex
	httpClient = newHTTPClient(false)
)

func newHTTPClient(proxyFromEnvironment bool) *http.Client {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.TLSClientConfig = &tls.Config{MinVersion: tls.VersionTLS12}
	// The Go default retains only two idle connections per host. This proxy has
	// a small number of hot upstream hosts, so a larger pool avoids repeated TCP
	// and TLS setup when concurrent request waves arrive.
	transport.MaxIdleConns = maxIdleConnections
	transport.MaxIdleConnsPerHost = maxIdleConnectionsPerHost
	transport.IdleConnTimeout = 90 * time.Second
	transport.ForceAttemptHTTP2 = true
	if !proxyFromEnvironment {
		transport.Proxy = nil
	}
	return &http.Client{Transport: transport}
}

// HTTPClient returns the shared application client. Its transport preserves
// Go's connection-pool and HTTP/2 defaults; request lifetimes are controlled
// by contexts rather than a client-wide timeout because responses may stream.
func HTTPClient() *http.Client {
	clientMu.RLock()
	defer clientMu.RUnlock()
	return httpClient
}

// ConfigureHTTPClient replaces the application client during startup.
func ConfigureHTTPClient(proxyFromEnvironment bool) {
	clientMu.Lock()
	defer clientMu.Unlock()
	httpClient = newHTTPClient(proxyFromEnvironment)
}

// SetHTTPClient installs a client for tests or embedding. It returns a restore
// function so callers cannot accidentally leave global test state behind.
func SetHTTPClient(client *http.Client) func() {
	clientMu.Lock()
	previous := httpClient
	httpClient = client
	clientMu.Unlock()
	return func() {
		clientMu.Lock()
		httpClient = previous
		clientMu.Unlock()
	}
}
