package management

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

func TestVirtualModelsManagementPutGetPatch(t *testing.T) {
	gin.SetMode(gin.TestMode)
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(configPath, []byte("port: 8317\n"), 0644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	manager := coreauth.NewManager(nil, nil, nil)
	h := NewHandler(&config.Config{}, configPath, manager)

	putBody := `{
		"enabled": true,
		"cache_ttl": "45s",
		"max_depth": 4,
		"combo_templates": {
			"default": {"targets": [{"target": "codex/gpt-5.1"}]}
		},
		"virtual_models": {
			"fast": {"combo-template": "default", "targets": [{"target": "claude/claude-sonnet-4-5"}]}
		}
	}`
	rec := performVirtualModelsRequest(h.PutVirtualModels, http.MethodPut, "/v0/management/virtual-models", putBody)
	if rec.Code != http.StatusOK {
		t.Fatalf("PUT status = %d body=%s", rec.Code, rec.Body.String())
	}
	if got := h.cfg.EffectiveVirtualModelCacheTTL(); got != "45s" {
		t.Fatalf("cache ttl = %q", got)
	}
	if got := h.cfg.VirtualModels["fast"].ComboTemplate; got != "default" {
		t.Fatalf("combo template = %q", got)
	}
	if resolved, err := manager.ResolveVirtualModel("fast"); err != nil || !resolved.Matched {
		t.Fatalf("manager virtual resolution = %#v err=%v", resolved, err)
	}

	rec = performVirtualModelsRequest(h.GetVirtualModels, http.MethodGet, "/v0/management/virtual-models", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET status = %d body=%s", rec.Code, rec.Body.String())
	}
	var payload map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode GET: %v", err)
	}
	if payload["enabled"] != true {
		t.Fatalf("enabled = %#v", payload["enabled"])
	}

	rec = performVirtualModelsRequest(h.PatchVirtualModelsEnabled, http.MethodPatch, "/v0/management/virtual-models/enabled", `{"enabled": false}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("PATCH status = %d body=%s", rec.Code, rec.Body.String())
	}
	if h.cfg.VirtualModelsRoutingEnabled() {
		t.Fatal("virtual routing still enabled after PATCH")
	}
}

func TestVirtualModelsManagementRejectsInvalidPayload(t *testing.T) {
	gin.SetMode(gin.TestMode)
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(configPath, []byte("port: 8317\n"), 0644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	h := NewHandler(&config.Config{}, configPath, coreauth.NewManager(nil, nil, nil))

	rec := performVirtualModelsRequest(h.PutVirtualModels, http.MethodPut, "/v0/management/virtual-models", `{
		"virtual_models": {
			"a": {"targets": [{"target": "b"}]},
			"b": {"targets": [{"target": "a"}]}
		}
	}`)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	if len(h.cfg.VirtualModels) != 0 {
		t.Fatalf("invalid payload persisted: %#v", h.cfg.VirtualModels)
	}
}

func performVirtualModelsRequest(handler gin.HandlerFunc, method, path, body string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	ctx.Request = httptest.NewRequest(method, path, strings.NewReader(body))
	if body != "" {
		ctx.Request.Header.Set("Content-Type", "application/json")
	}
	handler(ctx)
	return rec
}
