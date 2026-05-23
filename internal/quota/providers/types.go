package providers

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/quota/storage"
)

type Spec struct {
	ID             string
	OpenAICompat   bool
	DefaultBaseURL string
	Quota          QuotaSpec
	Runtime        RuntimeSpec
}

type QuotaSpec struct {
	Supported         bool
	UnsupportedReason string
	Strategy          string
	Fetch             QuotaFetchFunc
}

type QuotaFetchInput struct {
	ProviderID          string
	CredentialID        string
	Secret              string
	BaseURL             string
	Headers             map[string]string
	Label               string
	ValidationAccountID string
	OAuthAccountID      string
	OAuthRefreshToken   string
	Now                 func() time.Time
	HTTPClient          *http.Client
	Logger              *slog.Logger
}

type QuotaFetchFunc func(ctx context.Context, input QuotaFetchInput) (storage.QuotaData, error)

type RuntimeSpec struct {
	Protocols []string
	OpenAI    OpenAIStrategy
	Anthropic AnthropicStrategy
}

type OpenAIStrategy struct {
	DefaultBaseURL string
	AuthHeader     string
	AuthPrefix     string
	ExtraHeaders   map[string]string
}

type AnthropicStrategy struct {
	DefaultBaseURL string
	AuthHeader     string
	AuthPrefix     string
	ExtraHeaders   map[string]string
}
