package usagestats

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	coreusage "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/usage"
)

func TestServiceNoopsWhenDisabledAndRecordsWhenEnabled(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "usage.sqlite")
	service := NewService()
	t.Cleanup(func() {
		if err := service.Close(); err != nil {
			t.Fatalf("Close: %v", err)
		}
	})

	service.HandleUsage(ctx, coreusage.Record{
		Provider:    "codex",
		Model:       "gpt-5",
		Alias:       "client-gpt",
		RequestedAt: time.UnixMilli(1000),
		Detail:      coreusage.Detail{InputTokens: 10, OutputTokens: 5},
	})
	if status := service.Status(); status.Enabled || status.Open {
		t.Fatalf("disabled status = %#v", status)
	}

	if err := service.Configure(ctx, true, path); err != nil {
		t.Fatalf("Configure enabled: %v", err)
	}
	service.HandleUsage(ctx, coreusage.Record{
		Provider:    "codex",
		Model:       "gpt-5",
		Alias:       "client-gpt",
		RequestedAt: time.UnixMilli(1000),
		Detail:      coreusage.Detail{InputTokens: 10, OutputTokens: 5},
	})
	if err := service.Close(); err != nil {
		t.Fatalf("Close enabled service: %v", err)
	}

	store, err := OpenSQLiteStore(path)
	if err != nil {
		t.Fatalf("OpenSQLiteStore: %v", err)
	}
	defer store.Close()
	events, err := store.QueryEvents(ctx, SummaryFilter{Model: "gpt-5"}, 10, 0)
	if err != nil {
		t.Fatalf("QueryEvents: %v", err)
	}
	if len(events) != 1 || events[0].PromptTokens != 10 || events[0].CompletionTokens != 5 {
		t.Fatalf("events = %#v", events)
	}
}

func TestServiceConfigureClosesStoreWhenDisabled(t *testing.T) {
	ctx := context.Background()
	service := NewService()
	t.Cleanup(func() {
		_ = service.Close()
	})
	if err := service.Configure(ctx, true, filepath.Join(t.TempDir(), "usage.sqlite")); err != nil {
		t.Fatalf("Configure enabled: %v", err)
	}
	if status := service.Status(); !status.Enabled || !status.Open {
		t.Fatalf("enabled status = %#v", status)
	}
	if err := service.Configure(ctx, false, ""); err != nil {
		t.Fatalf("Configure disabled: %v", err)
	}
	if status := service.Status(); status.Enabled || status.Open || status.Path != "" {
		t.Fatalf("disabled status = %#v", status)
	}
}

func TestServiceAutoSyncsEmptyModelPrices(t *testing.T) {
	ctx := context.Background()
	service := NewService()
	t.Cleanup(func() {
		_ = service.Close()
	})

	source := newModelPriceSource(t, http.StatusOK, `{
		"gpt-test": {
			"input_cost_per_token": 0.000001,
			"output_cost_per_token": 0.000002
		}
	}`)
	if err := service.Configure(ctx, true, filepath.Join(t.TempDir(), "usage.sqlite")); err != nil {
		t.Fatalf("Configure: %v", err)
	}
	service.StartModelPriceAutoSync(ctx, source.Client(), source.URL, time.Hour)

	status := waitForServiceStatus(t, service, func(status ServiceStatus) bool {
		return status.ModelPricesCount == 1 && status.ModelPricesLastSyncedAtMS > 0 && !status.ModelPricesSyncing
	})
	if status.ModelPricesSyncError != "" {
		t.Fatalf("sync error = %q", status.ModelPricesSyncError)
	}
}

func TestServiceAutoSyncSkipsFreshPrices(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "usage.sqlite")
	now := time.Now().UnixMilli()

	store, err := OpenSQLiteStore(path)
	if err != nil {
		t.Fatalf("OpenSQLiteStore: %v", err)
	}
	if _, err := store.UpsertModelPrices(ctx, map[string]ModelPrice{
		"gpt-fresh": {
			Prompt:      1,
			Completion:  2,
			Cache:       1,
			UpdatedAtMS: now,
			SyncedAtMS:  &now,
		},
	}); err != nil {
		t.Fatalf("UpsertModelPrices: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("Close store: %v", err)
	}

	var hits atomic.Int64
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(source.Close)

	service := NewService()
	t.Cleanup(func() {
		_ = service.Close()
	})
	if err := service.Configure(ctx, true, path); err != nil {
		t.Fatalf("Configure: %v", err)
	}
	service.StartModelPriceAutoSync(ctx, source.Client(), source.URL, time.Hour)
	time.Sleep(150 * time.Millisecond)

	if got := hits.Load(); got != 0 {
		t.Fatalf("fresh prices should not sync; hits=%d", got)
	}
	status := service.Status()
	if status.ModelPricesCount != 1 || status.ModelPricesSyncError != "" {
		t.Fatalf("status = %#v", status)
	}
}

func TestServiceAutoSyncFailureDoesNotBreakEvents(t *testing.T) {
	ctx := context.Background()
	service := NewService()
	t.Cleanup(func() {
		_ = service.Close()
	})

	source := newModelPriceSource(t, http.StatusBadGateway, `sync failed`)
	if err := service.Configure(ctx, true, filepath.Join(t.TempDir(), "usage.sqlite")); err != nil {
		t.Fatalf("Configure: %v", err)
	}
	service.StartModelPriceAutoSync(ctx, source.Client(), source.URL, time.Hour)

	status := waitForServiceStatus(t, service, func(status ServiceStatus) bool {
		return status.ModelPricesSyncError != "" && !status.ModelPricesSyncing
	})
	if status.ModelPricesCount != 0 {
		t.Fatalf("status = %#v", status)
	}

	service.HandleUsage(ctx, coreusage.Record{
		Provider:    "codex",
		Model:       "gpt-5",
		RequestedAt: time.UnixMilli(1000),
		Detail:      coreusage.Detail{InputTokens: 10, OutputTokens: 5},
	})
	events, err := service.QueryEvents(ctx, SummaryFilter{}, 10, 0)
	if err != nil {
		t.Fatalf("QueryEvents: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("events = %#v", events)
	}
}

func TestResolveSQLitePath(t *testing.T) {
	if got := ResolveSQLitePath("/tmp/config.yaml", ""); got != "/tmp/usage-statistics.sqlite" {
		t.Fatalf("default path = %q", got)
	}
	if got := ResolveSQLitePath("/tmp/config.yaml", "custom.sqlite"); got != "/tmp/custom.sqlite" {
		t.Fatalf("relative path = %q", got)
	}
	if got := ResolveSQLitePath("/tmp/config.yaml", "/var/db/usage.sqlite"); got != "/var/db/usage.sqlite" {
		t.Fatalf("absolute path = %q", got)
	}
	if got := ResolveSQLitePath("/tmp/config.yaml", "file:usage.sqlite?cache=shared"); got != "file:usage.sqlite?cache=shared" {
		t.Fatalf("sqlite uri = %q", got)
	}
}

func newModelPriceSource(t *testing.T, status int, body string) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(server.Close)
	return server
}

func waitForServiceStatus(t *testing.T, service *Service, done func(ServiceStatus) bool) ServiceStatus {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	var status ServiceStatus
	for time.Now().Before(deadline) {
		status = service.Status()
		if done(status) {
			return status
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for status; last=%#v", status)
	return status
}
