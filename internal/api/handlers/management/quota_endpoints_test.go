package management

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/quota"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/quota/storage"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

func TestQuotaEndpointsRequireManagementKey(t *testing.T) {
	router, _ := testQuotaRouter(t)

	req := httptest.NewRequest(http.MethodGet, "/v0/management/quota", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d body=%s", rec.Code, http.StatusUnauthorized, rec.Body.String())
	}
}

func TestGetQuotaReturnsEmptyView(t *testing.T) {
	router, _ := testQuotaRouter(t)

	req := httptest.NewRequest(http.MethodGet, "/v0/management/quota", nil)
	req.Header.Set("Authorization", "Bearer test-management-key")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"providers":[]`) {
		t.Fatalf("body = %s, want empty providers", rec.Body.String())
	}
}

func TestProviderQuotaCompatibilityEndpointsReturnView(t *testing.T) {
	router, _ := testQuotaRouter(t)

	for _, path := range []string{"/v0/management/copilot-quota", "/v0/management/kiro-quota", "/v0/management/quota/providers/github-copilot"} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.Header.Set("Authorization", "Bearer test-management-key")
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("%s status = %d, want %d body=%s", path, rec.Code, http.StatusOK, rec.Body.String())
		}
		if !strings.Contains(rec.Body.String(), `"providers":[]`) {
			t.Fatalf("%s body = %s, want empty providers", path, rec.Body.String())
		}
	}
}

func TestPostQuotaRefreshSingleCredentialStatuses(t *testing.T) {
	router, manager := testQuotaRouter(t)
	if _, err := manager.Register(context.Background(), &coreauth.Auth{
		ID:       "codex-auth",
		Provider: "codex",
		Metadata: map[string]any{
			"type": "codex",
		},
	}); err != nil {
		t.Fatalf("register auth: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/v0/management/quota/refresh/xai/missing", nil)
	req.Header.Set("Authorization", "Bearer test-management-key")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusConflict {
		t.Fatalf("unsupported status = %d, want %d body=%s", rec.Code, http.StatusConflict, rec.Body.String())
	}

	req = httptest.NewRequest(http.MethodPost, "/v0/management/quota/refresh/codex/missing", nil)
	req.Header.Set("Authorization", "Bearer test-management-key")
	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("missing status = %d, want %d body=%s", rec.Code, http.StatusNotFound, rec.Body.String())
	}

	req = httptest.NewRequest(http.MethodPost, "/v0/management/quota/refresh/codex/codex-auth", nil)
	req.Header.Set("Authorization", "Bearer test-management-key")
	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("fetch failure status = %d, want %d body=%s", rec.Code, http.StatusBadGateway, rec.Body.String())
	}
}

func TestRenameKiroAuthRecordWithEmailRenamesARNFile(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "")
	gin.SetMode(gin.TestMode)

	store := &memoryAuthStore{}
	manager := coreauth.NewManager(nil, nil, nil)
	oldID := "kiro-signin_localhost-arn-aws-codewhisperer-us-east-1-699475941385-profile-EHGA3GRVQMUK.json"
	auth := &coreauth.Auth{
		ID:       oldID,
		Provider: "kiro",
		FileName: oldID,
		Metadata: map[string]any{
			"type":         "kiro",
			"auth_method":  "signin_localhost",
			"access_token": "kiro-access",
			quota.MetadataKey: storage.QuotaData{
				ProviderData: &storage.ProviderQuotaData{
					AccountLabel: "dev@example.com",
				},
			},
		},
		Attributes: map[string]string{
			"path": oldID,
		},
	}
	if _, err := manager.Register(context.Background(), auth); err != nil {
		t.Fatalf("register auth: %v", err)
	}
	if _, err := store.Save(context.Background(), auth); err != nil {
		t.Fatalf("save old auth: %v", err)
	}

	handler := NewHandler(&config.Config{AuthDir: t.TempDir()}, "", manager)
	handler.tokenStore = store
	handler.renameKiroAuthRecordWithEmail(context.Background(), auth)

	newID := "kiro-signin_localhost-dev-example.com.json"
	store.mu.Lock()
	_, oldStored := store.items[oldID]
	newStored := store.items[newID]
	store.mu.Unlock()
	if oldStored {
		t.Fatalf("old auth %q still stored", oldID)
	}
	if newStored == nil {
		t.Fatalf("new auth %q was not stored", newID)
	}
	if _, ok := manager.GetByID(oldID); ok {
		t.Fatalf("old auth %q still registered", oldID)
	}
	if _, ok := manager.GetByID(newID); !ok {
		t.Fatalf("new auth %q was not registered", newID)
	}
}

func testQuotaRouter(t *testing.T) (*gin.Engine, *coreauth.Manager) {
	t.Helper()
	t.Setenv("MANAGEMENT_PASSWORD", "test-management-key")
	gin.SetMode(gin.TestMode)

	manager := coreauth.NewManager(nil, nil, nil)
	handler := NewHandler(&config.Config{}, "", manager)
	router := gin.New()
	group := router.Group("/v0/management")
	group.Use(handler.Middleware())
	group.GET("/quota", handler.GetQuota)
	group.GET("/quota/summary", handler.GetQuotaSummary)
	group.GET("/quota/providers/:provider", handler.GetProviderQuota)
	group.GET("/copilot-quota", handler.GetCopilotQuota)
	group.GET("/kiro-quota", handler.GetKiroQuota)
	group.POST("/quota/refresh", handler.PostQuotaRefresh)
	group.POST("/quota/refresh/:provider/:authID", handler.PostQuotaRefresh)
	return router, manager
}
