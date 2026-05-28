package management

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/quota"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/quota/storage"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

func TestProviderFacadeListsAndPatchesOAuthCredentials(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "")
	gin.SetMode(gin.TestMode)

	manager := coreauth.NewManager(nil, nil, nil)
	auth := &coreauth.Auth{
		ID:       "copilot-1",
		Provider: "github-copilot",
		FileName: "github-copilot-test.json",
		Label:    "Old Label",
		Metadata: map[string]any{
			"username": "octo",
		},
	}
	if _, err := manager.Register(context.Background(), auth); err != nil {
		t.Fatalf("register auth: %v", err)
	}
	h := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: t.TempDir()}, manager)
	h.tokenStore = &memoryAuthStore{}

	listRec := httptest.NewRecorder()
	listCtx, _ := gin.CreateTestContext(listRec)
	listCtx.Request = httptest.NewRequest(http.MethodGet, "/v0/management/providers", nil)
	h.ListProviders(listCtx)
	if listRec.Code != http.StatusOK {
		t.Fatalf("list status = %d body=%s", listRec.Code, listRec.Body.String())
	}
	var listed []map[string]any
	if err := json.Unmarshal(listRec.Body.Bytes(), &listed); err != nil {
		t.Fatalf("decode list: %v", err)
	}
	if len(listed) != 1 || listed[0]["provider"] != "github-copilot" {
		t.Fatalf("unexpected list payload: %#v", listed)
	}
	validation, ok := listed[0]["validation"].(map[string]any)
	if !ok {
		t.Fatalf("missing validation payload: %#v", listed[0])
	}
	if got := validation["account_identity"]; got != "octo" {
		t.Fatalf("account identity = %#v, want octo", got)
	}

	patchRec := httptest.NewRecorder()
	patchCtx, _ := gin.CreateTestContext(patchRec)
	patchCtx.Params = gin.Params{{Key: "id", Value: "copilot-1"}}
	patchCtx.Request = httptest.NewRequest(http.MethodPatch, "/v0/management/providers/copilot-1", strings.NewReader(`{"label":"New Label","disabled":true}`))
	patchCtx.Request.Header.Set("Content-Type", "application/json")
	h.PatchProvider(patchCtx)
	if patchRec.Code != http.StatusOK {
		t.Fatalf("patch status = %d body=%s", patchRec.Code, patchRec.Body.String())
	}
	var patched map[string]any
	if err := json.Unmarshal(patchRec.Body.Bytes(), &patched); err != nil {
		t.Fatalf("decode patch: %v", err)
	}
	if patched["label"] != "New Label" || patched["disabled"] != true {
		t.Fatalf("unexpected patch payload: %#v", patched)
	}
}

func TestDeleteProviderRemovesCredentialFromProviderList(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "")
	gin.SetMode(gin.TestMode)

	authDir := t.TempDir()
	fileName := "kiro-test.json"
	authPath := filepath.Join(authDir, fileName)
	if err := os.WriteFile(authPath, []byte(`{"type":"kiro","access_token":"token"}`), 0o600); err != nil {
		t.Fatalf("write auth file: %v", err)
	}

	manager := coreauth.NewManager(nil, nil, nil)
	auth := &coreauth.Auth{
		ID:       "kiro-1",
		Provider: "kiro",
		FileName: fileName,
		Attributes: map[string]string{
			"path": authPath,
		},
	}
	if _, err := manager.Register(context.Background(), auth); err != nil {
		t.Fatalf("register auth: %v", err)
	}
	h := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: authDir}, manager)
	h.tokenStore = &memoryAuthStore{}

	deleteRec := httptest.NewRecorder()
	deleteCtx, _ := gin.CreateTestContext(deleteRec)
	deleteCtx.Params = gin.Params{{Key: "id", Value: "kiro-1"}}
	deleteCtx.Request = httptest.NewRequest(http.MethodDelete, "/v0/management/providers/kiro-1", nil)
	h.DeleteProvider(deleteCtx)
	if deleteRec.Code != http.StatusOK {
		t.Fatalf("delete status = %d body=%s", deleteRec.Code, deleteRec.Body.String())
	}
	if _, err := os.Stat(authPath); !os.IsNotExist(err) {
		t.Fatalf("auth file still exists or stat failed unexpectedly: %v", err)
	}
	if _, ok := manager.GetByID("kiro-1"); ok {
		t.Fatal("deleted provider remained in auth manager")
	}

	listRec := httptest.NewRecorder()
	listCtx, _ := gin.CreateTestContext(listRec)
	listCtx.Request = httptest.NewRequest(http.MethodGet, "/v0/management/providers", nil)
	h.ListProviders(listCtx)
	if listRec.Code != http.StatusOK {
		t.Fatalf("list status = %d body=%s", listRec.Code, listRec.Body.String())
	}
	var listed []map[string]any
	if err := json.Unmarshal(listRec.Body.Bytes(), &listed); err != nil {
		t.Fatalf("decode list: %v", err)
	}
	if len(listed) != 0 {
		t.Fatalf("providers after delete = %#v, want empty", listed)
	}
}

