package management

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/auth/copilot"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/auth/kiro"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

func TestRequestGitHubCopilotTokenStartsDeviceFlowAndSavesAuth(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "")
	gin.SetMode(gin.TestMode)
	resetOAuthSessionsForTest(t)

	origFactory := newGitHubCopilotDeviceAuth
	newGitHubCopilotDeviceAuth = func(*config.Config) githubCopilotDeviceAuth {
		return &fakeGitHubCopilotDeviceAuth{}
	}
	t.Cleanup(func() { newGitHubCopilotDeviceAuth = origFactory })

	store := &memoryAuthStore{}
	h := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: t.TempDir()}, coreauth.NewManager(nil, nil, nil))
	h.tokenStore = store

	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	ctx.Request = httptest.NewRequest(http.MethodGet, "/v0/management/github-copilot-auth-url", nil)
	h.RequestGitHubCopilotToken(ctx)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	var payload map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if payload["url"] != "https://github.com/login/device" || payload["user_code"] != "GH-CODE" {
		t.Fatalf("unexpected response payload: %#v", payload)
	}
	state, _ := payload["state"].(string)
	if state == "" {
		t.Fatalf("expected state in response")
	}

	waitForSavedAuth(t, store, "github-copilot-test-user.json")
	waitForSessionRemoved(t, state)
}

func TestStartProviderOAuthCopilotReturnsCanonicalSession(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "")
	gin.SetMode(gin.TestMode)
	resetOAuthSessionsForTest(t)
	resetProviderOAuthSessionsForTest(t)

	origFactory := newGitHubCopilotDeviceAuth
	newGitHubCopilotDeviceAuth = func(*config.Config) githubCopilotDeviceAuth {
		return &fakeGitHubCopilotDeviceAuth{}
	}
	t.Cleanup(func() { newGitHubCopilotDeviceAuth = origFactory })

	store := &memoryAuthStore{}
	h := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: t.TempDir()}, coreauth.NewManager(nil, nil, nil))
	h.tokenStore = store

	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/v0/management/providers/oauth/start", strings.NewReader(`{"provider":"copilot"}`))
	ctx.Request.Header.Set("Content-Type", "application/json")
	h.StartProviderOAuth(ctx)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	var payload providerOAuthSessionResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if payload.Provider != "github-copilot" || payload.Status != providerOAuthSessionAwaitingDeviceConfirmation {
		t.Fatalf("unexpected session: %#v", payload)
	}
	if payload.SessionID == "" || payload.AuthURL == "" || payload.UserCode != "GH-CODE" {
		t.Fatalf("missing session fields: %#v", payload)
	}

	waitForSavedAuth(t, store, "github-copilot-test-user.json")
	waitForProviderOAuthStatus(t, payload.SessionID, providerOAuthSessionCompleted)
}

func TestRequestKiroTokenStartsDeviceFlowAndSavesAuth(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "")
	gin.SetMode(gin.TestMode)
	resetOAuthSessionsForTest(t)

	origFactory := newKiroDeviceAuth
	newKiroDeviceAuth = func(*config.Config) kiroDeviceAuth {
		return &fakeKiroDeviceAuth{}
	}
	t.Cleanup(func() { newKiroDeviceAuth = origFactory })

	store := &memoryAuthStore{}
	h := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: t.TempDir()}, coreauth.NewManager(nil, nil, nil))
	h.tokenStore = store

	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	ctx.Request = httptest.NewRequest(http.MethodGet, "/v0/management/kiro-auth-url", nil)
	h.RequestKiroToken(ctx)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	var payload map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if payload["url"] != "https://device.example/complete" || payload["user_code"] != "KIRO-CODE" {
		t.Fatalf("unexpected response payload: %#v", payload)
	}
	state, _ := payload["state"].(string)
	if state == "" {
		t.Fatalf("expected state in response")
	}

	waitForSavedAuth(t, store, "kiro-aws-device-dev-example.com.json")
	waitForSessionRemoved(t, state)
}

func TestStartProviderOAuthRejectsUnsupportedKiroMethod(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "")
	gin.SetMode(gin.TestMode)
	resetOAuthSessionsForTest(t)
	resetProviderOAuthSessionsForTest(t)

	h := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: t.TempDir()}, coreauth.NewManager(nil, nil, nil))

	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/v0/management/providers/oauth/start", strings.NewReader(`{"provider":"kiro","method":"idc_auth_code"}`))
	ctx.Request.Header.Set("Content-Type", "application/json")
	h.StartProviderOAuth(ctx)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d body=%s", rec.Code, http.StatusBadRequest, rec.Body.String())
	}
}

