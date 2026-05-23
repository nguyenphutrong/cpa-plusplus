package kiro

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	DefaultRegion          = "us-east-1"
	defaultKiroClientName  = "cpa-plusplus"
	defaultKiroClientType  = "public"
	defaultKiroIssuerURL   = "https://auth.aws.amazon.com"
	defaultKiroRefreshURL  = "https://prod.us-east-1.auth.desktop.kiro.dev/refreshToken"
	defaultKiroSocialToken = "https://prod.us-east-1.auth.desktop.kiro.dev/oauth/token"
	defaultKiroSocialAuth  = "https://prod.us-east-1.auth.desktop.kiro.dev/login"
	defaultKiroSignInAuth  = "https://app.kiro.dev/signin"
	defaultKiroUsageURL    = "https://codewhisperer.us-east-1.amazonaws.com"
)

var (
	RefreshURL   = defaultKiroRefreshURL
	SocialToken  = defaultKiroSocialToken
	SocialAuth   = defaultKiroSocialAuth
	SignInAuth   = defaultKiroSignInAuth
	KiroUsageURL = defaultKiroUsageURL
	OIDCBaseURL  = "https://oidc.%s.amazonaws.com"
)

type TokenBundle struct {
	AccessToken  string
	RefreshToken string
	ProfileARN   string
	ClientID     string
	ClientSecret string
	Region       string
	ExpiresAt    time.Time
	Email        string
	Username     string
	Subject      string
}

type AvailableModel struct {
	ModelID        string
	ModelName      string
	RateMultiplier float64
	RateUnit       string
}

type ClientRegistration struct {
	ClientID              string
	ClientSecret          string
	ClientSecretExpiresAt int64
}

type DeviceAuthorization struct {
	DeviceCode              string
	UserCode                string
	VerificationURI         string
	VerificationURIComplete string
	ExpiresIn               int
	Interval                int
}

type DevicePollResult struct {
	Pending bool
	Bundle  *TokenBundle
	Err     error
}

type PKCECodes struct {
	CodeVerifier  string
	CodeChallenge string
}

type Service struct {
	httpClient *http.Client
	now        func() time.Time
}

func NewService(httpClient *http.Client) *Service {
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	return &Service{httpClient: httpClient, now: time.Now}
}

func (s *Service) RegisterClient(ctx context.Context, region string) (*ClientRegistration, error) {
	if strings.TrimSpace(region) == "" {
		region = DefaultRegion
	}
	body, err := json.Marshal(map[string]any{
		"clientName": defaultKiroClientName,
		"clientType": defaultKiroClientType,
		"scopes": []string{
			"openid",
			"profile",
			"aws.csi",
			"aws.csi:operational",
		},
		"grantTypes": []string{
			"refresh_token",
			"urn:ietf:params:oauth:grant-type:device_code",
		},
		"issuerUrl": defaultKiroIssuerURL,
	})
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, resolveOIDCBaseURL(region)+"/client/register", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := s.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("kiro register client failed with status %d: %s", resp.StatusCode, string(raw))
	}
	var payload struct {
		ClientID              string `json:"clientId"`
		ClientSecret          string `json:"clientSecret"`
		ClientSecretExpiresAt int64  `json:"clientSecretExpiresAt"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return nil, err
	}
	return &ClientRegistration{
		ClientID:              payload.ClientID,
		ClientSecret:          payload.ClientSecret,
		ClientSecretExpiresAt: payload.ClientSecretExpiresAt,
	}, nil
}

func (s *Service) StartDeviceAuthorization(ctx context.Context, region, clientID, clientSecret string) (*DeviceAuthorization, error) {
	if strings.TrimSpace(region) == "" {
		region = DefaultRegion
	}
	body, err := json.Marshal(map[string]string{
		"clientId":     clientID,
		"clientSecret": clientSecret,
		"startUrl":     "https://view.awsapps.com/start",
	})
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, resolveOIDCBaseURL(region)+"/device_authorization", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := s.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("kiro device authorization failed with status %d: %s", resp.StatusCode, string(raw))
	}
	var payload struct {
		DeviceCode              string `json:"deviceCode"`
		UserCode                string `json:"userCode"`
		VerificationURI         string `json:"verificationUri"`
		VerificationURIComplete string `json:"verificationUriComplete"`
		ExpiresIn               int    `json:"expiresIn"`
		Interval                int    `json:"interval"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return nil, err
	}
	if payload.Interval <= 0 {
		payload.Interval = 5
	}
	return &DeviceAuthorization{
		DeviceCode:              payload.DeviceCode,
		UserCode:                payload.UserCode,
		VerificationURI:         payload.VerificationURI,
		VerificationURIComplete: payload.VerificationURIComplete,
		ExpiresIn:               payload.ExpiresIn,
		Interval:                payload.Interval,
	}, nil
}

