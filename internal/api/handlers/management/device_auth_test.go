package management

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/auth/antigravity"
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

func TestStartProviderOAuthCodexReturnsCallbackSession(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "")
	gin.SetMode(gin.TestMode)
	resetOAuthSessionsForTest(t)
	resetProviderOAuthSessionsForTest(t)
	resetCallbackForwardersForTest(t)
	requireCallbackPortAvailable(t, codexCallbackPort)

	h := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: t.TempDir(), Port: 8317}, coreauth.NewManager(nil, nil, nil))

	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/v0/management/providers/oauth/start", strings.NewReader(`{"provider":"codex"}`))
	ctx.Request.Header.Set("Content-Type", "application/json")
	h.StartProviderOAuth(ctx)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	var payload providerOAuthSessionResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if payload.Provider != "codex" || payload.Status != providerOAuthSessionAwaitingCallback {
		t.Fatalf("unexpected session: %#v", payload)
	}
	if payload.SessionID == "" || payload.AuthURL == "" || payload.State == "" {
		t.Fatalf("missing callback session fields: %#v", payload)
	}
	legacyProvider, legacyStatus, ok := GetOAuthSession(payload.State)
	if !ok {
		t.Fatalf("expected legacy oauth state to be registered")
	}
	if legacyProvider != "codex" || legacyStatus != "" {
		t.Fatalf("legacy session = (%q, %q), want codex pending", legacyProvider, legacyStatus)
	}
	if !callbackForwarderIsActive(codexCallbackPort, "codex") {
		t.Fatalf("expected codex callback forwarder to be active")
	}
}

func TestStartProviderOAuthAntigravityReturnsCallbackSession(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "")
	gin.SetMode(gin.TestMode)
	resetOAuthSessionsForTest(t)
	resetProviderOAuthSessionsForTest(t)
	resetCallbackForwardersForTest(t)
	requireCallbackPortAvailable(t, antigravity.CallbackPort)

	h := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: t.TempDir(), Port: 8317}, coreauth.NewManager(nil, nil, nil))

	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/v0/management/providers/oauth/start", strings.NewReader(`{"provider":"antigravity"}`))
	ctx.Request.Header.Set("Content-Type", "application/json")
	h.StartProviderOAuth(ctx)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	var payload providerOAuthSessionResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if payload.Provider != "antigravity" || payload.Status != providerOAuthSessionAwaitingCallback {
		t.Fatalf("unexpected session: %#v", payload)
	}
	if payload.SessionID == "" || payload.AuthURL == "" || payload.State == "" || payload.IntervalSeconds == 0 {
		t.Fatalf("missing callback session fields: %#v", payload)
	}
	legacyProvider, legacyStatus, ok := GetOAuthSession(payload.State)
	if !ok {
		t.Fatalf("expected legacy oauth state to be registered")
	}
	if legacyProvider != "antigravity" || legacyStatus != "" {
		t.Fatalf("legacy session = (%q, %q), want antigravity pending", legacyProvider, legacyStatus)
	}
	if !callbackForwarderIsActive(antigravity.CallbackPort, "antigravity") {
		t.Fatalf("expected antigravity callback forwarder to be active")
	}
}

func TestStartProviderOAuthRejectsUnsupportedProvider(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "")
	gin.SetMode(gin.TestMode)
	resetOAuthSessionsForTest(t)
	resetProviderOAuthSessionsForTest(t)

	h := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: t.TempDir()}, coreauth.NewManager(nil, nil, nil))

	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/v0/management/providers/oauth/start", strings.NewReader(`{"provider":"unsupported"}`))
	ctx.Request.Header.Set("Content-Type", "application/json")
	h.StartProviderOAuth(ctx)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d body=%s", rec.Code, http.StatusBadRequest, rec.Body.String())
	}
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
	ctx.Request = httptest.NewRequest(http.MethodGet, "/v0/management/kiro-auth-url?method=device_code", nil)
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

