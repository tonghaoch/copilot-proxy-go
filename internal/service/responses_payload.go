package service

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"image/jpeg"
	"image/png"
	"log/slog"
	"net/http"
	"strings"

	"github.com/tonghaoch/copilot-proxy-go/internal/api"
)

// Copilot's Responses parser limits the decompressed JSON body to 32 MiB,
// independently of the local server's 100 MiB limit and the model's token limit.
const (
	maxResponsesBodySize    = 32 << 20
	minResponsesPNGSize     = 64 << 10
	maxResponsesImagePixels = 16 << 20
	responsesJPEGQuality    = 90
)

func prepareResponsesBody(ctx context.Context, body []byte) ([]byte, error) {
	if len(body) <= maxResponsesBodySize {
		return body, nil
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	var payload map[string]any
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	if err := decoder.Decode(&payload); err != nil {
		return nil, api.InvalidRequest("invalid Responses request body", err)
	}
	compact, err := marshalResponsesPayload(payload)
	if err != nil {
		return nil, err
	}
	if len(compact) <= maxResponsesBodySize {
		return compact, nil
	}

	var images []map[string]any
	collectResponsesImages(payload["input"], &images)
	size := len(compact)
	// Try lossless PNG compression first. Only if still necessary, encode older
	// opaque PNGs as high-quality JPEGs, retaining their dimensions and detail.
	// Never drop history, change transparent images to JPEG, or fetch remote URLs.
	for _, lossless := range []bool{true, false} {
		cache := make(map[string]string)
		for _, item := range images {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			url, _ := item["image_url"].(string)
			optimized, found := cache[url]
			if !found {
				optimized = optimizeResponsesPNG(url, lossless)
				cache[url] = optimized
			}
			if optimized == "" {
				continue
			}
			item["image_url"] = optimized
			size -= len(url) - len(optimized)
			if size <= maxResponsesBodySize {
				result, err := marshalResponsesPayload(payload)
				if err != nil {
					return nil, err
				}
				slog.Info("optimized Responses request images", "original_bytes", len(body), "optimized_bytes", len(result))
				return result, nil
			}
		}
	}
	return nil, &api.HTTPError{
		StatusCode: http.StatusRequestEntityTooLarge,
		Type:       "invalid_request_error",
		Message:    fmt.Sprintf("Responses request exceeds Copilot's 32 MiB body limit even after image optimization (%d bytes); reduce attached images or start a new conversation with a summary", size),
	}
}

func marshalResponsesPayload(payload map[string]any) ([]byte, error) {
	var buf bytes.Buffer
	encoder := json.NewEncoder(&buf)
	// Avoid expanding HTML characters in long code/tool outputs when re-encoding.
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(payload); err != nil {
		return nil, err
	}
	return bytes.TrimSuffix(buf.Bytes(), []byte{'\n'}), nil
}

// Walk only Responses content containers, including multimodal tool outputs.
// Tool definitions, text, opaque reasoning, and other metadata stay untouched.
func collectResponsesImages(value any, images *[]map[string]any) {
	switch value := value.(type) {
	case []any:
		for _, item := range value {
			collectResponsesImages(item, images)
		}
	case map[string]any:
		if value["type"] == "input_image" {
			*images = append(*images, value)
			return
		}
		for _, key := range []string{"content", "output"} {
			collectResponsesImages(value[key], images)
		}
	}
}

func optimizeResponsesPNG(url string, lossless bool) string {
	const prefix = "data:image/png;base64,"
	if len(url) < minResponsesPNGSize || !strings.HasPrefix(url, prefix) {
		return ""
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(url, prefix))
	if err != nil {
		return ""
	}
	config, err := png.DecodeConfig(bytes.NewReader(raw))
	// Bound decoded memory as well as encoded request bytes for untrusted PNGs.
	if err != nil || int64(config.Width)*int64(config.Height) > maxResponsesImagePixels {
		return ""
	}
	img, err := png.Decode(bytes.NewReader(raw))
	if err != nil {
		return ""
	}
	var encoded bytes.Buffer
	mime := "image/png"
	if lossless {
		encoder := png.Encoder{CompressionLevel: png.BestCompression}
		err = encoder.Encode(&encoded, img)
	} else {
		opaque, ok := img.(interface{ Opaque() bool })
		if !ok || !opaque.Opaque() {
			return ""
		}
		mime = "image/jpeg"
		err = jpeg.Encode(&encoded, img, &jpeg.Options{Quality: responsesJPEGQuality})
	}
	if err != nil {
		return ""
	}
	result := "data:" + mime + ";base64," + base64.StdEncoding.EncodeToString(encoded.Bytes())
	if len(result) >= len(url) {
		return ""
	}
	return result
}
