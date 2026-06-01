package quota

import (
	"context"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/quota/providers"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/quota/storage"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

func TestSyncCredentialPersistsQuotaDataInAuthMetadata(t *testing.T) {
	manager := coreauth.NewManager(nil, nil, nil)
	auth := &coreauth.Auth{
		ID:         "codex-auth",
		Provider:   "codex",
		Attributes: map[string]string{"api_key": "token"},
		Metadata:   map[string]any{},
	}
	if _, err := manager.Register(context.Background(), auth); err != nil {
		t.Fatalf("register auth: %v", err)
	}

	service := NewSyncService(manager, nil)
	service.SetFetchOverride(map[string]providers.QuotaFetchFunc{
		"codex": func(_ context.Context, input providers.QuotaFetchInput) (storage.QuotaData, error) {
			if input.Secret != "token" {
				t.Fatalf("secret = %q, want token", input.Secret)
			}
			return storage.QuotaData{
				UpdatedAt: time.Date(2026, 5, 23, 10, 0, 0, 0, time.UTC),
				ProviderData: &storage.ProviderQuotaData{
					PlanType: "plus",
				},
			}, nil
		},
	})

	if _, err := service.SyncCredential(context.Background(), auth); err != nil {
		t.Fatalf("sync credential: %v", err)
	}

	updated, ok := manager.GetByID("codex-auth")
	if !ok {
		t.Fatal("updated auth not found")
	}
	cached := CachedQuotaData(updated)
	if cached.ProviderData == nil || cached.ProviderData.PlanType != "plus" {
		t.Fatalf("cached quota = %#v", cached)
	}
}

func TestSyncProviderSyncsOnlyMatchingEnabledProvider(t *testing.T) {
	manager := coreauth.NewManager(nil, nil, nil)
	auths := []*coreauth.Auth{
		{
			ID:         "codex-auth",
			Provider:   "codex",
			Attributes: map[string]string{"api_key": "codex-token"},
			Metadata:   map[string]any{},
		},
		{
			ID:         "codex-disabled",
			Provider:   "codex",
			Disabled:   true,
			Attributes: map[string]string{"api_key": "disabled-token"},
			Metadata:   map[string]any{},
		},
		{
			ID:         "kiro-auth",
			Provider:   "kiro",
			Attributes: map[string]string{"api_key": "kiro-token"},
			Metadata:   map[string]any{},
		},
	}
	for _, auth := range auths {
		if _, err := manager.Register(context.Background(), auth); err != nil {
			t.Fatalf("register auth %s: %v", auth.ID, err)
		}
	}

	var codexFetches int
	var kiroFetches int
	service := NewSyncService(manager, nil)
	service.SetFetchOverride(map[string]providers.QuotaFetchFunc{
		"codex": func(_ context.Context, input providers.QuotaFetchInput) (storage.QuotaData, error) {
			codexFetches++
			if input.CredentialID != "codex-auth" {
				t.Fatalf("credential id = %q, want codex-auth", input.CredentialID)
			}
			return storage.QuotaData{
				ProviderData: &storage.ProviderQuotaData{
					PlanType: "plus",
				},
			}, nil
		},
		"kiro": func(context.Context, providers.QuotaFetchInput) (storage.QuotaData, error) {
			kiroFetches++
			return storage.QuotaData{}, nil
		},
	})

	if _, err := service.SyncProvider(context.Background(), "codex"); err != nil {
		t.Fatalf("sync provider: %v", err)
	}
	if codexFetches != 1 {
		t.Fatalf("codex fetches = %d, want 1", codexFetches)
	}
	if kiroFetches != 0 {
		t.Fatalf("kiro fetches = %d, want 0", kiroFetches)
	}

	updated, ok := manager.GetByID("codex-auth")
	if !ok {
		t.Fatal("updated codex auth not found")
	}
	cached := CachedQuotaData(updated)
	if cached.ProviderData == nil || cached.ProviderData.PlanType != "plus" {
		t.Fatalf("cached quota = %#v", cached)
	}

	disabled, _ := manager.GetByID("codex-disabled")
	if cached := CachedQuotaData(disabled); cached.ProviderData != nil {
		t.Fatalf("disabled cached quota = %#v, want none", cached)
	}
	kiro, _ := manager.GetByID("kiro-auth")
	if cached := CachedQuotaData(kiro); cached.ProviderData != nil {
		t.Fatalf("kiro cached quota = %#v, want none", cached)
	}
}

func TestSyncCredentialPersistsProviderAccountLabelAsEmail(t *testing.T) {
	manager := coreauth.NewManager(nil, nil, nil)
	auth := &coreauth.Auth{
		ID:       "kiro-auth",
		Provider: "kiro",
		Metadata: map[string]any{
			"access_token": "token",
		},
	}
	if _, err := manager.Register(context.Background(), auth); err != nil {
		t.Fatalf("register auth: %v", err)
	}

	service := NewSyncService(manager, nil)
	service.SetFetchOverride(map[string]providers.QuotaFetchFunc{
		"kiro": func(_ context.Context, input providers.QuotaFetchInput) (storage.QuotaData, error) {
			if input.Secret != "token" {
				t.Fatalf("secret = %q, want token", input.Secret)
			}
			return storage.QuotaData{
				ProviderData: &storage.ProviderQuotaData{
					AccountLabel: "dev@example.com",
				},
			}, nil
		},
	})

	if _, err := service.SyncCredential(context.Background(), auth); err != nil {
		t.Fatalf("sync credential: %v", err)
	}

	updated, ok := manager.GetByID("kiro-auth")
	if !ok {
		t.Fatal("updated auth not found")
	}
	if got := stringMetadata(updated.Metadata, "email"); got != "dev@example.com" {
		t.Fatalf("metadata email = %q, want dev@example.com", got)
	}
	if got := updated.Attributes["email"]; got != "dev@example.com" {
		t.Fatalf("attribute email = %q, want dev@example.com", got)
	}
}

func TestSyncCredentialPersistsProviderAccountLabelAsUsername(t *testing.T) {
	manager := coreauth.NewManager(nil, nil, nil)
	auth := &coreauth.Auth{
		ID:       "copilot-auth",
		Provider: "github-copilot",
		Metadata: map[string]any{
			"access_token": "token",
		},
	}
	if _, err := manager.Register(context.Background(), auth); err != nil {
		t.Fatalf("register auth: %v", err)
	}

	service := NewSyncService(manager, nil)
	service.SetFetchOverride(map[string]providers.QuotaFetchFunc{
		"github-copilot": func(_ context.Context, input providers.QuotaFetchInput) (storage.QuotaData, error) {
			if input.Secret != "token" {
				t.Fatalf("secret = %q, want token", input.Secret)
			}
			return storage.QuotaData{
				ProviderData: &storage.ProviderQuotaData{
					AccountLabel: "octo",
				},
			}, nil
		},
	})

	if _, err := service.SyncCredential(context.Background(), auth); err != nil {
		t.Fatalf("sync credential: %v", err)
	}

	updated, ok := manager.GetByID("copilot-auth")
	if !ok {
		t.Fatal("updated auth not found")
	}
	if got := stringMetadata(updated.Metadata, "username"); got != "octo" {
		t.Fatalf("metadata username = %q, want octo", got)
	}
	if got := updated.Attributes["account"]; got != "octo" {
		t.Fatalf("attribute account = %q, want octo", got)
	}
}

func TestSyncCredentialCachesFetchError(t *testing.T) {
	manager := coreauth.NewManager(nil, nil, nil)
	auth := &coreauth.Auth{
		ID:         "codex-auth",
		Provider:   "codex",
		Attributes: map[string]string{"api_key": "token"},
		Metadata:   map[string]any{},
	}
	if _, err := manager.Register(context.Background(), auth); err != nil {
		t.Fatalf("register auth: %v", err)
	}

	service := NewSyncService(manager, nil)
	service.SetFetchOverride(map[string]providers.QuotaFetchFunc{
		"codex": func(context.Context, providers.QuotaFetchInput) (storage.QuotaData, error) {
			return storage.QuotaData{}, providers.ErrRateLimited
		},
	})

	if _, err := service.SyncCredential(context.Background(), auth); err == nil {
		t.Fatal("expected sync error")
	}

	updated, _ := manager.GetByID("codex-auth")
	cached := CachedQuotaData(updated)
	if cached.Error != providers.ErrRateLimited.Error() {
		t.Fatalf("cached error = %q, want %q", cached.Error, providers.ErrRateLimited.Error())
	}
}
