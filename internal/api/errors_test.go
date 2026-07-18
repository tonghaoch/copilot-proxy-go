package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestForwardInvalidRequest(t *testing.T) {
	recorder := httptest.NewRecorder()
	cause := errors.New("bad json")
	err := InvalidRequest("invalid request body", cause)
	ForwardError(recorder, err)

	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", recorder.Code)
	}
	if !errors.Is(err, cause) {
		t.Fatal("expected invalid request to retain its cause")
	}
	want := `{"error":{"message":"invalid request body","type":"invalid_request_error"}}`
	if got := recorder.Body.String(); len(got) == 0 || got[:len(got)-1] != want {
		t.Fatalf("unexpected response: %s", got)
	}
}

func TestForwardErrorMapsUpstreamStatusAndHeaders(t *testing.T) {
	tests := []struct {
		status int
		want   string
	}{
		{http.StatusUnauthorized, "authentication_error"},
		{http.StatusForbidden, "permission_error"},
		{http.StatusNotFound, "not_found_error"},
		{http.StatusTooManyRequests, "rate_limit_error"},
		{http.StatusBadGateway, "api_error"},
	}
	for _, tt := range tests {
		t.Run(http.StatusText(tt.status), func(t *testing.T) {
			recorder := httptest.NewRecorder()
			ForwardError(recorder, &HTTPError{
				Message: "upstream failed", StatusCode: tt.status,
				Header: http.Header{"Retry-After": []string{"3"}},
			})
			var response ErrorResponse
			if err := json.NewDecoder(recorder.Body).Decode(&response); err != nil {
				t.Fatal(err)
			}
			if response.Error.Type != tt.want {
				t.Fatalf("expected %q, got %q", tt.want, response.Error.Type)
			}
			if recorder.Header().Get("Retry-After") != "3" {
				t.Fatal("expected Retry-After to be forwarded")
			}
		})
	}
}
