package management

import (
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/quota"
)

// Quota exceeded toggles
func (h *Handler) GetSwitchProject(c *gin.Context) {
	c.JSON(200, gin.H{"switch-project": h.cfg.QuotaExceeded.SwitchProject})
}
func (h *Handler) PutSwitchProject(c *gin.Context) {
	h.updateBoolField(c, func(v bool) { h.cfg.QuotaExceeded.SwitchProject = v })
}

func (h *Handler) GetSwitchPreviewModel(c *gin.Context) {
	c.JSON(200, gin.H{"switch-preview-model": h.cfg.QuotaExceeded.SwitchPreviewModel})
}
func (h *Handler) PutSwitchPreviewModel(c *gin.Context) {
	h.updateBoolField(c, func(v bool) { h.cfg.QuotaExceeded.SwitchPreviewModel = v })
}

func (h *Handler) GetQuota(c *gin.Context) {
	service := h.quotaService()
	if service == nil {
		c.JSON(http.StatusOK, quota.BuildQuotaView(nil, nil))
		return
	}
	c.JSON(http.StatusOK, quota.BuildQuotaView(service.Auths(), service.SupportsProvider))
}

func (h *Handler) GetCopilotQuota(c *gin.Context) {
	h.getProviderQuota(c, "github-copilot")
}

func (h *Handler) GetKiroQuota(c *gin.Context) {
	h.getProviderQuota(c, "kiro")
}

func (h *Handler) getProviderQuota(c *gin.Context, provider string) {
	service := h.quotaService()
	if service == nil {
		c.JSON(http.StatusOK, quota.BuildQuotaView(nil, nil))
		return
	}
	providerKey := quota.ProviderKeyForName(provider)
	auths := service.Auths()
	filtered := auths[:0]
	for _, auth := range auths {
		if auth != nil && quota.ProviderKey(auth) == providerKey {
			filtered = append(filtered, auth)
		}
	}
	c.JSON(http.StatusOK, quota.BuildQuotaView(filtered, service.SupportsProvider))
}

func (h *Handler) GetQuotaSummary(c *gin.Context) {
	service := h.quotaService()
	if service == nil {
		c.JSON(http.StatusOK, quota.QuotaSummaryView{})
		return
	}
	view := quota.BuildQuotaView(service.Auths(), service.SupportsProvider)
	c.JSON(http.StatusOK, quota.BuildQuotaSummaryView(view))
}

func (h *Handler) PostQuotaRefresh(c *gin.Context) {
	service := h.quotaService()
	if service == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "quota sync is not configured"})
		return
	}

	provider := c.Param("provider")
	authID := c.Param("authID")
	if provider == "" && authID == "" {
		view, _ := service.SyncAll(c.Request.Context())
		c.JSON(http.StatusOK, view)
		return
	}
	if provider == "" || authID == "" {
		c.JSON(http.StatusNotFound, gin.H{"error": "credential not found"})
		return
	}
	if capability := quota.SupportsProvider(provider); !capability.Supported {
		c.JSON(http.StatusConflict, gin.H{"error": capability.Reason})
		return
	}

	for _, auth := range service.Auths() {
		if auth == nil || auth.ID != authID || quota.ProviderKey(auth) != quota.ProviderKeyForName(provider) {
			continue
		}
		if _, err := service.SyncCredential(c.Request.Context(), auth); err != nil {
			c.JSON(http.StatusBadGateway, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, quota.BuildQuotaView(service.Auths(), service.SupportsProvider))
		return
	}
	c.JSON(http.StatusNotFound, gin.H{"error": "credential not found"})
}

func (h *Handler) quotaService() *quota.SyncService {
	if h == nil || h.authManager == nil {
		return nil
	}
	return quota.NewSyncService(h.authManager, h.resolveTokenForAuth)
}
