package management

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
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
	group.POST("/quota/refresh", handler.PostQuotaRefresh)
	group.POST("/quota/refresh/:provider/:authID", handler.PostQuotaRefresh)
	return router, manager
}
