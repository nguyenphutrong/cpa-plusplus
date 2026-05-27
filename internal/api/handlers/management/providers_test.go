package management

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/quota"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/quota/storage"
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
	validation, ok := listed[0]["validation"].(map[string]any)
	if !ok {
		t.Fatalf("missing validation payload: %#v", listed[0])
	}
	if got := validation["account_identity"]; got != "octo" {
		t.Fatalf("account identity = %#v, want octo", got)
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

func TestDeleteProviderRemovesCredentialFromProviderList(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "")
	gin.SetMode(gin.TestMode)

	authDir := t.TempDir()
	fileName := "kiro-test.json"
	authPath := filepath.Join(authDir, fileName)
	if err := os.WriteFile(authPath, []byte(`{"type":"kiro","access_token":"token"}`), 0o600); err != nil {
		t.Fatalf("write auth file: %v", err)
	}

	manager := coreauth.NewManager(nil, nil, nil)
	auth := &coreauth.Auth{
		ID:       "kiro-1",
		Provider: "kiro",
		FileName: fileName,
		Attributes: map[string]string{
			"path": authPath,
		},
	}
	if _, err := manager.Register(context.Background(), auth); err != nil {
		t.Fatalf("register auth: %v", err)
	}
	h := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: authDir}, manager)
	h.tokenStore = &memoryAuthStore{}

	deleteRec := httptest.NewRecorder()
	deleteCtx, _ := gin.CreateTestContext(deleteRec)
	deleteCtx.Params = gin.Params{{Key: "id", Value: "kiro-1"}}
	deleteCtx.Request = httptest.NewRequest(http.MethodDelete, "/v0/management/providers/kiro-1", nil)
	h.DeleteProvider(deleteCtx)
	if deleteRec.Code != http.StatusOK {
		t.Fatalf("delete status = %d body=%s", deleteRec.Code, deleteRec.Body.String())
	}
	if _, err := os.Stat(authPath); !os.IsNotExist(err) {
		t.Fatalf("auth file still exists or stat failed unexpectedly: %v", err)
	}
	if _, ok := manager.GetByID("kiro-1"); ok {
		t.Fatal("deleted provider remained in auth manager")
	}

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
	if len(listed) != 0 {
		t.Fatalf("providers after delete = %#v, want empty", listed)
	}
}

func TestProviderFacadeUsesCachedQuotaAccountLabel(t *testing.T) {
	h := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: t.TempDir()}, nil)
	auth := &coreauth.Auth{
		ID:       "kiro-1",
		Provider: "kiro",
		FileName: "kiro-signin_localhost-profile.json",
		Metadata: map[string]any{
			"username": "kiro-local-user",
			quota.MetadataKey: storage.QuotaData{
				ProviderData: &storage.ProviderQuotaData{
					AccountLabel: "dev@example.com",
				},
			},
		},
	}

	response := h.providerResponseFromAuth(auth)
	validation, ok := response["validation"].(gin.H)
	if !ok {
		t.Fatalf("missing validation payload: %#v", response)
	}
	if got := validation["account_identity"]; got != "dev@example.com" {
		t.Fatalf("account identity = %#v, want dev@example.com", got)
	}
}
