package main

import (
	"context"
	"io"
	"net"
	"net/http"
	"os"
	"syscall"
	"testing"
	"time"
)

func TestShutdownWaitsForInFlightRequest(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	started, release := make(chan struct{}), make(chan struct{})
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		close(started)
		<-release
		io.WriteString(w, "finished")
	})}
	signals := make(chan os.Signal, 1)
	canceled := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- serveUntilShutdown(srv, listener, signals, func() { close(canceled) })
	}()
	response := make(chan error, 1)
	go func() {
		resp, err := http.Get("http://" + listener.Addr().String())
		if err == nil {
			_, err = io.ReadAll(resp.Body)
			resp.Body.Close()
		}
		response <- err
	}()
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("request did not start")
	}
	signals <- syscall.SIGTERM
	<-canceled
	select {
	case err := <-done:
		t.Fatalf("server exited before request completed: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	close(release)
	if err := <-response; err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("server did not stop after the request completed")
	}
}

func TestShutdownReturnsListenErrors(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	listener.Close()
	srv := &http.Server{}
	if err := serveUntilShutdown(srv, listener, make(chan os.Signal), context.CancelFunc(func() {})); err == nil {
		t.Fatal("closed listener should return an error")
	}
}
