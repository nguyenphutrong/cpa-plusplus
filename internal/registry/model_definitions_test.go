package registry

import "testing"

func TestCodexFreeModelsExcludeGPT55(t *testing.T) {
	model := findModelInfo(GetCodexFreeModels(), "gpt-5.5")
	if model != nil {
		t.Fatal("expected codex free tier to NOT include gpt-5.5")
	}
}

func TestCodexStaticModelsIncludeGPT55(t *testing.T) {
	tierModels := map[string][]*ModelInfo{
		"team": GetCodexTeamModels(),
		"plus": GetCodexPlusModels(),
		"pro":  GetCodexProModels(),
	}

	for tier, models := range tierModels {
		t.Run(tier, func(t *testing.T) {
			model := findModelInfo(models, "gpt-5.5")
			if model == nil {
				t.Fatalf("expected codex %s tier to include gpt-5.5", tier)
			}
			assertGPT55ModelInfo(t, tier, model)
		})
	}

	model := LookupStaticModelInfo("gpt-5.5")
	if model == nil {
		t.Fatal("expected LookupStaticModelInfo to find gpt-5.5")
	}
	assertGPT55ModelInfo(t, "lookup", model)
}

func TestWithXAIBuiltinsAddsVideoModel(t *testing.T) {
	models := WithXAIBuiltins(nil)
	found := false
	for _, model := range models {
		if model != nil && model.ID == xaiBuiltinVideoModelID {
			found = true
			if model.OwnedBy != "xai" {
				t.Fatalf("OwnedBy = %q, want xai", model.OwnedBy)
			}
		}
	}
	if !found {
		t.Fatalf("expected %s builtin model", xaiBuiltinVideoModelID)
	}
}

func TestValidateModelsCatalogAllowsMissingSections(t *testing.T) {
	data := validTestModelsCatalog()
	data.XAI = nil

	if err := validateModelsCatalog(data); err != nil {
		t.Fatalf("validateModelsCatalog() error = %v", err)
	}
}

func TestOverlayEmbeddedCatalogDefaultsPreservesForkModels(t *testing.T) {
	current := validTestModelsCatalog()
	current.GitHubCopilot = nil
	current.Kiro = []*ModelInfo{{ID: "custom-kiro-model"}}
	embedded := validTestModelsCatalog()
	embedded.GitHubCopilot = []*ModelInfo{{ID: "copilot-model"}}
	embedded.Kiro = []*ModelInfo{{ID: "kiro-model"}}

	repaired := overlayEmbeddedCatalogDefaults(current, embedded)

	if findModelInfo(repaired.GitHubCopilot, "copilot-model") == nil {
		t.Fatalf("github-copilot fallback not preserved: %#v", repaired.GitHubCopilot)
	}
	if findModelInfo(repaired.Kiro, "custom-kiro-model") == nil {
		t.Fatalf("kiro existing model not preserved: %#v", repaired.Kiro)
	}
	if findModelInfo(repaired.Kiro, "kiro-model") == nil {
		t.Fatalf("kiro fallback not preserved: %#v", repaired.Kiro)
	}
}

func TestRepairModelsFromEmbeddedCatalogRestoresForkProviderDefaults(t *testing.T) {
	embedded, err := parseModelsFromBytes(embeddedModelsJSON, "test")
	if err != nil {
		t.Fatalf("parse embedded models: %v", err)
	}
	current := cloneStaticModelsJSON(embedded)
	current.GitHubCopilot = nil
	current.Kiro = []*ModelInfo{{ID: "custom-kiro-model"}}
	restoreModelsCatalogForTest(t, current)
	restoreModelRefreshCallbackForTest(t)

	changedCh := make(chan []string, 1)
	SetModelRefreshCallback(func(changed []string) {
		changedCh <- changed
	})

	repairModelsFromEmbeddedCatalog("test model catalog repair")

	if findModelInfo(GetGitHubCopilotModels(), "gpt-5.2-codex") == nil {
		t.Fatalf("github-copilot embedded defaults missing after repair: %#v", GetGitHubCopilotModels())
	}
	if findModelInfo(GetKiroModels(), "custom-kiro-model") == nil {
		t.Fatalf("kiro persisted model missing after repair: %#v", GetKiroModels())
	}
	if findModelInfo(GetKiroModels(), "auto") == nil {
		t.Fatalf("kiro embedded defaults missing after repair: %#v", GetKiroModels())
	}

	select {
	case changed := <-changedCh:
		if !stringSliceContains(changed, "github-copilot") {
			t.Fatalf("changed providers missing github-copilot: %#v", changed)
		}
		if !stringSliceContains(changed, "kiro") {
			t.Fatalf("changed providers missing kiro: %#v", changed)
		}
	default:
		t.Fatal("expected repair to notify changed fork providers")
	}
}

