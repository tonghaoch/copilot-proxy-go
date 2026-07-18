package api

import (
	"crypto/tls"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func BenchmarkHTTPConnectionPoolBursts(b *testing.B) {
	for _, enableHTTP2 := range []bool{false, true} {
		protocol := "http1"
		if enableHTTP2 {
			protocol = "http2"
		}
		b.Run(protocol, func(b *testing.B) {
			upstream := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				time.Sleep(time.Millisecond)
				w.WriteHeader(http.StatusNoContent)
			}))
			upstream.EnableHTTP2 = enableHTTP2
			upstream.StartTLS()
			defer upstream.Close()

			for _, concurrency := range []int{8, 32, 64} {
				for _, tuned := range []bool{false, true} {
					name := "default"
					if tuned {
						name = "tuned"
					}
					b.Run(name+"/c"+strconv.Itoa(concurrency), func(b *testing.B) {
						transport := http.DefaultTransport.(*http.Transport).Clone()
						transport.Proxy = nil
						transport.ForceAttemptHTTP2 = true
						transport.TLSClientConfig = &tls.Config{ // test server certificate
							InsecureSkipVerify: true, //nolint:gosec
							MinVersion:         tls.VersionTLS12,
						}
						if tuned {
							transport.MaxIdleConns = maxIdleConnections
							transport.MaxIdleConnsPerHost = maxIdleConnectionsPerHost
						}
						client := &http.Client{Transport: transport}
						defer transport.CloseIdleConnections()

						b.ReportAllocs()
						var failures atomic.Int64
						started := time.Now()
						b.ResetTimer()
						for range b.N {
							var wg sync.WaitGroup
							wg.Add(concurrency)
							for range concurrency {
								go func() {
									defer wg.Done()
									resp, err := client.Get(upstream.URL)
									if err != nil {
										failures.Add(1)
										return
									}
									_, _ = io.Copy(io.Discard, resp.Body)
									_ = resp.Body.Close()
								}()
							}
							wg.Wait()
						}
						b.StopTimer()
						if failures.Load() != 0 {
							b.Fatalf("%d requests failed", failures.Load())
						}
						b.ReportMetric(float64(b.N*concurrency)/time.Since(started).Seconds(), "req/s")
					})
				}
			}
		})
	}
}
