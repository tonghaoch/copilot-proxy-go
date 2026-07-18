package api

import (
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
