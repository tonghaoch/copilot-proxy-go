package handler

import "testing"

func TestAnthropicStreamStateCapturesFinalUsageOnlyChunk(t *testing.T) {
	stream := NewAnthropicStreamState("gpt-4.1")
	stream.TranslateChunk(&ChatCompletionChunk{
		ID:      "chatcmpl-test",
		Model:   "gpt-4.1",
		Choices: []ChatCompletionChunkChoice{{}},
	})
	stream.TranslateChunk(&ChatCompletionChunk{
		Usage: &ChatCompletionUsage{
			PromptTokens:     120,
			CompletionTokens: 30,
			PromptTokensDetails: &PromptTokensDetails{
				CachedTokens: 40,
			},
		},
	})

	input, output, cached := stream.TokenCounts()
	if input != 120 || output != 30 || cached != 40 {
		t.Fatalf("unexpected token counts: input=%d output=%d cached=%d", input, output, cached)
	}
}
