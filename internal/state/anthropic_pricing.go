package state

import (
	"fmt"
	"regexp"
)

type anthropicRates struct {
	input, output, cacheRead             float64
	longInput, longOutput, longCacheRead float64
}

// Anthropic's Models API does not include prices. These are first-party USD
// rates per million tokens from https://platform.claude.com/docs/en/about-claude/pricing.
var anthropicModelRates = map[string]anthropicRates{
	"claude-fable-5-2":  {input: 8, output: 40, cacheRead: 0.20},
	"claude-mythos-5-2": {input: 8, output: 40, cacheRead: 0.20},
	"claude-fable-5-1":  {input: 10, output: 50, cacheRead: 0.25},
	"claude-mythos-5-1": {input: 10, output: 50, cacheRead: 0.25},
	"claude-fable-5":    {input: 10, output: 50, cacheRead: 1},
	"claude-mythos-5":   {input: 10, output: 50, cacheRead: 1},
	"claude-opus-5-5":   {input: 4, output: 20, cacheRead: 0.20},
	"claude-opus-5":     {input: 5, output: 25, cacheRead: 0.50},
	"claude-opus-4-8":   {input: 5, output: 25, cacheRead: 0.50},
	"claude-opus-4-7":   {input: 5, output: 25, cacheRead: 0.50},
	"claude-opus-4-6":   {input: 5, output: 25, cacheRead: 0.50},
	"claude-opus-4-5":   {input: 5, output: 25, cacheRead: 0.50},
	"claude-opus-4-1":   {input: 15, output: 75, cacheRead: 1.50},
	"claude-opus-4":     {input: 15, output: 75, cacheRead: 1.50},
	"claude-sonnet-5-5": {input: 2, output: 10, cacheRead: 0.10},
	"claude-sonnet-5":   {input: 2, output: 10, cacheRead: 0.20},
	"claude-sonnet-4-6": {input: 3, output: 15, cacheRead: 0.30},
	"claude-sonnet-4-5": {input: 3, output: 15, cacheRead: 0.30},
	"claude-sonnet-4":   {input: 3, output: 15, cacheRead: 0.30},
	"claude-haiku-5-5":  {input: 0.10, output: 0.50, cacheRead: 0.01, longInput: 0.50, longOutput: 2.50, longCacheRead: 0.05},
	"claude-haiku-4-5":  {input: 1, output: 5, cacheRead: 0.10},
}

var anthropicSnapshotSuffix = regexp.MustCompile(`-\d{8}$`)

// AnthropicBudgetModel attaches direct API prices to a catalog model without
// using the potentially different Copilot prices.
func AnthropicBudgetModel(model *Model, speed, inferenceGeo string) (*Model, error) {
	if model == nil {
		return nil, fmt.Errorf("direct Anthropic model is missing")
	}
	rates, ok := anthropicModelRates[anthropicSnapshotSuffix.ReplaceAllString(model.ID, "")]
	if !ok {
		return nil, fmt.Errorf("direct Anthropic token pricing for model %q is unknown", model.ID)
	}
	if model.Capabilities.Limits.MaxContextWindowTokens <= 0 || model.Capabilities.Limits.MaxOutputTokens <= 0 {
		return nil, fmt.Errorf("direct Anthropic token limits for model %q are missing", model.ID)
	}

	multiplier := 1.0
	switch speed {
	case "", "standard":
	case "fast":
		multiplier *= 2
	default:
		return nil, fmt.Errorf("unknown direct Anthropic speed %q", speed)
	}
	switch inferenceGeo {
	case "", "global":
	case "us":
		multiplier *= 1.1
	default:
		return nil, fmt.Errorf("unknown direct Anthropic inference_geo %q", inferenceGeo)
	}

	priceTier := func(input, output, cacheRead float64) PriceTier {
		// Budget rates use one-cent credits; Anthropic publishes USD prices.
		factor := 100 * multiplier
		return PriceTier{
			InputPrice: input * factor, OutputPrice: output * factor,
			CacheReadPrice: cacheRead * factor, CacheWritePrice: input * 1.25 * factor,
			CacheWrite1hPrice: input * 2 * factor,
			hasInputPrice:     true, hasOutputPrice: true, hasCacheReadPrice: true,
			hasCacheWritePrice: true, hasCacheWrite1hPrice: true,
		}
	}
	modelCopy := cloneModel(*model)
	modelCopy.Billing = ModelBilling{
		BatchSize: 1_000_000,
		TokenPrices: map[string]PriceTier{
			"default": priceTier(rates.input, rates.output, rates.cacheRead),
		},
	}
	if rates.longInput > 0 {
		base := modelCopy.Billing.TokenPrices["default"]
		base.MaxPromptTokens = 100_000
		modelCopy.Billing.TokenPrices["default"] = base
		long := priceTier(rates.longInput, rates.longOutput, rates.longCacheRead)
		long.MaxPromptTokens = model.Capabilities.Limits.MaxContextWindowTokens
		modelCopy.Billing.TokenPrices["long_context"] = long
	}
	return &modelCopy, nil
}
