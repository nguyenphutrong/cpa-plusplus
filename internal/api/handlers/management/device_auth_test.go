package management

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
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

func resetOAuthSessionsForTest(t *testing.T) {
	t.Helper()
	orig := oauthSessions
	oauthSessions = newOAuthSessionStore(oauthSessionTTL)
	t.Cleanup(func() { oauthSessions = orig })
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
