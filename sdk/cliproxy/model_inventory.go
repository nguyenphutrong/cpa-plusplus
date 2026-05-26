package cliproxy

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/executor/helps"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/util"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

const (
	defaultCodexModelsURL  = "https://chatgpt.com/backend-api/codex/models"
	defaultOpenAIModelsURL = "https://api.openai.com/v1/models"
	defaultGeminiBaseURL   = "https://generativelanguage.googleapis.com"
	modelInventoryMaxBytes = 4 << 20
)

func (s *Service) refreshLiveModelInventory(ctx context.Context, auth *coreauth.Auth) *coreauth.Auth {
	if auth == nil {
		return nil
	}
	if !supportsLiveModelInventory(auth) {
		return auth
	}
	next := auth.Clone()
	inventory, err := s.discoverLiveModelInventory(ctx, next)
	if err != nil {
		if inventory.Provider == "" {
			inventory.Provider = strings.ToLower(strings.TrimSpace(next.Provider))
		}
		inventory.FetchedAt = time.Now().UTC()
		inventory.Warnings = append(inventory.Warnings, err.Error())
		if next.ModelInventory != nil && len(next.ModelInventory.Models) > 0 {
			inventory.Models = next.ModelInventory.Models
		}
		next.ModelInventory = &inventory
		return next
	}
	next.ModelInventory = &inventory
	return next
}

func (s *Service) discoverLiveModelInventory(ctx context.Context, auth *coreauth.Auth) (coreauth.LiveModelInventory, error) {
	provider := strings.ToLower(strings.TrimSpace(auth.Provider))
	if provider == "" {
		return coreauth.LiveModelInventory{}, fmt.Errorf("provider is required")
	}

	switch {
	case provider == "codex":
		return s.fetchCodexModelInventory(ctx, auth)
	case provider == "gemini":
		return s.fetchGeminiModelInventory(ctx, auth)
	case provider == "openai" || provider == "openai-compatibility" || hasOpenAICompatAttributes(auth):
		return s.fetchOpenAICompatModelInventory(ctx, auth)
	default:
		return coreauth.LiveModelInventory{}, fmt.Errorf("live model inventory is not supported for provider %q", provider)
	}
}

