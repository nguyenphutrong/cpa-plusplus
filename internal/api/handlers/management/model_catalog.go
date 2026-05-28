package management

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

type managementProviderRef struct {
	Public      string
	StorageKeys []string
}

type modelCatalogResponse struct {
	Providers []modelCatalogProvider `json:"providers"`
}

type modelCatalogProvider struct {
	ProviderID      string             `json:"provider_id"`
	ProviderName    string             `json:"provider_name"`
	Type            string             `json:"type,omitempty"`
	ConnectionCount int                `json:"connection_count"`
	Status          string             `json:"status"`
	Models          []modelCatalogItem `json:"models"`
}

type modelCatalogItem struct {
	ID              string          `json:"id"`
	ModelID         string          `json:"model_id"`
	Object          string          `json:"object,omitempty"`
	Created         int64           `json:"created,omitempty"`
	OwnedBy         string          `json:"owned_by,omitempty"`
	Provider        string          `json:"provider"`
	Type            string          `json:"type,omitempty"`
	DisplayName     string          `json:"display_name,omitempty"`
	Name            string          `json:"name,omitempty"`
	ContextWindow   int             `json:"context_window,omitempty"`
	MaxOutputTokens int             `json:"max_output_tokens,omitempty"`
	Available       bool            `json:"available"`
	Capabilities    map[string]bool `json:"capabilities,omitempty"`
	LiveDiscovered  bool            `json:"live_discovered"`
	MetadataSource  string          `json:"metadata_source,omitempty"`
	Warnings        []string        `json:"warnings,omitempty"`
	FetchedAt       string          `json:"fetched_at,omitempty"`
	AccountIdentity string          `json:"account_identity,omitempty"`
	IsEnabled       bool            `json:"is_enabled"`
}

// GetModelCatalog returns the provider/model rows consumed by the Models screen.
func (h *Handler) GetModelCatalog(c *gin.Context) {
	if h == nil || h.authManager == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "core auth manager unavailable"})
		return
	}
	c.JSON(http.StatusOK, modelCatalogResponse{Providers: h.buildModelCatalogProviders()})
}

func (h *Handler) GetProviderEnabledModels(c *gin.Context) {
	ref, provider, ok := h.catalogProviderForRequest(c)
	if !ok {
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"provider_id":      ref.Public,
		"enabled_models":   h.enabledModelsForProvider(provider),
		"catalog_models":   modelIDsForCatalog(provider.Models),
		"connection_count": provider.ConnectionCount,
	})
}

func (h *Handler) PutProviderEnabledModels(c *gin.Context) {
	ref, provider, ok := h.catalogProviderForRequest(c)
	if !ok {
		return
	}
	models, explicitNull, ok := decodeEnabledModelsRequest(c)
	if !ok {
		return
	}

	catalogModels := modelIDsForCatalog(provider.Models)
	if len(catalogModels) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "provider has no catalog models", "provider": ref.Public})
		return
	}

	var normalizedAllowlist []string
	if !explicitNull {
		var err error
		normalizedAllowlist, err = normalizeEnabledModelAllowlist(ref.Public, catalogModels, models)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error(), "provider": ref.Public})
			return
		}
	}

	h.mu.Lock()
	if h.cfg == nil {
		h.cfg = &config.Config{}
	}
	if h.cfg.OAuthExcludedModels == nil {
		h.cfg.OAuthExcludedModels = map[string][]string{}
	}
	if explicitNull {
		for _, key := range ref.StorageKeys {
			delete(h.cfg.OAuthExcludedModels, key)
		}
	} else {
		excluded := excludedFromAllowlist(catalogModels, normalizedAllowlist)
		for _, key := range ref.StorageKeys {
			if len(excluded) == 0 {
				delete(h.cfg.OAuthExcludedModels, key)
				continue
			}
			h.cfg.OAuthExcludedModels[key] = append([]string(nil), excluded...)
		}
	}
	h.cfg.OAuthExcludedModels = config.NormalizeOAuthExcludedModels(h.cfg.OAuthExcludedModels)
	err := error(nil)
	if strings.TrimSpace(h.configFilePath) != "" {
		err = config.SaveConfigPreserveComments(h.configFilePath, h.cfg)
	}
	h.mu.Unlock()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": fmt.Sprintf("failed to save config: %v", err)})
		return
	}

	provider.Models = applyEnabledStateToModels(provider.Models, h.excludedModelSet(ref.Public))
	c.JSON(http.StatusOK, gin.H{
		"success":        true,
		"provider_id":    ref.Public,
		"enabled_models": h.enabledModelsForProvider(provider),
	})
}

