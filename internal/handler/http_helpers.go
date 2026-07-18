package handler

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"

	"github.com/tonghaoch/copilot-proxy-go/internal/api"
)

func readRequestBody(w http.ResponseWriter, r *http.Request) ([]byte, error) {
	r.Body = http.MaxBytesReader(w, r.Body, maxRequestBodySize)
	body, err := io.ReadAll(r.Body)
	if err != nil {
		var maxBytesErr *http.MaxBytesError
		if errors.As(err, &maxBytesErr) {
			return nil, &api.HTTPError{
				Message:    "request body too large",
				Type:       "invalid_request_error",
				StatusCode: http.StatusRequestEntityTooLarge,
				Cause:      err,
			}
		}
		return nil, fmt.Errorf("reading request body: %w", err)
	}
	return body, nil
}

func decodeRequestBody(w http.ResponseWriter, r *http.Request, dst any) ([]byte, error) {
	body, err := readRequestBody(w, r)
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(body, dst); err != nil {
		return nil, api.InvalidRequest("invalid request body", err)
	}
	return body, nil
}

func beginSSE(w http.ResponseWriter) (http.Flusher, error) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		return nil, fmt.Errorf("streaming not supported")
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)
	return flusher, nil
}
