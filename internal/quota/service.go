package quota

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/quota/providerid"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/quota/providers"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/quota/storage"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

const MetadataKey = "quota_data"

type TokenResolver func(context.Context, *coreauth.Auth) (string, error)

type SyncService struct {
	manager     *coreauth.Manager
	token       TokenResolver
	httpClient  *http.Client
	fetchByName map[string]providers.QuotaFetchFunc
}

func NewSyncService(manager *coreauth.Manager, resolver TokenResolver) *SyncService {
	return &SyncService{
		manager: manager,
		token:   resolver,
	}
}

func (s *SyncService) SetHTTPClient(client *http.Client) {
	s.httpClient = client
}

func (s *SyncService) SetFetchOverride(fetch map[string]providers.QuotaFetchFunc) {
	s.fetchByName = fetch
}

func (s *SyncService) SupportsProvider(provider string) bool {
	return SupportsProvider(provider).Supported
}

func (s *SyncService) Auths() []*coreauth.Auth {
	if s == nil || s.manager == nil {
		return nil
	}
	return s.manager.List()
}

func (s *SyncService) SyncAll(ctx context.Context) (QuotaView, error) {
	var firstErr error
	for _, auth := range s.Auths() {
		if auth == nil || auth.Disabled || auth.Status == coreauth.StatusDisabled {
			continue
		}
		provider := ProviderKey(auth)
		if !s.SupportsProvider(provider) {
			continue
		}
		if _, err := s.SyncCredential(ctx, auth); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return BuildQuotaView(s.Auths(), s.SupportsProvider), firstErr
}

func (s *SyncService) SyncCredential(ctx context.Context, auth *coreauth.Auth) (storage.QuotaData, error) {
	if s == nil || auth == nil {
		return storage.QuotaData{}, fmt.Errorf("quota sync is not configured")
	}
	provider := ProviderKey(auth)
	if capability := SupportsProvider(provider); !capability.Supported {
		return storage.QuotaData{UpdatedAt: time.Now().UTC()}, nil
	}

	fetchFn := s.fetcher(provider)
	if fetchFn == nil {
		return storage.QuotaData{}, fmt.Errorf("quota fetch is not implemented for %s", provider)
	}

	secret, err := s.credentialSecret(ctx, auth)
	if err != nil {
		return s.persistQuotaData(ctx, auth, storage.QuotaData{}, err)
	}
	if strings.TrimSpace(secret) == "" {
		return s.persistQuotaData(ctx, auth, storage.QuotaData{}, fmt.Errorf("missing credential secret for %s", provider))
	}

	data, err := fetchFn(ctx, providers.QuotaFetchInput{
		ProviderID:          provider,
		CredentialID:        auth.ID,
		Secret:              secret,
		BaseURL:             baseURL(auth),
		Headers:             quotaHeaders(auth),
		Attributes:          cloneStringMap(auth.Attributes),
		Metadata:            cloneAnyMap(auth.Metadata),
		Label:               auth.Label,
		ValidationAccountID: authAccountLabel(auth),
		OAuthAccountID:      authAccountLabel(auth),
		OAuthRefreshToken:   stringMetadata(auth.Metadata, "refresh_token"),
		HTTPClient:          s.httpClient,
	})
	return s.persistQuotaData(ctx, auth, data, err)
}

func (s *SyncService) fetcher(provider string) providers.QuotaFetchFunc {
	provider = providerid.Normalize(provider)
	if s.fetchByName != nil {
		if fetch := s.fetchByName[provider]; fetch != nil {
			return fetch
		}
	}
	spec, ok := providers.Get(provider)
	if !ok {
		return nil
	}
	return spec.Quota.Fetch
}

func (s *SyncService) credentialSecret(ctx context.Context, auth *coreauth.Auth) (string, error) {
	if s != nil && s.token != nil {
		token, err := s.token(ctx, auth)
		if err != nil {
			return "", err
		}
		if strings.TrimSpace(token) != "" {
			return token, nil
		}
	}
	if auth == nil {
		return "", nil
	}
	if token := stringMetadata(auth.Metadata, "access_token"); token != "" {
		return token, nil
	}
	if token := nestedStringMetadata(auth.Metadata, "token", "access_token"); token != "" {
		return token, nil
	}
	if auth.Attributes != nil {
		return strings.TrimSpace(auth.Attributes["api_key"]), nil
	}
	return "", nil
}

func (s *SyncService) persistQuotaData(ctx context.Context, auth *coreauth.Auth, data storage.QuotaData, err error) (storage.QuotaData, error) {
	if auth == nil {
		return data, err
	}
	now := time.Now().UTC()
	if err != nil {
		data = storage.QuotaData{Error: err.Error(), UpdatedAt: now}
	} else if data.UpdatedAt.IsZero() {
		data.UpdatedAt = now
	}
	if data.ProviderData != nil && strings.TrimSpace(data.ProviderData.LastUpdated) == "" {
		data.ProviderData.LastUpdated = data.UpdatedAt.UTC().Format(time.RFC3339)
	}
	if auth.Metadata == nil {
		auth.Metadata = map[string]any{}
	}
	auth.Metadata[MetadataKey] = data
	if s != nil && s.manager != nil {
		_, _ = s.manager.Update(ctx, auth)
	}
	return data, err
}

func CachedQuotaData(auth *coreauth.Auth) storage.QuotaData {
	if auth == nil || auth.Metadata == nil {
		return storage.QuotaData{}
	}
	raw, ok := auth.Metadata[MetadataKey]
	if !ok || raw == nil {
		return storage.QuotaData{}
	}
	if data, ok := raw.(storage.QuotaData); ok {
		return data
	}
	bytes, err := json.Marshal(raw)
	if err != nil {
		return storage.QuotaData{}
	}
	var data storage.QuotaData
	if err := json.Unmarshal(bytes, &data); err != nil {
		return storage.QuotaData{}
	}
	return data
}

func ProviderKey(auth *coreauth.Auth) string {
	if auth == nil {
		return ""
	}
	provider := providerid.Normalize(auth.Provider)
	if auth.Attributes != nil {
		for _, key := range []string{"provider_key", "provider", "type"} {
			if value := providerid.Normalize(auth.Attributes[key]); value != "" {
				provider = value
				break
			}
		}
	}
	if provider == "gemini-cli" {
		return "gemini"
	}
	if provider == "claude" {
		return "anthropic"
	}
	return provider
}

func ProviderKeyForName(provider string) string {
	normalized := providerid.Normalize(provider)
	if normalized == "gemini-cli" {
		return "gemini"
	}
	if normalized == "claude" {
		return "anthropic"
	}
	return normalized
}

func baseURL(auth *coreauth.Auth) string {
	if auth == nil || auth.Attributes == nil {
		return ""
	}
	return strings.TrimSpace(auth.Attributes["base_url"])
}

func quotaHeaders(auth *coreauth.Auth) map[string]string {
	headers := map[string]string{}
	if auth == nil {
		return headers
	}
	for key, value := range coreauth.ExtractCustomHeadersFromMetadata(auth.Metadata) {
		headers[key] = value
	}
	return headers
}

func cloneStringMap(input map[string]string) map[string]string {
	if len(input) == 0 {
		return nil
	}
	out := make(map[string]string, len(input))
	for key, value := range input {
		out[key] = value
	}
	return out
}

func cloneAnyMap(input map[string]any) map[string]any {
	if len(input) == 0 {
		return nil
	}
	out := make(map[string]any, len(input))
	for key, value := range input {
		out[key] = value
	}
	return out
}

func stringMetadata(metadata map[string]any, key string) string {
	if metadata == nil {
		return ""
	}
	if value, ok := metadata[key].(string); ok {
		return strings.TrimSpace(value)
	}
	return ""
}

func nestedStringMetadata(metadata map[string]any, parent, key string) string {
	if metadata == nil {
		return ""
	}
	if nested, ok := metadata[parent].(map[string]any); ok {
		if value, ok := nested[key].(string); ok {
			return strings.TrimSpace(value)
		}
	}
	return ""
}
