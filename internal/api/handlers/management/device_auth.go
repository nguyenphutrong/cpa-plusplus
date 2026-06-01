package management

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/auth/copilot"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/auth/kiro"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/misc"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/quota"
	quotaProviders "github.com/router-for-me/CLIProxyAPI/v7/internal/quota/providers"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/quota/storage"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/util"
	sdkAuth "github.com/router-for-me/CLIProxyAPI/v7/sdk/auth"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	log "github.com/sirupsen/logrus"
)

const defaultDeviceAuthTimeout = 10 * time.Minute
const kiroSignInCallbackPort = 3128

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
	fetchKiroQuotaIdentity = fetchKiroQuotaIdentityDefault
)

func (h *Handler) RequestGitHubCopilotToken(c *gin.Context) {
	ctx := PopulateAuthContext(context.Background(), c)
	session, err := h.startGitHubCopilotOAuthSession(ctx)
	if err != nil {
		log.Errorf("Failed to start GitHub Copilot device flow: %v", err)
		c.JSON(http.StatusBadGateway, gin.H{"error": "failed to start github copilot device flow"})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"status":           "ok",
		"url":              session.AuthURL,
		"state":            session.State,
		"session_id":       session.SessionID,
		"user_code":        session.UserCode,
		"verification_uri": session.VerificationURI,
		"expires_in":       secondsUntil(session.ExpiresAt),
		"interval":         session.IntervalSeconds,
	})
}

func (h *Handler) RequestKiroToken(c *gin.Context) {
	ctx := PopulateAuthContext(context.Background(), c)
	session, err := h.startKiroOAuthSession(ctx, c.Query("method"), c.Query("region"))
	if err != nil {
		log.Errorf("Failed to start Kiro device authorization: %v", err)
		c.JSON(statusForOAuthStartError(err), gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"status":           "ok",
		"url":              session.AuthURL,
		"state":            session.State,
		"session_id":       session.SessionID,
		"user_code":        session.UserCode,
		"verification_uri": session.VerificationURI,
		"expires_in":       secondsUntil(session.ExpiresAt),
		"interval":         session.IntervalSeconds,
	})
}

func (h *Handler) StartProviderOAuth(c *gin.Context) {
	ctx := PopulateAuthContext(context.Background(), c)
	var req struct {
		Provider string         `json:"provider"`
		Method   string         `json:"method"`
		Options  map[string]any `json:"options"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body"})
		return
	}
	provider, errProvider := NormalizeOAuthProvider(req.Provider)
	if errProvider != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": errProvider.Error()})
		return
	}
	var (
		session providerOAuthSessionResponse
		err     error
	)
	switch provider {
	case "github-copilot":
		session, err = h.startGitHubCopilotOAuthSession(ctx)
	case "kiro":
		session, err = h.startKiroOAuthSession(ctx, req.Method, optionString(req.Options, "region"))
	case "anthropic", "codex", "gemini", "antigravity", "kimi", "xai":
		session, err = h.startCallbackProviderOAuthSession(c, provider)
	default:
		err = fmt.Errorf("unsupported oauth provider %q", req.Provider)
	}
	if err != nil {
		c.JSON(statusForOAuthStartError(err), gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, session)
}

func (h *Handler) startCallbackProviderOAuthSession(c *gin.Context, provider string) (providerOAuthSessionResponse, error) {
	handler, path, ok := callbackProviderOAuthStarter(h, provider)
	if !ok {
		return providerOAuthSessionResponse{}, fmt.Errorf("unsupported oauth provider %q", provider)
	}

	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	req := httptest.NewRequest(http.MethodGet, pathWithQueryFlag(path, "is_webui", "true"), nil)
	if c != nil && c.Request != nil {
		req = req.WithContext(c.Request.Context())
		req.Header = c.Request.Header.Clone()
	}
	ctx.Request = req

	handler(ctx)
	if rec.Code != http.StatusOK {
		var payload struct {
			Error string `json:"error"`
		}
		_ = json.Unmarshal(rec.Body.Bytes(), &payload)
		if strings.TrimSpace(payload.Error) == "" {
			payload.Error = strings.TrimSpace(rec.Body.String())
		}
		if strings.TrimSpace(payload.Error) == "" {
			payload.Error = fmt.Sprintf("failed to start %s oauth", provider)
		}
		return providerOAuthSessionResponse{}, errors.New(payload.Error)
	}

	var payload struct {
		URL   string `json:"url"`
		State string `json:"state"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		return providerOAuthSessionResponse{}, fmt.Errorf("decode oauth start response: %w", err)
	}
	state := strings.TrimSpace(payload.State)
	authURL := strings.TrimSpace(payload.URL)
	if state == "" || authURL == "" {
		return providerOAuthSessionResponse{}, fmt.Errorf("oauth start response missing state or url")
	}

	session := &providerOAuthSession{
		ID:              newProviderOAuthSessionID(provider),
		State:           state,
		Provider:        provider,
		Status:          providerOAuthSessionAwaitingCallback,
		AuthURL:         authURL,
		ExpiresAt:       time.Now().UTC().Add(oauthSessionTTL),
		IntervalSeconds: 2,
	}
	storeProviderOAuthSession(session)
	go bridgeLegacyOAuthSession(session.ID, state, provider)
	return providerOAuthSessionToResponse(session), nil
}

