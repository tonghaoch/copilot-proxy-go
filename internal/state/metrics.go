package state

import (
	"sync"
	"time"
)

// RequestRecord holds per-request metrics.
type RequestRecord struct {
	Timestamp           time.Time `json:"timestamp"`
	Endpoint            string    `json:"endpoint"`     // messages, chat_completions, responses, embeddings
	Model               string    `json:"model"`        // original model requested
	RoutedModel         string    `json:"routed_model"` // after small-model routing
	Backend             string    `json:"backend"`      // anthropic, messages, responses, chat_completions
	RequestType         string    `json:"request_type"` // normal, compact, warmup
	Initiator           string    `json:"initiator"`    // user, agent
	HasVision           bool      `json:"has_vision"`
	Streaming           bool      `json:"streaming"`
	ToolCount           int       `json:"tool_count"`
	ThinkingBudget      int       `json:"thinking_budget"`
	InputTokens         int64     `json:"input_tokens"`
	OutputTokens        int64     `json:"output_tokens"`
	CachedTokens        int64     `json:"cached_tokens"`
	CacheCreationTokens int64     `json:"cache_creation_tokens"`
	StopReason          string    `json:"stop_reason"`
	LatencyMs           int64     `json:"latency_ms"`
	StatusCode          int       `json:"status_code"`
	Error               string    `json:"error,omitempty"`
	Fallback            string    `json:"fallback,omitempty"` // why a direct Anthropic call was retried via Copilot

	UpstreamAttempted      bool                  `json:"-"`
	UpstreamRejected       bool                  `json:"-"`
	UncertainRetries       int64                 `json:"-"`
	InputUsageKnown        bool                  `json:"-"`
	OutputUsageKnown       bool                  `json:"-"`
	CacheWrite5mTokens     int64                 `json:"cache_write_5m_tokens,omitempty"`
	CacheWrite1hTokens     int64                 `json:"cache_write_1h_tokens,omitempty"`
	CacheWriteDetailsKnown bool                  `json:"cache_write_details_known,omitempty"`
	CacheWriteUsageKnown   bool                  `json:"-"`
	UsageIterations        []TokenUsageIteration `json:"-"`
	MultiIterationRisk     bool                  `json:"-"`
	RequireIterations      bool                  `json:"-"`
	UsageAccountingError   string                `json:"-"`
}

// TokenUsageIteration holds a single Anthropic sampling or compaction charge.
type TokenUsageIteration struct {
	Type                   string
	InputTokens            int64
	OutputTokens           int64
	CachedTokens           int64
	CacheCreationTokens    int64
	CacheWrite5mTokens     int64
	CacheWrite1hTokens     int64
	InputUsageKnown        bool
	OutputUsageKnown       bool
	CacheWriteDetailsKnown bool
}

// ClaudeMDFile represents an extracted CLAUDE.md file from the system prompt.
type ClaudeMDFile struct {
	Path    string `json:"path"`
	Content string `json:"content"`
}

// SessionSnapshot holds session data updated on each Messages request.
type SessionSnapshot struct {
	ClaudeMDFiles   []ClaudeMDFile        `json:"claude_md_files"`
	Tools           []string              `json:"tools"`
	MCPTools        []string              `json:"mcp_tools"`
	ThinkingEnabled bool                  `json:"thinking_enabled"`
	ThinkingBudget  int                   `json:"thinking_budget"`
	ThinkingType    string                `json:"thinking_type"`
	BetaFeatures    string                `json:"beta_features"`
	SubagentInfo    *SubagentInfoSnapshot `json:"subagent,omitempty"`
	UserID          string                `json:"user_id"`
	LastSeen        time.Time             `json:"last_seen"`
}

// SubagentInfoSnapshot holds subagent detection data for the session snapshot.
type SubagentInfoSnapshot struct {
	SessionID string `json:"session_id"`
	AgentID   string `json:"agent_id"`
	AgentType string `json:"agent_type"`
}

// Aggregates holds incrementally maintained statistics.
type Aggregates struct {
	TotalRequests            int64                    `json:"total_requests"`
	TotalInputTokens         int64                    `json:"total_input_tokens"`
	TotalOutputTokens        int64                    `json:"total_output_tokens"`
	TotalCachedTokens        int64                    `json:"total_cached_tokens"`
	TotalCacheCreationTokens int64                    `json:"total_cache_creation_tokens"`
	TotalCacheWrite5mTokens  int64                    `json:"total_cache_write_5m_tokens"`
	TotalCacheWrite1hTokens  int64                    `json:"total_cache_write_1h_tokens"`
	ModelCounts              map[string]int64         `json:"model_counts"`
	BackendCounts            map[string]int64         `json:"backend_counts"`
	TypeCounts               map[string]int64         `json:"type_counts"`
	Providers                map[string]ProviderUsage `json:"providers"`
	FallbackCount            int64                    `json:"fallback_count"`
	StartTime                time.Time                `json:"start_time"`
}

// ProviderUsage separates direct Anthropic API traffic from Copilot traffic.
type ProviderUsage struct {
	Requests            int64 `json:"requests"`
	Errors              int64 `json:"errors"`
	InputTokens         int64 `json:"input_tokens"`
	OutputTokens        int64 `json:"output_tokens"`
	CachedTokens        int64 `json:"cached_tokens"`
	CacheCreationTokens int64 `json:"cache_creation_tokens"`
}

