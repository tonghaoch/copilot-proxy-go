package state

import (
	"sync"
	"time"
)

// RequestRecord holds per-request metrics.
type RequestRecord struct {
	Timestamp   time.Time `json:"timestamp"`
	Endpoint    string    `json:"endpoint"`    // messages, chat_completions, responses
	Model       string    `json:"model"`       // original model requested
	RoutedModel string    `json:"routed_model"` // after small-model routing
	Backend     string    `json:"backend"`     // messages, responses, chat_completions
	RequestType string    `json:"request_type"` // normal, compact, warmup
	Initiator   string    `json:"initiator"`   // user, agent
	HasVision   bool      `json:"has_vision"`
	Streaming   bool      `json:"streaming"`
	ToolCount   int       `json:"tool_count"`
	ThinkingBudget int   `json:"thinking_budget"`
	InputTokens  int64   `json:"input_tokens"`
	OutputTokens int64   `json:"output_tokens"`
	CachedTokens int64   `json:"cached_tokens"`
	StopReason  string    `json:"stop_reason"`
	LatencyMs   int64     `json:"latency_ms"`
	StatusCode  int       `json:"status_code"`
	Error       string    `json:"error,omitempty"`
}

// ClaudeMDFile represents an extracted CLAUDE.md file from the system prompt.
type ClaudeMDFile struct {
	Path    string `json:"path"`
	Content string `json:"content"`
}

// SessionSnapshot holds session data updated on each Messages request.
type SessionSnapshot struct {
	ClaudeMDFiles   []ClaudeMDFile `json:"claude_md_files"`
	Tools           []string       `json:"tools"`
	MCPTools        []string       `json:"mcp_tools"`
	ThinkingEnabled bool           `json:"thinking_enabled"`
	ThinkingBudget  int            `json:"thinking_budget"`
	ThinkingType    string         `json:"thinking_type"`
	BetaFeatures    string         `json:"beta_features"`
	SubagentInfo    *SubagentInfoSnapshot `json:"subagent,omitempty"`
	UserID          string         `json:"user_id"`
	LastSeen        time.Time      `json:"last_seen"`
}

// SubagentInfoSnapshot holds subagent detection data for the session snapshot.
type SubagentInfoSnapshot struct {
	SessionID string `json:"session_id"`
	AgentID   string `json:"agent_id"`
	AgentType string `json:"agent_type"`
}

// Aggregates holds incrementally maintained statistics.
type Aggregates struct {
	TotalRequests     int64            `json:"total_requests"`
	TotalInputTokens  int64            `json:"total_input_tokens"`
	TotalOutputTokens int64            `json:"total_output_tokens"`
	TotalCachedTokens int64            `json:"total_cached_tokens"`
	ModelCounts       map[string]int64 `json:"model_counts"`
	BackendCounts     map[string]int64 `json:"backend_counts"`
	TypeCounts        map[string]int64 `json:"type_counts"`
	StartTime         time.Time        `json:"start_time"`
}

// MetricsSnapshot is the read-consistent copy returned by Snapshot().
type MetricsSnapshot struct {
	Aggregates Aggregates       `json:"aggregates"`
	Session    SessionSnapshot  `json:"session"`
	Recent     []RequestRecord  `json:"recent"`
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

	model := rec.RoutedModel
	if model == "" {
		model = rec.Model
	}
	m.agg.ModelCounts[model]++

	if rec.Backend != "" {
		m.agg.BackendCounts[rec.Backend]++
	}
	if rec.RequestType != "" {
		m.agg.TypeCounts[rec.RequestType]++
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

func copyMap(src map[string]int64) map[string]int64 {
	dst := make(map[string]int64, len(src))
	for k, v := range src {
		dst[k] = v
	}
	return dst
}
