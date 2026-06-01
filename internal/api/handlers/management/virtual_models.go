package management

import (
	"fmt"
	"net/http"
	"sort"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/routing"
)

type virtualModelsPayload struct {
	Enabled       *bool                                `json:"enabled"`
	CacheTTL      string                               `json:"cache_ttl"`
	MaxDepth      int                                  `json:"max_depth"`
	VirtualModels map[string]config.VirtualModelConfig `json:"virtual_models"`
}

// GetVirtualModels returns the virtual-model routing configuration.
func (h *Handler) GetVirtualModels(c *gin.Context) {
	cfg := h.currentConfig()
	payload := virtualModelsPayloadFromConfig(cfg)
	c.JSON(http.StatusOK, gin.H{
		"enabled":        cfg.VirtualModelsRoutingEnabled(),
		"cache_ttl":      payload.CacheTTL,
		"max_depth":      payload.MaxDepth,
		"virtual_models": payload.VirtualModels,
	})
}

// PutVirtualModels replaces the virtual-model routing configuration.
func (h *Handler) PutVirtualModels(c *gin.Context) {
	var payload virtualModelsPayload
	if err := c.ShouldBindJSON(&payload); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid body"})
		return
	}

	h.mu.Lock()
	defer h.mu.Unlock()
	if h.cfg == nil {
		h.cfg = &config.Config{}
	}

	next := *h.cfg
	next.Routing.VirtualModelsEnabled = payload.Enabled
	next.Routing.VirtualModelCacheTTL = payload.CacheTTL
	next.Routing.MaxVirtualDepth = payload.MaxDepth
	next.VirtualModels = payload.VirtualModels
	next.SanitizeVirtualModels()
	if err := validateVirtualModelsConfig(&next); err != nil {
		c.JSON(http.StatusUnprocessableEntity, gin.H{"error": "invalid_virtual_models", "message": err.Error()})
		return
	}

	h.cfg.Routing.VirtualModelsEnabled = next.Routing.VirtualModelsEnabled
	h.cfg.Routing.VirtualModelCacheTTL = next.Routing.VirtualModelCacheTTL
	h.cfg.Routing.MaxVirtualDepth = next.Routing.MaxVirtualDepth
	h.cfg.VirtualModels = next.VirtualModels
	if h.authManager != nil {
		h.authManager.SetConfig(h.cfg)
	}
	h.persistLocked(c)
}

// PatchVirtualModelsEnabled toggles virtual-model routing.
func (h *Handler) PatchVirtualModelsEnabled(c *gin.Context) {
	var body struct {
		Enabled *bool `json:"enabled"`
		Value   *bool `json:"value"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid body"})
		return
	}
	enabled := body.Enabled
	if enabled == nil {
		enabled = body.Value
	}
	if enabled == nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "missing enabled"})
		return
	}

	h.mu.Lock()
	defer h.mu.Unlock()
	if h.cfg == nil {
		h.cfg = &config.Config{}
	}
	h.cfg.Routing.VirtualModelsEnabled = enabled
	if h.authManager != nil {
		h.authManager.SetConfig(h.cfg)
	}
	h.persistLocked(c)
}

// GetVirtualModelAvailableTargets returns concrete provider/model targets from the registry.
func (h *Handler) GetVirtualModelAvailableTargets(c *gin.Context) {
	reg := registry.GetGlobalRegistry()
	models := reg.GetAvailableModels("")
	type target struct {
		Provider string `json:"provider"`
		Model    string `json:"model"`
		Target   string `json:"target"`
	}
	targets := make([]target, 0)
	for _, model := range models {
		modelID, _ := model["id"].(string)
		modelID = strings.TrimSpace(modelID)
		if modelID == "" {
			continue
		}
		if provider, localModelID, ok := registry.SplitProviderQualifiedModelID(modelID); ok {
			targets = append(targets, target{
				Provider: provider,
				Model:    localModelID,
				Target:   registry.QualifyModelID(provider, localModelID),
			})
			continue
		}
		for _, provider := range reg.GetModelProviders(modelID) {
			provider = strings.TrimSpace(strings.ToLower(provider))
			if provider == "" {
				continue
			}
			targets = append(targets, target{
				Provider: provider,
				Model:    modelID,
				Target:   provider + "/" + modelID,
			})
		}
	}
	sort.Slice(targets, func(i, j int) bool {
		return targets[i].Target < targets[j].Target
	})
	c.JSON(http.StatusOK, gin.H{"targets": targets})
}

func (h *Handler) currentConfig() *config.Config {
	if h == nil || h.cfg == nil {
		return &config.Config{}
	}
	return h.cfg
}

func virtualModelsPayloadFromConfig(cfg *config.Config) virtualModelsPayload {
	return virtualModelsPayload{
		Enabled:       cfg.Routing.VirtualModelsEnabled,
		CacheTTL:      cfg.EffectiveVirtualModelCacheTTL(),
		MaxDepth:      cfg.EffectiveMaxVirtualDepth(),
		VirtualModels: cfg.VirtualModels,
	}
}

func validateVirtualModelsConfig(cfg *config.Config) error {
	if cfg == nil {
		return nil
	}
	for name := range cfg.VirtualModels {
		if _, err := routing.ResolveVirtualModel(cfg, name); err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
	}
	return nil
}
