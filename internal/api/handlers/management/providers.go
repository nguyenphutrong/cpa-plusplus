package management

import (
	"context"
	"fmt"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/auth/copilot"
	sdkAuth "github.com/router-for-me/CLIProxyAPI/v7/sdk/auth"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

func (h *Handler) ListProviders(c *gin.Context) {
	if h.authManager == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "core auth manager unavailable"})
		return
	}
	auths := h.authManager.List()
	out := make([]gin.H, 0, len(auths))
	for _, auth := range auths {
		if response := h.providerResponseFromAuth(auth); response != nil {
			out = append(out, response)
		}
	}
	c.JSON(http.StatusOK, out)
}

func (h *Handler) GetProvider(c *gin.Context) {
	auth, ok := h.findProviderAuth(c.Param("id"))
	if !ok {
		c.JSON(http.StatusNotFound, gin.H{"error": "provider credential not found"})
		return
	}
	c.JSON(http.StatusOK, h.providerResponseFromAuth(auth))
}

func (h *Handler) PatchProvider(c *gin.Context) {
	auth, ok := h.findProviderAuth(c.Param("id"))
	if !ok {
		c.JSON(http.StatusNotFound, gin.H{"error": "provider credential not found"})
		return
	}
	var req struct {
		Label          *string           `json:"label"`
		Disabled       *bool             `json:"disabled"`
		Prefix         *string           `json:"prefix"`
		ProxyURL       *string           `json:"proxy_url"`
		Headers        map[string]string `json:"headers"`
		Priority       *int              `json:"priority"`
		ExcludedModels []string          `json:"excluded_models"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body"})
		return
	}
	if req.Label != nil {
		auth.Label = strings.TrimSpace(*req.Label)
	}
	if req.Disabled != nil {
		auth.Disabled = *req.Disabled
		if *req.Disabled {
			auth.Status = coreauth.StatusDisabled
			auth.StatusMessage = "disabled via management API"
		} else {
			auth.Status = coreauth.StatusActive
			auth.StatusMessage = ""
		}
	}
	if req.Prefix != nil {
		auth.Prefix = strings.TrimSpace(*req.Prefix)
		setAuthMetadataString(auth, "prefix", auth.Prefix)
	}
	if req.ProxyURL != nil {
		auth.ProxyURL = strings.TrimSpace(*req.ProxyURL)
		setAuthMetadataString(auth, "proxy_url", auth.ProxyURL)
	}
	if len(req.Headers) > 0 {
		setAuthHeaders(auth, req.Headers)
	}
	if req.Priority != nil {
		if auth.Metadata == nil {
			auth.Metadata = map[string]any{}
		}
		if auth.Attributes == nil {
			auth.Attributes = map[string]string{}
		}
		if *req.Priority == 0 {
			delete(auth.Metadata, "priority")
			delete(auth.Attributes, "priority")
		} else {
			auth.Metadata["priority"] = *req.Priority
			auth.Attributes["priority"] = strconv.Itoa(*req.Priority)
		}
	}
	if req.ExcludedModels != nil {
		models := make([]string, 0, len(req.ExcludedModels))
		for _, model := range req.ExcludedModels {
			if trimmed := strings.TrimSpace(model); trimmed != "" {
				models = append(models, trimmed)
			}
		}
		if auth.Metadata == nil {
			auth.Metadata = map[string]any{}
		}
		if len(models) == 0 {
			delete(auth.Metadata, "excluded_models")
		} else {
			auth.Metadata["excluded_models"] = models
		}
	}
	auth.UpdatedAt = time.Now().UTC()
	if _, err := h.authManager.Update(c.Request.Context(), auth); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": fmt.Sprintf("failed to update provider credential: %v", err)})
		return
	}
	c.JSON(http.StatusOK, h.providerResponseFromAuth(auth))
}

func (h *Handler) DeleteProvider(c *gin.Context) {
	auth, ok := h.findProviderAuth(c.Param("id"))
	if !ok {
		c.JSON(http.StatusNotFound, gin.H{"error": "provider credential not found"})
		return
	}
	name := strings.TrimSpace(auth.FileName)
	if name == "" {
		name = strings.TrimSpace(auth.ID)
	}
	if _, status, err := h.deleteAuthFileByName(c.Request.Context(), name); err != nil {
		c.JSON(status, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": "ok", "id": auth.ID, "provider": auth.Provider})
}

func (h *Handler) RefreshProvider(c *gin.Context) {
	auth, ok := h.findProviderAuth(c.Param("id"))
	if !ok {
		c.JSON(http.StatusNotFound, gin.H{"error": "provider credential not found"})
		return
	}
	refreshed, err := h.refreshProviderAuth(c.Request.Context(), auth)
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, h.providerResponseFromAuth(refreshed))
}

func (h *Handler) SyncProviderModels(c *gin.Context) {
	providerParam := firstProviderNonEmpty(c.Param("provider"), c.Param("id"))
	provider, err := NormalizeOAuthProvider(providerParam)
	if err != nil {
		provider = strings.TrimSpace(providerParam)
	}
	c.JSON(http.StatusOK, gin.H{
		"provider":   provider,
		"checked_at": time.Now().UTC().Format(time.RFC3339),
	})
}

func (h *Handler) refreshProviderAuth(ctx context.Context, auth *coreauth.Auth) (*coreauth.Auth, error) {
	if auth == nil {
		return nil, fmt.Errorf("provider credential not found")
	}
	switch strings.ToLower(strings.TrimSpace(auth.Provider)) {
	case "kiro":
		refreshed, err := sdkAuth.RefreshKiroToken(ctx, h.cfg, auth)
		if err != nil {
			return nil, err
		}
		if _, err := h.authManager.Update(ctx, refreshed); err != nil {
			return nil, err
		}
		return refreshed, nil
	case "github-copilot":
		storage := &copilot.TokenStorage{
			AccessToken: metaStringValue(auth.Metadata, "access_token"),
			TokenType:   metaStringValue(auth.Metadata, "token_type"),
			Scope:       metaStringValue(auth.Metadata, "scope"),
			Username:    metaStringValue(auth.Metadata, "username"),
			Type:        "github-copilot",
		}
		if storage.AccessToken == "" {
			return nil, fmt.Errorf("github-copilot refresh: missing access token")
		}
		if err := sdkAuth.RefreshGitHubCopilotToken(ctx, h.cfg, storage); err != nil {
			return nil, err
		}
		auth.LastRefreshedAt = time.Now().UTC()
		auth.UpdatedAt = auth.LastRefreshedAt
		if _, err := h.authManager.Update(ctx, auth); err != nil {
			return nil, err
		}
		return auth, nil
	default:
		return auth, nil
	}
}

func (h *Handler) findProviderAuth(id string) (*coreauth.Auth, bool) {
	if h == nil || h.authManager == nil {
		return nil, false
	}
	id = strings.TrimSpace(id)
	if id == "" {
		return nil, false
	}
	if auth, ok := h.authManager.GetByID(id); ok {
		return auth, true
	}
	for _, auth := range h.authManager.List() {
		if auth == nil {
			continue
		}
		if strings.TrimSpace(auth.FileName) == id || filepath.Base(strings.TrimSpace(authAttribute(auth, "path"))) == id {
			return auth, true
		}
	}
	return nil, false
}

func (h *Handler) providerResponseFromAuth(auth *coreauth.Auth) gin.H {
	if auth == nil {
		return nil
	}
	provider := strings.TrimSpace(auth.Provider)
	if provider == "" {
		provider = strings.TrimSpace(authAttribute(auth, "provider"))
	}
	label := strings.TrimSpace(auth.Label)
	if label == "" {
		label = strings.TrimSpace(auth.FileName)
	}
	validation := gin.H{
		"valid":     !auth.Disabled && auth.Status != coreauth.StatusDisabled && !auth.Unavailable,
		"auth_type": "oauth",
	}
	if account := firstProviderNonEmpty(authEmail(auth), authAttribute(auth, "email"), metaStringValue(auth.Metadata, "email"), authAttribute(auth, "account")); account != "" {
		validation["account_identity"] = account
	}
	if expiresAt := firstProviderNonEmpty(metaStringValue(auth.Metadata, "expires_at"), authAttribute(auth, "expires_at")); expiresAt != "" {
		validation["expires_at"] = expiresAt
	}
	if auth.StatusMessage != "" {
		validation["warnings"] = []string{auth.StatusMessage}
	}
	out := gin.H{
		"id":         auth.ID,
		"type":       "oauth",
		"provider":   provider,
		"label":      label,
		"disabled":   auth.Disabled || auth.Status == coreauth.StatusDisabled,
		"secret":     "",
		"priority":   providerPriority(auth),
		"prefix":     auth.Prefix,
		"proxy_url":  auth.ProxyURL,
		"headers":    coreauth.ExtractCustomHeadersFromMetadata(auth.Metadata),
		"validation": validation,
	}
	if projectID := authProjectID(auth); projectID != "" {
		out["project_id"] = projectID
	}
	if excluded := providerExcludedModels(auth); len(excluded) > 0 {
		out["excluded_models"] = excluded
	}
	return out
}

func setAuthMetadataString(auth *coreauth.Auth, key, value string) {
	if auth.Metadata == nil {
		auth.Metadata = map[string]any{}
	}
	if strings.TrimSpace(value) == "" {
		delete(auth.Metadata, key)
		return
	}
	auth.Metadata[key] = strings.TrimSpace(value)
}

func setAuthHeaders(auth *coreauth.Auth, headers map[string]string) {
	if auth.Metadata == nil {
		auth.Metadata = map[string]any{}
	}
	if auth.Attributes == nil {
		auth.Attributes = map[string]string{}
	}
	existing := coreauth.ExtractCustomHeadersFromMetadata(auth.Metadata)
	for key, value := range headers {
		name := strings.TrimSpace(key)
		if name == "" {
			continue
		}
		val := strings.TrimSpace(value)
		if val == "" {
			delete(existing, name)
			delete(auth.Attributes, "header:"+name)
			continue
		}
		existing[name] = val
		auth.Attributes["header:"+name] = val
	}
	if len(existing) == 0 {
		delete(auth.Metadata, "headers")
		return
	}
	metaHeaders := make(map[string]any, len(existing))
	for key, value := range existing {
		metaHeaders[key] = value
	}
	auth.Metadata["headers"] = metaHeaders
}

func providerPriority(auth *coreauth.Auth) int {
	if auth == nil {
		return 0
	}
	if raw := strings.TrimSpace(authAttribute(auth, "priority")); raw != "" {
		if parsed, err := strconv.Atoi(raw); err == nil {
			return parsed
		}
	}
	switch value := auth.Metadata["priority"].(type) {
	case int:
		return value
	case float64:
		return int(value)
	case string:
		if parsed, err := strconv.Atoi(strings.TrimSpace(value)); err == nil {
			return parsed
		}
	}
	return 0
}

func providerExcludedModels(auth *coreauth.Auth) []string {
	if auth == nil || auth.Metadata == nil {
		return nil
	}
	raw, ok := auth.Metadata["excluded_models"]
	if !ok {
		return nil
	}
	out := make([]string, 0)
	switch values := raw.(type) {
	case []string:
		for _, value := range values {
			if trimmed := strings.TrimSpace(value); trimmed != "" {
				out = append(out, trimmed)
			}
		}
	case []any:
		for _, value := range values {
			if s, okString := value.(string); okString {
				if trimmed := strings.TrimSpace(s); trimmed != "" {
					out = append(out, trimmed)
				}
			}
		}
	}
	return out
}

func metaStringValue(meta map[string]any, key string) string {
	if meta == nil {
		return ""
	}
	if value, ok := meta[key].(string); ok {
		return strings.TrimSpace(value)
	}
	return ""
}

func firstProviderNonEmpty(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}
