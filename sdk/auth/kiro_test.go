package auth

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	kiroauth "github.com/router-for-me/CLIProxyAPI/v7/internal/auth/kiro"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

func TestBuildKiroAuthRecordAvoidsGenericAccountIDCollisions(t *testing.T) {
	first := BuildKiroAuthRecord(&kiroauth.TokenBundle{
		AccessToken:  "access-1",
		RefreshToken: "refresh-1",
	}, "signin_localhost")
	second := BuildKiroAuthRecord(&kiroauth.TokenBundle{
		AccessToken:  "access-2",
		RefreshToken: "refresh-2",
	}, "signin_localhost")
	repeated := BuildKiroAuthRecord(&kiroauth.TokenBundle{
		AccessToken:  "access-1",
		RefreshToken: "refresh-1",
	}, "signin_localhost")

	if first.ID == "kiro-signin_localhost-account.json" {
		t.Fatalf("generic Kiro auth ID was reused: %q", first.ID)
	}
	if first.ID == second.ID {
		t.Fatalf("Kiro auth IDs collided: %q", first.ID)
	}
	if repeated.ID != first.ID {
		t.Fatalf("same fallback credentials produced unstable ID: got %q want %q", repeated.ID, first.ID)
	}
	if !strings.HasPrefix(first.ID, "kiro-signin_localhost-account-") {
		t.Fatalf("fallback Kiro auth ID = %q", first.ID)
	}

	withEmail := BuildKiroAuthRecord(&kiroauth.TokenBundle{
		Email:        "dev@example.com",
		RefreshToken: "refresh-1",
	}, "signin_localhost")
	if withEmail.ID != "kiro-signin_localhost-dev-example.com.json" {
		t.Fatalf("email-based Kiro auth ID = %q", withEmail.ID)
	}
}

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
