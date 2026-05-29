package usagestats

import (
	"context"
	"net/http"
	"testing"
	"time"

	internallogging "github.com/router-for-me/CLIProxyAPI/v7/internal/logging"
	coreusage "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/usage"
)

func TestEventFromUsageRecordMapsStableFields(t *testing.T) {
	ctx := internallogging.WithRequestID(context.Background(), "req-1")
	ctx = internallogging.WithEndpoint(ctx, "POST /v1/chat/completions")
	ctx = internallogging.WithResponseStatusHolder(ctx)
	internallogging.SetResponseStatus(ctx, http.StatusOK)

	event := EventFromUsageRecord(ctx, coreusage.Record{
		Provider:    "codex",
		Model:       "gpt-5.4",
		Alias:       "client-model",
		APIKey:      "client-secret",
		AuthIndex:   "auth-1",
		AuthType:    "oauth",
		Source:      "person@example.com",
		RequestedAt: time.Date(2026, 5, 30, 1, 2, 3, 0, time.UTC),
		Latency:     1500 * time.Millisecond,
		Detail: coreusage.Detail{
			InputTokens:         100,
			OutputTokens:        40,
			ReasoningTokens:     5,
			CachedTokens:        10,
			CacheReadTokens:     12,
			CacheCreationTokens: 4,
		},
	})

	if event.RequestID != "req-1" {
		t.Fatalf("RequestID = %q", event.RequestID)
	}
	if event.Provider != "codex" || event.Channel != "codex" {
		t.Fatalf("provider/channel = %q/%q", event.Provider, event.Channel)
	}
	if event.Model != "client-model" || event.RequestedModel != "client-model" || event.ResolvedModel != "gpt-5.4" {
		t.Fatalf("models = %q/%q/%q", event.Model, event.RequestedModel, event.ResolvedModel)
	}
	if event.Method != "POST" || event.Path != "/v1/chat/completions" {
		t.Fatalf("method/path = %q/%q", event.Method, event.Path)
	}
	if event.Account != "per***@example.com" {
		t.Fatalf("account = %q", event.Account)
	}
	if event.AccountHash == "" || event.APIKeyHash == "" {
		t.Fatalf("hashes should be populated")
	}
	if event.StatusCode != http.StatusOK || event.Failed {
		t.Fatalf("status/failed = %d/%v", event.StatusCode, event.Failed)
	}
	if event.CachedTokens != 12 || event.CacheTokens != 4 {
		t.Fatalf("cache tokens = %d/%d", event.CachedTokens, event.CacheTokens)
	}
	if event.TotalTokens != 157 {
		t.Fatalf("TotalTokens = %d", event.TotalTokens)
	}
	if event.LatencyMS == nil || *event.LatencyMS != 1500 {
		t.Fatalf("LatencyMS = %#v", event.LatencyMS)
	}
	if event.EventHash == "" {
		t.Fatalf("EventHash should be populated")
	}
}

func TestEventFromUsageRecordUsesFailureStatus(t *testing.T) {
	event := EventFromUsageRecord(context.Background(), coreusage.Record{
		Model:  "gpt-5.4",
		Failed: true,
		Fail: coreusage.Failure{
			StatusCode: http.StatusBadGateway,
		},
	})

	if event.StatusCode != http.StatusBadGateway || !event.Failed {
		t.Fatalf("status/failed = %d/%v", event.StatusCode, event.Failed)
	}
}
