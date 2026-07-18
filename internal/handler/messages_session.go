package handler

import (
	"strings"
	"time"

	"github.com/tonghaoch/copilot-proxy-go/internal/state"
)

func buildSessionSnapshot(req *AnthropicRequest, betaHeader string, subagent *SubagentInfo, metrics MetricsStore) {
	snap := state.SessionSnapshot{
		ClaudeMDFiles: extractClaudeMDFiles(ParseSystemPrompt(req.System)),
		BetaFeatures:  betaHeader,
		LastSeen:      time.Now(),
	}
	for _, tool := range req.Tools {
		if strings.HasPrefix(tool.Name, "mcp__") {
			snap.MCPTools = append(snap.MCPTools, tool.Name)
		} else {
			snap.Tools = append(snap.Tools, tool.Name)
		}
	}
	if req.Thinking != nil {
		snap.ThinkingEnabled = req.Thinking.BudgetTokens > 0 || req.Thinking.Type != "disabled"
		snap.ThinkingBudget = req.Thinking.BudgetTokens
		snap.ThinkingType = req.Thinking.Type
	}
	if subagent != nil {
		snap.SubagentInfo = &state.SubagentInfoSnapshot{
			SessionID: subagent.SessionID, AgentID: subagent.AgentID, AgentType: subagent.AgentType,
		}
	}
	if req.Metadata != nil {
		snap.UserID = req.Metadata.UserID
	}
	metrics.UpdateSession(snap)
}
