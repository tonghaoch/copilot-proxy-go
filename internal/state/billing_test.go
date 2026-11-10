package state

import (
	"encoding/json"
	"testing"
)

func TestModelBillingParsesTiers(t *testing.T) {
	raw := `{"id":"claude-haiku-5.5","billing":{"auto_discount":0.1,"token_prices":{"batch_size":1000000,
		"default":{"max_prompt_tokens":100000,"input_price":10,"output_price":50,"cache_write_1h_price":20},
		"long_context":{"max_prompt_tokens":936000,"input_price":50,"output_price":250}}}}`
	var m Model
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		t.Fatal(err)
	}
	if m.Billing.AutoDiscount != 0.1 || m.Billing.BatchSize != 1000000 ||
		m.Billing.TokenPrices["default"].CacheWrite1hPrice != 20 || len(m.Billing.TokenPrices) != 2 {
		t.Fatalf("billing = %+v", m.Billing)
	}
	if got := m.DefaultPromptTokens(); got != 100000 {
		t.Fatalf("DefaultPromptTokens = %d", got)
	}

	var single Model
	_ = json.Unmarshal([]byte(`{"billing":{"token_prices":{"batch_size":1,"default":{"input_price":1,"output_price":2}}}}`), &single)
	if got := single.DefaultPromptTokens(); got != 0 || len(single.Billing.TokenPrices) != 1 {
		t.Fatalf("single-tier pricing = %+v, default prompt tokens = %d", single.Billing, got)
	}
}
