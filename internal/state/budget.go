package state

import (
	"errors"
	"fmt"
	"log/slog"
	"math"
	"sync"
)

const nanoUSD = 1_000_000_000

var (
	ErrBudgetExceeded    = errors.New("token budget exhausted")
	ErrBudgetUnavailable = errors.New("token budget accounting unavailable")
)

// TokenBudget tracks a per-process estimate, not a GitHub invoice or hard cap.
type TokenBudget struct {
	mu       sync.Mutex
	limit    int64
	spent    int64
	inFlight int
	blocked  string
}

type BudgetAdmission struct {
	model    Model
	backend  string
	finished bool
}

type BudgetSnapshot struct {
	LimitUSD          float64 `json:"limit_usd"`
	EstimatedSpentUSD float64 `json:"estimated_spent_usd"`
	RemainingUSD      float64 `json:"remaining_usd"`
	InFlight          int     `json:"in_flight"`
	Blocked           bool    `json:"blocked"`
	Reason            string  `json:"reason,omitempty"`
}

func NewTokenBudget(limitUSD float64) (*TokenBudget, error) {
	nanos := math.Floor(limitUSD * nanoUSD)
	if math.IsNaN(nanos) || math.IsInf(nanos, 0) || nanos < 1 || nanos >= float64(math.MaxInt64) {
		return nil, errors.New("budget must be a positive, finite USD amount of at least $0.000000001")
	}
	return &TokenBudget{limit: int64(nanos)}, nil
}

// Admit snapshots prices when a request enters; already admitted work may finish.
func (b *TokenBudget) Admit(model *Model, backend string) (*BudgetAdmission, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.blocked != "" {
		return nil, fmt.Errorf("%w: %s", ErrBudgetUnavailable, b.blocked)
	}
	if b.spent >= b.limit {
		return nil, ErrBudgetExceeded
	}
	if model == nil {
		return nil, fmt.Errorf("%w: model pricing is missing", ErrBudgetUnavailable)
	}
	if err := validateBudgetPricing(model.Billing, backend); err != nil {
		return nil, fmt.Errorf("%w for model %q: %v", ErrBudgetUnavailable, model.ID, err)
	}
	b.inFlight++
	return &BudgetAdmission{model: cloneModel(*model), backend: backend}, nil
}

func validateBudgetPricing(billing ModelBilling, backend string) error {
	if backend != "messages" && backend != "responses" && backend != "chat_completions" && backend != "embeddings" {
		return errors.New("unknown model backend")
	}
	if billing.BatchSize <= 0 {
		return errors.New("token price batch size is missing")
	}
	tier, ok := billing.TokenPrices["default"]
	if !ok {
		return errors.New("default token prices are missing")
	}
	if err := validateBaseRates(tier, backend); err != nil {
		return err
	}
	if long, ok := billing.TokenPrices["long_context"]; ok {
		if tier.MaxPromptTokens <= 0 {
			return errors.New("long-context pricing threshold is missing")
		}
		if err := validateBaseRates(long, backend); err != nil {
			return err
		}
	}
	return nil
}

func validateBaseRates(tier PriceTier, backend string) error {
	if !tier.hasInputPrice || !validPrice(tier.InputPrice) {
		return errors.New("input token price is missing or invalid")
	}
	if backend != "embeddings" && (!tier.hasOutputPrice || !validPrice(tier.OutputPrice)) {
		return errors.New("output token price is missing or invalid")
	}
	if backend != "embeddings" {
		if !tier.hasCacheReadPrice || !validPrice(tier.CacheReadPrice) {
			return errors.New("cache-read token price is missing or invalid")
		}
		if !tier.hasCacheWritePrice || !validPrice(tier.CacheWritePrice) {
			return errors.New("cache-write token price is missing or invalid")
		}
		if backend == "messages" && (!tier.hasCacheWrite1hPrice || !validPrice(tier.CacheWrite1hPrice)) {
			return errors.New("one-hour cache-write token price is missing or invalid")
		}
	}
	return nil
}

func validPrice(price float64) bool {
	return price >= 0 && !math.IsNaN(price) && !math.IsInf(price, 0)
}

// Complete accounts for an admitted request, or pauses admission if its cost
// cannot be determined. Explicit upstream 4xx rejections are not charged.
func (b *TokenBudget) Complete(admission *BudgetAdmission, rec RequestRecord) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if admission == nil || admission.finished {
		return
	}
	admission.finished = true
	b.inFlight--
	if !rec.UpstreamAttempted || rec.UpstreamRejected {
		return
	}
	if rec.Backend != admission.backend {
		b.pause(admission.model.ID, "upstream backend differed from the admitted pricing")
		return
	}
	if !rec.InputUsageKnown || (admission.backend != "embeddings" && !rec.OutputUsageKnown) {
		b.pause(admission.model.ID, "upstream did not report complete token usage")
		return
	}
	cost, err := estimateTokenCost(admission.model.Billing, rec)
	if err != nil {
		b.pause(admission.model.ID, err.Error())
		return
	}
	if cost > math.MaxInt64-b.spent {
		b.pause(admission.model.ID, "estimated cost overflowed")
		return
	}
	b.spent += cost
}

