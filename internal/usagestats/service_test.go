package usagestats

import (
	"context"
	"path/filepath"
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
