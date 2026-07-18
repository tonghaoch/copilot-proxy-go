package state

import (
	"testing"
	"time"
)

func TestModelsAreReturnedAsCopies(t *testing.T) {
	s := &State{}
	s.SetModels([]Model{{ID: "model-a", SupportedEndpoints: []string{"/responses"}}})

	models := s.GetModels()
	models[0].ID = "changed"
	models[0].SupportedEndpoints[0] = "/changed"

	model := s.FindModel("model-a")
	if model == nil || model.SupportedEndpoints[0] != "/responses" {
		t.Fatalf("stored model was mutated through GetModels: %+v", model)
	}

	model.ID = "also-changed"
	if s.FindModel("model-a") == nil {
		t.Fatal("stored model was mutated through FindModel")
	}
}

func TestMetricsDoNotCreateEmptyModelBucket(t *testing.T) {
	m := &metricsStore{
		agg: Aggregates{
			ModelCounts:   make(map[string]int64),
			BackendCounts: make(map[string]int64),
			TypeCounts:    make(map[string]int64),
			StartTime:     time.Now(),
		},
		ring: make([]RequestRecord, ringBufferSize),
	}
	m.RecordRequest(RequestRecord{Endpoint: "responses", StatusCode: 400})
	if _, exists := m.Snapshot().Aggregates.ModelCounts[""]; exists {
		t.Fatal("malformed requests must not create an empty model bucket")
	}
}
