package service

import (
	"context"
	"net/http"
	"testing"

	"github.com/tonghaoch/copilot-proxy-go/internal/api"
)

func TestRequestHeadersInteractionType(t *testing.T) {
	cases := []struct {
		name string
		ctx  context.Context
		want string
	}{
		{"plain agent request", context.Background(), api.InteractionAgent},
		{"subagent request", WithSubagentInteraction(context.Background()), api.InteractionSubagent},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			header := http.Header{}
			requestHeaders(tt.ctx, true, false, "")(header)
			if got := header.Get("X-Interaction-Type"); got != tt.want {
				t.Fatalf("X-Interaction-Type = %q, want %q", got, tt.want)
			}
		})
	}
}
