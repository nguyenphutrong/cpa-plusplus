package management

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
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
