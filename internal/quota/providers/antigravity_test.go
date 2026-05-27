package providers

import (
	"context"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/quota/storage"
)

func TestAntigravityFetchQuota(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1internal:loadCodeAssist":
			_, _ = w.Write([]byte(`{
				"paidTier":{"id":"pro","displayName":"Pro"},
				"cloudaicompanionProject":"project-123"
			}`))
		case "/v1internal:fetchAvailableModels":
			if !strings.Contains(r.Header.Get("Authorization"), "token") {
				t.Fatalf("missing auth header")
			}
			_, _ = w.Write([]byte(`{
				"models":{
					"gemini-2.5-pro":{"quotaInfo":{"remainingFraction":0.8,"resetTime":"2026-05-01T00:00:00Z"}},
					"gemini-2.5-pro-high":{"quotaInfo":{"remainingFraction":0.6,"resetTime":"2026-05-01T01:00:00Z"}},
					"claude-sonnet-4-5":{"quotaInfo":{"remainingFraction":0.4,"resetTime":"2026-05-01T00:00:00Z"}},
					"gpt-5":{"quotaInfo":{"remainingFraction":0.9,"resetTime":"2026-05-01T00:00:00Z"}}
				}
			}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	fetcher := NewAntigravity(server.Client())
	data, err := fetcher.Fetch(context.Background(), QuotaFetchInput{
		ProviderID: "antigravity",
		Secret:     "token",
		BaseURL:    server.URL,
	})
	if err != nil {
		t.Fatalf("fetch quota: %v", err)
	}
	if data.ProviderData == nil {
		t.Fatal("expected provider data")
	}
	if data.ProviderData.PlanType != "pro" {
		t.Fatalf("plan type = %q", data.ProviderData.PlanType)
	}
	if len(data.ProviderData.Models) != 3 {
		t.Fatalf("models len = %d", len(data.ProviderData.Models))
	}
	byName := antigravityModelsByName(data.ProviderData.Models)
	if _, ok := byName["gemini-2.5-pro"]; !ok {
		t.Fatalf("missing gemini-2.5-pro in models: %#v", byName)
	}
	if got := derefFloat(byName["gemini-2.5-pro-high"].RemainingPercent); !approxEqual(got, 60) {
		t.Fatalf("remaining percent = %v, want 60", got)
	}
	if got := data.ProviderData.Models[0].QuotaKind; got != "window" {
		t.Fatalf("quota kind = %q", got)
	}
	if got := data.ProviderData.Models[0].DisplayUnit; got != "percent" {
		t.Fatalf("display unit = %q", got)
	}
}

func TestAntigravityFetchQuotaUsesProjectFromLoadCodeAssist(t *testing.T) {
	t.Parallel()

	projectSeen := ""
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1internal:loadCodeAssist":
			_, _ = w.Write([]byte(`{"cloudaicompanionProject":{"id":"project-map-id"}}`))
		case "/v1internal:fetchAvailableModels":
			body, _ := io.ReadAll(r.Body)
			projectSeen = string(body)
			_, _ = w.Write([]byte(`{"models":{"gemini-2.5-pro":{"quotaInfo":{"remainingFraction":0.5}}}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	fetcher := NewAntigravity(server.Client())
	_, err := fetcher.Fetch(context.Background(), QuotaFetchInput{
		ProviderID: "antigravity",
		Secret:     "token",
		BaseURL:    server.URL,
	})
	if err != nil {
		t.Fatalf("fetch quota: %v", err)
	}
	if !strings.Contains(projectSeen, "project-map-id") {
		t.Fatalf("project payload not sent: %s", projectSeen)
	}
}

func TestAntigravityFetchQuotaForbiddenFromModelsEndpoint(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1internal:loadCodeAssist":
			_, _ = w.Write([]byte(`{"paidTier":{"id":"pro"}}`))
		case "/v1internal:fetchAvailableModels":
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`{"error":"forbidden"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	fetcher := NewAntigravity(server.Client())
	data, err := fetcher.Fetch(context.Background(), QuotaFetchInput{
		ProviderID: "antigravity",
		Secret:     "token",
		BaseURL:    server.URL,
	})
	if err != nil {
		t.Fatalf("fetch quota: %v", err)
	}
	if data.ProviderData == nil || !data.ProviderData.IsForbidden {
		t.Fatalf("expected forbidden data: %#v", data.ProviderData)
	}
}

func TestAntigravityFetchQuotaRateLimited(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1internal:loadCodeAssist":
			_, _ = w.Write([]byte(`{"paidTier":{"id":"pro"}}`))
		case "/v1internal:fetchAvailableModels":
			w.WriteHeader(http.StatusTooManyRequests)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	fetcher := NewAntigravity(server.Client())
	_, err := fetcher.Fetch(context.Background(), QuotaFetchInput{
		ProviderID: "antigravity",
		Secret:     "token",
		BaseURL:    server.URL,
	})
	if err != ErrRateLimited {
		t.Fatalf("err=%v, want ErrRateLimited", err)
	}
}

func TestAntigravityFetchQuotaGroupsModelFamilies(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1internal:loadCodeAssist":
			_, _ = w.Write([]byte(`{"paidTier":{"id":"pro"}}`))
		case "/v1internal:fetchAvailableModels":
			_, _ = w.Write([]byte(`{
				"models":{
					"gemini-3-pro":{"quotaInfo":{"remainingFraction":0.72,"resetTime":"2026-05-01T10:00:00Z"}},
					"gemini-3-pro-high":{"quotaInfo":{"remainingFraction":0.31,"resetTime":"2026-05-01T09:00:00Z"}},
					"gemini-3.1-pro-low":{"quotaInfo":{"remainingFraction":0.22,"resetTime":"2026-05-01T09:30:00Z"}},
					"gemini-3-flash":{"quotaInfo":{"remainingFraction":0.67,"resetTime":"2026-05-01T08:00:00Z"}},
					"gemini-3.1-flash-lite":{"quotaInfo":{"remainingFraction":0.55,"resetTime":"2026-05-01T08:30:00Z"}},
					"gemini-3-pro-image":{"quotaInfo":{"remainingFraction":0.42,"resetTime":"2026-05-01T07:00:00Z"}},
					"gemini-3.1-flash-image":{"quotaInfo":{"remainingFraction":0.21,"resetTime":"2026-05-01T06:00:00Z"}},
					"claude-sonnet-4-5":{"quotaInfo":{"remainingFraction":0.48,"resetTime":"2026-05-01T05:00:00Z"}},
					"claude-sonnet-4-5-thinking":{"quotaInfo":{"remainingFraction":0.29,"resetTime":"2026-05-01T04:00:00Z"}},
					"gemini-default":{"quotaInfo":{"remainingFraction":0.99,"resetTime":"2026-05-01T02:00:00Z"}}
				}
			}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	fetcher := NewAntigravity(server.Client())
	data, err := fetcher.Fetch(context.Background(), QuotaFetchInput{
		ProviderID: "antigravity",
		Secret:     "token",
		BaseURL:    server.URL,
	})
	if err != nil {
		t.Fatalf("fetch quota: %v", err)
	}
	if data.ProviderData == nil {
		t.Fatal("expected provider data")
	}

	models := data.ProviderData.Models
	if len(models) != 9 {
		t.Fatalf("models len = %d, want 9", len(models))
	}

	byName := antigravityModelsByName(models)

	if got := derefFloat(byName["gemini-3.1-pro-low"].RemainingPercent); !approxEqual(got, 22) {
		t.Fatalf("gemini-pro remaining = %v, want 22", got)
	}
	if got := derefFloat(byName["gemini-3.1-flash-image"].RemainingPercent); !approxEqual(got, 21) {
		t.Fatalf("gemini-flash remaining = %v, want 21", got)
	}
	if got := derefFloat(byName["claude-sonnet-4-5-thinking"].RemainingPercent); !approxEqual(got, 29) {
		t.Fatalf("claude remaining = %v, want 29", got)
	}
	if got := byName["gemini-3-pro"].DisplayName; got != "Gemini 3 Pro" {
		t.Fatalf("display = %q, want Gemini 3 Pro", got)
	}
	if got := byName["gemini-3-flash"].DisplayName; got != "Gemini 3 Flash" {
		t.Fatalf("display = %q, want Gemini 3 Flash", got)
	}
	if got := byName["claude-sonnet-4-5"].DisplayName; got != "Claude Sonnet 4 5" {
		t.Fatalf("display = %q, want Claude Sonnet 4 5", got)
	}
	if _, exists := byName["gemini-default"]; exists {
		t.Fatal("gemini-default should be filtered out")
	}
}

func antigravityModelsByName(models []storage.QuotaModel) map[string]storage.QuotaModel {
	byName := make(map[string]storage.QuotaModel, len(models))
	for _, model := range models {
		byName[model.Name] = model
	}
	return byName
}

func derefFloat(v *float64) float64 {
	if v == nil {
		return -1
	}
	return *v
}

func approxEqual(a, b float64) bool {
	return math.Abs(a-b) < 0.001
}
