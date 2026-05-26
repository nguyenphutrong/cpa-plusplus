package copilot

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/util"
	log "github.com/sirupsen/logrus"
)

const (
	clientID          = "Iv1.b507a08c87ecfe98"
	deviceCodeURL     = "https://github.com/login/device/code"
	tokenURL          = "https://github.com/login/oauth/access_token"
	userInfoURL       = "https://api.github.com/user"
	defaultPoll       = 5 * time.Second
	maxPollDuration   = 15 * time.Minute
	defaultUserAgent  = "GitHubCopilotChat/0.35.0"
	defaultEditor     = "vscode/1.107.0"
	defaultPlugin     = "copilot-chat/0.35.0"
	defaultGitHubAPIV = "2025-04-01"
)

type DeviceFlowClient struct {
	httpClient *http.Client
}

func NewDeviceFlowClient(cfg *config.Config) *DeviceFlowClient {
	client := &http.Client{Timeout: 30 * time.Second}
	if cfg != nil {
		client = util.SetProxy(&cfg.SDKConfig, client)
	}
	return &DeviceFlowClient{httpClient: client}
}

func (c *DeviceFlowClient) RequestDeviceCode(ctx context.Context) (*DeviceCodeResponse, error) {
	form := url.Values{}
	form.Set("client_id", clientID)
	form.Set("scope", "user:email")
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, deviceCodeURL, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, newAuthError(ErrDeviceCodeFailed, err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, newAuthError(ErrDeviceCodeFailed, err)
	}
	defer func() {
		if errClose := resp.Body.Close(); errClose != nil {
			log.Errorf("copilot device code: close body error: %v", errClose)
		}
	}()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<10))
		return nil, newAuthError(ErrDeviceCodeFailed, fmt.Errorf("status %d: %s", resp.StatusCode, strings.TrimSpace(string(body))))
	}
	var out DeviceCodeResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, newAuthError(ErrDeviceCodeFailed, err)
	}
	return &out, nil
}

func (c *DeviceFlowClient) PollForToken(ctx context.Context, code *DeviceCodeResponse) (*TokenData, error) {
	if code == nil {
		return nil, newAuthError(ErrTokenExchangeFailed, fmt.Errorf("device code is nil"))
	}
	interval := time.Duration(code.Interval) * time.Second
	if interval < defaultPoll {
		interval = defaultPoll
	}
	deadline := time.Now().Add(maxPollDuration)
	if code.ExpiresIn > 0 {
		codeDeadline := time.Now().Add(time.Duration(code.ExpiresIn) * time.Second)
		if codeDeadline.Before(deadline) {
			deadline = codeDeadline
		}
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil, newAuthError(ErrPollingTimeout, ctx.Err())
		case <-ticker.C:
			if time.Now().After(deadline) {
				return nil, newAuthError(ErrPollingTimeout, fmt.Errorf("authorization timed out"))
			}
			token, err := c.exchangeDeviceCode(ctx, code.DeviceCode)
			if err == nil {
				return token, nil
			}
			var authErr *AuthenticationError
			if errors.As(err, &authErr) {
				switch authErr.Type {
				case ErrAuthorizationPending:
					continue
				case ErrSlowDown:
					interval += 5 * time.Second
					ticker.Reset(interval)
					continue
				}
			}
			return nil, err
		}
	}
}

func (c *DeviceFlowClient) exchangeDeviceCode(ctx context.Context, deviceCode string) (*TokenData, error) {
	form := url.Values{}
	form.Set("client_id", clientID)
	form.Set("device_code", deviceCode)
	form.Set("grant_type", "urn:ietf:params:oauth:grant-type:device_code")
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, tokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, newAuthError(ErrTokenExchangeFailed, err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, newAuthError(ErrTokenExchangeFailed, err)
	}
	defer func() {
		if errClose := resp.Body.Close(); errClose != nil {
			log.Errorf("copilot token exchange: close body error: %v", errClose)
		}
	}()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if err != nil {
		return nil, newAuthError(ErrTokenExchangeFailed, err)
	}
	var parsed struct {
		Error            string `json:"error"`
		ErrorDescription string `json:"error_description"`
		AccessToken      string `json:"access_token"`
		TokenType        string `json:"token_type"`
		Scope            string `json:"scope"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, newAuthError(ErrTokenExchangeFailed, err)
	}
	if parsed.Error != "" {
		return nil, newAuthError(parsed.Error, fmt.Errorf("%s", parsed.ErrorDescription))
	}
	if parsed.AccessToken == "" {
		return nil, newAuthError(ErrTokenExchangeFailed, fmt.Errorf("empty access token"))
	}
	return &TokenData{AccessToken: parsed.AccessToken, TokenType: parsed.TokenType, Scope: parsed.Scope}, nil
}

func (c *DeviceFlowClient) FetchUserInfo(ctx context.Context, accessToken string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, userInfoURL, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(accessToken))
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", err
	}
	defer func() {
		if errClose := resp.Body.Close(); errClose != nil {
			log.Errorf("copilot userinfo: close body error: %v", errClose)
		}
	}()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<10))
		return "", fmt.Errorf("status %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	var out struct {
		Login string `json:"login"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", err
	}
	return strings.TrimSpace(out.Login), nil
}
