package service

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"image"
	"image/color"
	"image/png"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/tonghaoch/copilot-proxy-go/internal/api"
)

func screenshotPNG(t *testing.T, transparent bool) (*image.NRGBA, string) {
	t.Helper()
	img := image.NewNRGBA(image.Rect(0, 0, 512, 512))
	var seed uint32 = 1
	for y := range 512 {
		for x := range 512 {
			seed = seed*1664525 + 1013904223
			alpha := uint8(255)
			if transparent {
				alpha = uint8(seed >> 24)
			}
			img.SetNRGBA(x, y, color.NRGBA{uint8(seed), uint8(seed >> 8), uint8(seed >> 16), alpha})
		}
	}
	var encoded bytes.Buffer
	encoder := png.Encoder{CompressionLevel: png.NoCompression}
	if err := encoder.Encode(&encoded, img); err != nil {
		t.Fatal(err)
	}
	dataURL := "data:image/png;base64," + base64.StdEncoding.EncodeToString(encoded.Bytes())
	return img, dataURL
}

func responseImage(t *testing.T, dataURL string) image.Image {
	t.Helper()
	_, encoded, ok := strings.Cut(dataURL, ",")
	if !ok {
		t.Fatal("image is not a data URL")
	}
	raw, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		t.Fatal(err)
	}
	decoded, _, err := image.Decode(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	return decoded
}

func TestProxyResponsesOptimizesOversizedScreenshotHistory(t *testing.T) {
	const limit = 32 << 20 // Copilot rejects larger JSON bodies, including with gzip.
	_, dataURL := screenshotPNG(t, false)
	var input []any
	for i := 0; i < limit/len(dataURL)+1; i++ {
		input = append(input, map[string]any{
			"type": "custom_tool_call_output", "call_id": "screenshot-call",
			"output": []any{
				map[string]any{"type": "input_text", "text": "Screenshot and tool result must be retained."},
				map[string]any{"type": "input_image", "image_url": dataURL, "detail": "original"},
			},
		})
	}
	payload := map[string]any{
		"model": "test-model", "stream": true, "input": input,
		"instructions": "Keep the complete history.",
		"reasoning":    map[string]any{"effort": "high"},
		"large_number": json.Number("9007199254740993"),
	}
	body, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	if len(body) <= limit {
		t.Fatal("fixture must exceed the upstream request limit")
	}
	var forwarded []byte
	client := NewClientWithOptions(ClientOptions{
		HTTPClient: &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			forwarded, err = io.ReadAll(req.Body)
			if err != nil {
				t.Fatal(err)
			}
			status, response := http.StatusOK, `{"id":"resp-test","output":[]}`
			if len(forwarded) > limit {
				status, response = http.StatusRequestEntityTooLarge, `{"error":{"message":"failed to parse request","code":""}}`
			}
			return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(response))}, nil
		})},
		BuildHeaders: func() http.Header { return make(http.Header) },
		BuildURL:     func(path string) string { return "https://example.test" + path },
	})
	resp, err := client.ProxyResponses(context.Background(), body, true, true)
	if err != nil {
		t.Fatalf("screenshot history must fit upstream without losing items: %v", err)
	}
	resp.Body.Close()
	var got map[string]any
	if err := json.Unmarshal(forwarded, &got); err != nil {
		t.Fatal(err)
	}
	gotInput := got["input"].([]any)
	if len(gotInput) != len(input) || got["instructions"] != payload["instructions"] {
		t.Fatal("request history or instructions were lost")
	}
	if !bytes.Contains(forwarded, []byte(`"large_number":9007199254740993`)) {
		t.Fatal("numeric metadata lost precision")
	}
	if got["reasoning"].(map[string]any)["effort"] != "high" || got["stream"] != true {
		t.Fatal("reasoning or streaming settings were changed")
	}
	changed := 0
	for _, item := range gotInput {
		tool := item.(map[string]any)
		output := tool["output"].([]any)
		if tool["call_id"] != "screenshot-call" || len(output) != 2 || output[0].(map[string]any)["text"] != "Screenshot and tool result must be retained." {
			t.Fatal("tool result was changed")
		}
		imageItem := output[1].(map[string]any)
		if imageItem["detail"] != "original" {
			t.Fatal("image detail was changed")
		}
		url := imageItem["image_url"].(string)
		if url != dataURL {
			changed++
			if !strings.HasPrefix(url, "data:image/png;base64,") {
				t.Fatal("lossless compression should suffice for this screenshot history")
			}
			if responseImage(t, url).Bounds() != image.Rect(0, 0, 512, 512) {
				t.Fatal("image dimensions changed")
			}
		}
	}
	if changed == 0 {
		t.Fatal("expected image optimization")
	}
	// Optimizing the oldest screenshots first should leave the newest intact.
	last := gotInput[len(gotInput)-1].(map[string]any)["output"].([]any)[1].(map[string]any)
	if last["image_url"] != dataURL {
		t.Fatal("newest screenshot should remain unchanged once the request fits")
	}
}

