package state

import "testing"

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
