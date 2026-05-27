package auth

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	kiroauth "github.com/router-for-me/CLIProxyAPI/v7/internal/auth/kiro"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

func TestRefreshKiroTokenPersistsProfileMetadata(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"accessToken":"new-access","refreshToken":"new-refresh","profileArn":"arn:aws:kiro:us-east-1:123:profile/dev","expiresIn":3600,"email":"dev@example.com","username":"dev","sub":"subject-1"}`))
	}))
	defer server.Close()

	origRefreshURL := kiroauth.RefreshURL
	kiroauth.RefreshURL = server.URL
	t.Cleanup(func() { kiroauth.RefreshURL = origRefreshURL })

	auth := &coreauth.Auth{
		Provider: "kiro",
		Metadata: map[string]any{
			"refresh_token": "old-refresh",
		},
		Attributes: map[string]string{},
	}
	refreshed, err := RefreshKiroToken(context.Background(), &config.Config{}, auth)
	if err != nil {
		t.Fatalf("RefreshKiroToken: %v", err)
	}
	if got := metaString(refreshed.Metadata, "access_token"); got != "new-access" {
		t.Fatalf("access_token = %q", got)
	}
	if got := metaString(refreshed.Metadata, "refresh_token"); got != "new-refresh" {
		t.Fatalf("refresh_token = %q", got)
	}
	if got := metaString(refreshed.Metadata, "profile_arn"); got != "arn:aws:kiro:us-east-1:123:profile/dev" {
		t.Fatalf("profile_arn = %q", got)
	}
	if got := refreshed.Attributes["profile_arn"]; got != "arn:aws:kiro:us-east-1:123:profile/dev" {
		t.Fatalf("attribute profile_arn = %q", got)
	}
	if got := metaString(refreshed.Metadata, "email"); got != "dev@example.com" {
		t.Fatalf("email = %q", got)
	}
	if refreshed.NextRefreshAfter.IsZero() || !refreshed.NextRefreshAfter.After(time.Now()) {
		t.Fatalf("next refresh = %v", refreshed.NextRefreshAfter)
	}
}
