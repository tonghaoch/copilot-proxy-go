package handler

import (
	"testing"

	"github.com/tonghaoch/copilot-proxy-go/internal/state"
)

func TestCaptureResponsesUsage(t *testing.T) {
	rec := &state.RequestRecord{}
	captureResponsesStreamUsage(`{"type":"response.completed","response":{"usage":{"input_tokens":120,"output_tokens":30,"input_tokens_details":{"cached_tokens":40}}}}`, rec)
	if rec.InputTokens != 120 || rec.OutputTokens != 30 || rec.CachedTokens != 40 {
		t.Fatalf("unexpected usage: %+v", rec)
	}
}

func TestCaptureChatUsage(t *testing.T) {
	rec := &state.RequestRecord{}
	captureChatUsage(rec, &ChatCompletionUsage{
		PromptTokens:     90,
		CompletionTokens: 10,
		PromptTokensDetails: &PromptTokensDetails{
			CachedTokens: 20,
		},
	})
	if rec.InputTokens != 90 || rec.OutputTokens != 10 || rec.CachedTokens != 20 {
		t.Fatalf("unexpected usage: %+v", rec)
	}
}
