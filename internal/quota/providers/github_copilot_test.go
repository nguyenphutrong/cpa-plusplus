package providers

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestParseCopilotModelsResponse(t *testing.T) {
	t.Parallel()

	raw := []byte(`{
		"data":[
			{"id":"claude-haiku-4.5","name":"Claude Haiku 4.5","billing":{"is_premium":true,"multiplier":0.33}},
			{"id":"gpt-5.2","name":"GPT-5.2","premium_multiplier":"1.0"}
		]
	}`)
	models := parseCopilotModelsResponse(raw, "paid")
	if len(models) != 2 {
		t.Fatalf("expected 2 models, got %d", len(models))
	}
	if models[0].ModelID != "claude-haiku-4.5" || models[0].MultiplierValue != "0.33" {
		t.Fatalf("unexpected first model: %#v", models[0])
	}
	if models[1].ModelID != "gpt-5.2" || models[1].MultiplierValue != "1.0" {
		t.Fatalf("unexpected second model: %#v", models[1])
	}
}

func TestGitHubCopilotFetchUsesModelsEndpointWhenAvailable(t *testing.T) {
	t.Parallel()

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/copilot_internal/user":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"access_type_sku":"pro","copilot_plan":"individual","quota_snapshots":{"chat":{"entitlement":50,"remaining":40,"percent_remaining":80}}}`))
		case "/models":
			if r.Method != http.MethodGet {
				t.Fatalf("expected GET /models, got %s", r.Method)
			}
			if got := r.Header.Get("X-GitHub-Api-Version"); got != "2026-01-09" {
				t.Fatalf("unexpected api version header: %q", got)
			}
			body, _ := io.ReadAll(r.Body)
			if !strings.Contains(string(body), "auto_mode") {
				t.Fatalf("missing auto_mode payload: %s", string(body))
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"data":[{"id":"claude-haiku-4.5","name":"Claude Haiku 4.5","multiplier":"0.33"},{"id":"gpt-5.2","name":"GPT-5.2","multiplier":"1.0"}]}`))
		default:
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
	}))
	defer upstream.Close()

	originalEntitlementURL := githubCopilotEntitlementURL
	originalModelsURL := githubCopilotModelsURL
	githubCopilotEntitlementURL = upstream.URL + "/copilot_internal/user"
	githubCopilotModelsURL = upstream.URL + "/models"
	defer func() {
		githubCopilotEntitlementURL = originalEntitlementURL
		githubCopilotModelsURL = originalModelsURL
	}()

	data, err := NewGitHubCopilot(upstream.Client()).Fetch(context.Background(), QuotaFetchInput{
		Secret: "token",
	})
	if err != nil {
		t.Fatalf("Fetch error: %v", err)
	}
	if data.ProviderData == nil {
		t.Fatalf("expected provider data")
	}
	if len(data.ProviderData.CopilotChatModels) != 2 {
		t.Fatalf("expected models from endpoint, got %#v", data.ProviderData.CopilotChatModels)
	}
}