func (h *Handler) catalogProviderForRequest(c *gin.Context) (managementProviderRef, modelCatalogProvider, bool) {
	rawProvider := firstProviderNonEmpty(c.Param("provider"), c.Param("id"))
	ref, err := normalizeManagementProvider(rawProvider)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "unknown provider", "provider": strings.TrimSpace(rawProvider)})
		return managementProviderRef{}, modelCatalogProvider{}, false
	}
	for _, provider := range h.buildModelCatalogProviders() {
		if provider.ProviderID == ref.Public {
			return ref, provider, true
		}
	}
	c.JSON(http.StatusNotFound, gin.H{"error": "provider not found", "provider": ref.Public})
	return managementProviderRef{}, modelCatalogProvider{}, false
}

func (h *Handler) buildModelCatalogProviders() []modelCatalogProvider {
	auths := h.authManager.List()
	builders := map[string]*modelCatalogProviderBuilder{}
	for _, auth := range auths {
		if auth == nil {
			continue
		}
		ref := managementProviderRefFromAuth(auth)
		if ref.Public == "" {
			continue
		}
		builder := builders[ref.Public]
		if builder == nil {
			builder = &modelCatalogProviderBuilder{
				provider: modelCatalogProvider{
					ProviderID:   ref.Public,
					ProviderName: managementProviderDisplayName(ref.Public),
					Type:         "oauth",
					Status:       "disabled",
					Models:       []modelCatalogItem{},
				},
				seenModels: map[string]int{},
			}
			builders[ref.Public] = builder
		}
		builder.provider.ConnectionCount++
		if providerAuthAvailable(auth) {
			builder.provider.Status = "active"
		} else if builder.provider.Status != "active" && auth.Unavailable {
			builder.provider.Status = "unavailable"
		}
		for _, item := range modelCatalogItemsForAuth(ref.Public, auth) {
			builder.add(item)
		}
	}

	providers := make([]modelCatalogProvider, 0, len(builders))
	for public, builder := range builders {
		provider := builder.provider
		sort.SliceStable(provider.Models, func(i, j int) bool {
			return strings.Compare(provider.Models[i].ModelID, provider.Models[j].ModelID) < 0
		})
		provider.Models = applyEnabledStateToModels(provider.Models, h.excludedModelSet(public))
		providers = append(providers, provider)
	}
	sort.SliceStable(providers, func(i, j int) bool {
		return strings.Compare(providers[i].ProviderName, providers[j].ProviderName) < 0
	})
	return providers
}

type modelCatalogProviderBuilder struct {
	provider   modelCatalogProvider
	seenModels map[string]int
}

func (b *modelCatalogProviderBuilder) add(item modelCatalogItem) {
	key := strings.ToLower(strings.TrimSpace(item.ModelID))
	if key == "" {
		return
	}
	if existingIndex, ok := b.seenModels[key]; ok {
		existing := &b.provider.Models[existingIndex]
		existing.Available = existing.Available || item.Available
		existing.LiveDiscovered = existing.LiveDiscovered || item.LiveDiscovered
		if existing.MetadataSource != "live" && item.MetadataSource != "" {
			existing.MetadataSource = item.MetadataSource
		}
		if existing.FetchedAt == "" {
			existing.FetchedAt = item.FetchedAt
		}
		if existing.AccountIdentity == "" {
			existing.AccountIdentity = item.AccountIdentity
		}
		existing.Warnings = appendUniqueStrings(existing.Warnings, item.Warnings...)
		return
	}
	b.seenModels[key] = len(b.provider.Models)
	b.provider.Models = append(b.provider.Models, item)
}