func (s *Service) fetchCodexModelInventory(ctx context.Context, auth *coreauth.Auth) (coreauth.LiveModelInventory, error) {
	apiKey, baseURL := authAPIKeyAndBaseURL(auth)
	if apiKey == "" {
		return coreauth.LiveModelInventory{}, fmt.Errorf("codex model inventory requires an access token or API key")
	}
	targetURL := defaultCodexModelsURL
	if baseURL != "" {
		root := strings.TrimRight(baseURL, "/")
		root = strings.TrimSuffix(root, "/v1")
		targetURL = root + "/models"
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, targetURL, nil)
	if err != nil {
		return coreauth.LiveModelInventory{}, err
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("User-Agent", "codex_cli_rs/0.118.0")
	req.Header.Set("Originator", "codex_cli_rs")
	util.ApplyCustomHeadersFromAttrs(req, auth.Attributes)

	resp, err := helps.NewProxyAwareHTTPClient(ctx, s.cfg, auth, 0).Do(req)
	if err != nil {
		return coreauth.LiveModelInventory{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return coreauth.LiveModelInventory{}, fmt.Errorf("codex model inventory failed with HTTP %d", resp.StatusCode)
	}
	return ParseCodexModelInventory(resp.Body)
}

func (s *Service) fetchOpenAICompatModelInventory(ctx context.Context, auth *coreauth.Auth) (coreauth.LiveModelInventory, error) {
	apiKey, baseURL := authAPIKeyAndBaseURL(auth)
	if baseURL == "" && strings.EqualFold(strings.TrimSpace(auth.Provider), "openai") {
		baseURL = strings.TrimSuffix(defaultOpenAIModelsURL, "/v1/models")
	}
	if apiKey == "" {
		return coreauth.LiveModelInventory{}, fmt.Errorf("openai-compatible model inventory requires an API key")
	}
	if baseURL == "" {
		return coreauth.LiveModelInventory{}, fmt.Errorf("openai-compatible model inventory requires a base URL")
	}

	targetURL := strings.TrimRight(baseURL, "/")
	if !strings.HasSuffix(targetURL, "/models") {
		targetURL = strings.TrimSuffix(targetURL, "/v1") + "/v1/models"
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, targetURL, nil)
	if err != nil {
		return coreauth.LiveModelInventory{}, err
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)
	util.ApplyCustomHeadersFromAttrs(req, auth.Attributes)

	resp, err := helps.NewProxyAwareHTTPClient(ctx, s.cfg, auth, 0).Do(req)
	if err != nil {
		return coreauth.LiveModelInventory{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return coreauth.LiveModelInventory{}, fmt.Errorf("openai-compatible model inventory failed with HTTP %d", resp.StatusCode)
	}
	return ParseOpenAICompatModelInventory(strings.ToLower(strings.TrimSpace(auth.Provider)), resp.Body)
}

func (s *Service) fetchGeminiModelInventory(ctx context.Context, auth *coreauth.Auth) (coreauth.LiveModelInventory, error) {
	apiKey, baseURL := authAPIKeyAndBaseURL(auth)
	if apiKey == "" {
		return coreauth.LiveModelInventory{}, fmt.Errorf("gemini model inventory requires an API key")
	}
	if baseURL == "" {
		baseURL = defaultGeminiBaseURL
	}
	targetURL := strings.TrimRight(baseURL, "/") + "/v1beta/models?key=" + url.QueryEscape(apiKey)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, targetURL, nil)
	if err != nil {
		return coreauth.LiveModelInventory{}, err
	}
	util.ApplyCustomHeadersFromAttrs(req, auth.Attributes)

	resp, err := helps.NewProxyAwareHTTPClient(ctx, s.cfg, auth, 0).Do(req)
	if err != nil {
		return coreauth.LiveModelInventory{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return coreauth.LiveModelInventory{}, fmt.Errorf("gemini model inventory failed with HTTP %d", resp.StatusCode)
	}
	return ParseGeminiModelInventory(resp.Body)
}

func ParseCodexModelInventory(body io.Reader) (coreauth.LiveModelInventory, error) {
	var payload struct {
		Models []map[string]any `json:"models"`
	}
	if err := json.NewDecoder(io.LimitReader(body, modelInventoryMaxBytes)).Decode(&payload); err != nil {
		return coreauth.LiveModelInventory{}, err
	}
	models := make([]coreauth.LiveModelEntry, 0, len(payload.Models))
	seen := map[string]struct{}{}
	for _, item := range payload.Models {
		id, _ := item["slug"].(string)
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		models = append(models, coreauth.LiveModelEntry{ID: id, Raw: cloneMap(item)})
	}
	return coreauth.LiveModelInventory{Provider: "codex", Models: models, FetchedAt: time.Now().UTC()}, nil
}

func ParseOpenAICompatModelInventory(provider string, body io.Reader) (coreauth.LiveModelInventory, error) {
	var payload struct {
		Data []struct {
			ID      string         `json:"id"`
			OwnedBy string         `json:"owned_by"`
			Raw     map[string]any `json:"-"`
		} `json:"data"`
	}
	raw, err := io.ReadAll(io.LimitReader(body, modelInventoryMaxBytes))
	if err != nil {
		return coreauth.LiveModelInventory{}, err
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return coreauth.LiveModelInventory{}, err
	}
	var rawPayload struct {
		Data []map[string]any `json:"data"`
	}
	_ = json.Unmarshal(raw, &rawPayload)

	models := make([]coreauth.LiveModelEntry, 0, len(payload.Data))
	seen := map[string]struct{}{}
	for i, item := range payload.Data {
		id := strings.TrimSpace(item.ID)
		if id == "" {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		var rawItem map[string]any
		if i < len(rawPayload.Data) {
			rawItem = cloneMap(rawPayload.Data[i])
		}
		models = append(models, coreauth.LiveModelEntry{ID: id, OwnedBy: strings.TrimSpace(item.OwnedBy), Raw: rawItem})
	}
	return coreauth.LiveModelInventory{Provider: strings.TrimSpace(provider), Models: models, FetchedAt: time.Now().UTC()}, nil
}

func ParseGeminiModelInventory(body io.Reader) (coreauth.LiveModelInventory, error) {
	var payload struct {
		Models []map[string]any `json:"models"`
	}
	if err := json.NewDecoder(io.LimitReader(body, modelInventoryMaxBytes)).Decode(&payload); err != nil {
		return coreauth.LiveModelInventory{}, err
	}
	models := make([]coreauth.LiveModelEntry, 0, len(payload.Models))
	seen := map[string]struct{}{}
	for _, item := range payload.Models {
		name, _ := item["name"].(string)
		id := strings.TrimPrefix(strings.TrimSpace(name), "models/")
		if id == "" {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		models = append(models, coreauth.LiveModelEntry{ID: id, OwnedBy: "google", Raw: cloneMap(item)})
	}
	return coreauth.LiveModelInventory{Provider: "gemini", Models: models, FetchedAt: time.Now().UTC()}, nil
}

func liveInventoryModelInfos(auth *coreauth.Auth, provider, ownedBy, modelType string) []*ModelInfo {
	if auth == nil || auth.ModelInventory == nil || len(auth.ModelInventory.Models) == 0 {
		return nil
	}
	now := time.Now().Unix()
	models := make([]*ModelInfo, 0, len(auth.ModelInventory.Models))
	seen := map[string]struct{}{}
	for _, live := range auth.ModelInventory.Models {
		id := strings.TrimSpace(live.ID)
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
				OwnedBy:     ownedBy,
				Type:        modelType,
				DisplayName: id,
			}
		}
		info.ID = id
		if info.Object == "" {
			info.Object = "model"
		}
		if info.Created == 0 {
			info.Created = now
		}
		if live.OwnedBy != "" {
			info.OwnedBy = live.OwnedBy
		} else if info.OwnedBy == "" {
			info.OwnedBy = ownedBy
		}
		if info.Type == "" {
			info.Type = modelType
		}
		if info.DisplayName == "" {
			info.DisplayName = id
		}
		models = append(models, info)
	}
	return models
}

func authAPIKeyAndBaseURL(auth *coreauth.Auth) (apiKey, baseURL string) {
	if auth == nil {
		return "", ""
	}
	if auth.Attributes != nil {
		apiKey = strings.TrimSpace(auth.Attributes["api_key"])
		baseURL = strings.TrimSpace(auth.Attributes["base_url"])
	}
	if apiKey == "" && auth.Metadata != nil {
		if v, ok := auth.Metadata["access_token"].(string); ok {
			apiKey = strings.TrimSpace(v)
		}
	}
	if baseURL == "" && auth.Metadata != nil {
		if v, ok := auth.Metadata["base_url"].(string); ok {
			baseURL = strings.TrimSpace(v)
		}
	}
	return apiKey, baseURL
}

func hasOpenAICompatAttributes(auth *coreauth.Auth) bool {
	if auth == nil || auth.Attributes == nil {
		return false
	}
	return strings.TrimSpace(auth.Attributes["compat_name"]) != "" || strings.TrimSpace(auth.Attributes["provider_key"]) != ""
}

func supportsLiveModelInventory(auth *coreauth.Auth) bool {
	if auth == nil {
		return false
	}
	provider := strings.ToLower(strings.TrimSpace(auth.Provider))
	return provider == "codex" || provider == "gemini" || provider == "openai" || provider == "openai-compatibility" || hasOpenAICompatAttributes(auth)
}

func cloneMap(src map[string]any) map[string]any {
	if len(src) == 0 {
		return nil
	}
	dst := make(map[string]any, len(src))
	for key, value := range src {
		dst[key] = value
	}
	return dst
}
