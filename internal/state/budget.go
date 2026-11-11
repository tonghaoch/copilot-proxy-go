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
	mu                    sync.Mutex
	limit                 int64
	spent                 int64
	reserved              int64
	inFlight              int
	blocked               string
	conservativeEstimates int64
	providerSpent         map[string]int64
}

type BudgetAdmission struct {
	model    Model
	backend  string
	finished bool
}

type BudgetSnapshot struct {
	LimitUSD              float64 `json:"limit_usd"`
	EstimatedSpentUSD     float64 `json:"estimated_spent_usd"`
	ReportedUsageUSD      float64 `json:"reported_usage_usd"`
	ReservedUSD           float64 `json:"reserved_usd"`
	RemainingUSD          float64 `json:"remaining_usd"`
	InFlight              int     `json:"in_flight"`
	Blocked               bool    `json:"blocked"`
	Reason                string  `json:"reason,omitempty"`
	ConservativeEstimates int64   `json:"conservative_estimates"`
	// ProviderSpentUSD splits this process's estimate (excluding restored spend) by provider.
	ProviderSpentUSD map[string]float64 `json:"provider_spent_usd,omitempty"`
}

func NewTokenBudget(limitUSD float64) (*TokenBudget, error) {
	return NewTokenBudgetWithSpent(limitUSD, 0)
}

// NewTokenBudgetWithSpent restores an estimate carried over from an earlier process.
func NewTokenBudgetWithSpent(limitUSD, spentUSD float64) (*TokenBudget, error) {
	return NewTokenBudgetWithRestored(limitUSD, spentUSD, 0)
}

// NewTokenBudgetWithRestored splits a restored total into reported usage and reserve.
func NewTokenBudgetWithRestored(limitUSD, spentUSD, reservedUSD float64) (*TokenBudget, error) {
	nanos := math.Floor(limitUSD * nanoUSD)
	if math.IsNaN(nanos) || math.IsInf(nanos, 0) || nanos < 1 || nanos >= float64(math.MaxInt64) {
		return nil, errors.New("budget must be a positive, finite USD amount of at least $0.000000001")
	}
	spent := math.Ceil(spentUSD * nanoUSD)
	if math.IsNaN(spent) || math.IsInf(spent, 0) || spentUSD < 0 || spent >= float64(math.MaxInt64) {
		return nil, errors.New("prior estimated spend must be a non-negative, finite USD amount")
	}
	reserved := math.Ceil(reservedUSD * nanoUSD)
	if math.IsNaN(reserved) || math.IsInf(reserved, 0) || reservedUSD < 0 || reservedUSD > spentUSD || reserved >= float64(math.MaxInt64) {
		return nil, errors.New("prior reserved amount must be non-negative, finite, and no greater than prior estimated spend")
	}
	return &TokenBudget{limit: int64(nanos), spent: int64(spent), reserved: int64(reserved)}, nil
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
	if !isMessagesBackend(backend) && backend != "responses" && backend != "chat_completions" && backend != "embeddings" {
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
		if isMessagesBackend(backend) && (!tier.hasCacheWrite1hPrice || !validPrice(tier.CacheWrite1hPrice)) {
			return errors.New("one-hour cache-write token price is missing or invalid")
		}
	}
	return nil
}

func isMessagesBackend(backend string) bool {
	return backend == "messages" || backend == "anthropic"
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
	if !rec.UpstreamAttempted || (rec.UpstreamRejected && rec.UncertainRetries == 0) {
		return
	}
	if rec.Backend != admission.backend {
		b.pause(admission.model.ID, "upstream backend differed from the admitted pricing")
		return
	}
	complete := rec.InputUsageKnown && (admission.backend == "embeddings" || rec.OutputUsageKnown)
	if !rec.UpstreamRejected {
		if rec.UsageAccountingError != "" {
			b.pause(admission.model.ID, rec.UsageAccountingError)
			return
		}
		if rec.RequireIterations && len(rec.UsageIterations) == 0 {
			b.pause(admission.model.ID, "upstream omitted compaction iteration usage")
			return
		}
		if !complete && (rec.MultiIterationRisk || len(rec.UsageIterations) > 0) {
			b.pause(admission.model.ID, "multi-iteration request did not report complete usage")
			return
		}
	}
	var cost, reserve int64
	var err error
	if !rec.UpstreamRejected {
		if complete && len(rec.UsageIterations) > 0 {
			cost, err = estimateIterationTokenCost(admission.model.Billing, admission.backend, rec.UsageIterations)
		} else if complete {
			cost, err = estimateTokenCost(admission.model.Billing, rec)
		} else {
			cost, err = estimateIncompleteTokenCost(admission.model, rec)
			reserve = cost
		}
		if err != nil {
			b.pause(admission.model.ID, err.Error())
			return
		}
	}
	if rec.UncertainRetries > 0 {
		bound, err := estimateIncompleteTokenCost(admission.model, RequestRecord{Backend: admission.backend})
		if err != nil {
			b.pause(admission.model.ID, err.Error())
			return
		}
		if bound > 0 && rec.UncertainRetries > (math.MaxInt64-cost)/bound {
			b.pause(admission.model.ID, "estimated cost overflowed")
			return
		}
		retries := bound * rec.UncertainRetries
		cost += retries
		reserve += retries
	}
	if cost > math.MaxInt64-b.spent {
		b.pause(admission.model.ID, "estimated cost overflowed")
		return
	}
	b.spent += cost
	b.reserved += reserve
	if b.providerSpent == nil {
		b.providerSpent = make(map[string]int64)
	}
	b.providerSpent[Provider(admission.backend)] += cost
	if reserve > 0 {
		b.conservativeEstimates += rec.UncertainRetries
		if !complete && !rec.UpstreamRejected {
			b.conservativeEstimates++
		}
		slog.Warn("upstream usage incomplete; reserved conservative token cost",
			"model", admission.model.ID, "reserved_usd", float64(reserve)/nanoUSD)
	}
}

