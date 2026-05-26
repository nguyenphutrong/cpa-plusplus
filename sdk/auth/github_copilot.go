package auth

import (
	"context"
	"fmt"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/auth/copilot"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/browser"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	log "github.com/sirupsen/logrus"
)

type GitHubCopilotAuthenticator struct{}

func NewGitHubCopilotAuthenticator() Authenticator {
	return &GitHubCopilotAuthenticator{}
}

func (GitHubCopilotAuthenticator) Provider() string {
	return "github-copilot"
}

func (GitHubCopilotAuthenticator) RefreshLead() *time.Duration {
	return nil
}

func (a GitHubCopilotAuthenticator) Login(ctx context.Context, cfg *config.Config, opts *LoginOptions) (*coreauth.Auth, error) {
	if cfg == nil {
		return nil, fmt.Errorf("cliproxy auth: configuration is required")
	}
	if opts == nil {
		opts = &LoginOptions{}
	}
	authSvc := copilot.NewAuth(cfg, nil)
	fmt.Println("Starting GitHub Copilot authentication...")
	deviceCode, err := authSvc.StartDeviceFlow(ctx)
	if err != nil {
		return nil, fmt.Errorf("github-copilot: failed to start device flow: %w", err)
	}
	fmt.Printf("\nTo authenticate, visit: %s\n", deviceCode.VerificationURI)
	fmt.Printf("Enter code: %s\n\n", deviceCode.UserCode)
	if !opts.NoBrowser && browser.IsAvailable() {
		if errOpen := browser.OpenURL(deviceCode.VerificationURI); errOpen != nil {
			log.Warnf("failed to open browser automatically: %v", errOpen)
		}
	}
	fmt.Println("Waiting for GitHub authorization...")
	if deviceCode.ExpiresIn > 0 {
		fmt.Printf("(This will timeout in %d seconds if not authorized)\n", deviceCode.ExpiresIn)
	}
	bundle, err := authSvc.WaitForAuthorization(ctx, deviceCode)
	if err != nil {
		return nil, fmt.Errorf("github-copilot: %s", copilot.UserFriendlyMessage(err))
	}
	fmt.Println("Verifying Copilot access...")
	apiToken, err := authSvc.GetAPIToken(ctx, bundle.TokenData.AccessToken)
	if err != nil {
		return nil, fmt.Errorf("github-copilot: failed to verify Copilot access: %w", err)
	}
	storage := authSvc.CreateTokenStorage(bundle)
	now := time.Now()
	metadata := map[string]any{
		"type":         "github-copilot",
		"username":     bundle.Username,
		"access_token": bundle.TokenData.AccessToken,
		"token_type":   bundle.TokenData.TokenType,
		"scope":        bundle.TokenData.Scope,
		"timestamp":    now.UnixMilli(),
	}
	if apiToken.ExpiresAt > 0 {
		metadata["api_token_expires_at"] = apiToken.ExpiresAt
	}
	fileName := fmt.Sprintf("github-copilot-%s.json", bundle.Username)
	fmt.Printf("\nGitHub Copilot authentication successful for user: %s\n", bundle.Username)
	return &coreauth.Auth{
		ID:       fileName,
		Provider: a.Provider(),
		FileName: fileName,
		Label:    bundle.Username,
		Storage:  storage,
		Metadata: metadata,
	}, nil
}

func RefreshGitHubCopilotToken(ctx context.Context, cfg *config.Config, storage *copilot.TokenStorage) error {
	if storage == nil || storage.AccessToken == "" {
		return fmt.Errorf("no token available")
	}
	_, err := copilot.NewAuth(cfg, nil).GetAPIToken(ctx, storage.AccessToken)
	if err != nil {
		return fmt.Errorf("token validation failed: %w", err)
	}
	return nil
}