func pathWithQueryFlag(path, key, value string) string {
	separator := "?"
	if strings.Contains(path, "?") {
		separator = "&"
	}
	return path + separator + key + "=" + value
}

func callbackProviderOAuthStarter(h *Handler, provider string) (func(*gin.Context), string, bool) {
	switch provider {
	case "anthropic":
		return h.RequestAnthropicToken, "/v0/management/anthropic-auth-url", true
	case "codex":
		return h.RequestCodexToken, "/v0/management/codex-auth-url", true
	case "gemini":
		return h.RequestGeminiCLIToken, "/v0/management/gemini-cli-auth-url", true
	case "antigravity":
		return h.RequestAntigravityToken, "/v0/management/antigravity-auth-url", true
	case "kimi":
		return h.RequestKimiToken, "/v0/management/kimi-auth-url", true
	case "xai":
		return h.RequestXAIToken, "/v0/management/xai-auth-url", true
	default:
		return nil, "", false
	}
}

func bridgeLegacyOAuthSession(sessionID, state, provider string) {
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	deadline := time.Now().Add(oauthSessionTTL)
	for {
		if isProviderOAuthSessionTerminal(sessionID) {
			return
		}
		legacyProvider, legacyStatus, ok := GetOAuthSession(state)
		if !ok {
			completeProviderOAuthSession(sessionID, nil)
			return
		}
		if legacyStatus != "" {
			failProviderOAuthSession(sessionID, legacyStatus)
			return
		}
		if !strings.EqualFold(legacyProvider, provider) {
			failProviderOAuthSession(sessionID, "OAuth provider changed while waiting for callback")
			return
		}
		if time.Now().After(deadline) {
			failProviderOAuthSession(sessionID, "OAuth session expired")
			return
		}
		<-ticker.C
	}
}

func (h *Handler) GetProviderOAuthSession(c *gin.Context) {
	sessionID := strings.TrimSpace(c.Param("sessionID"))
	session, ok := providerOAuthSessions.Status(sessionID)
	if !ok {
		c.JSON(http.StatusNotFound, gin.H{"error": "oauth session not found"})
		return
	}
	c.JSON(http.StatusOK, session)
}

func (h *Handler) DeleteProviderOAuthSession(c *gin.Context) {
	sessionID := strings.TrimSpace(c.Param("sessionID"))
	session, ok := providerOAuthSessions.Cancel(sessionID)
	if !ok {
		c.JSON(http.StatusNotFound, gin.H{"error": "oauth session not found"})
		return
	}
	c.JSON(http.StatusOK, session)
}