func TestPrepareResponsesBodyPreservesSmallPayload(t *testing.T) {
	body := []byte(`{ "model":"test", "input":[{"type":"message","content":[{"type":"input_image","image_url":"https://example.test/image.png"}]}], "metadata":{"n":9007199254740993} }`)
	got, err := prepareResponsesBody(context.Background(), body)
	if err != nil || !bytes.Equal(got, body) {
		t.Fatalf("ordinary requests must pass through unchanged: %v", err)
	}
}

func TestPrepareResponsesBodyRemovesUnnecessaryHTMLEscaping(t *testing.T) {
	const characters = (32<<20)/6 + 1
	body := []byte(`{"model":"test","input":"` + strings.Repeat(`\u003c`, characters) + `"}`)
	got, err := prepareResponsesBody(context.Background(), body)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) > 32<<20 {
		t.Fatal("compacted JSON still exceeds the upstream limit")
	}
	var payload struct{ Input string }
	if err := json.Unmarshal(got, &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Input != strings.Repeat("<", characters) {
		t.Fatal("compacting JSON changed the input text")
	}
}

func TestProxyResponsesRejectsUnshrinkableBodyBeforeUpstream(t *testing.T) {
	client := NewClientWithOptions(ClientOptions{
		HTTPClient: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			t.Fatal("unshrinkable body must not be sent upstream")
			return nil, nil
		})},
	})
	body := []byte(`{"model":"test","input":"` + strings.Repeat("x", 32<<20) + `"}`)
	_, err := client.ProxyResponses(context.Background(), body, false, false)
	var httpErr *api.HTTPError
	if !errors.As(err, &httpErr) || httpErr.StatusCode != http.StatusRequestEntityTooLarge || !strings.Contains(httpErr.Message, "32 MiB") {
		t.Fatalf("expected an actionable size error, got %v", err)
	}
}

func TestPrepareResponsesBodyStopsCanceledOversizedRequest(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	body := []byte(`{"model":"test","input":"` + strings.Repeat("x", 32<<20) + `"}`)
	_, err := prepareResponsesBody(ctx, body)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected cancellation before processing an oversized body, got %v", err)
	}
}

func TestOptimizeResponsesPNGPreservesLosslessPixelsAndTransparency(t *testing.T) {
	tests := []struct {
		name        string
		transparent bool
	}{
		{name: "opaque"},
		{name: "transparent", transparent: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			source, url := screenshotPNG(t, tt.transparent)
			if len(url) < 64<<10 {
				t.Fatal("fixture must exercise image encoding")
			}
			optimized := optimizeResponsesPNG(url, true)
			if optimized == "" {
				t.Fatal("expected lossless compression of an uncompressed PNG")
			}
			if !strings.HasPrefix(optimized, "data:image/png;base64,") {
				t.Fatal("lossless optimization must retain PNG encoding")
			}
			decoded := responseImage(t, optimized)
			for y := range 512 {
				for x := range 512 {
					if color.NRGBAModel.Convert(decoded.At(x, y)) != source.NRGBAAt(x, y) {
						t.Fatal("lossless optimization changed pixels")
					}
				}
			}
			jpegURL := optimizeResponsesPNG(url, false)
			if tt.transparent {
				if jpegURL != "" {
					t.Fatal("transparent PNG must not be converted to JPEG")
				}
			} else {
				if !strings.HasPrefix(jpegURL, "data:image/jpeg;base64,") || len(jpegURL) >= len(url) {
					t.Fatal("opaque PNG should have a smaller JPEG representation")
				}
				if responseImage(t, jpegURL).Bounds() != source.Bounds() {
					t.Fatal("JPEG conversion changed image dimensions")
				}
			}
		})
	}
}
