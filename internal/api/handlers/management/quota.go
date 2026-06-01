package management

import (
	"context"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/auth/kiro"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/quota"
	sdkAuth "github.com/router-for-me/CLIProxyAPI/v7/sdk/auth"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	log "github.com/sirupsen/logrus"
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

func (h *Handler) GetProviderQuota(c *gin.Context) {
	provider := c.Param("provider")
	if provider == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "provider is required"})
		return
	}
	h.getProviderQuota(c, provider)
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
		_, _ = service.SyncAll(c.Request.Context())
		h.renameKiroAuthRecordsWithEmail(c.Request.Context(), service.Auths())
		c.JSON(http.StatusOK, quota.BuildQuotaView(service.Auths(), service.SupportsProvider))
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
		h.renameKiroAuthRecordsWithEmail(c.Request.Context(), []*coreauth.Auth{auth})
		c.JSON(http.StatusOK, quota.BuildQuotaView(service.Auths(), service.SupportsProvider))
		return
	}
	c.JSON(http.StatusNotFound, gin.H{"error": "credential not found"})
}

func (h *Handler) renameKiroAuthRecordsWithEmail(ctx context.Context, auths []*coreauth.Auth) {
	for _, auth := range auths {
		h.renameKiroAuthRecordWithEmail(ctx, auth)
	}
}

func (h *Handler) renameKiroAuthRecordWithEmail(ctx context.Context, auth *coreauth.Auth) {
	if h == nil || h.authManager == nil || auth == nil || quota.ProviderKey(auth) != "kiro" {
		return
	}
	email := firstProviderNonEmpty(authEmail(auth), quota.ProviderDataAccountLabel("kiro", quota.CachedQuotaData(auth)))
	if !strings.Contains(email, "@") {
		return
	}
	source := firstProviderNonEmpty(metaStringValue(auth.Metadata, "auth_method"), authAttribute(auth, "source"), "signin_localhost")
	desiredID := sdkAuth.BuildKiroAuthRecord(&kiro.TokenBundle{Email: email}, source).ID
	if desiredID == "" || desiredID == auth.ID {
		return
	}
	if existing, ok := h.authManager.GetByID(desiredID); ok && existing != nil {
		log.Warnf("skip Kiro auth rename from %s to %s: destination already exists", auth.ID, desiredID)
		return
	}

	renamed := auth.Clone()
	oldID := renamed.ID
	renamed.ID = desiredID
	renamed.FileName = desiredID
	if renamed.Metadata == nil {
		renamed.Metadata = map[string]any{}
	}
	if renamed.Attributes == nil {
		renamed.Attributes = map[string]string{}
	}
	renamed.Metadata["email"] = email
	renamed.Attributes["email"] = email
	delete(renamed.Attributes, "path")

	if _, err := h.saveTokenRecord(ctx, renamed); err != nil {
		log.Warnf("failed to save renamed Kiro auth %s: %v", desiredID, err)
		return
	}
	if err := h.deleteTokenRecord(ctx, oldID); err != nil {
		log.Warnf("failed to delete old Kiro auth %s after rename to %s: %v", oldID, desiredID, err)
		return
	}
	h.authManager.UnregisterAuth(oldID)
	if _, err := h.authManager.Register(coreauth.WithSkipPersist(ctx), renamed); err != nil {
		log.Warnf("failed to register renamed Kiro auth %s: %v", desiredID, err)
	}
}

func (h *Handler) quotaService() *quota.SyncService {
	if h == nil || h.authManager == nil {
		return nil
	}
	return quota.NewSyncService(h.authManager, h.resolveTokenForAuth)
}