func TestProviderFacadeUsesCachedQuotaAccountLabel(t *testing.T) {
	h := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: t.TempDir()}, nil)
	auth := &coreauth.Auth{
		ID:       "kiro-1",
		Provider: "kiro",
		FileName: "kiro-signin_localhost-profile.json",
		Metadata: map[string]any{
			"username": "kiro-local-user",
			quota.MetadataKey: storage.QuotaData{
				ProviderData: &storage.ProviderQuotaData{
					AccountLabel: "dev@example.com",
				},
			},
		},
	}

	response := h.providerResponseFromAuth(auth)
	validation, ok := response["validation"].(gin.H)
	if !ok {
		t.Fatalf("missing validation payload: %#v", response)
	}
	if got := validation["account_identity"]; got != "dev@example.com" {
		t.Fatalf("account identity = %#v, want dev@example.com", got)
	}
}

func TestModelCatalogReturnsRegistryLiveInventoryAndEnablement(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "")
	gin.SetMode(gin.TestMode)

	manager := coreauth.NewManager(nil, nil, nil)
	fetchedAt := time.Date(2026, 5, 28, 10, 0, 0, 0, time.UTC)
	auth := &coreauth.Auth{
		ID:       "catalog-gemini-auth",
		Provider: "gemini",
		Metadata: map[string]any{
			"email": "dev@example.com",
		},
		ModelInventory: &coreauth.LiveModelInventory{
			Provider:  "gemini",
			FetchedAt: fetchedAt,
			Models: []coreauth.LiveModelEntry{
				{ID: "gemini-live", OwnedBy: "google"},
				{ID: "gemini-disabled", OwnedBy: "google"},
			},
		},
	}
	if _, err := manager.Register(context.Background(), auth); err != nil {
		t.Fatalf("register auth: %v", err)
	}
	registry.GetGlobalRegistry().RegisterClient(auth.ID, "gemini", []*registry.ModelInfo{
		{ID: "gemini-live", Object: "model", OwnedBy: "google", Type: "gemini", DisplayName: "Gemini Live", SupportedParameters: []string{"tools"}},
	})
	defer registry.GetGlobalRegistry().UnregisterClient(auth.ID)

	h := NewHandlerWithoutConfigFilePath(&config.Config{
		AuthDir:             t.TempDir(),
		OAuthExcludedModels: map[string][]string{"gemini": []string{"gemini-disabled"}},
	}, manager)

	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	ctx.Request = httptest.NewRequest(http.MethodGet, "/v0/management/models/catalog", nil)
	h.GetModelCatalog(ctx)
	if rec.Code != http.StatusOK {
		t.Fatalf("catalog status = %d body=%s", rec.Code, rec.Body.String())
	}

	var payload modelCatalogResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode catalog: %v", err)
	}
	if len(payload.Providers) != 1 {
		t.Fatalf("providers = %#v", payload.Providers)
	}
	provider := payload.Providers[0]
	if provider.ProviderID != "gemini" || provider.ConnectionCount != 1 || provider.Status != "active" {
		t.Fatalf("provider = %#v", provider)
	}
	if len(provider.Models) != 2 {
		t.Fatalf("models = %#v", provider.Models)
	}
	byID := map[string]modelCatalogItem{}
	for _, model := range provider.Models {
		byID[model.ModelID] = model
	}
	if !byID["gemini-live"].IsEnabled || byID["gemini-live"].MetadataSource != "live" || !byID["gemini-live"].Capabilities["tools"] {
		t.Fatalf("live model row = %#v", byID["gemini-live"])
	}
	if byID["gemini-live"].FetchedAt != fetchedAt.Format(time.RFC3339) {
		t.Fatalf("fetched_at = %q", byID["gemini-live"].FetchedAt)
	}
	if byID["gemini-disabled"].IsEnabled {
		t.Fatalf("disabled model row = %#v", byID["gemini-disabled"])
	}
}

