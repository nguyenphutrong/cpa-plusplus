package quota

import (
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/quota/storage"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

func TestBuildQuotaViewSkipsDisabledAuthsAndMarksUnsupported(t *testing.T) {
	view := BuildQuotaView([]*coreauth.Auth{
		{ID: "enabled", Provider: "xai", Metadata: map[string]any{}},
		{ID: "disabled", Provider: "codex", Disabled: true, Metadata: map[string]any{}},
	}, testSupportsProvider)

	if len(view.Providers) != 1 {
		t.Fatalf("providers len = %d, want 1", len(view.Providers))
	}
	got := view.Providers[0]
	if got.Provider != "xai" {
		t.Fatalf("provider = %q, want xai", got.Provider)
	}
	if got.QuotaStatus != string(CapabilityUnsupported) {
		t.Fatalf("quota status = %q, want unsupported", got.QuotaStatus)
	}
	if got.Accounts[0].Error != "" {
		t.Fatalf("unsupported account error = %q, want empty", got.Accounts[0].Error)
	}
}

func TestBuildQuotaViewUsesCachedQuotaData(t *testing.T) {
	now := time.Date(2026, 5, 23, 10, 0, 0, 0, time.UTC)
	remaining := 75.0
	auth := &coreauth.Auth{
		ID:       "codex-auth",
		Provider: "codex",
		Label:    "Codex User",
		Metadata: map[string]any{
			MetadataKey: storage.QuotaData{
				UpdatedAt: now,
				ProviderData: &storage.ProviderQuotaData{
					AccountLabel:    "user@example.com",
					PlanType:        "plus",
					PlanDisplayName: "Plus",
					Models: []storage.QuotaModel{{
						Name:             "codex-weekly",
						DisplayName:      "Weekly",
						RemainingPercent: &remaining,
					}},
				},
			},
		},
	}

	view := BuildQuotaView([]*coreauth.Auth{auth}, testSupportsProvider)
	if len(view.Providers) != 1 || len(view.Providers[0].Accounts) != 1 {
		t.Fatalf("unexpected view shape: %#v", view)
	}
	account := view.Providers[0].Accounts[0]
	if account.AccountKey != "user@example.com" {
		t.Fatalf("account key = %q, want user@example.com", account.AccountKey)
	}
	if account.PlanDisplayName != "Plus" {
		t.Fatalf("plan = %q, want Plus", account.PlanDisplayName)
	}
	if account.LastUpdated != now.Format(time.RFC3339) {
		t.Fatalf("last updated = %q, want %q", account.LastUpdated, now.Format(time.RFC3339))
	}
	if len(account.Models) != 1 || account.Models[0].RemainingPercent == nil || *account.Models[0].RemainingPercent != remaining {
		t.Fatalf("models = %#v", account.Models)
	}
}

func testSupportsProvider(provider string) bool {
	return SupportsProvider(provider).Supported
}
