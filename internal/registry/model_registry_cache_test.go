package registry

import "testing"

func TestGetAvailableModelsReturnsClonedSnapshots(t *testing.T) {
	r := newTestModelRegistry()
	r.RegisterClient("client-1", "OpenAI", []*ModelInfo{{ID: "m1", OwnedBy: "team-a", DisplayName: "Model One"}})

	first := r.GetAvailableModels("openai")
	if len(first) != 1 {
		t.Fatalf("expected 1 model, got %d", len(first))
	}
	first[0]["id"] = "mutated"
	first[0]["display_name"] = "Mutated"

	second := r.GetAvailableModels("openai")
	if got := second[0]["id"]; got != "openai/m1" {
		t.Fatalf("expected cached snapshot to stay isolated, got id %v", got)
	}
	if got := second[0]["display_name"]; got != "Model One" {
		t.Fatalf("expected cached snapshot to stay isolated, got display_name %v", got)
	}
}

func TestGetAvailableModelsInvalidatesCacheOnRegistryChanges(t *testing.T) {
	r := newTestModelRegistry()
	r.RegisterClient("client-1", "OpenAI", []*ModelInfo{{ID: "m1", OwnedBy: "team-a", DisplayName: "Model One"}})

	models := r.GetAvailableModels("openai")
	if len(models) != 1 {
		t.Fatalf("expected 1 model, got %d", len(models))
	}
	if got := models[0]["display_name"]; got != "Model One" {
		t.Fatalf("expected initial display_name Model One, got %v", got)
	}

	r.RegisterClient("client-1", "OpenAI", []*ModelInfo{{ID: "m1", OwnedBy: "team-a", DisplayName: "Model One Updated"}})
	models = r.GetAvailableModels("openai")
	if got := models[0]["display_name"]; got != "Model One Updated" {
		t.Fatalf("expected updated display_name after cache invalidation, got %v", got)
	}

	r.SuspendClientModel("client-1", "m1", "manual")
	models = r.GetAvailableModels("openai")
	if len(models) != 0 {
		t.Fatalf("expected no available models after suspension, got %d", len(models))
	}

	r.ResumeClientModel("client-1", "m1")
	models = r.GetAvailableModels("openai")
	if len(models) != 1 {
		t.Fatalf("expected model to reappear after resume, got %d", len(models))
	}
}

func TestGetAvailableModelsQualifiesSharedModelByProvider(t *testing.T) {
	r := newTestModelRegistry()
	r.RegisterClient("client-codex", "codex", []*ModelInfo{{ID: "gpt-5.5", OwnedBy: "openai", DisplayName: "Codex GPT"}})
	r.RegisterClient("client-copilot", "github-copilot", []*ModelInfo{{ID: "gpt-5.5", OwnedBy: "github", DisplayName: "Copilot GPT"}})

	models := r.GetAvailableModels("openai")
	if len(models) != 2 {
		t.Fatalf("expected 2 provider-qualified models, got %#v", models)
	}

	byID := map[string]map[string]any{}
	for _, model := range models {
		id, _ := model["id"].(string)
		byID[id] = model
	}
	if got := byID["codex/gpt-5.5"]["display_name"]; got != "Codex GPT" {
		t.Fatalf("codex model = %#v", byID["codex/gpt-5.5"])
	}
	if got := byID["github-copilot/gpt-5.5"]["display_name"]; got != "Copilot GPT" {
		t.Fatalf("github-copilot model = %#v", byID["github-copilot/gpt-5.5"])
	}
}
