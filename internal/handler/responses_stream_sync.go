package handler

import (
	"encoding/json"
	"fmt"
	"math/rand"
)

// StreamIDSync fixes ID inconsistencies between response.output_item.added
// and response.output_item.done events from Copilot, which would otherwise
// crash @ai-sdk/openai.
type StreamIDSync struct {
	// Maps output_index to canonical ID from the "added" event
	canonicalIDs map[int]string
}

// NewStreamIDSync creates a new StreamIDSync.
func NewStreamIDSync() *StreamIDSync {
	return &StreamIDSync{
		canonicalIDs: make(map[int]string),
	}
}

// Process applies ID synchronization to a stream event, returning the
// (potentially modified) data string.
func (s *StreamIDSync) Process(eventType, data string) string {
	switch eventType {
	case "response.output_item.added":
		return s.processAdded(data)
	case "response.output_item.done":
		return s.processDone(data)
	default:
		return s.patchItemID(data)
	}
}

// patchItemID patches the item_id field in events that have output_index,
// matching the canonical ID from the added event.
func (s *StreamIDSync) patchItemID(data string) string {
	var evt struct {
		OutputIndex *int   `json:"output_index,omitempty"`
		ItemID      string `json:"item_id,omitempty"`
	}
	if err := json.Unmarshal([]byte(data), &evt); err != nil || evt.OutputIndex == nil {
		return data
	}

	canonicalID, exists := s.canonicalIDs[*evt.OutputIndex]
	if !exists || canonicalID == "" {
		return data
	}

	// Patch item_id if it differs
	if evt.ItemID != canonicalID {
		var raw map[string]any
		if err := json.Unmarshal([]byte(data), &raw); err != nil {
			return data
		}
		raw["item_id"] = canonicalID
		patched, _ := json.Marshal(raw)
		return string(patched)
	}

	return data
}

func (s *StreamIDSync) processAdded(data string) string {
	var evt struct {
		OutputIndex int `json:"output_index"`
		Item        struct {
			ID string `json:"id"`
		} `json:"item"`
	}
	if err := json.Unmarshal([]byte(data), &evt); err != nil {
		return data
	}

	id := evt.Item.ID
	if id == "" {
		// Generate synthetic ID
		id = fmt.Sprintf("oi_%d_%s", evt.OutputIndex, randomBase36(16))
		// Patch the data with the synthetic ID
		var raw map[string]any
		json.Unmarshal([]byte(data), &raw)
		if item, ok := raw["item"].(map[string]any); ok {
			item["id"] = id
		}
		patched, _ := json.Marshal(raw)
		data = string(patched)
	}

	s.canonicalIDs[evt.OutputIndex] = id
	return data
}

func (s *StreamIDSync) processDone(data string) string {
	var evt struct {
		OutputIndex int `json:"output_index"`
		Item        struct {
			ID string `json:"id"`
		} `json:"item"`
	}
	if err := json.Unmarshal([]byte(data), &evt); err != nil {
		return data
	}

	canonicalID, exists := s.canonicalIDs[evt.OutputIndex]
	if !exists {
		return data
	}

	// If IDs don't match, patch the done event with the canonical ID
	if evt.Item.ID != canonicalID {
		var raw map[string]any
		json.Unmarshal([]byte(data), &raw)
		if item, ok := raw["item"].(map[string]any); ok {
			item["id"] = canonicalID
		}
		patched, _ := json.Marshal(raw)
		return string(patched)
	}

	return data
}

func randomBase36(n int) string {
	const base36Chars = "0123456789abcdefghijklmnopqrstuvwxyz"
	b := make([]byte, n)
	for i := range b {
		b[i] = base36Chars[rand.Intn(len(base36Chars))]
	}
	return string(b)
}
