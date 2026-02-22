package handler

import (
	"bufio"
	"fmt"
	"io"
	"log/slog"
	"net/http"

	"github.com/tonghaoch/copilot-proxy-go/internal/api"
	"github.com/tonghaoch/copilot-proxy-go/internal/service"
)

// ChatCompletions handles POST /chat/completions and /v1/chat/completions.
// It proxies requests to the Copilot API, supporting both streaming and
// non-streaming modes.
func ChatCompletions(w http.ResponseWriter, r *http.Request) {
	body, isStream, isAgent, err := service.ParseAndPatchChatCompletion(r.Body)
	if err != nil {
		api.ForwardError(w, err)
		return
	}

	slog.Info("chat completion request", "stream", isStream, "initiator", initiatorStr(isAgent))

	resp, err := service.ProxyChatCompletion(body, isAgent)
	if err != nil {
		api.ForwardError(w, err)
		return
	}
	defer resp.Body.Close()

	if isStream {
		streamSSE(w, resp.Body)
	} else {
		forwardJSON(w, resp)
	}
}

// streamSSE proxies an SSE stream from the Copilot API to the client.
func streamSSE(w http.ResponseWriter, body io.Reader) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming not supported", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)

	scanner := bufio.NewScanner(body)
	// Increase buffer size for large SSE events
	scanner.Buffer(make([]byte, 0, 256*1024), 1024*1024)

	for scanner.Scan() {
		line := scanner.Text()
		fmt.Fprintf(w, "%s\n", line)
		// Flush after empty lines (SSE event boundary)
		if line == "" {
			flusher.Flush()
		}
	}

	if err := scanner.Err(); err != nil {
		slog.Error("SSE stream error", "error", err)
	}
}

// forwardJSON forwards a non-streaming JSON response.
func forwardJSON(w http.ResponseWriter, resp *http.Response) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(resp.StatusCode)
	io.Copy(w, resp.Body)
}

