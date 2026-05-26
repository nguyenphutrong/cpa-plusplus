package management

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/auth/copilot"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/auth/kiro"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/misc"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/util"
	sdkAuth "github.com/router-for-me/CLIProxyAPI/v7/sdk/auth"
	log "github.com/sirupsen/logrus"
)

const defaultDeviceAuthTimeout = 10 * time.Minute

type githubCopilotDeviceAuth interface {
	StartDeviceFlow(context.Context) (*copilot.DeviceCodeResponse, error)
	WaitForAuthorization(context.Context, *copilot.DeviceCodeResponse) (*copilot.AuthBundle, error)
	GetAPIToken(context.Context, string) (*copilot.APIToken, error)
}

type kiroDeviceAuth interface {
	RegisterClient(context.Context, string) (*kiro.ClientRegistration, error)
	StartDeviceAuthorization(context.Context, string, string, string) (*kiro.DeviceAuthorization, error)
	PollDeviceToken(context.Context, string, string, string, string) kiro.DevicePollResult
}

var (
	newGitHubCopilotDeviceAuth = func(cfg *config.Config) githubCopilotDeviceAuth {
		return copilot.NewAuth(cfg, nil)
	}
	newKiroDeviceAuth = func(cfg *config.Config) kiroDeviceAuth {
		if cfg == nil {
			cfg = &config.Config{}
		}
		return kiro.NewService(util.SetProxy(&cfg.SDKConfig, &http.Client{Timeout: 30 * time.Second}))
	}
)

func (h *Handler) RequestGitHubCopilotToken(c *gin.Context) {
	ctx := PopulateAuthContext(context.Background(), c)

	state, errState := misc.GenerateRandomState()
	if errState != nil {
		log.Errorf("Failed to generate GitHub Copilot OAuth state: %v", errState)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to generate state parameter"})
		return
	}

	authSvc := newGitHubCopilotDeviceAuth(h.cfg)
	deviceCode, errDevice := authSvc.StartDeviceFlow(ctx)
	if errDevice != nil {
		log.Errorf("Failed to start GitHub Copilot device flow: %v", errDevice)
		c.JSON(http.StatusBadGateway, gin.H{"error": "failed to start github copilot device flow"})
		return
	}

	RegisterOAuthSession(state, "github-copilot")

	go func() {
		bundle, errWait := authSvc.WaitForAuthorization(ctx, deviceCode)
		if errWait != nil {
			log.Errorf("GitHub Copilot authorization failed: %v", errWait)
			SetOAuthSessionError(state, copilot.UserFriendlyMessage(errWait))
			return
		}
		if bundle == nil || bundle.TokenData == nil || strings.TrimSpace(bundle.TokenData.AccessToken) == "" {
			SetOAuthSessionError(state, "GitHub Copilot authorization returned no token")
			return
		}
		apiToken, errToken := authSvc.GetAPIToken(ctx, bundle.TokenData.AccessToken)
		if errToken != nil {
			log.Errorf("GitHub Copilot API token verification failed: %v", errToken)
			SetOAuthSessionError(state, oauthSessionErrorWithCause("Failed to verify Copilot access", errToken))
			return
		}
		record := sdkAuth.BuildGitHubCopilotAuthRecord(bundle, apiToken)
		savedPath, errSave := h.saveTokenRecord(ctx, record)
		if errSave != nil {
			log.Errorf("Failed to save GitHub Copilot token: %v", errSave)
			SetOAuthSessionError(state, "Failed to save authentication tokens")
			return
		}
		log.Infof("GitHub Copilot authentication token saved to %s", savedPath)
		CompleteOAuthSession(state)
		CompleteOAuthSessionsByProvider("github-copilot")
	}()

	c.JSON(http.StatusOK, gin.H{
		"status":           "ok",
		"url":              deviceCode.VerificationURI,
		"state":            state,
		"user_code":        deviceCode.UserCode,
		"verification_uri": deviceCode.VerificationURI,
		"expires_in":       deviceCode.ExpiresIn,
		"interval":         deviceCode.Interval,
	})
}

func (h *Handler) RequestKiroToken(c *gin.Context) {
	ctx := PopulateAuthContext(context.Background(), c)
	region := strings.TrimSpace(c.Query("region"))
	if region == "" {
		region = kiro.DefaultRegion
	}

	state, errState := misc.GenerateRandomState()
	if errState != nil {
		log.Errorf("Failed to generate Kiro OAuth state: %v", errState)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to generate state parameter"})
		return
	}

	service := newKiroDeviceAuth(h.cfg)
	reg, errReg := service.RegisterClient(ctx, region)
	if errReg != nil {
		log.Errorf("Failed to register Kiro client: %v", errReg)
		c.JSON(http.StatusBadGateway, gin.H{"error": "failed to register kiro client"})
		return
	}
	device, errDevice := service.StartDeviceAuthorization(ctx, region, reg.ClientID, reg.ClientSecret)
	if errDevice != nil {
		log.Errorf("Failed to start Kiro device authorization: %v", errDevice)
		c.JSON(http.StatusBadGateway, gin.H{"error": "failed to start kiro device authorization"})
		return
	}

	RegisterOAuthSession(state, "kiro")

	go h.pollKiroDeviceAuthorization(ctx, state, region, service, reg, device)

	authURL := strings.TrimSpace(device.VerificationURIComplete)
	if authURL == "" {
		authURL = device.VerificationURI
	}
	c.JSON(http.StatusOK, gin.H{
		"status":           "ok",
		"url":              authURL,
		"state":            state,
		"user_code":        device.UserCode,
		"verification_uri": device.VerificationURI,
		"expires_in":       device.ExpiresIn,
		"interval":         device.Interval,
	})
}

func (h *Handler) pollKiroDeviceAuthorization(ctx context.Context, state, region string, service kiroDeviceAuth, reg *kiro.ClientRegistration, device *kiro.DeviceAuthorization) {
	timeout := defaultDeviceAuthTimeout
	if device.ExpiresIn > 0 {
		timeout = time.Duration(device.ExpiresIn) * time.Second
	}
	interval := time.Duration(device.Interval) * time.Second
	if interval <= 0 {
		interval = 5 * time.Second
	}
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if !IsOAuthSessionPending(state, "kiro") {
			return
		}
		select {
		case <-ctx.Done():
			SetOAuthSessionError(state, ctx.Err().Error())
			return
		case <-time.After(interval):
		}
		result := service.PollDeviceToken(ctx, region, reg.ClientID, reg.ClientSecret, device.DeviceCode)
		if result.Pending {
			continue
		}
		if result.Err != nil {
			log.Errorf("Kiro device authorization failed: %v", result.Err)
			SetOAuthSessionError(state, result.Err.Error())
			return
		}
		if result.Bundle == nil {
			SetOAuthSessionError(state, "Kiro device authorization returned no token")
			return
		}
		record := sdkAuth.BuildKiroAuthRecord(result.Bundle, "aws-device")
		savedPath, errSave := h.saveTokenRecord(ctx, record)
		if errSave != nil {
			log.Errorf("Failed to save Kiro token: %v", errSave)
			SetOAuthSessionError(state, "Failed to save authentication tokens")
			return
		}
		log.Infof("Kiro authentication token saved to %s", savedPath)
		CompleteOAuthSession(state)
		CompleteOAuthSessionsByProvider("kiro")
		return
	}
	SetOAuthSessionError(state, fmt.Sprintf("Kiro device authorization timed out after %s", timeout.Round(time.Second)))
}