func TestModelCatalogFallsBackToStaticProviderDefinitions(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "")
	gin.SetMode(gin.TestMode)

	manager := coreauth.NewManager(nil, nil, nil)
	auths := []*coreauth.Auth{
		{ID: "catalog-copilot-auth", Provider: "github-copilot", Metadata: map[string]any{"username": "octo"}},
		{ID: "catalog-kiro-auth", Provider: "kiro", Metadata: map[string]any{"email": "dev@example.com"}},
	}
	for _, auth := range auths {
		if _, err := manager.Register(context.Background(), auth); err != nil {
			t.Fatalf("register auth %s: %v", auth.ID, err)
		}
	}

	h := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: t.TempDir()}, manager)
	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	ctx.Request = httptest.NewRequest(http.MethodGet, "/v0/management/models/catalog", nil)
	h.GetModelCatalog(ctx)
	if rec.Code != http.StatusOK {
		t.Fatalf("catalog status = %d body=%s", rec.Code, rec.Body.String())
	}

	var payload modelCatalogResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode catalog: %v", err)
	}
	providers := map[string]modelCatalogProvider{}
	for _, provider := range payload.Providers {
		providers[provider.ProviderID] = provider
	}
	if !catalogProviderHasModel(providers["github-copilot"], "gpt-5.2-codex") {
		t.Fatalf("github-copilot static models missing: %#v", providers["github-copilot"].Models)
	}
	if !catalogProviderHasModel(providers["kiro"], "auto") {
		t.Fatalf("kiro static models missing: %#v", providers["kiro"].Models)
	}
}

func TestProviderEnabledModelsRoundTripPersistsAsExcludedModels(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "")
	gin.SetMode(gin.TestMode)

	configPath := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(configPath, []byte("debug: false\n"), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	manager := coreauth.NewManager(nil, nil, nil)
	auth := &coreauth.Auth{ID: "enabled-gemini-auth", Provider: "gemini", Metadata: map[string]any{"email": "dev@example.com"}}
	if _, err := manager.Register(context.Background(), auth); err != nil {
		t.Fatalf("register auth: %v", err)
	}
	registry.GetGlobalRegistry().RegisterClient(auth.ID, "gemini", []*registry.ModelInfo{
		{ID: "gemini-a", Object: "model", OwnedBy: "google", Type: "gemini"},
		{ID: "gemini-b", Object: "model", OwnedBy: "google", Type: "gemini"},
	})
	defer registry.GetGlobalRegistry().UnregisterClient(auth.ID)

	h := NewHandler(&config.Config{AuthDir: t.TempDir()}, configPath, manager)

	putRec := httptest.NewRecorder()
	putCtx, _ := gin.CreateTestContext(putRec)
	putCtx.Params = gin.Params{{Key: "provider", Value: "gemini"}}
	putCtx.Request = httptest.NewRequest(http.MethodPut, "/v0/management/providers/gemini/enabled-models", strings.NewReader(`{"enabled_models":["gemini/gemini-a"]}`))
	putCtx.Request.Header.Set("Content-Type", "application/json")
	h.PutProviderEnabledModels(putCtx)
	if putRec.Code != http.StatusOK {
		t.Fatalf("put status = %d body=%s", putRec.Code, putRec.Body.String())
	}
	if got := h.cfg.OAuthExcludedModels["gemini"]; len(got) != 1 || got[0] != "gemini-b" {
		t.Fatalf("gemini exclusions = %#v", got)
	}
	if got := h.cfg.OAuthExcludedModels["gemini-cli"]; len(got) != 1 || got[0] != "gemini-b" {
		t.Fatalf("gemini-cli exclusions = %#v", got)
	}

	getRec := httptest.NewRecorder()
	getCtx, _ := gin.CreateTestContext(getRec)
	getCtx.Params = gin.Params{{Key: "provider", Value: "gemini-cli"}}
	getCtx.Request = httptest.NewRequest(http.MethodGet, "/v0/management/providers/gemini-cli/enabled-models", nil)
	h.GetProviderEnabledModels(getCtx)
	if getRec.Code != http.StatusOK {
		t.Fatalf("get status = %d body=%s", getRec.Code, getRec.Body.String())
	}
	var payload struct {
		ProviderID    string   `json:"provider_id"`
		EnabledModels []string `json:"enabled_models"`
	}
	if err := json.Unmarshal(getRec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode get: %v", err)
	}
	if payload.ProviderID != "gemini" || len(payload.EnabledModels) != 1 || payload.EnabledModels[0] != "gemini-a" {
		t.Fatalf("enabled response = %#v", payload)
	}

	clearRec := httptest.NewRecorder()
	clearCtx, _ := gin.CreateTestContext(clearRec)
	clearCtx.Params = gin.Params{{Key: "provider", Value: "gemini"}}
	clearCtx.Request = httptest.NewRequest(http.MethodPut, "/v0/management/providers/gemini/enabled-models", strings.NewReader(`{"enabled_models":null}`))
	clearCtx.Request.Header.Set("Content-Type", "application/json")
	h.PutProviderEnabledModels(clearCtx)
	if clearRec.Code != http.StatusOK {
		t.Fatalf("clear status = %d body=%s", clearRec.Code, clearRec.Body.String())
	}
	if h.cfg.OAuthExcludedModels != nil {
		t.Fatalf("exclusions after clear = %#v, want nil", h.cfg.OAuthExcludedModels)
	}
}

