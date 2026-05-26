package auth

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/auth/kiro"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/browser"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/util"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	log "github.com/sirupsen/logrus"
)

type KiroAuthenticator struct{}

func NewKiroAuthenticator() Authenticator { return &KiroAuthenticator{} }

func (KiroAuthenticator) Provider() string { return "kiro" }

func (KiroAuthenticator) RefreshLead() *time.Duration {
	d := 20 * time.Minute
	return &d
}

func (KiroAuthenticator) Login(ctx context.Context, cfg *config.Config, opts *LoginOptions) (*coreauth.Auth, error) {
	if cfg == nil {
		return nil, fmt.Errorf("kiro auth: configuration is required")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if opts == nil {
		opts = &LoginOptions{}
	}
	if opts.Metadata == nil {
		opts.Metadata = map[string]string{}
	}
	if importPath := strings.TrimSpace(opts.Metadata["import"]); importPath != "" {
		return importKiroToken(ctx, importPath)
	}
	return loginKiroDeviceFlow(ctx, cfg, opts)
}

func loginKiroDeviceFlow(ctx context.Context, cfg *config.Config, opts *LoginOptions) (*coreauth.Auth, error) {
	region := strings.TrimSpace(opts.Metadata["region"])
	if region == "" {
		region = kiro.DefaultRegion
	}
	httpClient := util.SetProxy(&cfg.SDKConfig, &http.Client{Timeout: 30 * time.Second})
	service := kiro.NewService(httpClient)

	reg, err := service.RegisterClient(ctx, region)
	if err != nil {
		return nil, fmt.Errorf("kiro register client: %w", err)
	}
	device, err := service.StartDeviceAuthorization(ctx, region, reg.ClientID, reg.ClientSecret)
	if err != nil {
		return nil, fmt.Errorf("kiro start device authorization: %w", err)
	}

	authURL := device.VerificationURIComplete
	if strings.TrimSpace(authURL) == "" {
		authURL = device.VerificationURI
	}
	fmt.Printf("Kiro device code: %s\n", device.UserCode)
	if !opts.NoBrowser && browser.IsAvailable() {
		if errOpen := browser.OpenURL(authURL); errOpen != nil {
			log.Warnf("kiro: failed to open browser automatically: %v", errOpen)
			fmt.Printf("Open this URL to continue authentication:\n%s\n", authURL)
		}
	} else {
		fmt.Printf("Open this URL to continue authentication:\n%s\n", authURL)
	}

	interval := time.Duration(device.Interval) * time.Second
	if interval <= 0 {
		interval = 5 * time.Second
	}
	deadline := time.Now().Add(time.Duration(device.ExpiresIn) * time.Second)
	if device.ExpiresIn <= 0 {
		deadline = time.Now().Add(10 * time.Minute)
	}
	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(interval):
		}
		result := service.PollDeviceToken(ctx, region, reg.ClientID, reg.ClientSecret, device.DeviceCode)
		if result.Pending {
			continue
		}
		if result.Err != nil {
			return nil, result.Err
		}
		if result.Bundle != nil {
			return authRecordFromKiroBundle(result.Bundle, "aws-device"), nil
		}
	}
	return nil, fmt.Errorf("kiro device authorization timed out")
}

func importKiroToken(ctx context.Context, path string) (*coreauth.Auth, error) {
	raw, err := os.ReadFile(filepath.Clean(path))
	if err != nil {
		return nil, fmt.Errorf("kiro import: read token file: %w", err)
	}
	var values map[string]any
	if err := json.Unmarshal(raw, &values); err != nil {
		return nil, fmt.Errorf("kiro import: parse token file: %w", err)
	}
	bundle := &kiro.TokenBundle{
		AccessToken:  mapString(values, "access_token", "accessToken"),
		RefreshToken: mapString(values, "refresh_token", "refreshToken"),
		ProfileARN:   mapString(values, "profile_arn", "profileArn"),
		ClientID:     mapString(values, "client_id", "clientId"),
		ClientSecret: mapString(values, "client_secret", "clientSecret"),
		Region:       mapString(values, "region"),
		Email:        mapString(values, "email"),
		Username:     mapString(values, "username"),
		Subject:      mapString(values, "subject", "sub"),
	}
	if expires := mapString(values, "expires_at", "expiresAt", "expiry"); expires != "" {
		if t, errParse := time.Parse(time.RFC3339, expires); errParse == nil {
			bundle.ExpiresAt = t
		}
	}
	if bundle.ExpiresAt.IsZero() {
		bundle.ExpiresAt = time.Now().Add(time.Hour)
	}
	if bundle.AccessToken == "" || bundle.RefreshToken == "" {
		return nil, fmt.Errorf("kiro import: token file must contain access_token and refresh_token")
	}
	return authRecordFromKiroBundle(bundle, "import"), nil
}