func (s *Service) PollDeviceToken(ctx context.Context, region, clientID, clientSecret, deviceCode string) DevicePollResult {
	if strings.TrimSpace(region) == "" {
		region = DefaultRegion
	}
	body, err := json.Marshal(map[string]string{
		"clientId":     clientID,
		"clientSecret": clientSecret,
		"deviceCode":   deviceCode,
		"grantType":    "urn:ietf:params:oauth:grant-type:device_code",
	})
	if err != nil {
		return DevicePollResult{Err: err}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, resolveOIDCBaseURL(region)+"/token", bytes.NewReader(body))
	if err != nil {
		return DevicePollResult{Err: err}
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := s.httpClient.Do(req)
	if err != nil {
		return DevicePollResult{Err: err}
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return DevicePollResult{Err: err}
	}
	var payload struct {
		AccessToken      string `json:"accessToken"`
		RefreshToken     string `json:"refreshToken"`
		ProfileARN       string `json:"profileArn"`
		ExpiresIn        int    `json:"expiresIn"`
		Error            string `json:"error"`
		ErrorDescription string `json:"error_description"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return DevicePollResult{Err: err}
	}
	if payload.Error != "" || resp.StatusCode < 200 || resp.StatusCode >= 300 {
		switch payload.Error {
		case "authorization_pending", "slow_down":
			return DevicePollResult{Pending: true}
		default:
			return DevicePollResult{Err: fmt.Errorf("kiro device token polling failed: %s %s", payload.Error, payload.ErrorDescription)}
		}
	}
	return DevicePollResult{Bundle: &TokenBundle{
		AccessToken:  payload.AccessToken,
		RefreshToken: payload.RefreshToken,
		ProfileARN:   payload.ProfileARN,
		ExpiresAt:    s.now().Add(time.Duration(payload.ExpiresIn) * time.Second),
		ClientID:     clientID,
		ClientSecret: clientSecret,
		Region:       region,
	}}
}

func GeneratePKCECodes() (*PKCECodes, error) {
	verifier, err := randomBase64URL(32)
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256([]byte(verifier))
	return &PKCECodes{
		CodeVerifier:  verifier,
		CodeChallenge: base64.RawURLEncoding.EncodeToString(sum[:]),
	}, nil
}

func (s *Service) BuildSocialAuthURL(provider, codeChallenge, state, redirectURI string) (string, error) {
	var idp string
	switch strings.ToLower(strings.TrimSpace(provider)) {
	case "google":
		idp = "Google"
	case "github":
		idp = "Github"
	default:
		return "", fmt.Errorf("unsupported kiro social provider %q", provider)
	}
	if strings.TrimSpace(redirectURI) == "" {
		redirectURI = "kiro://kiro.kiroAgent/authenticate-success"
	}
	parsed, err := url.Parse(SocialAuth)
	if err != nil {
		return "", err
	}
	query := parsed.Query()
	query.Set("idp", idp)
	query.Set("redirect_uri", redirectURI)
	query.Set("code_challenge", codeChallenge)
	query.Set("code_challenge_method", "S256")
	query.Set("state", state)
	query.Set("prompt", "select_account")
	parsed.RawQuery = query.Encode()
	return parsed.String(), nil
}

func (s *Service) BuildSignInAuthURL(codeChallenge, state, redirectURI string) (string, error) {
	if strings.TrimSpace(codeChallenge) == "" {
		return "", fmt.Errorf("missing code challenge")
	}
	if strings.TrimSpace(state) == "" {
		return "", fmt.Errorf("missing oauth state")
	}
	if strings.TrimSpace(redirectURI) == "" {
		redirectURI = "http://localhost:3128"
	}
	parsed, err := url.Parse(SignInAuth)
	if err != nil {
		return "", err
	}
	query := parsed.Query()
	query.Set("state", state)
	query.Set("code_challenge", codeChallenge)
	query.Set("code_challenge_method", "S256")
	query.Set("redirect_uri", redirectURI)
	query.Set("redirect_from", "KiroIDE")
	parsed.RawQuery = query.Encode()
	return parsed.String(), nil
}

func (s *Service) ExchangeSocialCode(ctx context.Context, code, codeVerifier, redirectURI string) (*TokenBundle, error) {
	if strings.TrimSpace(redirectURI) == "" {
		redirectURI = "kiro://kiro.kiroAgent/authenticate-success"
	}
	body, err := json.Marshal(map[string]string{
		"code":          code,
		"code_verifier": codeVerifier,
		"redirect_uri":  redirectURI,
	})
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, SocialToken, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	resp, err := s.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("kiro token exchange failed with status %d: %s", resp.StatusCode, string(raw))
	}
	var tokenResp struct {
		AccessToken  string `json:"accessToken"`
		RefreshToken string `json:"refreshToken"`
		ProfileARN   string `json:"profileArn"`
		ExpiresIn    int    `json:"expiresIn"`
		Email        string `json:"email"`
		Username     string `json:"username"`
		Subject      string `json:"sub"`
	}
	if err := json.Unmarshal(raw, &tokenResp); err != nil {
		return nil, err
	}
	claims := parseJWTClaims(tokenResp.AccessToken)

	return &TokenBundle{
		AccessToken:  tokenResp.AccessToken,
		RefreshToken: tokenResp.RefreshToken,
		ProfileARN:   tokenResp.ProfileARN,
		ExpiresAt:    s.now().Add(time.Duration(tokenResp.ExpiresIn) * time.Second),
		Email:        firstNonEmpty(tokenResp.Email, claimString(claims, "email")),
		Username: firstNonEmpty(
			tokenResp.Username,
			claimString(claims, "username"),
			claimString(claims, "preferred_username"),
		),
		Subject: claimString(claims, "sub"),
	}, nil
}

func (s *Service) RefreshTokens(ctx context.Context, refreshToken, clientID, clientSecret, region string) (*TokenBundle, error) {
	var oidcErr error
	if strings.TrimSpace(clientID) != "" && strings.TrimSpace(clientSecret) != "" {
		if strings.TrimSpace(region) == "" {
			region = DefaultRegion
		}
		body, err := json.Marshal(map[string]string{
			"clientId":     clientID,
			"clientSecret": clientSecret,
			"refreshToken": refreshToken,
			"grantType":    "refresh_token",
		})
		if err != nil {
			return nil, err
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, resolveOIDCBaseURL(region)+"/token", bytes.NewReader(body))
		if err != nil {
			return nil, err
		}
		req.Header.Set("Content-Type", "application/json")
		resp, err := s.httpClient.Do(req)
		if err != nil {
			return nil, err
		}
		defer resp.Body.Close()
		raw, err := io.ReadAll(resp.Body)
		if err != nil {
			oidcErr = err
		} else if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			oidcErr = fmt.Errorf("kiro oidc token refresh failed with status %d: %s", resp.StatusCode, string(raw))
		} else {
			var tokenResp struct {
				AccessToken  string `json:"accessToken"`
				RefreshToken string `json:"refreshToken"`
				ExpiresIn    int    `json:"expiresIn"`
			}
			if err := json.Unmarshal(raw, &tokenResp); err != nil {
				oidcErr = err
			} else {
				claims := parseJWTClaims(tokenResp.AccessToken)
				return &TokenBundle{
					AccessToken:  tokenResp.AccessToken,
					RefreshToken: firstNonEmpty(tokenResp.RefreshToken, refreshToken),
					ClientID:     clientID,
					ClientSecret: clientSecret,
					Region:       region,
					ExpiresAt:    s.now().Add(time.Duration(tokenResp.ExpiresIn) * time.Second),
					Email:        claimString(claims, "email"),
					Username: firstNonEmpty(
						claimString(claims, "username"),
						claimString(claims, "preferred_username"),
					),
					Subject: claimString(claims, "sub"),
				}, nil
			}
		}
	}

	body, err := json.Marshal(map[string]string{"refreshToken": refreshToken})
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, RefreshURL, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	resp, err := s.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		if oidcErr != nil {
			return nil, fmt.Errorf("kiro token refresh failed (oidc then social): oidc=%v; social_status=%d social_body=%s", oidcErr, resp.StatusCode, string(raw))
		}
		return nil, fmt.Errorf("kiro token refresh failed with status %d: %s", resp.StatusCode, string(raw))
	}

	var tokenResp struct {
		AccessToken  string `json:"accessToken"`
		RefreshToken string `json:"refreshToken"`
		ProfileARN   string `json:"profileArn"`
		ExpiresIn    int    `json:"expiresIn"`
		Email        string `json:"email"`
		Username     string `json:"username"`
		Subject      string `json:"sub"`
	}
	if err := json.Unmarshal(raw, &tokenResp); err != nil {
		return nil, err
	}
	claims := parseJWTClaims(tokenResp.AccessToken)

	return &TokenBundle{
		AccessToken:  tokenResp.AccessToken,
		RefreshToken: firstNonEmpty(tokenResp.RefreshToken, refreshToken),
		ProfileARN:   tokenResp.ProfileARN,
		ExpiresAt:    s.now().Add(time.Duration(tokenResp.ExpiresIn) * time.Second),
		Email:        firstNonEmpty(tokenResp.Email, claimString(claims, "email")),
		Username: firstNonEmpty(
			tokenResp.Username,
			claimString(claims, "username"),
			claimString(claims, "preferred_username"),
		),
		Subject: firstNonEmpty(tokenResp.Subject, claimString(claims, "sub")),
	}, nil
}

func (s *Service) ListAvailableModels(ctx context.Context, accessToken, profileARN, region string) ([]AvailableModel, error) {
	resolvedRegion := strings.TrimSpace(region)
	if resolvedRegion == "" {
		if fromProfile := extractRegionFromProfileARN(profileARN); fromProfile != "" {
			resolvedRegion = fromProfile
		}
	}
	if resolvedRegion == "" {
		resolvedRegion = DefaultRegion
	}
	requestURL, err := url.Parse(fmt.Sprintf("https://q.%s.amazonaws.com/ListAvailableModels", resolvedRegion))
	if err != nil {
		return nil, err
	}
	query := requestURL.Query()
	query.Set("origin", "AI_EDITOR")
	if trimmedARN := strings.TrimSpace(profileARN); trimmedARN != "" {
		query.Set("profileArn", trimmedARN)
	}
	requestURL.RawQuery = query.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, requestURL.String(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(accessToken))
	req.Header.Set("Accept", "application/json")
	req.Header.Set("x-amzn-codewhisperer-optout", "true")

	resp, err := s.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("kiro list models failed with status %d: %s", resp.StatusCode, string(raw))
	}
	var payload struct {
		Models []struct {
			ModelID        string  `json:"modelId"`
			ModelName      string  `json:"modelName"`
			RateMultiplier float64 `json:"rateMultiplier"`
			RateUnit       string  `json:"rateUnit"`
		} `json:"models"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return nil, err
	}
	models := make([]AvailableModel, 0, len(payload.Models))
	for _, item := range payload.Models {
		if trimmed := strings.TrimSpace(item.ModelID); trimmed != "" {
			models = append(models, AvailableModel{
				ModelID:        trimmed,
				ModelName:      strings.TrimSpace(item.ModelName),
				RateMultiplier: item.RateMultiplier,
				RateUnit:       strings.TrimSpace(item.RateUnit),
			})
		}
	}
	return models, nil
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func resolveOIDCBaseURL(region string) string {
	base := strings.TrimSpace(OIDCBaseURL)
	if strings.Contains(base, "%s") {
		return strings.TrimRight(fmt.Sprintf(base, region), "/")
	}
	return strings.TrimRight(base, "/")
}

func randomBase64URL(size int) (string, error) {
	buf := make([]byte, size)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

func parseJWTClaims(token string) map[string]any {
	parts := strings.Split(strings.TrimSpace(token), ".")
	if len(parts) < 2 {
		return nil
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return nil
	}
	var claims map[string]any
	if err := json.Unmarshal(raw, &claims); err != nil {
		return nil
	}
	return claims
}

func claimString(claims map[string]any, key string) string {
	if claims == nil {
		return ""
	}
	value, ok := claims[key]
	if !ok {
		return ""
	}
	switch typed := value.(type) {
	case string:
		return strings.TrimSpace(typed)
	case []any:
		for _, item := range typed {
			if stringItem, ok := item.(string); ok && strings.TrimSpace(stringItem) != "" {
				return strings.TrimSpace(stringItem)
			}
		}
	case []string:
		for _, item := range typed {
			if strings.TrimSpace(item) != "" {
				return strings.TrimSpace(item)
			}
		}
	}
	return ""
}

func extractRegionFromProfileARN(profileARN string) string {
	parts := strings.Split(strings.TrimSpace(profileARN), ":")
	// arn:partition:service:region:account-id:resource
	if len(parts) > 3 && strings.TrimSpace(parts[3]) != "" {
		return strings.TrimSpace(parts[3])
	}
	return ""
}

func ResolveProfileARNForRequest(authMethod, profileARN string) string {
	method := strings.ToLower(strings.TrimSpace(authMethod))
	// Social callback flows should not send profileArn.
	if method == "social" || method == "social_google" || method == "social_github" {
		return ""
	}
	return strings.TrimSpace(profileARN)
}

func ResolveRegionForRequest(profileARN, headerRegion string) string {
	candidates := []string{
		strings.TrimSpace(headerRegion),
		extractRegionFromProfileARN(profileARN),
		DefaultRegion,
	}
	for _, candidate := range candidates {
		if candidate != "" {
			return candidate
		}
	}
	return DefaultRegion
}