func (h *Handler) PostProviderOAuthCallback(c *gin.Context) {
	var req struct {
		SessionID string `json:"session_id"`
		State     string `json:"state"`
		Code      string `json:"code"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body"})
		return
	}
	session, ok := providerOAuthSessions.Status(req.SessionID)
	if !ok {
		c.JSON(http.StatusNotFound, gin.H{"error": "oauth session not found"})
		return
	}
	if session.Status != providerOAuthSessionAwaitingCallback {
		c.JSON(http.StatusBadRequest, gin.H{"error": fmt.Sprintf("session is not awaiting callback (status=%s)", session.Status)})
		return
	}
	if strings.TrimSpace(req.State) == "" || strings.TrimSpace(req.Code) == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "code and state are required"})
		return
	}
	if req.State != session.State {
		failProviderOAuthSession(req.SessionID, "OAuth state mismatch")
		SetOAuthSessionError(session.State, "OAuth state mismatch")
		c.JSON(http.StatusBadRequest, gin.H{"error": "oauth state mismatch"})
		return
	}
	if session.Provider == "kiro" {
		completed, err := h.completeKiroOAuthCallback(c.Request.Context(), req.SessionID, req.State, req.Code, "")
		if err != nil {
			c.JSON(statusForOAuthStartError(err), gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, completed)
		return
	}
	if _, err := WriteOAuthCallbackFileForPendingSession(h.cfg.AuthDir, session.Provider, req.State, req.Code, ""); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, session)
}

func (h *Handler) startGitHubCopilotOAuthSession(ctx context.Context) (providerOAuthSessionResponse, error) {
	state, errState := misc.GenerateRandomState()
	if errState != nil {
		return providerOAuthSessionResponse{}, fmt.Errorf("failed to generate state parameter: %w", errState)
	}

	authSvc := newGitHubCopilotDeviceAuth(h.cfg)
	deviceCode, errDevice := authSvc.StartDeviceFlow(ctx)
	if errDevice != nil {
		return providerOAuthSessionResponse{}, fmt.Errorf("failed to start github copilot device flow: %w", errDevice)
	}

	RegisterOAuthSession(state, "github-copilot")
	timeout := defaultDeviceAuthTimeout
	if deviceCode.ExpiresIn > 0 {
		timeout = time.Duration(deviceCode.ExpiresIn) * time.Second
	}
	interval := deviceCode.Interval
	if interval <= 0 {
		interval = 5
	}
	session := &providerOAuthSession{
		ID:              newProviderOAuthSessionID("github-copilot"),
		State:           state,
		Provider:        "github-copilot",
		Method:          "device_code",
		Status:          providerOAuthSessionAwaitingDeviceConfirmation,
		AuthURL:         deviceCode.VerificationURI,
		VerificationURI: deviceCode.VerificationURI,
		UserCode:        deviceCode.UserCode,
		ExpiresAt:       time.Now().Add(timeout),
		IntervalSeconds: interval,
	}
	storeProviderOAuthSession(session)

	go func() {
		bundle, errWait := authSvc.WaitForAuthorization(ctx, deviceCode)
		if errWait != nil {
			log.Errorf("GitHub Copilot authorization failed: %v", errWait)
			SetOAuthSessionError(state, copilot.UserFriendlyMessage(errWait))
			failProviderOAuthSession(session.ID, copilot.UserFriendlyMessage(errWait))
			return
		}
		if bundle == nil || bundle.TokenData == nil || strings.TrimSpace(bundle.TokenData.AccessToken) == "" {
			SetOAuthSessionError(state, "GitHub Copilot authorization returned no token")
			failProviderOAuthSession(session.ID, "GitHub Copilot authorization returned no token")
			return
		}
		apiToken, errToken := authSvc.GetAPIToken(ctx, bundle.TokenData.AccessToken)
		if errToken != nil {
			log.Errorf("GitHub Copilot API token verification failed: %v", errToken)
			SetOAuthSessionError(state, oauthSessionErrorWithCause("Failed to verify Copilot access", errToken))
			failProviderOAuthSession(session.ID, oauthSessionErrorWithCause("Failed to verify Copilot access", errToken))
			return
		}
		record := sdkAuth.BuildGitHubCopilotAuthRecord(bundle, apiToken)
		savedPath, errSave := h.saveTokenRecord(ctx, record)
		if errSave != nil {
			log.Errorf("Failed to save GitHub Copilot token: %v", errSave)
			SetOAuthSessionError(state, "Failed to save authentication tokens")
			failProviderOAuthSession(session.ID, "Failed to save authentication tokens")
			return
		}
		log.Infof("GitHub Copilot authentication token saved to %s", savedPath)
		CompleteOAuthSession(state)
		CompleteOAuthSessionsByProvider("github-copilot")
		completeProviderOAuthSession(session.ID, h.providerResponseFromAuth(record))
	}()

	return providerOAuthSessionToResponse(session), nil
}

func (h *Handler) startKiroOAuthSession(ctx context.Context, method, region string) (providerOAuthSessionResponse, error) {
	method = strings.TrimSpace(strings.ToLower(method))
	if method == "" {
		method = "signin_localhost"
	}
	if isKiroSignInMethod(method) {
		return h.startKiroSignInLocalhost(ctx)
	}
	switch method {
	case "builder_id_device", "aws_device", "device_code":
	default:
		return providerOAuthSessionResponse{}, fmt.Errorf("unsupported kiro oauth method %q", method)
	}
	region = strings.TrimSpace(region)
	if region == "" {
		region = kiro.DefaultRegion
	}

	state, errState := misc.GenerateRandomState()
	if errState != nil {
		return providerOAuthSessionResponse{}, fmt.Errorf("failed to generate state parameter: %w", errState)
	}

	service := newKiroDeviceAuth(h.cfg)
	reg, errReg := service.RegisterClient(ctx, region)
	if errReg != nil {
		return providerOAuthSessionResponse{}, fmt.Errorf("failed to register kiro client: %w", errReg)
	}
	device, errDevice := service.StartDeviceAuthorization(ctx, region, reg.ClientID, reg.ClientSecret)
	if errDevice != nil {
		return providerOAuthSessionResponse{}, fmt.Errorf("failed to start kiro device authorization: %w", errDevice)
	}

	RegisterOAuthSession(state, "kiro")
	timeout := defaultDeviceAuthTimeout
	if device.ExpiresIn > 0 {
		timeout = time.Duration(device.ExpiresIn) * time.Second
	}
	interval := device.Interval
	if interval <= 0 {
		interval = 5
	}
	authURL := strings.TrimSpace(device.VerificationURIComplete)
	if authURL == "" {
		authURL = device.VerificationURI
	}
	session := &providerOAuthSession{
		ID:              newProviderOAuthSessionID("kiro"),
		State:           state,
		Provider:        "kiro",
		Method:          method,
		Status:          providerOAuthSessionAwaitingDeviceConfirmation,
		AuthURL:         authURL,
		VerificationURI: device.VerificationURI,
		UserCode:        device.UserCode,
		ExpiresAt:       time.Now().Add(timeout),
		IntervalSeconds: interval,
	}
	storeProviderOAuthSession(session)

	go h.pollKiroDeviceAuthorization(ctx, session.ID, state, region, service, reg, device)
	return providerOAuthSessionToResponse(session), nil
}

func isKiroSignInMethod(method string) bool {
	switch strings.ToLower(strings.TrimSpace(method)) {
	case "kiro", "signin", "signin_localhost", "builder_id_auth_code":
		return true
	default:
		return false
	}
}

func (h *Handler) startKiroSignInLocalhost(ctx context.Context) (providerOAuthSessionResponse, error) {
	cfg := h.cfg
	if cfg == nil {
		cfg = &config.Config{}
	}
	service := kiro.NewService(util.SetProxy(&cfg.SDKConfig, &http.Client{Timeout: 30 * time.Second}))
	pkce, err := kiro.GeneratePKCECodes()
	if err != nil {
		return providerOAuthSessionResponse{}, fmt.Errorf("failed to generate kiro pkce: %w", err)
	}
	state, errState := misc.GenerateRandomState()
	if errState != nil {
		return providerOAuthSessionResponse{}, fmt.Errorf("failed to generate state parameter: %w", errState)
	}
	signInRedirectURI := fmt.Sprintf("http://localhost:%d", kiroSignInCallbackPort)
	tokenRedirectURI := fmt.Sprintf("http://localhost:%d/oauth/callback", kiroSignInCallbackPort)
	authURL, err := service.BuildSignInAuthURL(pkce.CodeChallenge, state, signInRedirectURI)
	if err != nil {
		return providerOAuthSessionResponse{}, fmt.Errorf("failed to build kiro signin url: %w", err)
	}
	session := &providerOAuthSession{
		ID:              newProviderOAuthSessionID("kiro"),
		State:           state,
		Provider:        "kiro",
		Method:          "signin_localhost",
		Status:          providerOAuthSessionAwaitingCallback,
		AuthURL:         authURL,
		ExpiresAt:       time.Now().Add(defaultDeviceAuthTimeout),
		IntervalSeconds: 2,
		CodeVerifier:    pkce.CodeVerifier,
		RedirectURI:     tokenRedirectURI,
	}
	RegisterOAuthSession(state, "kiro")
	storeProviderOAuthSession(session)
	if err := h.startKiroLocalCallbackReceiver(ctx, session.ID, state); err != nil {
		CompleteOAuthSession(state)
		failProviderOAuthSession(session.ID, err.Error())
		return providerOAuthSessionResponse{}, err
	}
	return providerOAuthSessionToResponse(session), nil
}

func (h *Handler) startKiroLocalCallbackReceiver(ctx context.Context, sessionID, expectedState string) error {
	addr := fmt.Sprintf("127.0.0.1:%d", kiroSignInCallbackPort)
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("failed to bind localhost callback port %d: %w", kiroSignInCallbackPort, err)
	}
	server := &http.Server{ReadHeaderTimeout: 10 * time.Second}
	shutdown := func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdownCtx)
	}
	server.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/oauth/callback" {
			http.NotFound(w, r)
			return
		}
		query := r.URL.Query()
		code := strings.TrimSpace(query.Get("code"))
		state := strings.TrimSpace(query.Get("state"))
		errValue := strings.TrimSpace(query.Get("error"))
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if errValue != "" {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte("<html><body><h1>Login failed</h1><p>You can close this window.</p></body></html>"))
			failProviderOAuthSession(sessionID, fmt.Sprintf("oauth callback error: %s", errValue))
			CompleteOAuthSession(expectedState)
			go shutdown()
			return
		}
		if state != expectedState {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte("<html><body><h1>State mismatch</h1><p>You can close this window.</p></body></html>"))
			failProviderOAuthSession(sessionID, "oauth state mismatch")
			CompleteOAuthSession(expectedState)
			go shutdown()
			return
		}
		if code == "" {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte("<html><body><h1>Missing code</h1><p>You can close this window.</p></body></html>"))
			failProviderOAuthSession(sessionID, "oauth code is missing")
			CompleteOAuthSession(expectedState)
			go shutdown()
			return
		}
		_, _ = w.Write([]byte("<html><body><h1>Login successful</h1><p>You can close this window.</p></body></html>"))
		redirectURI := resolveKiroExchangeRedirectURI(r.URL)
		if _, errComplete := h.completeKiroOAuthCallback(context.Background(), sessionID, state, code, redirectURI); errComplete != nil {
			log.WithError(errComplete).Warn("failed to complete kiro oauth callback")
		}
		go shutdown()
	})
	go func() {
		if errServe := server.Serve(listener); errServe != nil && errServe != http.ErrServerClosed {
			failProviderOAuthSession(sessionID, fmt.Sprintf("localhost oauth callback server failed: %v", errServe))
		}
	}()
	go func() {
		deadline := time.Now().Add(defaultDeviceAuthTimeout)
		if session, ok := providerOAuthSessions.Session(sessionID); ok && !session.ExpiresAt.IsZero() {
			deadline = session.ExpiresAt
		}
		wait := time.Until(deadline)
		if wait < 0 {
			wait = 0
		}
		timer := time.NewTimer(wait)
		defer timer.Stop()
		select {
		case <-ctx.Done():
		case <-timer.C:
		}
		shutdown()
		if session, ok := providerOAuthSessions.Status(sessionID); ok && session.Status == providerOAuthSessionAwaitingCallback {
			failProviderOAuthSession(sessionID, "OAuth session expired")
			CompleteOAuthSession(expectedState)
		}
	}()
	return nil
}

func resolveKiroExchangeRedirectURI(callbackURL *url.URL) string {
	base := fmt.Sprintf("http://localhost:%d/oauth/callback", kiroSignInCallbackPort)
	if callbackURL == nil {
		return base
	}
	loginOption := strings.ToLower(strings.TrimSpace(callbackURL.Query().Get("login_option")))
	if loginOption == "" {
		return base
	}
	parsed, err := url.Parse(base)
	if err != nil {
		return base
	}
	query := parsed.Query()
	query.Set("login_option", loginOption)
	parsed.RawQuery = query.Encode()
	return parsed.String()
}

func fetchKiroQuotaIdentityDefault(ctx context.Context, cfg *config.Config, bundle *kiro.TokenBundle, method string) (storage.QuotaData, error) {
	if bundle == nil || strings.TrimSpace(bundle.AccessToken) == "" {
		return storage.QuotaData{}, fmt.Errorf("missing kiro access token")
	}
	if cfg == nil {
		cfg = &config.Config{}
	}
	region := strings.TrimSpace(bundle.Region)
	if region == "" {
		region = kiro.DefaultRegion
	}
	client := util.SetProxy(&cfg.SDKConfig, &http.Client{Timeout: 30 * time.Second})
	return quotaProviders.NewKiro(client).Fetch(ctx, quotaProviders.QuotaFetchInput{
		ProviderID: "kiro",
		Secret:     strings.TrimSpace(bundle.AccessToken),
		Metadata: map[string]any{
			"auth_method":   strings.TrimSpace(method),
			"profile_arn":   strings.TrimSpace(bundle.ProfileARN),
			"region":        region,
			"client_id":     strings.TrimSpace(bundle.ClientID),
			"client_secret": strings.TrimSpace(bundle.ClientSecret),
		},
		OAuthRefreshToken: strings.TrimSpace(bundle.RefreshToken),
	})
}

func (h *Handler) enrichKiroBundleWithQuotaIdentity(ctx context.Context, bundle *kiro.TokenBundle, method string) storage.QuotaData {
	if bundle == nil || strings.TrimSpace(bundle.Email) != "" || strings.TrimSpace(bundle.AccessToken) == "" {
		return storage.QuotaData{}
	}
	data, err := fetchKiroQuotaIdentity(ctx, h.cfg, bundle, method)
	if err != nil {
		log.Debugf("kiro quota identity lookup failed: %v", err)
		return storage.QuotaData{}
	}
	email := quota.ProviderDataAccountLabel("kiro", data)
	if strings.Contains(email, "@") {
		bundle.Email = email
	}
	return data
}

func applyKiroQuotaIdentityToAuthRecord(record *coreauth.Auth, data storage.QuotaData) {
	if record == nil {
		return
	}
	email := quota.ProviderDataAccountLabel("kiro", data)
	if !strings.Contains(email, "@") {
		return
	}
	if record.Metadata == nil {
		record.Metadata = map[string]any{}
	}
	if record.Attributes == nil {
		record.Attributes = map[string]string{}
	}
	record.Metadata[quota.MetadataKey] = data
	record.Metadata["email"] = email
	record.Attributes["email"] = email
}

func (h *Handler) completeKiroOAuthCallback(ctx context.Context, sessionID, state, code, redirectURIOverride string) (providerOAuthSessionResponse, error) {
	session, ok := providerOAuthSessions.Session(sessionID)
	if !ok {
		return providerOAuthSessionResponse{}, errors.New("oauth session not found")
	}
	if session.Provider != "kiro" {
		return providerOAuthSessionResponse{}, errors.New("callback completion is only supported for kiro sessions")
	}
	if session.Status != providerOAuthSessionAwaitingCallback {
		return providerOAuthSessionResponse{}, fmt.Errorf("session is not awaiting callback (status=%s)", session.Status)
	}
	if strings.TrimSpace(state) == "" || strings.TrimSpace(code) == "" {
		return providerOAuthSessionResponse{}, errors.New("code and state are required")
	}
	if state != session.State {
		failProviderOAuthSession(sessionID, "OAuth state mismatch")
		SetOAuthSessionError(session.State, "OAuth state mismatch")
		return providerOAuthSessionResponse{}, errors.New("oauth state mismatch")
	}
	redirectURI := strings.TrimSpace(session.RedirectURI)
	if strings.TrimSpace(redirectURIOverride) != "" {
		redirectURI = strings.TrimSpace(redirectURIOverride)
	}
	cfg := h.cfg
	if cfg == nil {
		cfg = &config.Config{}
	}
	service := kiro.NewService(util.SetProxy(&cfg.SDKConfig, &http.Client{Timeout: 30 * time.Second}))
	bundle, err := service.ExchangeSocialCode(ctx, code, session.CodeVerifier, redirectURI)
	if err != nil {
		err = fmt.Errorf("kiro oauth token exchange failed (redirect_uri=%q): %w", redirectURI, err)
		failProviderOAuthSession(sessionID, err.Error())
		SetOAuthSessionError(session.State, err.Error())
		return providerOAuthSessionResponse{}, err
	}
	quotaData := h.enrichKiroBundleWithQuotaIdentity(ctx, bundle, session.Method)
	record := sdkAuth.BuildKiroAuthRecord(bundle, session.Method)
	applyKiroQuotaIdentityToAuthRecord(record, quotaData)
	if record.Metadata == nil {
		record.Metadata = map[string]any{}
	}
	if record.Attributes == nil {
		record.Attributes = map[string]string{}
	}
	record.Metadata["redirect_uri"] = redirectURI
	record.Attributes["redirect_uri"] = redirectURI
	savedPath, errSave := h.saveTokenRecord(ctx, record)
	if errSave != nil {
		errSave = fmt.Errorf("failed to save kiro authentication tokens: %w", errSave)
		failProviderOAuthSession(sessionID, errSave.Error())
		SetOAuthSessionError(session.State, errSave.Error())
		return providerOAuthSessionResponse{}, errSave
	}
	log.Infof("Kiro authentication token saved to %s", savedPath)
	CompleteOAuthSession(session.State)
	CompleteOAuthSessionsByProvider("kiro")
	completeProviderOAuthSession(sessionID, h.providerResponseFromAuth(record))
	completed, _ := providerOAuthSessions.Status(sessionID)
	return completed, nil
}

func (h *Handler) pollKiroDeviceAuthorization(ctx context.Context, sessionID, state, region string, service kiroDeviceAuth, reg *kiro.ClientRegistration, device *kiro.DeviceAuthorization) {
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
			failProviderOAuthSession(sessionID, ctx.Err().Error())
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
			failProviderOAuthSession(sessionID, result.Err.Error())
			return
		}
		if result.Bundle == nil {
			SetOAuthSessionError(state, "Kiro device authorization returned no token")
			failProviderOAuthSession(sessionID, "Kiro device authorization returned no token")
			return
		}
		quotaData := h.enrichKiroBundleWithQuotaIdentity(ctx, result.Bundle, "aws-device")
		record := sdkAuth.BuildKiroAuthRecord(result.Bundle, "aws-device")
		applyKiroQuotaIdentityToAuthRecord(record, quotaData)
		savedPath, errSave := h.saveTokenRecord(ctx, record)
		if errSave != nil {
			log.Errorf("Failed to save Kiro token: %v", errSave)
			SetOAuthSessionError(state, "Failed to save authentication tokens")
			failProviderOAuthSession(sessionID, "Failed to save authentication tokens")
			return
		}
		log.Infof("Kiro authentication token saved to %s", savedPath)
		CompleteOAuthSession(state)
		CompleteOAuthSessionsByProvider("kiro")
		completeProviderOAuthSession(sessionID, h.providerResponseFromAuth(record))
		return
	}
	SetOAuthSessionError(state, fmt.Sprintf("Kiro device authorization timed out after %s", timeout.Round(time.Second)))
	failProviderOAuthSession(sessionID, fmt.Sprintf("Kiro device authorization timed out after %s", timeout.Round(time.Second)))
}

func secondsUntil(expiresAt string) int {
	if strings.TrimSpace(expiresAt) == "" {
		return 0
	}
	t, err := time.Parse(time.RFC3339, expiresAt)
	if err != nil {
		return 0
	}
	seconds := int(time.Until(t).Seconds())
	if seconds < 0 {
		return 0
	}
	return seconds
}

func optionString(options map[string]any, key string) string {
	if options == nil {
		return ""
	}
	if value, ok := options[key].(string); ok {
		return strings.TrimSpace(value)
	}
	return ""
}

func statusForOAuthStartError(err error) int {
	if err == nil {
		return http.StatusOK
	}
	if strings.Contains(strings.ToLower(err.Error()), "unsupported") || errors.Is(err, errUnsupportedOAuthFlow) {
		return http.StatusBadRequest
	}
	return http.StatusBadGateway
}
