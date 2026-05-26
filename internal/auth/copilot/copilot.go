package copilot

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/util"
	log "github.com/sirupsen/logrus"
)

const (
	APITokenURL = "https://api.github.com/copilot_internal/v2/token"
	BaseURL     = "https://api.githubcopilot.com"
)

type Auth struct {
	httpClient   *http.Client
	deviceClient *DeviceFlowClient
}

func NewAuth(cfg *config.Config, httpClient *http.Client) *Auth {
	if cfg == nil {
		cfg = &config.Config{}
	}
	if httpClient == nil {
		httpClient = util.SetProxy(&cfg.SDKConfig, &http.Client{Timeout: 30 * time.Second})
	}
	return &Auth{
		httpClient:   httpClient,
		deviceClient: NewDeviceFlowClient(cfg),
	}
}

func (a *Auth) StartDeviceFlow(ctx context.Context) (*DeviceCodeResponse, error) {
	return a.deviceClient.RequestDeviceCode(ctx)
}

func (a *Auth) WaitForAuthorization(ctx context.Context, code *DeviceCodeResponse) (*AuthBundle, error) {
	token, err := a.deviceClient.PollForToken(ctx, code)
	if err != nil {
		return nil, err
	}
	username, err := a.deviceClient.FetchUserInfo(ctx, token.AccessToken)
	if err != nil {
		log.Warnf("copilot: failed to fetch user info: %v", err)
		username = "unknown"
	}
	return &AuthBundle{TokenData: token, Username: username}, nil
}

func (a *Auth) GetAPIToken(ctx context.Context, githubAccessToken string) (*APIToken, error) {
	githubAccessToken = strings.TrimSpace(githubAccessToken)
	if githubAccessToken == "" {
		return nil, newAuthError(ErrTokenExchangeFailed, fmt.Errorf("github access token is empty"))
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, APITokenURL, nil)
	if err != nil {
		return nil, newAuthError(ErrTokenExchangeFailed, err)
	}
	req.Header.Set("Authorization", "token "+githubAccessToken)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", defaultUserAgent)
	req.Header.Set("Editor-Version", defaultEditor)
	req.Header.Set("Editor-Plugin-Version", defaultPlugin)
	resp, err := a.httpClient.Do(req)
	if err != nil {
		return nil, newAuthError(ErrTokenExchangeFailed, err)
	}
	defer func() {
		if errClose := resp.Body.Close(); errClose != nil {
			log.Errorf("copilot api token: close body error: %v", errClose)
		}
	}()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 256<<10))
	if err != nil {
		return nil, newAuthError(ErrTokenExchangeFailed, err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, newAuthError(ErrTokenExchangeFailed, fmt.Errorf("status %d: %s", resp.StatusCode, strings.TrimSpace(string(body))))
	}
	var token APIToken
	if err := json.Unmarshal(body, &token); err != nil {
		return nil, newAuthError(ErrTokenExchangeFailed, err)
	}
	if token.Token == "" {
		return nil, newAuthError(ErrTokenExchangeFailed, fmt.Errorf("empty copilot api token"))
	}
	return &token, nil
}

func (a *Auth) CreateTokenStorage(bundle *AuthBundle) *TokenStorage {
	if bundle == nil || bundle.TokenData == nil {
		return &TokenStorage{Type: "github-copilot"}
	}
	return &TokenStorage{
		AccessToken: bundle.TokenData.AccessToken,
		TokenType:   bundle.TokenData.TokenType,
		Scope:       bundle.TokenData.Scope,
		Username:    bundle.Username,
		Type:        "github-copilot",
	}
}
