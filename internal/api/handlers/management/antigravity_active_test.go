package management

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	_ "modernc.org/sqlite"
)

func TestPostAntigravityActiveAccountPatchesIDEStateDB(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "")
	gin.SetMode(gin.TestMode)

	dbPath := filepath.Join(t.TempDir(), "state.vscdb")
	createAntigravityTestStateDB(t, dbPath)
	previousOverride := antigravityIDEDBPathOverride
	antigravityIDEDBPathOverride = dbPath
	t.Cleanup(func() { antigravityIDEDBPathOverride = previousOverride })

	manager := coreauth.NewManager(nil, nil, nil)
	expired := time.Now().UTC().Add(time.Hour).Format(time.RFC3339)
	if _, err := manager.Register(context.Background(), &coreauth.Auth{
		ID:       "ag-1",
		Provider: "antigravity",
		Label:    "Target Account",
		Metadata: map[string]any{
			"access_token":  "access-secret",
			"refresh_token": "refresh-secret",
			"id_token":      "id-secret",
			"expired":       expired,
			"email":         "target@example.com",
			"project_id":    "project-123",
		},
	}); err != nil {
		t.Fatalf("register target auth: %v", err)
	}
	if _, err := manager.Register(context.Background(), &coreauth.Auth{
		ID:       "ag-2",
		Provider: "antigravity",
		Metadata: map[string]any{"priority": 1, "email": "other@example.com"},
		Attributes: map[string]string{
			"priority": "1",
		},
	}); err != nil {
		t.Fatalf("register other auth: %v", err)
	}

	h := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: t.TempDir()}, manager)
	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/v0/management/providers/antigravity/active-account", strings.NewReader(`{"id":"ag-1"}`))
	ctx.Request.Header.Set("Content-Type", "application/json")

	h.PostAntigravityActiveAccount(ctx)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	var payload antigravityActiveAccountResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if payload.Provider != "antigravity" || payload.ActiveID != "ag-1" || payload.AccountIdentity != "target@example.com" || payload.ProjectID != "project-123" || !payload.IDEPatched || !payload.RestartRequired {
		t.Fatalf("unexpected response: %#v", payload)
	}
	if strings.Contains(rec.Body.String(), "access-secret") || strings.Contains(rec.Body.String(), "refresh-secret") || strings.Contains(rec.Body.String(), "id-secret") {
		t.Fatalf("response leaked token material: %s", rec.Body.String())
	}

	db := openAntigravityTestStateDB(t, dbPath)
	defer func() { _ = db.Close() }()
	for _, key := range []string{
		"antigravityUnifiedStateSync.oauthToken",
		"antigravityUnifiedStateSync.userStatus",
		"antigravityUnifiedStateSync.enterprisePreferences",
		"antigravityAuthStatus",
		"antigravityOnboarding",
	} {
		if !antigravityTestItemExists(t, db, key) {
			t.Fatalf("expected key %s to be written", key)
		}
	}
	if antigravityTestItemExists(t, db, "google.antigravity") {
		t.Fatal("google.antigravity cleanup key remained")
	}
	var authStatus string
	if err := db.QueryRow("SELECT value FROM ItemTable WHERE key = ?", "antigravityAuthStatus").Scan(&authStatus); err != nil {
		t.Fatalf("read auth status: %v", err)
	}
	if !strings.Contains(authStatus, "target@example.com") || !strings.Contains(authStatus, "access-secret") {
		t.Fatalf("auth status not updated correctly: %s", authStatus)
	}

	updated, ok := manager.GetByID("ag-1")
	if !ok {
		t.Fatal("updated auth missing")
	}
	if got := providerPriority(updated); got != 1 {
		t.Fatalf("target priority = %d, want 1", got)
	}
	other, ok := manager.GetByID("ag-2")
	if !ok {
		t.Fatal("other auth missing")
	}
	if got := providerPriority(other); got != 0 {
		t.Fatalf("other priority = %d, want 0", got)
	}

	matches, errGlob := filepath.Glob(dbPath + ".cpa-backup-*")
	if errGlob != nil {
		t.Fatalf("glob backup: %v", errGlob)
	}
	if len(matches) != 1 {
		t.Fatalf("backup count = %d, want 1 (%v)", len(matches), matches)
	}
}

func TestPostAntigravityActiveAccountValidatesProvider(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "")
	gin.SetMode(gin.TestMode)

	manager := coreauth.NewManager(nil, nil, nil)
	if _, err := manager.Register(context.Background(), &coreauth.Auth{ID: "gemini-1", Provider: "gemini"}); err != nil {
		t.Fatalf("register auth: %v", err)
	}
	h := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: t.TempDir()}, manager)
	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/v0/management/providers/antigravity/active-account", strings.NewReader(`{"id":"gemini-1"}`))
	ctx.Request.Header.Set("Content-Type", "application/json")

	h.PostAntigravityActiveAccount(ctx)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d body=%s, want 400", rec.Code, rec.Body.String())
	}
}

func createAntigravityTestStateDB(t *testing.T, dbPath string) {
	t.Helper()
	db := openAntigravityTestStateDB(t, dbPath)
	defer func() { _ = db.Close() }()
	if _, err := db.Exec(`CREATE TABLE ItemTable (key TEXT PRIMARY KEY, value TEXT)`); err != nil {
		t.Fatalf("create ItemTable: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO ItemTable(key, value) VALUES (?, ?), (?, ?)`, "antigravityUnifiedStateSync.oauthToken", "old-token", "google.antigravity", "old-google"); err != nil {
		t.Fatalf("seed ItemTable: %v", err)
	}
}

func openAntigravityTestStateDB(t *testing.T, dbPath string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open sqlite db: %v", err)
	}
	return db
}

func antigravityTestItemExists(t *testing.T, db *sql.DB, key string) bool {
	t.Helper()
	var count int
	if err := db.QueryRow("SELECT count(*) FROM ItemTable WHERE key = ?", key).Scan(&count); err != nil {
		t.Fatalf("count key %s: %v", key, err)
	}
	return count > 0
}