func (b *TokenBudget) pause(model, reason string) {
	if b.blocked == "" {
		b.blocked = fmt.Sprintf("model %q: %s", model, reason)
		slog.Error("token budget paused; refusing new model requests", "reason", b.blocked)
	}
}

func (b *TokenBudget) Snapshot() BudgetSnapshot {
	b.mu.Lock()
	defer b.mu.Unlock()
	remaining := max(int64(0), b.limit-b.spent)
	reason := b.blocked
	if reason == "" && b.spent >= b.limit {
		reason = ErrBudgetExceeded.Error()
	}
	return BudgetSnapshot{
		LimitUSD: float64(b.limit) / nanoUSD, EstimatedSpentUSD: float64(b.spent) / nanoUSD,
		RemainingUSD: float64(remaining) / nanoUSD, InFlight: b.inFlight,
		Blocked: reason != "", Reason: reason,
	}
}

func estimateTokenCost(billing ModelBilling, rec RequestRecord) (int64, error) {
	if rec.InputTokens < 0 || rec.OutputTokens < 0 || rec.CachedTokens < 0 || rec.CacheCreationTokens < 0 {
		return 0, errors.New("upstream reported invalid token counts")
	}
	totalInput := float64(rec.InputTokens) + float64(rec.CachedTokens) + float64(rec.CacheCreationTokens)
	tier := billing.TokenPrices["default"]
	if long, ok := billing.TokenPrices["long_context"]; ok && totalInput > float64(tier.MaxPromptTokens) {
		tier = long
	}

	inputPrice := tier.InputPrice
	if rec.Backend != "messages" && rec.Backend != "embeddings" && rec.InputTokens > 0 && !rec.CacheWriteUsageKnown {
		// These protocols may not report which uncached input tokens were
		// written to a cache, so use the higher rate for those tokens.
		if !tier.hasCacheWritePrice || !validPrice(tier.CacheWritePrice) {
			return 0, errors.New("cache-write token price is missing or invalid")
		}
		inputPrice = max(inputPrice, tier.CacheWritePrice)
	}
	credits := float64(rec.InputTokens)*inputPrice + float64(rec.OutputTokens)*tier.OutputPrice
	if rec.CachedTokens > 0 {
		if !tier.hasCacheReadPrice || !validPrice(tier.CacheReadPrice) {
			return 0, errors.New("cache-read token price is missing or invalid")
		}
		credits += float64(rec.CachedTokens) * tier.CacheReadPrice
	}
	if rec.CacheCreationTokens > 0 {
		if rec.Backend == "messages" {
			writes, err := cacheWriteCost(tier, rec)
			if err != nil {
				return 0, err
			}
			credits += writes
		} else {
			if !tier.hasCacheWritePrice || !validPrice(tier.CacheWritePrice) {
				return 0, errors.New("cache-write token price is missing or invalid")
			}
			credits += float64(rec.CacheCreationTokens) * tier.CacheWritePrice
		}
	}
	// Prices are AI credits per batch; one AI credit is $0.01. Round up
	// per request so rounding cannot slowly understate the ceiling.
	nanos := math.Ceil(credits / float64(billing.BatchSize) * (nanoUSD / 100))
	if math.IsNaN(nanos) || math.IsInf(nanos, 0) || nanos < 0 || nanos >= float64(math.MaxInt64) {
		return 0, errors.New("estimated cost is invalid or too large")
	}
	return int64(nanos), nil
}

func cacheWriteCost(tier PriceTier, rec RequestRecord) (float64, error) {
	if rec.CacheWriteDetailsKnown {
		if rec.CacheWrite5mTokens < 0 || rec.CacheWrite1hTokens < 0 ||
			rec.CacheWrite5mTokens > rec.CacheCreationTokens ||
			rec.CacheWrite1hTokens != rec.CacheCreationTokens-rec.CacheWrite5mTokens {
			return 0, errors.New("cache-write token breakdown is invalid")
		}
		if rec.CacheWrite5mTokens > 0 && (!tier.hasCacheWritePrice || !validPrice(tier.CacheWritePrice)) {
			return 0, errors.New("five-minute cache-write token price is missing or invalid")
		}
		if rec.CacheWrite1hTokens > 0 && (!tier.hasCacheWrite1hPrice || !validPrice(tier.CacheWrite1hPrice)) {
			return 0, errors.New("one-hour cache-write token price is missing or invalid")
		}
		return float64(rec.CacheWrite5mTokens)*tier.CacheWritePrice +
			float64(rec.CacheWrite1hTokens)*tier.CacheWrite1hPrice, nil
	}
	if !tier.hasCacheWritePrice || !validPrice(tier.CacheWritePrice) ||
		!tier.hasCacheWrite1hPrice || !validPrice(tier.CacheWrite1hPrice) {
		return 0, errors.New("cache-write TTL or token price is missing")
	}
	return float64(rec.CacheCreationTokens) * max(tier.CacheWritePrice, tier.CacheWrite1hPrice), nil
}
