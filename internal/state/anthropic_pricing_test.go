package state

import (
	"math"
	"strings"
	"testing"
)

func TestAnthropicBudgetUsesDirectRatesAndModifiers(t *testing.T) {
	model := &Model{
		ID: "claude-opus-5-5",
		Capabilities: ModelCapabilities{Limits: ModelLimits{
			MaxContextWindowTokens: 1_000_000,
			MaxOutputTokens:        128_000,
		}},
	}
	priced, err := AnthropicBudgetModel(model, "fast", "us")
	if err != nil {
		t.Fatal(err)
	}
	budget, err := NewTokenBudget(100)
	if err != nil {
		t.Fatal(err)
	}
	admission, err := budget.Admit(priced, "anthropic")
	if err != nil {
		t.Fatal(err)
	}
	budget.Complete(admission, RequestRecord{
		Backend: "anthropic", UpstreamAttempted: true,
		InputUsageKnown: true, OutputUsageKnown: true,
		InputTokens: 100_000, OutputTokens: 10_000, CachedTokens: 400_000,
		CacheCreationTokens: 100_000, CacheWrite1hTokens: 100_000, CacheWriteDetailsKnown: true,
	})
	if got, want := budget.Snapshot().EstimatedSpentUSD, 3.256; math.Abs(got-want) > 0.000000002 {
		t.Fatalf("direct token spend = %v, want %v", got, want)
	}
	if model.Billing.TokenPrices != nil {
		t.Fatal("pricing modified the shared catalog model")
	}
}

func TestAnthropicBudgetHaikuLongContextAndUnknownPrice(t *testing.T) {
	model := &Model{
		ID: "claude-haiku-5-5",
		Capabilities: ModelCapabilities{Limits: ModelLimits{
			MaxContextWindowTokens: 1_000_000,
			MaxOutputTokens:        128_000,
		}},
	}
	priced, err := AnthropicBudgetModel(model, "", "")
	if err != nil {
		t.Fatal(err)
	}
	cost, err := estimateTokenCost(priced.Billing, RequestRecord{
		Backend: "anthropic", InputTokens: 1, OutputTokens: 1_000, CachedTokens: 100_000,
	})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := float64(cost)/nanoUSD, 0.0075005; math.Abs(got-want) > 0.000000002 {
		t.Fatalf("long-context token spend = %v, want %v", got, want)
	}

	model.ID = "claude-unknown-1"
	if _, err := AnthropicBudgetModel(model, "", ""); err == nil || !strings.Contains(err.Error(), "pricing") {
		t.Fatalf("unknown model pricing must fail closed, got %v", err)
	}
}