func modelCatalogItemsForAuth(publicProvider string, auth *coreauth.Auth) []modelCatalogItem {
	models := registry.GetGlobalRegistry().GetModelsForClient(auth.ID)
	if len(models) == 0 && auth.ModelInventory != nil {
		models = modelInfosFromInventory(publicProvider, auth.ModelInventory)
	}
	items := make([]modelCatalogItem, 0, len(models))
	for _, model := range models {
		if model == nil {
			continue
		}
		modelID := providerLocalModelID(publicProvider, auth.Provider, model.ID)
		if modelID == "" {
			continue
		}
		source, live := modelMetadataSource(auth, modelID, model)
		item := modelCatalogItem{
			ID:              publicProvider + "/" + modelID,
			ModelID:         modelID,
			Object:          model.Object,
			Created:         model.Created,
			OwnedBy:         model.OwnedBy,
			Provider:        publicProvider,
			Type:            model.Type,
			DisplayName:     firstProviderNonEmpty(model.DisplayName, model.Name, modelID),
			Name:            firstProviderNonEmpty(model.Name, model.DisplayName, modelID),
			ContextWindow:   model.ContextLength,
			MaxOutputTokens: model.MaxCompletionTokens,
			Available:       providerAuthAvailable(auth),
			Capabilities:    modelCapabilities(model),
			LiveDiscovered:  live,
			MetadataSource:  source,
			Warnings:        inventoryWarnings(auth),
			AccountIdentity: providerAccountIdentity(auth),
			IsEnabled:       true,
		}
		if auth.ModelInventory != nil && !auth.ModelInventory.FetchedAt.IsZero() {
			item.FetchedAt = auth.ModelInventory.FetchedAt.UTC().Format(time.RFC3339)
		}
		items = append(items, item)
	}
	return items
}

func modelInfosFromInventory(provider string, inventory *coreauth.LiveModelInventory) []*registry.ModelInfo {
	if inventory == nil || len(inventory.Models) == 0 {
		return nil
	}
	now := time.Now().Unix()
	out := make([]*registry.ModelInfo, 0, len(inventory.Models))
	seen := map[string]struct{}{}
	for _, entry := range inventory.Models {
		id := strings.TrimSpace(entry.ID)
		if id == "" {
			continue
		}
		key := strings.ToLower(id)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		info := registry.LookupStaticModelInfo(id)
		if info == nil {
			info = &registry.ModelInfo{
				ID:          id,
				Object:      "model",
				Created:     now,
				OwnedBy:     firstProviderNonEmpty(entry.OwnedBy, provider),
				Type:        provider,
				DisplayName: id,
				Name:        id,
			}
		}
		if entry.OwnedBy != "" {
			info.OwnedBy = entry.OwnedBy
		}
		out = append(out, info)
	}
	return out
}

func providerAuthAvailable(auth *coreauth.Auth) bool {
	if auth == nil {
		return false
	}
	return !auth.Disabled && auth.Status != coreauth.StatusDisabled && !auth.Unavailable
}

func modelMetadataSource(auth *coreauth.Auth, modelID string, model *registry.ModelInfo) (string, bool) {
	if auth != nil && auth.ModelInventory != nil {
		for _, entry := range auth.ModelInventory.Models {
			if strings.EqualFold(strings.TrimSpace(entry.ID), modelID) {
				return "live", true
			}
		}
	}
	if model != nil && model.UserDefined {
		return "config", false
	}
	return "static", false
}

