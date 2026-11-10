package state

import (
	"testing"
	"time"
)

func TestModelsAreReturnedAsCopies(t *testing.T) {
	s := &State{}
	input := []Model{{
		ID: "model-a", SupportedEndpoints: []string{"/responses"},
		Billing:      ModelBilling{TokenPrices: map[string]PriceTier{"default": {InputPrice: 10}}},
		Capabilities: ModelCapabilities{Limits: ModelLimits{Vision: VisionLimit{SupportedMediaTypes: []string{"image/png"}}}},
	}}
	s.SetModels(input)
	input[0].Capabilities.Limits.Vision.SupportedMediaTypes[0] = "changed"

	models := s.GetModels()
	models[0].ID = "changed"
	models[0].SupportedEndpoints[0] = "/changed"
	models[0].Billing.TokenPrices["default"] = PriceTier{InputPrice: 999}
	models[0].Capabilities.Limits.Vision.SupportedMediaTypes[0] = "changed"

	model := s.FindModel("model-a")
	if model == nil || model.SupportedEndpoints[0] != "/responses" || model.Billing.TokenPrices["default"].InputPrice != 10 ||
		model.Capabilities.Limits.Vision.SupportedMediaTypes[0] != "image/png" {
		t.Fatalf("stored model was mutated through GetModels: %+v", model)
	}

	model.ID = "also-changed"
	model.Capabilities.Limits.Vision.SupportedMediaTypes[0] = "changed"
	if got := s.FindModel("model-a"); got == nil || got.Capabilities.Limits.Vision.SupportedMediaTypes[0] != "image/png" {
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