func TestStartProviderOAuthKiroSignInCallbackSavesFullAuth(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "")
	gin.SetMode(gin.TestMode)
	resetOAuthSessionsForTest(t)
	resetProviderOAuthSessionsForTest(t)
	requireCallbackPortAvailable(t, kiroSignInCallbackPort)

	redirectURISeen := make(chan string, 1)
	tokenServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload map[string]string
		if err := json.NewDecoder(r.Body).Decode(&payload); err == nil {
			select {
			case redirectURISeen <- strings.TrimSpace(payload["redirect_uri"]):
			default:
			}
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"accessToken":"kiro-access","refreshToken":"kiro-refresh","profileArn":"arn:aws:kiro:us-east-1:123:profile/dev","expiresIn":3600,"email":"dev@example.com","username":"dev","sub":"subject-1"}`))
	}))
	defer tokenServer.Close()
	origSocialToken := kiro.SocialToken
	kiro.SocialToken = tokenServer.URL
	t.Cleanup(func() { kiro.SocialToken = origSocialToken })

	store := &memoryAuthStore{}
	h := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: t.TempDir()}, coreauth.NewManager(nil, nil, nil))
	h.tokenStore = store

	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/v0/management/providers/oauth/start", strings.NewReader(`{"provider":"kiro"}`))
	ctx.Request.Header.Set("Content-Type", "application/json")
	h.StartProviderOAuth(ctx)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	var started providerOAuthSessionResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &started); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if started.Provider != "kiro" || started.Status != providerOAuthSessionAwaitingCallback {
		t.Fatalf("unexpected session: %#v", started)
	}
	authURL, err := url.Parse(started.AuthURL)
	if err != nil {
		t.Fatalf("parse auth url: %v", err)
	}
	if authURL.Host != "app.kiro.dev" || authURL.Path != "/signin" {
		t.Fatalf("auth url = %q, want kiro signin", started.AuthURL)
	}
	if got := authURL.Query().Get("redirect_uri"); got != "http://localhost:3128" {
		t.Fatalf("redirect_uri = %q, want localhost signin redirect", got)
	}
	state := strings.TrimSpace(authURL.Query().Get("state"))
	if state == "" {
		t.Fatalf("missing state in auth url: %q", started.AuthURL)
	}
	callbackResp, err := http.Get("http://127.0.0.1:3128/oauth/callback?code=auth-code-1&state=" + url.QueryEscape(state) + "&login_option=github")
	if err != nil {
		t.Fatalf("invoke localhost callback: %v", err)
	}
	_ = callbackResp.Body.Close()

	waitForProviderOAuthStatus(t, started.SessionID, providerOAuthSessionCompleted)
	auth := waitForSavedProviderAuth(t, store, "kiro")
	if got := metaStringValue(auth.Metadata, "profile_arn"); got != "arn:aws:kiro:us-east-1:123:profile/dev" {
		t.Fatalf("profile_arn = %q", got)
	}
	if got := metaStringValue(auth.Metadata, "refresh_token"); got != "kiro-refresh" {
		t.Fatalf("refresh_token = %q", got)
	}
	if got := metaStringValue(auth.Metadata, "email"); got != "dev@example.com" {
		t.Fatalf("email = %q", got)
	}
	select {
	case seen := <-redirectURISeen:
		if seen != "http://localhost:3128/oauth/callback?login_option=github" {
			t.Fatalf("redirect_uri = %q", seen)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for token exchange payload")
	}
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

func resetCallbackForwardersForTest(t *testing.T) {
	t.Helper()
	callbackForwardersMu.Lock()
	orig := callbackForwarders
	callbackForwarders = make(map[int]*callbackForwarder)
	callbackForwardersMu.Unlock()
	t.Cleanup(func() {
		callbackForwardersMu.Lock()
		current := callbackForwarders
		callbackForwarders = orig
		callbackForwardersMu.Unlock()
		for port, forwarder := range current {
			stopForwarderInstance(port, forwarder)
		}
	})
}

func requireCallbackPortAvailable(t *testing.T, port int) {
	t.Helper()
	ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		t.Skipf("callback port %d is already in use: %v", port, err)
	}
	if errClose := ln.Close(); errClose != nil {
		t.Fatalf("close callback port probe: %v", errClose)
	}
}

func callbackForwarderIsActive(port int, provider string) bool {
	callbackForwardersMu.Lock()
	defer callbackForwardersMu.Unlock()
	forwarder := callbackForwarders[port]
	return forwarder != nil && forwarder.provider == provider
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

func waitForSavedProviderAuth(t *testing.T, store *memoryAuthStore, provider string) *coreauth.Auth {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		store.mu.Lock()
		for _, item := range store.items {
			if item != nil && item.Provider == provider {
				store.mu.Unlock()
				return item
			}
		}
		store.mu.Unlock()
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("auth for provider %q was not saved", provider)
	return nil
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