func modelCapabilities(model *registry.ModelInfo) map[string]bool {
	if model == nil {
		return nil
	}
	capabilities := map[string]bool{}
	text := strings.ToLower(strings.Join([]string{model.ID, model.Name, model.DisplayName, model.Type}, " "))
	if model.Thinking != nil || strings.Contains(text, "reasoning") || strings.Contains(text, "thinking") || strings.Contains(text, "o1") || strings.Contains(text, "o3") || strings.Contains(text, "o4") || strings.Contains(text, "opus") {
		capabilities["reasoning"] = true
	}
	if strings.Contains(text, "vision") || strings.Contains(text, "visual") || strings.Contains(text, "image") {
		capabilities["vision"] = true
	}
	if strings.Contains(text, "embed") || strings.EqualFold(model.Type, "embedding") {
		capabilities["embeddings"] = true
	}
	for _, modality := range append(model.SupportedInputModalities, model.SupportedOutputModalities...) {
		if strings.EqualFold(modality, "image") {
			capabilities["vision"] = true
		}
	}
	for _, param := range model.SupportedParameters {
		switch strings.ToLower(strings.TrimSpace(param)) {
		case "tools", "tool_choice", "function_call", "functions":
			capabilities["tools"] = true
		}
	}
	if len(capabilities) == 0 {
		return nil
	}
	return capabilities
}

func inventoryWarnings(auth *coreauth.Auth) []string {
	if auth == nil || auth.ModelInventory == nil || len(auth.ModelInventory.Warnings) == 0 {
		return nil
	}
	return append([]string(nil), auth.ModelInventory.Warnings...)
}

func applyEnabledStateToModels(models []modelCatalogItem, excluded map[string]struct{}) []modelCatalogItem {
	if len(models) == 0 {
		return models
	}
	out := make([]modelCatalogItem, len(models))
	for i, model := range models {
		out[i] = model
		out[i].IsEnabled = !modelExcluded(excluded, model.ModelID)
	}
	return out
}

func (h *Handler) enabledModelsForProvider(provider modelCatalogProvider) []string {
	excluded := h.excludedModelSet(provider.ProviderID)
	if len(excluded) == 0 {
		return nil
	}
	enabled := make([]string, 0, len(provider.Models))
	for _, model := range provider.Models {
		if !modelExcluded(excluded, model.ModelID) {
			enabled = append(enabled, model.ModelID)
		}
	}
	sort.Strings(enabled)
	return enabled
}

func (h *Handler) excludedModelSet(publicProvider string) map[string]struct{} {
	if h == nil || h.cfg == nil || len(h.cfg.OAuthExcludedModels) == 0 {
		return nil
	}
	ref, err := normalizeManagementProvider(publicProvider)
	if err != nil {
		ref = managementProviderRef{Public: strings.ToLower(strings.TrimSpace(publicProvider)), StorageKeys: []string{strings.ToLower(strings.TrimSpace(publicProvider))}}
	}
	excluded := map[string]struct{}{}
	for _, key := range ref.StorageKeys {
		for _, model := range h.cfg.OAuthExcludedModels[key] {
			normalized := normalizeProviderModelID(ref.Public, model)
			if normalized != "" {
				excluded[strings.ToLower(normalized)] = struct{}{}
			}
		}
	}
	if len(excluded) == 0 {
		return nil
	}
	return excluded
}

func modelExcluded(excluded map[string]struct{}, modelID string) bool {
	if len(excluded) == 0 {
		return false
	}
	modelKey := strings.ToLower(strings.TrimSpace(modelID))
	if _, ok := excluded[modelKey]; ok {
		return true
	}
	for pattern := range excluded {
		if matchModelPattern(pattern, modelKey) {
			return true
		}
	}
	return false
}

func decodeEnabledModelsRequest(c *gin.Context) ([]string, bool, bool) {
	data, err := io.ReadAll(c.Request.Body)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "failed to read body"})
		return nil, false, false
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body"})
		return nil, false, false
	}
	value, ok := raw["enabled_models"]
	if !ok {
		value, ok = raw["models"]
	}
	if !ok {
		c.JSON(http.StatusBadRequest, gin.H{"error": "enabled_models is required"})
		return nil, false, false
	}
	if strings.EqualFold(strings.TrimSpace(string(value)), "null") {
		return nil, true, true
	}
	var models []string
	if err := json.Unmarshal(value, &models); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "enabled_models must be null or an array of strings"})
		return nil, false, false
	}
	return models, false, true
}

