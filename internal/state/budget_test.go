package state

import (
	"encoding/json"
	"errors"
	"math"
	"sync"
	"testing"
)

func budgetModel(t *testing.T) *Model {
	t.Helper()
	var model Model
	err := json.Unmarshal([]byte(`{"id":"test-model","billing":{"token_prices":{"batch_size":1000,
		"default":{"max_prompt_tokens":100,"input_price":100,"output_price":200,"cache_read_price":10,"cache_write_price":125,"cache_write_1h_price":200},
		"long_context":{"max_prompt_tokens":1000,"input_price":200,"output_price":400,"cache_read_price":20,"cache_write_price":250,"cache_write_1h_price":400}}}}`), &model)
	if err != nil {
		t.Fatal(err)
	}
	return &model
}

func TestBudgetAccountsCacheRatesAndInflightRequests(t *testing.T) {
	budget, err := NewTokenBudget(0.1)
	if err != nil {
		t.Fatal(err)
	}
	model := budgetModel(t)
	first, err := budget.Admit(model, "messages")
	if err != nil {
		t.Fatal(err)
	}
	second, err := budget.Admit(model, "messages")
	if err != nil {
		t.Fatal(err)
	}
	record := RequestRecord{
		Backend: "messages", UpstreamAttempted: true, InputUsageKnown: true, OutputUsageKnown: true,
		InputTokens: 10, CachedTokens: 20, CacheCreationTokens: 20, OutputTokens: 5,
		CacheWriteDetailsKnown: true, CacheWrite5mTokens: 10, CacheWrite1hTokens: 10,
	}
	budget.Complete(first, record)
	if got := budget.Snapshot().EstimatedSpentUSD; math.Abs(got-0.0545) > 0.000000001 {
		t.Fatalf("first estimated cost = %.9f", got)
	}
	budget.Complete(second, record)
	snapshot := budget.Snapshot()
	if math.Abs(snapshot.EstimatedSpentUSD-0.109) > 0.000000002 || snapshot.InFlight != 0 || !snapshot.Blocked {
		t.Fatalf("budget snapshot = %+v", snapshot)
	}
	if _, err := budget.Admit(model, "messages"); !errors.Is(err, ErrBudgetExceeded) {
		t.Fatalf("expected exhausted budget, got %v", err)
	}
}

func TestBudgetUsesLongContextAndConservativeUnknownTTL(t *testing.T) {
	model := budgetModel(t)
	budget, _ := NewTokenBudget(1)
	admission, err := budget.Admit(model, "messages")
	if err != nil {
		t.Fatal(err)
	}
	budget.Complete(admission, RequestRecord{
		Backend: "messages", UpstreamAttempted: true, InputUsageKnown: true, OutputUsageKnown: true,
		InputTokens: 60, CachedTokens: 50, OutputTokens: 5,
	})
	if got := budget.Snapshot().EstimatedSpentUSD; math.Abs(got-0.15) > 0.000000001 {
		t.Fatalf("long-context estimated cost = %.9f", got)
	}

	admission, err = budget.Admit(model, "messages")
	if err != nil {
		t.Fatal(err)
	}
	budget.Complete(admission, RequestRecord{
		Backend: "messages", UpstreamAttempted: true, InputUsageKnown: true, OutputUsageKnown: true,
		CacheCreationTokens: 10,
	})
	if got := budget.Snapshot().EstimatedSpentUSD; math.Abs(got-0.17) > 0.000000001 {
		t.Fatalf("unknown-TTL estimated cost = %.9f", got)
	}
}

func TestBudgetPausesWhenUsageOrPricesAreMissing(t *testing.T) {
	model := budgetModel(t)
	budget, _ := NewTokenBudget(1)
	if _, err := budget.Admit(nil, "messages"); !errors.Is(err, ErrBudgetUnavailable) {
		t.Fatalf("expected missing model pricing error, got %v", err)
	}
	admission, err := budget.Admit(model, "messages")
	if err != nil {
		t.Fatal(err)
	}
	budget.Complete(admission, RequestRecord{Backend: "messages", UpstreamAttempted: true})
	if got := budget.Snapshot(); !got.Blocked || got.Reason == "" {
		t.Fatalf("missing usage did not pause budget: %+v", got)
	}
	if _, err := budget.Admit(model, "messages"); !errors.Is(err, ErrBudgetUnavailable) {
		t.Fatalf("expected paused budget, got %v", err)
	}

	budget, _ = NewTokenBudget(1)
	delete(model.Billing.TokenPrices, "default")
	if _, err := budget.Admit(model, "messages"); !errors.Is(err, ErrBudgetUnavailable) {
		t.Fatalf("expected missing price error, got %v", err)
	}
}

func TestBudgetDoesNotChargeRejectedOrUnsentRequests(t *testing.T) {
	budget, _ := NewTokenBudget(1)
	model := budgetModel(t)
	for _, record := range []RequestRecord{{}, {UpstreamAttempted: true, UpstreamRejected: true}} {
		admission, err := budget.Admit(model, "messages")
		if err != nil {
			t.Fatal(err)
		}
		budget.Complete(admission, record)
	}
	if got := budget.Snapshot(); got.Blocked || got.EstimatedSpentUSD != 0 || got.InFlight != 0 {
		t.Fatalf("rejected or unsent request changed budget: %+v", got)
	}
}

func TestBudgetAccountsConcurrentAdmissions(t *testing.T) {
	budget, _ := NewTokenBudget(0.01)
	model := budgetModel(t)
	const count = 32
	admissions := make([]*BudgetAdmission, count)
	var wg sync.WaitGroup
	for i := range admissions {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			admission, err := budget.Admit(model, "messages")
			if err != nil {
				t.Errorf("admission %d: %v", i, err)
				return
			}
			admissions[i] = admission
		}(i)
	}
	wg.Wait()
	for _, admission := range admissions {
		wg.Add(1)
		go func(admission *BudgetAdmission) {
			defer wg.Done()
			budget.Complete(admission, RequestRecord{
				Backend: "messages", UpstreamAttempted: true, InputUsageKnown: true, OutputUsageKnown: true,
				InputTokens: 10, OutputTokens: 5,
			})
		}(admission)
	}
	wg.Wait()
	snapshot := budget.Snapshot()
	if !snapshot.Blocked || snapshot.InFlight != 0 || math.Abs(snapshot.EstimatedSpentUSD-0.64) > 0.000000032 {
		t.Fatalf("concurrent budget snapshot = %+v", snapshot)
	}
}

func TestBudgetPricesReportedAndUnknownOpenAICacheWrites(t *testing.T) {
	billing := budgetModel(t).Billing
	reported, err := estimateTokenCost(billing, RequestRecord{
		Backend: "responses", InputTokens: 8, CachedTokens: 2, CacheCreationTokens: 5, OutputTokens: 1,
		CacheWriteUsageKnown: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	unknown, err := estimateTokenCost(billing, RequestRecord{
		Backend: "responses", InputTokens: 13, CachedTokens: 2, OutputTokens: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if math.Abs(float64(reported)/nanoUSD-0.01645) > 0.000000001 ||
		math.Abs(float64(unknown)/nanoUSD-0.01845) > 0.000000001 {
		t.Fatalf("reported cost=%d nanos, unknown cost=%d nanos", reported, unknown)
	}
}
