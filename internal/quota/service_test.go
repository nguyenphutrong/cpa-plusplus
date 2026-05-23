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
