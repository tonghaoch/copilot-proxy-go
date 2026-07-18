package handler

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/tonghaoch/copilot-proxy-go/internal/api"
)

type embeddingRoundTripFunc func(*http.Request) (*http.Response, error)

func (f embeddingRoundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func TestEmbeddingsNormalizesStringInputAndResponse(t *testing.T) {
	client := &http.Client{Transport: embeddingRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		var payload map[string]any
		if err := json.NewDecoder(req.Body).Decode(&payload); err != nil {
			t.Fatal(err)
		}
		input, ok := payload["input"].([]any)
		if !ok || len(input) != 1 || input[0] != "hello" {
			t.Fatalf("input was not normalized: %#v", payload["input"])
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Body: io.NopCloser(strings.NewReader(
				`{"data":[{"object":"embedding","index":0,"embedding":[0.1]}],"usage":{"prompt_tokens":1,"total_tokens":1}}`,
			)),
			Header: make(http.Header),
		}, nil
	})}
	restore := api.SetHTTPClient(client)
	defer restore()

	req := httptest.NewRequest(http.MethodPost, "/v1/embeddings", strings.NewReader(
		`{"model":"text-embedding-3-small","input":"hello"}`,
	))
	recorder := httptest.NewRecorder()
	Embeddings(recorder, req)
	if recorder.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", recorder.Code, recorder.Body.String())
	}
	var result map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result["object"] != "list" || result["model"] != "text-embedding-3-small" {
		t.Fatalf("response was not normalized: %#v", result)
	}
}

func TestEmbeddingsRejectsInvalidInput(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/v1/embeddings", strings.NewReader(
		`{"model":"text-embedding-3-small","input":42}`,
	))
	recorder := httptest.NewRecorder()
	Embeddings(recorder, req)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", recorder.Code)
	}
	if !strings.Contains(recorder.Body.String(), `"type":"invalid_request_error"`) {
		t.Fatalf("unexpected error response: %s", recorder.Body.String())
	}
}

func BenchmarkNormalizeEmbeddingsResponse1536Dimensions(b *testing.B) {
	vector := strings.TrimSuffix(strings.Repeat("0.123,", 1536), ",")
	body := []byte(`{"data":[{"object":"embedding","index":0,"embedding":[` + vector +
		`]}],"usage":{"prompt_tokens":2,"total_tokens":2}}`)
	b.ReportAllocs()
	b.SetBytes(int64(len(body)))
	for b.Loop() {
		if _, _, err := normalizeEmbeddingsResponse(bytes.NewReader(body), "text-embedding-3-small"); err != nil {
			b.Fatal(err)
		}
	}
}
