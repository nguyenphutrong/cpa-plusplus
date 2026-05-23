package kiro

import (
	"context"
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