// Provider names the upstream account a backend bills against.
func Provider(backend string) string {
	if backend == "anthropic" {
		return "anthropic"
	}
	return "copilot"
}

// MetricsSnapshot is the read-consistent copy returned by Snapshot().
type MetricsSnapshot struct {
	Aggregates Aggregates      `json:"aggregates"`
	Session    SessionSnapshot `json:"session"`
	Recent     []RequestRecord `json:"recent"`
}

const ringBufferSize = 200

// metricsStore is the in-memory metrics store.
type metricsStore struct {
	mu        sync.RWMutex
	agg       Aggregates
	session   SessionSnapshot
	ring      []RequestRecord
	ringPos   int
	ringCount int
}

// Metrics is the singleton metrics store instance.
var Metrics = &metricsStore{
	agg: Aggregates{
		ModelCounts:   make(map[string]int64),
		BackendCounts: make(map[string]int64),
		TypeCounts:    make(map[string]int64),
		Providers:     make(map[string]ProviderUsage),
		StartTime:     time.Now(),
	},
	ring: make([]RequestRecord, ringBufferSize),
}

// RecordRequest appends a record to the ring buffer and updates aggregates.
func (m *metricsStore) RecordRequest(rec RequestRecord) {
	m.mu.Lock()
	defer m.mu.Unlock()

	// Append to ring buffer
	m.ring[m.ringPos] = rec
	m.ringPos = (m.ringPos + 1) % ringBufferSize
	if m.ringCount < ringBufferSize {
		m.ringCount++
	}

	// Update aggregates
	m.agg.TotalRequests++
	m.agg.TotalInputTokens += rec.InputTokens
	m.agg.TotalOutputTokens += rec.OutputTokens
	m.agg.TotalCachedTokens += rec.CachedTokens
	m.agg.TotalCacheCreationTokens += rec.CacheCreationTokens
	if rec.CacheWriteDetailsKnown {
		m.agg.TotalCacheWrite5mTokens += rec.CacheWrite5mTokens
		m.agg.TotalCacheWrite1hTokens += rec.CacheWrite1hTokens
	}

	model := rec.RoutedModel
	if model == "" {
		model = rec.Model
	}
	if model != "" {
		m.agg.ModelCounts[model]++
	}

	if rec.Backend != "" {
		m.agg.BackendCounts[rec.Backend]++
	}
	if rec.RequestType != "" {
		m.agg.TypeCounts[rec.RequestType]++
	}
	if rec.Fallback != "" {
		m.agg.FallbackCount++
	}
	if rec.Backend != "" {
		if m.agg.Providers == nil {
			m.agg.Providers = make(map[string]ProviderUsage)
		}
		p := m.agg.Providers[Provider(rec.Backend)]
		p.Requests++
		if rec.StatusCode >= 400 {
			p.Errors++
		}
		p.InputTokens += rec.InputTokens
		p.OutputTokens += rec.OutputTokens
		p.CachedTokens += rec.CachedTokens
		p.CacheCreationTokens += rec.CacheCreationTokens
		m.agg.Providers[Provider(rec.Backend)] = p
	}
}

// UpdateSession updates the session snapshot.
func (m *metricsStore) UpdateSession(snap SessionSnapshot) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.session = snap
}

// Snapshot returns a read-consistent copy of all metrics.
func (m *metricsStore) Snapshot() MetricsSnapshot {
	m.mu.RLock()
	defer m.mu.RUnlock()

	// Copy aggregates
	agg := m.agg
	agg.ModelCounts = copyMap(m.agg.ModelCounts)
	agg.BackendCounts = copyMap(m.agg.BackendCounts)
	agg.TypeCounts = copyMap(m.agg.TypeCounts)
	agg.Providers = copyMap(m.agg.Providers)

	// Copy session
	session := m.session
	if m.session.ClaudeMDFiles != nil {
		session.ClaudeMDFiles = make([]ClaudeMDFile, len(m.session.ClaudeMDFiles))
		copy(session.ClaudeMDFiles, m.session.ClaudeMDFiles)
	}
	if m.session.Tools != nil {
		session.Tools = make([]string, len(m.session.Tools))
		copy(session.Tools, m.session.Tools)
	}
	if m.session.MCPTools != nil {
		session.MCPTools = make([]string, len(m.session.MCPTools))
		copy(session.MCPTools, m.session.MCPTools)
	}

	// Copy recent records from ring buffer (newest first)
	recent := make([]RequestRecord, 0, m.ringCount)
	for i := 0; i < m.ringCount; i++ {
		idx := (m.ringPos - 1 - i + ringBufferSize) % ringBufferSize
		recent = append(recent, m.ring[idx])
	}

	return MetricsSnapshot{
		Aggregates: agg,
		Session:    session,
		Recent:     recent,
	}
}

func copyMap[V any](src map[string]V) map[string]V {
	dst := make(map[string]V, len(src))
	for k, v := range src {
		dst[k] = v
	}
	return dst
}