func TestProviderEnabledModelsValidatesProviderAndModels(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "")
	gin.SetMode(gin.TestMode)

	manager := coreauth.NewManager(nil, nil, nil)
	auth := &coreauth.Auth{ID: "validate-codex-auth", Provider: "codex", Metadata: map[string]any{"email": "dev@example.com"}}
	if _, err := manager.Register(context.Background(), auth); err != nil {
		t.Fatalf("register auth: %v", err)
	}
	registry.GetGlobalRegistry().RegisterClient(auth.ID, "codex", []*registry.ModelInfo{
		{ID: "gpt-5", Object: "model", OwnedBy: "openai", Type: "openai"},
	})
	defer registry.GetGlobalRegistry().UnregisterClient(auth.ID)

	h := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: t.TempDir()}, manager)

	unknownProviderRec := httptest.NewRecorder()
	unknownProviderCtx, _ := gin.CreateTestContext(unknownProviderRec)
	unknownProviderCtx.Params = gin.Params{{Key: "provider", Value: "unknown"}}
	unknownProviderCtx.Request = httptest.NewRequest(http.MethodGet, "/v0/management/providers/unknown/enabled-models", nil)
	h.GetProviderEnabledModels(unknownProviderCtx)
	if unknownProviderRec.Code != http.StatusNotFound {
		t.Fatalf("unknown provider status = %d body=%s", unknownProviderRec.Code, unknownProviderRec.Body.String())
	}

	unknownModelRec := httptest.NewRecorder()
	unknownModelCtx, _ := gin.CreateTestContext(unknownModelRec)
	unknownModelCtx.Params = gin.Params{{Key: "provider", Value: "codex"}}
	unknownModelCtx.Request = httptest.NewRequest(http.MethodPut, "/v0/management/providers/codex/enabled-models", strings.NewReader(`{"enabled_models":["missing-model"]}`))
	unknownModelCtx.Request.Header.Set("Content-Type", "application/json")
	h.PutProviderEnabledModels(unknownModelCtx)
	if unknownModelRec.Code != http.StatusBadRequest {
		t.Fatalf("unknown model status = %d body=%s", unknownModelRec.Code, unknownModelRec.Body.String())
	}
}

func TestProviderResponsesIncludeSupportedModels(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "")
	gin.SetMode(gin.TestMode)

	manager := coreauth.NewManager(nil, nil, nil)
	auth := &coreauth.Auth{ID: "provider-models-copilot", Provider: "github-copilot", FileName: "copilot.json", Metadata: map[string]any{"username": "octo"}}
	if _, err := manager.Register(context.Background(), auth); err != nil {
		t.Fatalf("register auth: %v", err)
	}
	registry.GetGlobalRegistry().RegisterClient(auth.ID, "github-copilot", []*registry.ModelInfo{
		{ID: "gpt-5-codex", Object: "model", OwnedBy: "github-copilot", Type: "github-copilot"},
	})
	defer registry.GetGlobalRegistry().UnregisterClient(auth.ID)

	h := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: t.TempDir()}, manager)
	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	ctx.Request = httptest.NewRequest(http.MethodGet, "/v0/management/providers", nil)
	h.ListProviders(ctx)
	if rec.Code != http.StatusOK {
		t.Fatalf("list status = %d body=%s", rec.Code, rec.Body.String())
	}
	var payload []map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode providers: %v", err)
	}
	if len(payload) != 1 {
		t.Fatalf("providers = %#v", payload)
	}
	if !stringSliceContains(anyStringSlice(payload[0]["supported_models"]), "gpt-5-codex") {
		t.Fatalf("supported_models = %#v", payload[0]["supported_models"])
	}
	validation, ok := payload[0]["validation"].(map[string]any)
	if !ok || !stringSliceContains(anyStringSlice(validation["supported_models"]), "gpt-5-codex") {
		t.Fatalf("validation = %#v", payload[0]["validation"])
	}
}

