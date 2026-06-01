package kiro

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestResolveProfileARNForRequest(t *testing.T) {
	t.Parallel()

	arn := "arn:aws:kiro:us-east-1:123456789012:profile/demo"
	if got := ResolveProfileARNForRequest("social", arn); got != "" {
		t.Fatalf("social profileArn must be omitted, got %q", got)
	}
	if got := ResolveProfileARNForRequest("device_code", arn); got != arn {
		t.Fatalf("device_code profileArn mismatch: %q", got)
	}
	if got := ResolveProfileARNForRequest("idc", arn); got != arn {
		t.Fatalf("idc profileArn mismatch: %q", got)
	}
}

func TestListAvailableModelsUsesQEndpoint(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.Method != http.MethodGet {
			t.Fatalf("method = %q", req.Method)
		}
		if req.URL.Path != "/ListAvailableModels" {
			t.Fatalf("path = %q", req.URL.Path)
		}
		if req.URL.Query().Get("origin") != "AI_EDITOR" {
			t.Fatalf("origin = %q", req.URL.Query().Get("origin"))
		}
		if req.URL.Query().Get("profileArn") != "arn:aws:kiro:us-east-1:123:profile/demo" {
			t.Fatalf("profileArn = %q", req.URL.Query().Get("profileArn"))
		}
		_, _ = w.Write([]byte(`{"models":[{"modelId":"kiro/auto","modelName":"Auto","rateMultiplier":1.0,"rateUnit":"Credit"},{"modelId":"kiro/claude-sonnet-4.5","modelName":"Claude Sonnet 4.5","rateMultiplier":1.3,"rateUnit":"Credit"}]}`))
	}))
	defer server.Close()

	transport := &http.Transport{}
	transport.RegisterProtocol("https", roundTripRewriteHost(server.URL))
	client := &http.Client{Transport: transport}
	service := NewService(client)

	models, err := service.ListAvailableModels(
		context.Background(),
		"token",
		"arn:aws:kiro:us-east-1:123:profile/demo",
		"us-east-1",
	)
	if err != nil {
		t.Fatalf("ListAvailableModels: %v", err)
	}
	if len(models) != 2 || !strings.Contains(models[0].ModelID, "kiro/") {
		t.Fatalf("models = %#v", models)
	}
	if models[1].RateMultiplier != 1.3 {
		t.Fatalf("rate multiplier = %v", models[1].RateMultiplier)
	}
	if models[1].RateUnit != "Credit" {
		t.Fatalf("rate unit = %q", models[1].RateUnit)
	}
}

func TestBuildSignInAuthURL(t *testing.T) {
	t.Parallel()

	service := NewService(nil)
	authURL, err := service.BuildSignInAuthURL("challenge-123", "state-abc", "http://localhost:3128")
	if err != nil {
		t.Fatalf("BuildSignInAuthURL: %v", err)
	}
	parsed, err := url.Parse(authURL)
	if err != nil {
		t.Fatalf("parse auth url: %v", err)
	}
	if parsed.Host != "app.kiro.dev" || parsed.Path != "/signin" {
		t.Fatalf("unexpected signin url: %q", authURL)
	}
	if parsed.Query().Get("state") != "state-abc" {
		t.Fatalf("state = %q", parsed.Query().Get("state"))
	}
	if parsed.Query().Get("code_challenge") != "challenge-123" {
		t.Fatalf("code_challenge = %q", parsed.Query().Get("code_challenge"))
	}
	if parsed.Query().Get("code_challenge_method") != "S256" {
		t.Fatalf("code_challenge_method = %q", parsed.Query().Get("code_challenge_method"))
	}
	if parsed.Query().Get("redirect_uri") != "http://localhost:3128" {
		t.Fatalf("redirect_uri = %q", parsed.Query().Get("redirect_uri"))
	}
	if parsed.Query().Get("redirect_from") != "KiroIDE" {
		t.Fatalf("redirect_from = %q", parsed.Query().Get("redirect_from"))
	}
}

func TestPollDeviceTokenUsesAccessTokenClaimsForIdentity(t *testing.T) {
	accessToken := fakeJWT(t, map[string]any{
		"email":              "dev@example.com",
		"preferred_username": "dev-user",
		"sub":                "subject-1",
	})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.URL.Path != "/token" {
			t.Fatalf("path = %q", req.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"accessToken":"` + accessToken + `","refreshToken":"refresh-token","profileArn":"arn:aws:kiro:us-east-1:123:profile/dev","expiresIn":3600}`))
	}))
	defer server.Close()

	origOIDCBaseURL := OIDCBaseURL
	OIDCBaseURL = server.URL
	t.Cleanup(func() { OIDCBaseURL = origOIDCBaseURL })

	result := NewService(server.Client()).PollDeviceToken(context.Background(), "us-east-1", "client-id", "client-secret", "device-code")
	if result.Err != nil {
		t.Fatalf("PollDeviceToken: %v", result.Err)
	}
	if result.Bundle == nil {
		t.Fatal("expected token bundle")
	}
	if result.Bundle.Email != "dev@example.com" {
		t.Fatalf("email = %q", result.Bundle.Email)
	}
	if result.Bundle.Username != "dev-user" {
		t.Fatalf("username = %q", result.Bundle.Username)
	}
	if result.Bundle.Subject != "subject-1" {
		t.Fatalf("subject = %q", result.Bundle.Subject)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func roundTripRewriteHost(target string) roundTripFunc {
	return func(req *http.Request) (*http.Response, error) {
		clone := req.Clone(req.Context())
		clone.URL.Scheme = "http"
		clone.URL.Host = strings.TrimPrefix(target, "http://")
		return http.DefaultTransport.RoundTrip(clone)
	}
}

func fakeJWT(t *testing.T, claims map[string]any) string {
	t.Helper()
	rawClaims, err := json.Marshal(claims)
	if err != nil {
		t.Fatalf("marshal claims: %v", err)
	}
	return "header." + base64.RawURLEncoding.EncodeToString(rawClaims) + ".signature"
}