func estimateIterationTokenCost(billing ModelBilling, backend string, iterations []TokenUsageIteration) (int64, error) {
	var total int64
	for _, iteration := range iterations {
		if iteration.Type != "message" && iteration.Type != "compaction" {
			return 0, fmt.Errorf("unpriced Anthropic usage iteration %q", iteration.Type)
		}
		if !iteration.InputUsageKnown || !iteration.OutputUsageKnown {
			return 0, errors.New("Anthropic iteration token usage is incomplete")
		}
		cost, err := estimateTokenCost(billing, RequestRecord{
			Backend:     backend,
			InputTokens: iteration.InputTokens, OutputTokens: iteration.OutputTokens,
			CachedTokens: iteration.CachedTokens, CacheCreationTokens: iteration.CacheCreationTokens,
			CacheWrite5mTokens: iteration.CacheWrite5mTokens, CacheWrite1hTokens: iteration.CacheWrite1hTokens,
			CacheWriteDetailsKnown: iteration.CacheWriteDetailsKnown,
		})
		if err != nil {
			return 0, err
		}
		if cost > math.MaxInt64-total {
			return 0, errors.New("estimated cost overflowed")
		}
		total += cost
	}
	return total, nil
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
		ReportedUsageUSD: float64(b.spent-b.reserved) / nanoUSD, ReservedUSD: float64(b.reserved) / nanoUSD,
		RemainingUSD: float64(remaining) / nanoUSD, InFlight: b.inFlight,
		Blocked: reason != "", Reason: reason, ConservativeEstimates: b.conservativeEstimates,
		ProviderSpentUSD: providerUSD(b.providerSpent),
	}
}

func providerUSD(spent map[string]int64) map[string]float64 {
	if len(spent) == 0 {
		return nil
	}
	out := make(map[string]float64, len(spent))
	for provider, nanos := range spent {
		out[provider] = float64(nanos) / nanoUSD
	}
	return out
}

func estimateIncompleteTokenCost(model Model, rec RequestRecord) (int64, error) {
	limits := model.Capabilities.Limits
	if !rec.OutputUsageKnown && rec.Backend != "embeddings" {
		if limits.MaxOutputTokens <= 0 {
			return 0, errors.New("upstream did not report output usage and model output limit is missing")
		}
		rec.OutputTokens = int64(limits.MaxOutputTokens)
	}
	if rec.InputUsageKnown {
		return estimateTokenCost(model.Billing, rec)
	}
	if rec.Backend == "embeddings" {
		return 0, errors.New("upstream did not report input usage for an embeddings batch")
	}
	if rec.InputTokens < 0 || rec.OutputTokens < 0 || rec.CachedTokens < 0 || rec.CacheCreationTokens < 0 {
		return 0, errors.New("upstream reported invalid token counts")
	}
	maxInput := max(limits.MaxContextWindowTokens, limits.MaxPromptTokens)
	for _, tier := range model.Billing.TokenPrices {
		maxInput = max(maxInput, tier.MaxPromptTokens)
	}
	if maxInput <= 0 {
		return 0, errors.New("upstream did not report input usage and model input limit is missing")
	}
	observedInput := float64(rec.InputTokens) + float64(rec.CachedTokens) + float64(rec.CacheCreationTokens)
	if observedInput > float64(maxInput) {
		return 0, errors.New("partial input usage exceeds the model limit")
	}
	var inputPrice, outputPrice float64
	for _, tier := range model.Billing.TokenPrices {
		inputPrice = max(inputPrice, tier.InputPrice, tier.CacheReadPrice, tier.CacheWritePrice)
		if isMessagesBackend(rec.Backend) {
			inputPrice = max(inputPrice, tier.CacheWrite1hPrice)
		}
		outputPrice = max(outputPrice, tier.OutputPrice)
	}
	credits := float64(maxInput)*inputPrice + float64(rec.OutputTokens)*outputPrice
	return creditsToNanoUSD(model.Billing.BatchSize, credits)
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
	if !isMessagesBackend(rec.Backend) && rec.Backend != "embeddings" && rec.InputTokens > 0 && !rec.CacheWriteUsageKnown {
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
		if isMessagesBackend(rec.Backend) {
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
	return creditsToNanoUSD(billing.BatchSize, credits)
}

func creditsToNanoUSD(batchSize int64, credits float64) (int64, error) {
	// Prices are AI credits per batch; one AI credit is $0.01.
	nanos := math.Ceil(credits / float64(batchSize) * (nanoUSD / 100))
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
