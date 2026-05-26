// Package copilot provides GitHub Copilot device-flow authentication helpers.
package copilot

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/misc"
)

type TokenStorage struct {
	AccessToken string         `json:"access_token"`
	TokenType   string         `json:"token_type,omitempty"`
	Scope       string         `json:"scope,omitempty"`
	Username    string         `json:"username,omitempty"`
	Type        string         `json:"type"`
	Metadata    map[string]any `json:"-"`
}

func (s *TokenStorage) SetMetadata(meta map[string]any) {
	s.Metadata = meta
}

func (s *TokenStorage) SaveTokenToFile(path string) error {
	misc.LogSavingCredentials(path)
	s.Type = "github-copilot"
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create copilot auth dir: %w", err)
	}
	data, err := misc.MergeMetadata(s, s.Metadata)
	if err != nil {
		return fmt.Errorf("merge copilot metadata: %w", err)
	}
	raw, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal copilot token: %w", err)
	}
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		return fmt.Errorf("write copilot token: %w", err)
	}
	return nil
}

type TokenData struct {
	AccessToken string `json:"access_token"`
	TokenType   string `json:"token_type"`
	Scope       string `json:"scope"`
}

type AuthBundle struct {
	TokenData *TokenData
	Username  string
}

type DeviceCodeResponse struct {
	DeviceCode      string `json:"device_code"`
	UserCode        string `json:"user_code"`
	VerificationURI string `json:"verification_uri"`
	ExpiresIn       int    `json:"expires_in"`
	Interval        int    `json:"interval"`
}

type APIToken struct {
	Token     string `json:"token"`
	ExpiresAt int64  `json:"expires_at"`
	Endpoints struct {
		API           string `json:"api"`
		Proxy         string `json:"proxy"`
		OriginTracker string `json:"origin-tracker"`
		Telemetry     string `json:"telemetry"`
	} `json:"endpoints,omitempty"`
}

func (t *APIToken) Expired() bool {
	if t == nil || t.ExpiresAt <= 0 {
		return false
	}
	return time.Now().Add(5 * time.Minute).After(time.Unix(t.ExpiresAt, 0))
}
