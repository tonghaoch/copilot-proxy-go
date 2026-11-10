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

func TestBudgetConservativelyAccountsForMissingUsage(t *testing.T) {
	model := budgetModel(t)
	model.Capabilities.Limits = ModelLimits{MaxContextWindowTokens: 1000, MaxOutputTokens: 100}
	tests := []struct {
		name   string
		record RequestRecord
		want   float64
	}{
		{
			name: "missing output",
			record: RequestRecord{Backend: "messages", UpstreamAttempted: true, InputUsageKnown: true,
				InputTokens: 10, CachedTokens: 20, CacheCreationTokens: 5},
			want: 0.222,
		},
		{
			name: "missing input",
			record: RequestRecord{Backend: "messages", UpstreamAttempted: true, OutputUsageKnown: true,
				OutputTokens: 5},
			want: 4.02,
		},
		{
			name:   "missing both",
			record: RequestRecord{Backend: "messages", UpstreamAttempted: true},
			want:   4.4,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			budget, err := NewTokenBudget(100)
			if err != nil {
				t.Fatal(err)
			}
			admission, err := budget.Admit(model, "messages")
			if err != nil {
				t.Fatal(err)
			}
			budget.Complete(admission, tt.record)
			got := budget.Snapshot()
			if got.Blocked || got.ConservativeEstimates != 1 || math.Abs(got.EstimatedSpentUSD-tt.want) > 0.000000001 ||
				math.Abs(got.ReservedUSD-tt.want) > 0.000000001 || got.ReportedUsageUSD != 0 {
				t.Fatalf("conservative budget = %+v, want spend %.3f", got, tt.want)
			}
			if _, err := budget.Admit(model, "messages"); err != nil {
				t.Fatalf("a bounded incomplete request should not pause the proxy: %v", err)
			}
		})
	}
}

func TestBudgetRestoresPriorSpend(t *testing.T) {
	budget, err := NewTokenBudgetWithSpent(250, 69.341515202)
	if err != nil {
		t.Fatal(err)
	}
	got := budget.Snapshot()
	if got.LimitUSD != 250 || got.EstimatedSpentUSD < 69.341515202 || got.RemainingUSD > 180.658484798 {
		t.Fatalf("restored budget = %+v", got)
	}
	for _, invalid := range []float64{-1, math.NaN(), math.Inf(1)} {
		if _, err := NewTokenBudgetWithSpent(250, invalid); err == nil {
			t.Fatalf("accepted invalid prior spend %v", invalid)
		}
	}
	exhausted, err := NewTokenBudgetWithSpent(10, 11)
	if err != nil || !exhausted.Snapshot().Blocked {
		t.Fatalf("restored spend above limit must remain blocked: budget=%+v err=%v", exhausted, err)
	}
}

func TestBudgetRestoresAndSeparatesReserves(t *testing.T) {
	budget, err := NewTokenBudgetWithRestored(350, 119.133312633, 65.9597968)
	if err != nil {
		t.Fatal(err)
	}
	got := budget.Snapshot()
	if math.Abs(got.ReportedUsageUSD-53.173515833) > 0.000000002 || math.Abs(got.ReservedUSD-65.9597968) > 0.000000002 {
		t.Fatalf("restored split = %+v", got)
	}
	admission, err := budget.Admit(budgetModel(t), "messages")
	if err != nil {
		t.Fatal(err)
	}
	budget.Complete(admission, RequestRecord{
		Backend: "messages", UpstreamAttempted: true, InputUsageKnown: true, OutputUsageKnown: true,
		InputTokens: 10, OutputTokens: 5,
	})
	after := budget.Snapshot()
	if after.ReservedUSD != got.ReservedUSD || after.ReportedUsageUSD <= got.ReportedUsageUSD {
		t.Fatalf("reported usage did not increase separately from the reserve: %+v", after)
	}
	for _, invalid := range []float64{-1, 120, math.NaN(), math.Inf(1)} {
		if _, err := NewTokenBudgetWithRestored(350, 119, invalid); err == nil {
			t.Fatalf("accepted invalid reserve %v", invalid)
		}
	}
}

func TestBudgetSeparatesReportedUsageFromUncertainRetries(t *testing.T) {
	model := budgetModel(t)
	model.Capabilities.Limits = ModelLimits{MaxContextWindowTokens: 1000, MaxOutputTokens: 100}
	for _, tt := range []struct {
		name              string
		record            RequestRecord
		reported, reserve float64
		estimates         int64
	}{
		{"successful", RequestRecord{Backend: "messages", UpstreamAttempted: true, InputUsageKnown: true, OutputUsageKnown: true,
			InputTokens: 10, OutputTokens: 5, UncertainRetries: 2}, 0.02, 8.8, 2},
		{"rejected", RequestRecord{Backend: "messages", UpstreamAttempted: true, UpstreamRejected: true,
			UncertainRetries: 1}, 0, 4.4, 1},
		{"missing usage", RequestRecord{Backend: "messages", UpstreamAttempted: true,
			UncertainRetries: 1}, 0, 8.8, 2},
	} {
		t.Run(tt.name, func(t *testing.T) {
			budget, _ := NewTokenBudget(100)
			admission, err := budget.Admit(model, "messages")
			if err != nil {
				t.Fatal(err)
			}
			budget.Complete(admission, tt.record)
			got := budget.Snapshot()
			if got.Blocked || math.Abs(got.ReportedUsageUSD-tt.reported) > 1e-9 ||
				math.Abs(got.ReservedUSD-tt.reserve) > 1e-9 || got.ConservativeEstimates != tt.estimates {
				t.Fatalf("budget = %+v, want reported %v, reserve %v, estimates %d", got, tt.reported, tt.reserve, tt.estimates)
			}
		})
	}
}

func TestBudgetFailsClosedWithoutReliableFallbackBounds(t *testing.T) {
	tests := []struct {
		name    string
		model   *Model
		backend string
		record  RequestRecord
	}{
		{"missing output limit", budgetModel(t), "messages",
			RequestRecord{Backend: "messages", UpstreamAttempted: true, InputUsageKnown: true}},
		{"unknown embeddings batch", budgetModel(t), "embeddings",
			RequestRecord{Backend: "embeddings", UpstreamAttempted: true}},
		{"invalid partial usage", budgetModel(t), "messages",
			RequestRecord{Backend: "messages", UpstreamAttempted: true, OutputUsageKnown: true, CachedTokens: -1}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			budget, _ := NewTokenBudget(100)
			admission, err := budget.Admit(tt.model, tt.backend)
			if err != nil {
				t.Fatal(err)
			}
			budget.Complete(admission, tt.record)
			if got := budget.Snapshot(); !got.Blocked || got.Reason == "" {
				t.Fatalf("unsafe fallback did not pause the budget: %+v", got)
			}
		})
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
