package cliproxy

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/config"
)

func TestParseCodexModelInventory(t *testing.T) {
	inventory, err := ParseCodexModelInventory(strings.NewReader(`{"models":[{"slug":"gpt-5.3-codex","display_name":"GPT"},{"slug":"gpt-5.3-codex"},{"slug":""}]}`))
	if err != nil {
		t.Fatalf("ParseCodexModelInventory: %v", err)
	}
	if inventory.Provider != "codex" {
		t.Fatalf("provider = %q, want codex", inventory.Provider)
	}
	if len(inventory.Models) != 1 || inventory.Models[0].ID != "gpt-5.3-codex" {
		t.Fatalf("models = %#v", inventory.Models)
	}
	if inventory.Models[0].Raw["display_name"] != "GPT" {
		t.Fatalf("raw model not preserved: %#v", inventory.Models[0].Raw)
	}
}

func TestParseOpenAICompatModelInventory(t *testing.T) {
	inventory, err := ParseOpenAICompatModelInventory("openai", strings.NewReader(`{"data":[{"id":"gpt-4.1","owned_by":"openai"},{"id":"gpt-4.1"},{"id":"gpt-4.1-mini","owned_by":"openai"}]}`))
	if err != nil {
		t.Fatalf("ParseOpenAICompatModelInventory: %v", err)
	}
	if len(inventory.Models) != 2 {
		t.Fatalf("models len = %d, want 2: %#v", len(inventory.Models), inventory.Models)
	}
	if inventory.Models[0].ID != "gpt-4.1" || inventory.Models[0].OwnedBy != "openai" {
		t.Fatalf("first model = %#v", inventory.Models[0])
	}
	if inventory.Models[1].ID != "gpt-4.1-mini" {
		t.Fatalf("second model = %#v", inventory.Models[1])
	}
}

func TestParseGeminiModelInventory(t *testing.T) {
	inventory, err := ParseGeminiModelInventory(strings.NewReader(`{"models":[{"name":"models/gemini-2.5-pro"},{"name":"models/gemini-2.5-pro"},{"name":"gemini-2.5-flash"}]}`))
	if err != nil {
		t.Fatalf("ParseGeminiModelInventory: %v", err)
	}
	if len(inventory.Models) != 2 {
		t.Fatalf("models len = %d, want 2: %#v", len(inventory.Models), inventory.Models)
	}
	if inventory.Models[0].ID != "gemini-2.5-pro" || inventory.Models[1].ID != "gemini-2.5-flash" {
		t.Fatalf("models = %#v", inventory.Models)
	}
}

func TestRegisterModelsForAuthUsesLiveInventory(t *testing.T) {
	clientID := "test-live-inventory-auth"
	t.Cleanup(func() {
		registry.GetGlobalRegistry().UnregisterClient(clientID)
	})

	service := &Service{cfg: &config.Config{}}
	auth := &coreauth.Auth{
		ID:       clientID,
		Provider: "gemini",
		Attributes: map[string]string{
			"api_key": "test-key",
		},
		ModelInventory: &coreauth.LiveModelInventory{
			Provider: "gemini",
			Models: []coreauth.LiveModelEntry{
				{ID: "live-gemini-model", OwnedBy: "google"},
			},
		},
	}

	service.registerModelsForAuth(t.Context(), auth)
	models := registry.GetGlobalRegistry().GetModelsForClient(clientID)
	if len(models) != 1 {
		t.Fatalf("models len = %d, want 1: %#v", len(models), models)
	}
	if models[0].ID != "live-gemini-model" {
		t.Fatalf("model id = %q, want live-gemini-model", models[0].ID)
	}
}