func TestDetectChangedProvidersIncludesForkModelSections(t *testing.T) {
	oldData := validTestModelsCatalog()
	newData := validTestModelsCatalog()
	oldData.GitHubCopilot = []*ModelInfo{{ID: "copilot-old"}}
	newData.GitHubCopilot = []*ModelInfo{{ID: "copilot-new"}}
	oldData.Kiro = []*ModelInfo{{ID: "kiro-old"}}
	newData.Kiro = []*ModelInfo{{ID: "kiro-new"}}

	changed := detectChangedProviders(oldData, newData)

	if !stringSliceContains(changed, "github-copilot") {
		t.Fatalf("changed providers missing github-copilot: %#v", changed)
	}
	if !stringSliceContains(changed, "kiro") {
		t.Fatalf("changed providers missing kiro: %#v", changed)
	}
}

func TestValidateModelsCatalogRejectsInvalidDefinitions(t *testing.T) {
	data := validTestModelsCatalog()
	data.Claude = []*ModelInfo{{ID: ""}}

	if err := validateModelsCatalog(data); err == nil {
		t.Fatal("expected invalid model definition error")
	}
}

func validTestModelsCatalog() *staticModelsJSON {
	models := []*ModelInfo{{ID: "test-model"}}
	return &staticModelsJSON{
		Claude:        models,
		Gemini:        models,
		Vertex:        models,
		GeminiCLI:     models,
		AIStudio:      models,
		CodexFree:     models,
		CodexTeam:     models,
		CodexPlus:     models,
		CodexPro:      models,
		Kimi:          models,
		Antigravity:   models,
		GitHubCopilot: models,
		Kiro:          models,
		XAI:           models,
	}
}

func findModelInfo(models []*ModelInfo, id string) *ModelInfo {
	for _, model := range models {
		if model != nil && model.ID == id {
			return model
		}
	}
	return nil
}

func assertGPT55ModelInfo(t *testing.T, source string, model *ModelInfo) {
	t.Helper()

	if model.ID != "gpt-5.5" {
		t.Fatalf("%s id mismatch: got %q", source, model.ID)
	}
	if model.Object != "model" {
		t.Fatalf("%s object mismatch: got %q", source, model.Object)
	}
	if model.Created != 1776902400 {
		t.Fatalf("%s created timestamp mismatch: got %d", source, model.Created)
	}
	if model.OwnedBy != "openai" {
		t.Fatalf("%s owned_by mismatch: got %q", source, model.OwnedBy)
	}
	if model.Type != "openai" {
		t.Fatalf("%s type mismatch: got %q", source, model.Type)
	}
	if model.DisplayName != "GPT 5.5" {
		t.Fatalf("%s display name mismatch: got %q", source, model.DisplayName)
	}
	if model.Version != "gpt-5.5" {
		t.Fatalf("%s version mismatch: got %q", source, model.Version)
	}
	if model.Description != "Frontier model for complex coding, research, and real-world work." {
		t.Fatalf("%s description mismatch: got %q", source, model.Description)
	}
	if model.ContextLength != 272000 {
		t.Fatalf("%s context length mismatch: got %d", source, model.ContextLength)
	}
	if model.MaxCompletionTokens != 128000 {
		t.Fatalf("%s max completion tokens mismatch: got %d", source, model.MaxCompletionTokens)
	}
	if len(model.SupportedParameters) != 1 || model.SupportedParameters[0] != "tools" {
		t.Fatalf("%s supported parameters mismatch: got %v", source, model.SupportedParameters)
	}
	if model.Thinking == nil {
		t.Fatalf("%s missing thinking support", source)
	}

	want := []string{"low", "medium", "high", "xhigh"}
	if len(model.Thinking.Levels) != len(want) {
		t.Fatalf("%s thinking level count mismatch: got %d, want %d", source, len(model.Thinking.Levels), len(want))
	}
	for i, level := range want {
		if model.Thinking.Levels[i] != level {
			t.Fatalf("%s thinking level %d mismatch: got %q, want %q", source, i, model.Thinking.Levels[i], level)
		}
	}
}

func stringSliceContains(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func restoreModelsCatalogForTest(t *testing.T, data *staticModelsJSON) {
	t.Helper()

	original := cloneStaticModelsJSON(getModels())
	modelsCatalogStore.mu.Lock()
	modelsCatalogStore.data = cloneStaticModelsJSON(data)
	modelsCatalogStore.mu.Unlock()

	t.Cleanup(func() {
		modelsCatalogStore.mu.Lock()
		modelsCatalogStore.data = original
		modelsCatalogStore.mu.Unlock()
	})
}

func restoreModelRefreshCallbackForTest(t *testing.T) {
	t.Helper()

	refreshCallbackMu.Lock()
	originalCallback := refreshCallback
	originalPending := append([]string(nil), pendingRefreshChanges...)
	refreshCallback = nil
	pendingRefreshChanges = nil
	refreshCallbackMu.Unlock()

	t.Cleanup(func() {
		refreshCallbackMu.Lock()
		refreshCallback = originalCallback
		pendingRefreshChanges = originalPending
		refreshCallbackMu.Unlock()
	})
}