func normalizeEnabledModelAllowlist(provider string, catalogModels []string, requested []string) ([]string, error) {
	catalogSet := map[string]string{}
	for _, model := range catalogModels {
		normalized := normalizeProviderModelID(provider, model)
		if normalized != "" {
			catalogSet[strings.ToLower(normalized)] = normalized
		}
	}
	seen := map[string]struct{}{}
	out := make([]string, 0, len(requested))
	for _, model := range requested {
		normalized := normalizeProviderModelID(provider, model)
		if normalized == "" {
			continue
		}
		catalogModel, ok := catalogSet[strings.ToLower(normalized)]
		if !ok {
			return nil, fmt.Errorf("unknown model %q for provider %q", model, provider)
		}
		key := strings.ToLower(catalogModel)
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, catalogModel)
	}
	sort.Strings(out)
	return out, nil
}

func excludedFromAllowlist(catalogModels []string, allowlist []string) []string {
	allowed := map[string]struct{}{}
	for _, model := range allowlist {
		allowed[strings.ToLower(strings.TrimSpace(model))] = struct{}{}
	}
	excluded := make([]string, 0, len(catalogModels))
	for _, model := range catalogModels {
		trimmed := strings.TrimSpace(model)
		if trimmed == "" {
			continue
		}
		if _, ok := allowed[strings.ToLower(trimmed)]; ok {
			continue
		}
		excluded = append(excluded, trimmed)
	}
	sort.Strings(excluded)
	return excluded
}

func modelIDsForCatalog(models []modelCatalogItem) []string {
	out := make([]string, 0, len(models))
	for _, model := range models {
		if model.ModelID != "" {
			out = append(out, model.ModelID)
		}
	}
	sort.Strings(out)
	return out
}

func providerSupportedModelIDs(auth *coreauth.Auth) []string {
	if auth == nil {
		return nil
	}
	models := registry.GetGlobalRegistry().GetModelsForClient(auth.ID)
	if len(models) == 0 && auth.ModelInventory != nil {
		models = modelInfosFromInventory(publicProviderFromAuth(auth), auth.ModelInventory)
	}
	out := make([]string, 0, len(models))
	public := publicProviderFromAuth(auth)
	for _, model := range models {
		if model == nil {
			continue
		}
		id := providerLocalModelID(public, auth.Provider, model.ID)
		if id != "" {
			out = append(out, id)
		}
	}
	sort.Strings(out)
	return out
}

func modelIDsFromRegistryModels(publicProvider, rawProvider string, models []*registry.ModelInfo) []string {
	out := make([]string, 0, len(models))
	for _, model := range models {
		if model == nil {
			continue
		}
		if id := providerLocalModelID(publicProvider, rawProvider, model.ID); id != "" {
			out = append(out, id)
		}
	}
	sort.Strings(out)
	return out
}

func normalizeManagementProvider(provider string) (managementProviderRef, error) {
	key := strings.ToLower(strings.TrimSpace(provider))
	switch key {
	case "github", "copilot", "github-copilot":
		return managementProviderRef{Public: "github-copilot", StorageKeys: []string{"github-copilot"}}, nil
	case "claude", "claude-code", "anthropic":
		return managementProviderRef{Public: "anthropic", StorageKeys: []string{"anthropic", "claude"}}, nil
	case "gemini", "gemini-cli", "google":
		return managementProviderRef{Public: "gemini", StorageKeys: []string{"gemini", "gemini-cli"}}, nil
	case "codex", "antigravity", "kiro", "kimi", "xai", "openai", "openai-compatibility", "vertex", "vertex-anthropic":
		return managementProviderRef{Public: key, StorageKeys: []string{key}}, nil
	default:
		return managementProviderRef{}, fmt.Errorf("unknown provider %q", provider)
	}
}