func TestDeleteProviderOAuthSessionCancelsPendingSession(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "")
	gin.SetMode(gin.TestMode)
	resetProviderOAuthSessionsForTest(t)

	storeProviderOAuthSession(&providerOAuthSession{
		ID:        "session-1",
		State:     "state-1",
		Provider:  "kiro",
		Status:    providerOAuthSessionAwaitingDeviceConfirmation,
		ExpiresAt: time.Now().Add(time.Minute),
	})
	RegisterOAuthSession("state-1", "kiro")

	h := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: t.TempDir()}, coreauth.NewManager(nil, nil, nil))
	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	ctx.Params = gin.Params{{Key: "sessionID", Value: "session-1"}}
	ctx.Request = httptest.NewRequest(http.MethodDelete, "/v0/management/providers/oauth/sessions/session-1", nil)
	h.DeleteProviderOAuthSession(ctx)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	var payload providerOAuthSessionResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if payload.Status != providerOAuthSessionCancelled {
		t.Fatalf("status = %s, want %s", payload.Status, providerOAuthSessionCancelled)
	}
}

func resetOAuthSessionsForTest(t *testing.T) {
	t.Helper()
	orig := oauthSessions
	oauthSessions = newOAuthSessionStore(oauthSessionTTL)
	t.Cleanup(func() { oauthSessions = orig })
}

func resetProviderOAuthSessionsForTest(t *testing.T) {
	t.Helper()
	orig := providerOAuthSessions
	providerOAuthSessions = newProviderOAuthSessionStore()
	t.Cleanup(func() { providerOAuthSessions = orig })
}

func waitForSavedAuth(t *testing.T, store *memoryAuthStore, id string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		store.mu.Lock()
		_, ok := store.items[id]
		store.mu.Unlock()
		if ok {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("auth %q was not saved", id)
}

func waitForSessionRemoved(t *testing.T, state string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if _, _, ok := GetOAuthSession(state); !ok {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("expected completed session %q to be removed", state)
}

func waitForProviderOAuthStatus(t *testing.T, sessionID string, want providerOAuthSessionStatus) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		session, ok := providerOAuthSessions.Status(sessionID)
		if ok && session.Status == want {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	session, _ := providerOAuthSessions.Status(sessionID)
	t.Fatalf("session %q status = %s, want %s", sessionID, session.Status, want)
}

type fakeGitHubCopilotDeviceAuth struct{}

func (fakeGitHubCopilotDeviceAuth) StartDeviceFlow(context.Context) (*copilot.DeviceCodeResponse, error) {
	return &copilot.DeviceCodeResponse{
		DeviceCode:      "device",
		UserCode:        "GH-CODE",
		VerificationURI: "https://github.com/login/device",
		ExpiresIn:       600,
		Interval:        1,
	}, nil
}

func (fakeGitHubCopilotDeviceAuth) WaitForAuthorization(context.Context, *copilot.DeviceCodeResponse) (*copilot.AuthBundle, error) {
	return &copilot.AuthBundle{
		TokenData: &copilot.TokenData{
			AccessToken: "github-access-token",
			TokenType:   "bearer",
			Scope:       "user:email",
		},
		Username: "test-user",
	}, nil
}

func (fakeGitHubCopilotDeviceAuth) GetAPIToken(context.Context, string) (*copilot.APIToken, error) {
	return &copilot.APIToken{Token: "copilot-token", ExpiresAt: time.Now().Add(time.Hour).Unix()}, nil
}

type fakeKiroDeviceAuth struct{}

func (fakeKiroDeviceAuth) RegisterClient(context.Context, string) (*kiro.ClientRegistration, error) {
	return &kiro.ClientRegistration{ClientID: "client-id", ClientSecret: "client-secret"}, nil
}

func (fakeKiroDeviceAuth) StartDeviceAuthorization(context.Context, string, string, string) (*kiro.DeviceAuthorization, error) {
	return &kiro.DeviceAuthorization{
		DeviceCode:              "device",
		UserCode:                "KIRO-CODE",
		VerificationURI:         "https://device.example",
		VerificationURIComplete: "https://device.example/complete",
		ExpiresIn:               600,
		Interval:                1,
	}, nil
}

func (fakeKiroDeviceAuth) PollDeviceToken(context.Context, string, string, string, string) kiro.DevicePollResult {
	return kiro.DevicePollResult{Bundle: &kiro.TokenBundle{
		AccessToken:  "kiro-access-token",
		RefreshToken: "kiro-refresh-token",
		ProfileARN:   "arn:aws:kiro:us-east-1:123:profile/dev",
		ClientID:     "client-id",
		ClientSecret: "client-secret",
		Region:       kiro.DefaultRegion,
		ExpiresAt:    time.Now().Add(time.Hour),
		Email:        "dev@example.com",
	}}
}
