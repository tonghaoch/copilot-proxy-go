package handler

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/tonghaoch/copilot-proxy-go/internal/config"
	"github.com/tonghaoch/copilot-proxy-go/internal/state"
)

// statsResponse is the JSON response for GET /api/stats.
type statsResponse struct {
	UptimeSeconds int64              `json:"uptime_seconds"`
	TotalRequests int64              `json:"total_requests"`
	Tokens        statsTokens        `json:"tokens"`
	ModelCounts   map[string]int64   `json:"model_counts"`
	BackendCounts map[string]int64   `json:"backend_counts"`
	TypeCounts    map[string]int64   `json:"type_counts"`
	Session       *statsSession      `json:"session"`
	Recent        []state.RequestRecord `json:"recent"`
	Config        statsConfig        `json:"config"`
}

type statsTokens struct {
	Input  int64 `json:"input"`
	Output int64 `json:"output"`
	Cached int64 `json:"cached"`
}

type statsSession struct {
	ClaudeMDFiles   []state.ClaudeMDFile         `json:"claude_md_files"`
	Tools           []string                      `json:"tools"`
	MCPTools        []string                      `json:"mcp_tools"`
	Thinking        statsThinking                 `json:"thinking"`
	BetaFeatures    string                        `json:"beta_features"`
	Subagent        *state.SubagentInfoSnapshot   `json:"subagent,omitempty"`
	UserID          string                        `json:"user_id"`
	LastSeen        *time.Time                    `json:"last_seen,omitempty"`
}

type statsThinking struct {
	Enabled bool   `json:"enabled"`
	Budget  int    `json:"budget"`
	Type    string `json:"type"`
}

type statsConfig struct {
	AccountType          string            `json:"account_type"`
	VSCodeVersion        string            `json:"vs_code_version"`
	SmallModel           string            `json:"small_model"`
	CompactUseSmallModel bool              `json:"compact_use_small_model"`
	ReasoningEfforts     map[string]string `json:"reasoning_efforts"`
	AuthEnabled          bool              `json:"auth_enabled"`
	APIKeyCount          int               `json:"api_key_count"`
}

// Stats handles GET /api/stats — returns all dashboard metrics as JSON.
func Stats(w http.ResponseWriter, r *http.Request) {
	snap := state.Metrics.Snapshot()
	cfg := config.Get()
	apiKeys := config.GetAPIKeys()

	// Limit recent to last 50 for the API response
	recent := snap.Recent
	if len(recent) > 50 {
		recent = recent[:50]
	}

	var session *statsSession
	if !snap.Session.LastSeen.IsZero() {
		lastSeen := snap.Session.LastSeen
		session = &statsSession{
			ClaudeMDFiles: snap.Session.ClaudeMDFiles,
			Tools:         snap.Session.Tools,
			MCPTools:      snap.Session.MCPTools,
			Thinking: statsThinking{
				Enabled: snap.Session.ThinkingEnabled,
				Budget:  snap.Session.ThinkingBudget,
				Type:    snap.Session.ThinkingType,
			},
			BetaFeatures: snap.Session.BetaFeatures,
			Subagent:     snap.Session.SubagentInfo,
			UserID:       snap.Session.UserID,
			LastSeen:     &lastSeen,
		}
	}

	resp := statsResponse{
		UptimeSeconds: int64(time.Since(snap.Aggregates.StartTime).Seconds()),
		TotalRequests: snap.Aggregates.TotalRequests,
		Tokens: statsTokens{
			Input:  snap.Aggregates.TotalInputTokens,
			Output: snap.Aggregates.TotalOutputTokens,
			Cached: snap.Aggregates.TotalCachedTokens,
		},
		ModelCounts:   snap.Aggregates.ModelCounts,
		BackendCounts: snap.Aggregates.BackendCounts,
		TypeCounts:    snap.Aggregates.TypeCounts,
		Session:       session,
		Recent:        recent,
		Config: statsConfig{
			AccountType:          state.Global.GetAccountType(),
			VSCodeVersion:        state.Global.GetVSCodeVersion(),
			SmallModel:           cfg.SmallModel,
			CompactUseSmallModel: cfg.CompactUseSmallModel,
			ReasoningEfforts:     cfg.ModelReasoningEfforts,
			AuthEnabled:          len(apiKeys) > 0,
			APIKeyCount:          len(apiKeys),
		},
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}
