package handler

import (
	"testing"

	"github.com/tonghaoch/copilot-proxy-go/internal/state"
)

func TestCaptureResponsesUsage(t *testing.T) {
	rec := &state.RequestRecord{}
	captureResponsesStreamUsage("response.completed", `{"type":"response.completed","response":{"usage":{"input_tokens":120,"output_tokens":30,"input_tokens_details":{"cached_tokens":40,"cache_write_tokens":10}}}}`, rec)
	if rec.InputTokens != 70 || rec.OutputTokens != 30 || rec.CachedTokens != 40 || rec.CacheCreationTokens != 10 || !rec.CacheWriteUsageKnown {
		t.Fatalf("unexpected usage: %+v", rec)
	}
}

func TestCaptureChatUsage(t *testing.T) {
	rec := &state.RequestRecord{}
	writes := 10
	captureChatUsage(rec, &ChatCompletionUsage{
		PromptTokens:     90,
		CompletionTokens: 10,
		PromptTokensDetails: &PromptTokensDetails{
			CachedTokens: 20, CacheWriteTokens: &writes,
		},
	})
	if rec.InputTokens != 60 || rec.OutputTokens != 10 || rec.CachedTokens != 20 || rec.CacheCreationTokens != 10 || !rec.CacheWriteUsageKnown {
		t.Fatalf("unexpected usage: %+v", rec)
	}
}
