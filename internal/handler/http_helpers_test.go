package handler

import (
	"bytes"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/tonghaoch/copilot-proxy-go/internal/api"
)

func TestReadRequestBodyRejectsOversizedBody(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(make([]byte, maxRequestBodySize+1)))
	recorder := httptest.NewRecorder()
	_, err := readRequestBody(recorder, req)
	var httpErr *api.HTTPError
	if !errors.As(err, &httpErr) {
		t.Fatalf("expected HTTPError, got %v", err)
	}
	if httpErr.StatusCode != http.StatusRequestEntityTooLarge {
		t.Fatalf("expected 413, got %d", httpErr.StatusCode)
	}
}

func TestDecodeRequestBodyReturnsInvalidRequest(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewBufferString(`{"model":`))
	recorder := httptest.NewRecorder()
	var payload map[string]any
	_, err := decodeRequestBody(recorder, req, &payload)
	var httpErr *api.HTTPError
	if !errors.As(err, &httpErr) || httpErr.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected invalid request error, got %v", err)
	}
}
