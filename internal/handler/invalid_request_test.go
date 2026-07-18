package handler

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestJSONHandlersRejectMalformedBodiesConsistently(t *testing.T) {
	tests := []struct {
		name    string
		handler http.HandlerFunc
	}{
		{name: "chat completions", handler: ChatCompletions},
		{name: "responses", handler: Responses},
		{name: "messages", handler: Messages},
		{name: "embeddings", handler: Embeddings},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader("not-json"))
			recorder := httptest.NewRecorder()
			test.handler(recorder, req)
			if recorder.Code != http.StatusBadRequest {
				t.Fatalf("expected 400, got %d: %s", recorder.Code, recorder.Body.String())
			}
			if !strings.Contains(recorder.Body.String(), `"type":"invalid_request_error"`) {
				t.Fatalf("unexpected error response: %s", recorder.Body.String())
			}
		})
	}
}