func managementProviderRefFromAuth(auth *coreauth.Auth) managementProviderRef {
	public := publicProviderFromAuth(auth)
	ref, err := normalizeManagementProvider(public)
	if err == nil {
		return ref
	}
	return managementProviderRef{Public: public, StorageKeys: []string{public}}
}

func publicProviderFromAuth(auth *coreauth.Auth) string {
	if auth == nil {
		return ""
	}
	provider := firstProviderNonEmpty(auth.Provider, authAttribute(auth, "provider"), authAttribute(auth, "type"))
	if auth.Attributes != nil {
		provider = firstProviderNonEmpty(auth.Attributes["provider_key"], provider)
	}
	ref, err := normalizeManagementProvider(provider)
	if err == nil {
		return ref.Public
	}
	return strings.ToLower(strings.TrimSpace(provider))
}

func authMatchesManagementProvider(auth *coreauth.Auth, ref managementProviderRef) bool {
	if auth == nil {
		return false
	}
	if publicProviderFromAuth(auth) == ref.Public {
		return true
	}
	raw := strings.ToLower(strings.TrimSpace(firstProviderNonEmpty(auth.Provider, authAttribute(auth, "provider"), authAttribute(auth, "type"))))
	for _, key := range ref.StorageKeys {
		if raw == key {
			return true
		}
	}
	return false
}

func providerLocalModelID(publicProvider, rawProvider, modelID string) string {
	trimmed := strings.TrimSpace(modelID)
	if trimmed == "" {
		return ""
	}
	candidates := []string{publicProvider, rawProvider}
	if ref, err := normalizeManagementProvider(publicProvider); err == nil {
		candidates = append(candidates, ref.StorageKeys...)
	}
	for _, provider := range candidates {
		prefix := strings.ToLower(strings.TrimSpace(provider)) + "/"
		if prefix != "/" && strings.HasPrefix(strings.ToLower(trimmed), prefix) {
			return strings.TrimSpace(trimmed[len(prefix):])
		}
	}
	return trimmed
}

func normalizeProviderModelID(provider, modelID string) string {
	return providerLocalModelID(provider, provider, modelID)
}

func managementProviderDisplayName(provider string) string {
	switch provider {
	case "anthropic":
		return "Anthropic"
	case "github-copilot":
		return "GitHub Copilot"
	case "gemini":
		return "Gemini"
	case "codex":
		return "Codex"
	case "antigravity":
		return "Antigravity"
	case "kiro":
		return "Kiro"
	case "kimi":
		return "Kimi"
	case "xai":
		return "xAI"
	case "openai":
		return "OpenAI"
	case "openai-compatibility":
		return "OpenAI Compatibility"
	case "vertex":
		return "Vertex"
	case "vertex-anthropic":
		return "Vertex Anthropic"
	default:
		return provider
	}
}

func appendUniqueStrings(values []string, extras ...string) []string {
	seen := map[string]struct{}{}
	out := make([]string, 0, len(values)+len(extras))
	for _, value := range append(values, extras...) {
		trimmed := strings.TrimSpace(value)
		if trimmed == "" {
			continue
		}
		key := strings.ToLower(trimmed)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, trimmed)
	}
	return out
}

func matchModelPattern(pattern, value string) bool {
	pattern = strings.ToLower(strings.TrimSpace(pattern))
	value = strings.ToLower(strings.TrimSpace(value))
	if pattern == "" {
		return false
	}
	if !strings.Contains(pattern, "*") {
		return pattern == value
	}
	parts := strings.Split(pattern, "*")
	position := 0
	for i, part := range parts {
		if part == "" {
			continue
		}
		index := strings.Index(value[position:], part)
		if index < 0 {
			return false
		}
		if i == 0 && !strings.HasPrefix(pattern, "*") && index != 0 {
			return false
		}
		position += index + len(part)
	}
	last := parts[len(parts)-1]
	if last != "" && !strings.HasSuffix(pattern, "*") && !strings.HasSuffix(value, last) {
		return false
	}
	return true
}