func TestProviderResponsesFallBackToStaticSupportedModels(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "")
	gin.SetMode(gin.TestMode)

	manager := coreauth.NewManager(nil, nil, nil)
	auth := &coreauth.Auth{ID: "provider-models-kiro", Provider: "kiro", FileName: "kiro.json", Metadata: map[string]any{"email": "dev@example.com"}}
	if _, err := manager.Register(context.Background(), auth); err != nil {
		t.Fatalf("register auth: %v", err)
	}

	h := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: t.TempDir()}, manager)
	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	ctx.Request = httptest.NewRequest(http.MethodGet, "/v0/management/providers", nil)
	h.ListProviders(ctx)
	if rec.Code != http.StatusOK {
		t.Fatalf("list status = %d body=%s", rec.Code, rec.Body.String())
	}
	var payload []map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode providers: %v", err)
	}
	if len(payload) != 1 {
		t.Fatalf("providers = %#v", payload)
	}
	if !stringSliceContains(anyStringSlice(payload[0]["supported_models"]), "auto") {
		t.Fatalf("supported_models = %#v", payload[0]["supported_models"])
	}
	validation, ok := payload[0]["validation"].(map[string]any)
	if !ok || !stringSliceContains(anyStringSlice(validation["supported_models"]), "auto") {
		t.Fatalf("validation = %#v", payload[0]["validation"])
	}
}

func TestProviderModelSyncUsesCallbackAndReportsUnsupported(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "")
	gin.SetMode(gin.TestMode)

	manager := coreauth.NewManager(nil, nil, nil)
	codexAuth := &coreauth.Auth{ID: "sync-codex-auth", Provider: "codex", Metadata: map[string]any{"email": "dev@example.com"}}
	kiroAuth := &coreauth.Auth{ID: "sync-kiro-auth", Provider: "kiro", Metadata: map[string]any{"username": "dev"}}
	if _, err := manager.Register(context.Background(), codexAuth); err != nil {
		t.Fatalf("register codex auth: %v", err)
	}
	if _, err := manager.Register(context.Background(), kiroAuth); err != nil {
		t.Fatalf("register kiro auth: %v", err)
	}
	h := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: t.TempDir()}, manager)
	h.SetProviderModelSyncer(func(_ context.Context, auth *coreauth.Auth) (*coreauth.Auth, []*registry.ModelInfo, error) {
		if auth.Provider == "kiro" {
			return nil, nil, fmt.Errorf("live model inventory is not supported for provider %q", auth.Provider)
		}
		return auth, []*registry.ModelInfo{{ID: "gpt-5", Object: "model", OwnedBy: "openai", Type: "openai"}}, nil
	})

	okRec := httptest.NewRecorder()
	okCtx, _ := gin.CreateTestContext(okRec)
	okCtx.Params = gin.Params{{Key: "id", Value: "codex"}}
	okCtx.Request = httptest.NewRequest(http.MethodPost, "/v0/management/providers/codex/models/sync", nil)
	h.SyncProviderModels(okCtx)
	if okRec.Code != http.StatusOK {
		t.Fatalf("sync status = %d body=%s", okRec.Code, okRec.Body.String())
	}
	var okPayload struct {
		Provider        string   `json:"provider"`
		SupportedModels []string `json:"supported_models"`
	}
	if err := json.Unmarshal(okRec.Body.Bytes(), &okPayload); err != nil {
		t.Fatalf("decode sync: %v", err)
	}
	if okPayload.Provider != "codex" || !stringSliceContains(okPayload.SupportedModels, "gpt-5") {
		t.Fatalf("sync payload = %#v", okPayload)
	}

	unsupportedRec := httptest.NewRecorder()
	unsupportedCtx, _ := gin.CreateTestContext(unsupportedRec)
	unsupportedCtx.Params = gin.Params{{Key: "id", Value: "kiro"}}
	unsupportedCtx.Request = httptest.NewRequest(http.MethodPost, "/v0/management/providers/kiro/models/sync", nil)
	h.SyncProviderModels(unsupportedCtx)
	if unsupportedRec.Code != http.StatusNotImplemented {
		t.Fatalf("unsupported status = %d body=%s", unsupportedRec.Code, unsupportedRec.Body.String())
	}
}

func anyStringSlice(value any) []string {
	raw, ok := value.([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(raw))
	for _, item := range raw {
		if s, ok := item.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

func catalogProviderHasModel(provider modelCatalogProvider, modelID string) bool {
	for _, model := range provider.Models {
		if model.ModelID == modelID {
			return true
		}
	}
	return false
}

func stringSliceContains(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}