func TestRegisterModelsForAuthPreservesPrefixBehaviorWithLiveInventory(t *testing.T) {
	clientID := "test-live-prefix-auth"
	t.Cleanup(func() {
		registry.GetGlobalRegistry().UnregisterClient(clientID)
	})

	service := &Service{cfg: &config.Config{}}
	auth := &coreauth.Auth{
		ID:       clientID,
		Provider: "gemini",
		Prefix:   "team-a",
		Attributes: map[string]string{
			"api_key": "test-key",
		},
		ModelInventory: &coreauth.LiveModelInventory{
			Provider: "gemini",
			Models: []coreauth.LiveModelEntry{
				{ID: "live-prefix-model"},
			},
		},
	}

	service.registerModelsForAuth(t.Context(), auth)
	models := registry.GetGlobalRegistry().GetModelsForClient(clientID)
	ids := modelIDSet(models)
	if !ids["live-prefix-model"] || !ids["team-a/live-prefix-model"] {
		t.Fatalf("prefix behavior changed, models = %#v", ids)
	}
}

func TestRegisterModelsForAuthForcePrefixWithLiveInventory(t *testing.T) {
	clientID := "test-live-force-prefix-auth"
	t.Cleanup(func() {
		registry.GetGlobalRegistry().UnregisterClient(clientID)
	})

	service := &Service{cfg: &config.Config{SDKConfig: config.SDKConfig{ForceModelPrefix: true}}}
	auth := &coreauth.Auth{
		ID:       clientID,
		Provider: "gemini",
		Prefix:   "team-a",
		Attributes: map[string]string{
			"api_key": "test-key",
		},
		ModelInventory: &coreauth.LiveModelInventory{
			Provider: "gemini",
			Models: []coreauth.LiveModelEntry{
				{ID: "live-force-prefix-model"},
			},
		},
	}

	service.registerModelsForAuth(t.Context(), auth)
	models := registry.GetGlobalRegistry().GetModelsForClient(clientID)
	ids := modelIDSet(models)
	if ids["live-force-prefix-model"] || !ids["team-a/live-force-prefix-model"] {
		t.Fatalf("force prefix behavior changed, models = %#v", ids)
	}
}

func TestRegisterModelsForAuthFallsBackWhenInventoryEmpty(t *testing.T) {
	clientID := "test-live-inventory-fallback-auth"
	t.Cleanup(func() {
		registry.GetGlobalRegistry().UnregisterClient(clientID)
	})

	service := &Service{cfg: &config.Config{}}
	auth := &coreauth.Auth{
		ID:       clientID,
		Provider: "codex",
		Attributes: map[string]string{
			"api_key": "test-key",
		},
		ModelInventory: &coreauth.LiveModelInventory{
			Provider: "codex",
			Warnings: []string{"fetch failed"},
		},
	}

	service.registerModelsForAuth(t.Context(), auth)
	models := registry.GetGlobalRegistry().GetModelsForClient(clientID)
	if len(models) == 0 {
		t.Fatal("expected static codex fallback models")
	}
}

func TestRefreshLiveModelInventoryFailureFallsBackToStaticModels(t *testing.T) {
	clientID := "test-live-inventory-fetch-failure-auth"
	t.Cleanup(func() {
		registry.GetGlobalRegistry().UnregisterClient(clientID)
	})

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "upstream unavailable", http.StatusInternalServerError)
	}))
	defer upstream.Close()

	service := &Service{cfg: &config.Config{}}
	auth := &coreauth.Auth{
		ID:       clientID,
		Provider: "codex",
		Attributes: map[string]string{
			"api_key":  "test-key",
			"base_url": upstream.URL + "/backend-api/codex",
		},
	}

	refreshed := service.refreshLiveModelInventory(t.Context(), auth)
	if refreshed == nil || refreshed.ModelInventory == nil || len(refreshed.ModelInventory.Warnings) == 0 {
		t.Fatalf("expected fetch warning inventory, got %#v", refreshed)
	}
	service.registerModelsForAuth(t.Context(), refreshed)
	models := registry.GetGlobalRegistry().GetModelsForClient(clientID)
	if len(models) == 0 {
		t.Fatal("expected static codex fallback models after fetch failure")
	}
}

func modelIDSet(models []*registry.ModelInfo) map[string]bool {
	out := make(map[string]bool, len(models))
	for _, model := range models {
		if model != nil {
			out[model.ID] = true
		}
	}
	return out
}
