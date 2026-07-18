package api

import (
	"crypto/tls"
	"net/http"
	"sync"
)

var (
	clientMu   sync.RWMutex
	httpClient = newHTTPClient(false)
)

func newHTTPClient(proxyFromEnvironment bool) *http.Client {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.TLSClientConfig = &tls.Config{MinVersion: tls.VersionTLS12}
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