func authRecordFromKiroBundle(bundle *kiro.TokenBundle, source string) *coreauth.Auth {
	if bundle.Region == "" {
		bundle.Region = kiro.DefaultRegion
	}
	label := "kiro-" + source
	idPart := sanitizeKiroIdentifier(firstKiroNonEmpty(bundle.Email, bundle.Username, bundle.Subject, bundle.ProfileARN, bundle.ClientID, "account"))
	fileName := fmt.Sprintf("%s-%s.json", label, idPart)
	now := time.Now().UTC()
	expiresAt := bundle.ExpiresAt
	if expiresAt.IsZero() {
		expiresAt = now.Add(time.Hour)
	}
	email := firstKiroNonEmpty(bundle.Email, bundle.Username)
	return &coreauth.Auth{
		ID:        fileName,
		Provider:  "kiro",
		FileName:  fileName,
		Label:     label,
		Status:    coreauth.StatusActive,
		CreatedAt: now,
		UpdatedAt: now,
		Metadata: map[string]any{
			"type":          "kiro",
			"access_token":  bundle.AccessToken,
			"refresh_token": bundle.RefreshToken,
			"profile_arn":   bundle.ProfileARN,
			"client_id":     bundle.ClientID,
			"client_secret": bundle.ClientSecret,
			"region":        bundle.Region,
			"expires_at":    expiresAt.Format(time.RFC3339),
			"email":         email,
			"username":      bundle.Username,
			"subject":       bundle.Subject,
			"auth_method":   source,
		},
		Attributes: map[string]string{
			"profile_arn": bundle.ProfileARN,
			"region":      bundle.Region,
			"source":      source,
			"email":       email,
		},
		NextRefreshAfter: expiresAt.Add(-20 * time.Minute),
	}
}

func RefreshKiroToken(ctx context.Context, cfg *config.Config, auth *coreauth.Auth) (*coreauth.Auth, error) {
	if auth == nil {
		return nil, fmt.Errorf("kiro refresh: missing auth")
	}
	if cfg == nil {
		cfg = &config.Config{}
	}
	refreshToken := metaString(auth.Metadata, "refresh_token")
	if refreshToken == "" {
		return auth, nil
	}
	clientID := metaString(auth.Metadata, "client_id")
	clientSecret := metaString(auth.Metadata, "client_secret")
	region := firstKiroNonEmpty(metaString(auth.Metadata, "region"), auth.Attributes["region"], kiro.DefaultRegion)
	service := kiro.NewService(util.SetProxy(&cfg.SDKConfig, &http.Client{Timeout: 30 * time.Second}))
	bundle, err := service.RefreshTokens(ctx, refreshToken, clientID, clientSecret, region)
	if err != nil {
		return nil, err
	}
	if bundle == nil {
		return auth, nil
	}
	if auth.Metadata == nil {
		auth.Metadata = map[string]any{}
	}
	auth.Metadata["access_token"] = bundle.AccessToken
	auth.Metadata["refresh_token"] = firstKiroNonEmpty(bundle.RefreshToken, refreshToken)
	auth.Metadata["expires_at"] = bundle.ExpiresAt.Format(time.RFC3339)
	if bundle.Email != "" {
		auth.Metadata["email"] = bundle.Email
	}
	auth.UpdatedAt = time.Now().UTC()
	auth.NextRefreshAfter = bundle.ExpiresAt.Add(-20 * time.Minute)
	return auth, nil
}

func sanitizeKiroIdentifier(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		value = "account"
	}
	value = regexp.MustCompile(`[^a-zA-Z0-9._-]+`).ReplaceAllString(value, "-")
	value = strings.Trim(value, ".-_")
	if value == "" {
		return "account"
	}
	return value
}

func firstKiroNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func mapString(values map[string]any, keys ...string) string {
	for _, key := range keys {
		if value, ok := values[key]; ok {
			if s, okString := value.(string); okString && strings.TrimSpace(s) != "" {
				return strings.TrimSpace(s)
			}
		}
	}
	return ""
}

func metaString(meta map[string]any, key string) string {
	if meta == nil {
		return ""
	}
	if value, ok := meta[key].(string); ok {
		return strings.TrimSpace(value)
	}
	return ""
}
