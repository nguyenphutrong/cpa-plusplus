package providers

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	kiroauth "github.com/router-for-me/CLIProxyAPI/v7/internal/auth/kiro"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/quota/storage"
)

const (
	kiroUsageURL           = "https://codewhisperer.us-east-1.amazonaws.com/getUsageLimits"
	kiroUsageRootURL       = "https://codewhisperer.us-east-1.amazonaws.com"
	kiroUsageUserAgent     = "aws-sdk-js/1.0.0 ua/2.1 os/darwin lang/js api/codewhispererruntime#1.0.0"
	kiroUsageAmzUserAgent  = "aws-sdk-js/1.0.0 KiroIDE-0.9.2"
	kiroUsageResourceType  = "AGENTIC_REQUEST"
	kiroUsageRequestOrigin = "AI_EDITOR"
)

type Kiro struct {
	client *http.Client
}

type kiroUsageAttemptResult struct {
	name   string
	status int
	raw    []byte
	err    error
}

type kiroUsageResponse struct {
	UsageBreakdownList []kiroUsageBreakdown `json:"usageBreakdownList"`
	SubscriptionInfo   kiroSubscriptionInfo `json:"subscriptionInfo"`
	UserInfo           kiroUserInfo         `json:"userInfo"`
	NextDateReset      float64              `json:"nextDateReset"`
}

type kiroUsageBreakdown struct {
	DisplayName          string  `json:"displayName"`
	DisplayNamePlural    string  `json:"displayNamePlural"`
	ResourceType         string  `json:"resourceType"`
	CurrentUsage         float64 `json:"currentUsage"`
	CurrentUsageWithPrec float64 `json:"currentUsageWithPrecision"`
	UsageLimit           float64 `json:"usageLimit"`
	UsageLimitWithPrec   float64 `json:"usageLimitWithPrecision"`
	NextDateReset        float64 `json:"nextDateReset"`
}

type kiroSubscriptionInfo struct {
	SubscriptionTitle string `json:"subscriptionTitle"`
	Type              string `json:"type"`
}

type kiroUserInfo struct {
	Email  string `json:"email"`
	UserID string `json:"userId"`
}

func NewKiro(client *http.Client) *Kiro {
	if client == nil {
		client = http.DefaultClient
	}
	return &Kiro{client: client}
}

func (k *Kiro) Provider() string {
	return "kiro"
}

func (k *Kiro) Fetch(ctx context.Context, credential QuotaFetchInput) (storage.QuotaData, error) {
	authMethod := inputString(credential, "auth_method", "kiro_auth_method")
	profileARNInput := inputString(credential, "profile_arn", "kiro_profile_arn")
	regionInput := inputString(credential, "region", "kiro_region")
	profileARN := kiroauth.ResolveProfileARNForRequest(authMethod, profileARNInput)
	region := kiroauth.ResolveRegionForRequest(profileARNInput, regionInput)
	isDeviceCodeLike := strings.EqualFold(authMethod, "device_code") || strings.EqualFold(authMethod, "idc")
	if isDeviceCodeLike && strings.TrimSpace(profileARN) == "" {
		return buildKiroQuotaUnavailableData("profileArn is missing for device_code/idc session"), nil
	}
	runAttempts := func(token string) (*kiroUsageAttemptResult, bool) {
		results := k.fetchUsageAttempts(ctx, token, profileARN, region)
		needRefresh := false
		var last *kiroUsageAttemptResult
		for index := range results {
			attempt := &results[index]
			last = attempt
			if attempt.err != nil {
				continue
			}
			switch attempt.status {
			case http.StatusOK, http.StatusLocked:
				return attempt, false
			case http.StatusUnauthorized, http.StatusForbidden:
				needRefresh = true
				continue
			}
		}
		return last, needRefresh
	}

	result, needRefresh := runAttempts(strings.TrimSpace(credential.Secret))
	if result == nil {
		return storage.QuotaData{}, fmt.Errorf("kiro quota upstream unavailable")
	}
	if needRefresh &&
		strings.TrimSpace(credential.OAuthRefreshToken) != "" {
		bundle, refreshErr := kiroauth.NewService(nil).RefreshTokens(
			ctx,
			credential.OAuthRefreshToken,
			inputString(credential, "client_id", "kiro_client_id"),
			inputString(credential, "client_secret", "kiro_client_secret"),
			firstNonEmpty(inputString(credential, "region", "kiro_region"), kiroauth.DefaultRegion),
		)
		if refreshErr == nil && strings.TrimSpace(bundle.AccessToken) != "" {
			if strings.TrimSpace(bundle.ProfileARN) != "" {
				profileARN = kiroauth.ResolveProfileARNForRequest(authMethod, bundle.ProfileARN)
				region = kiroauth.ResolveRegionForRequest(bundle.ProfileARN, regionInput)
			}
			result, _ = runAttempts(strings.TrimSpace(bundle.AccessToken))
		}
	}

	if result == nil {
		return storage.QuotaData{}, fmt.Errorf("kiro quota upstream unavailable")
	}

	switch result.status {
	case http.StatusUnauthorized:
		return storage.QuotaData{}, ErrUnauthorized
	case http.StatusTooManyRequests:
		return storage.QuotaData{}, ErrRateLimited
	case http.StatusLocked:
		return storage.QuotaData{
			UpdatedAt: time.Now().UTC(),
			ProviderData: &storage.ProviderQuotaData{
				IsForbidden:     true,
				PlanType:        "locked",
				PlanDisplayName: "Locked",
				LastUpdated:     time.Now().UTC().Format(time.RFC3339),
				Models:          []storage.QuotaModel{},
			},
		}, nil
	case http.StatusForbidden:
		body := strings.ToLower(strings.TrimSpace(string(result.raw)))
		if strings.Contains(body, "ban") || strings.Contains(body, "suspended") || strings.Contains(body, "terminated") {
			return storage.QuotaData{
				UpdatedAt: time.Now().UTC(),
				ProviderData: &storage.ProviderQuotaData{
					IsForbidden:     true,
					PlanType:        "banned",
					PlanDisplayName: "Banned",
					LastUpdated:     time.Now().UTC().Format(time.RFC3339),
					Models:          []storage.QuotaModel{},
				},
			}, nil
		}
		if isDeviceCodeLike && strings.TrimSpace(profileARN) == "" {
			return buildKiroQuotaUnavailableData("missing profileArn for device_code/idc"), nil
		}
		return storage.QuotaData{}, fmt.Errorf("kiro quota upstream unavailable (forbidden): auth_method=%q profile_arn_sent=%t region=%q", authMethod, profileARN != "", region)
	}

	if result.err != nil {
		return storage.QuotaData{}, fmt.Errorf("kiro quota upstream unavailable: %w", result.err)
	}
	if result.status != http.StatusOK {
		return storage.QuotaData{}, fmt.Errorf("kiro quota upstream unavailable: status=%d", result.status)
	}

	var payload kiroUsageResponse
	if err := json.Unmarshal(result.raw, &payload); err != nil {
		return storage.QuotaData{}, fmt.Errorf("decode response: %w", err)
	}

	now := time.Now().UTC()
	providerData := &storage.ProviderQuotaData{
		IsForbidden:       false,
		AccountLabel:      firstNonEmpty(strings.TrimSpace(payload.UserInfo.Email), strings.TrimSpace(payload.UserInfo.UserID), firstNonEmpty(credential.ValidationAccountID, credential.OAuthAccountID, derivedAccountIdentity(credential)), credential.Label),
		PlanType:          strings.ToLower(strings.ReplaceAll(strings.TrimSpace(payload.SubscriptionInfo.SubscriptionTitle), " ", "_")),
		PlanDisplayName:   strings.TrimSpace(payload.SubscriptionInfo.SubscriptionTitle),
		LastUpdated:       now.Format(time.RFC3339),
		Models:            make([]storage.QuotaModel, 0, len(payload.UsageBreakdownList)),
		CopilotChatModels: nil,
	}
	if providerData.PlanType == "" {
		providerData.PlanType = strings.ToLower(strings.TrimSpace(payload.SubscriptionInfo.Type))
	}
	if providerData.PlanDisplayName == "" {
		providerData.PlanDisplayName = strings.TrimSpace(payload.SubscriptionInfo.Type)
	}

	for _, breakdown := range payload.UsageBreakdownList {
		limit := firstPositive(breakdown.UsageLimitWithPrec, breakdown.UsageLimit)
		used := firstPositive(breakdown.CurrentUsageWithPrec, breakdown.CurrentUsage)
		if limit <= 0 {
			continue
		}

		remaining := limit - used
		if remaining < 0 {
			remaining = 0
		}
		remainingPercent, usedPercent := quotaPercentPointers((remaining / limit) * 100)

		displayName := strings.TrimSpace(breakdown.DisplayName)
		if displayName == "" {
			displayName = strings.TrimSpace(breakdown.DisplayNamePlural)
		}
		if displayName == "" {
			displayName = strings.TrimSpace(breakdown.ResourceType)
		}
		if displayName == "" {
			displayName = "Kiro"
		}

		modelName := strings.ToLower(strings.ReplaceAll(displayName, " ", "-"))
		modelName = strings.ReplaceAll(modelName, "_", "-")
		modelName = strings.Trim(modelName, "-")
		if modelName == "" {
			modelName = "kiro"
		}

		usedValue := used
		limitValue := limit
		remainingValue := remaining
		providerData.Models = append(providerData.Models, storage.QuotaModel{
			Name:              modelName,
			DisplayName:       displayName,
			RemainingPercent:  remainingPercent,
			UsedPercent:       usedPercent,
			Used:              &usedValue,
			Limit:             &limitValue,
			Remaining:         &remainingValue,
			ResetTime:         formatResetTime(firstPositiveUnixTimestamp(breakdown.NextDateReset, payload.NextDateReset)),
			TimeBoundaryKind:  "reset",
			QuotaKind:         "absolute-credits",
			DisplayUnit:       "requests",
			Source:            "kiro_usage_api",
			SourceDescription: "Kiro usage breakdown from codewhisperer usage API",
		})
	}

	return storage.QuotaData{
		UpdatedAt:    now,
		ProviderData: providerData,
	}, nil
}

func (k *Kiro) fetchUsageAttempts(ctx context.Context, token, profileARN, region string) []kiroUsageAttemptResult {
	results := []kiroUsageAttemptResult{
		k.fetchUsageGETCodeWhisperer(ctx, token, profileARN, region),
		k.fetchUsagePOSTCodeWhisperer(ctx, token, profileARN),
		k.fetchUsageGETQEndpoint(ctx, token, profileARN, region),
	}
	return results
}

func (k *Kiro) fetchUsageGETCodeWhisperer(ctx context.Context, token, profileARN, region string) kiroUsageAttemptResult {
	requestURL, err := url.Parse(kiroUsageURL)
	if err != nil {
		return kiroUsageAttemptResult{name: "codewhisperer-get", err: err}
	}
	query := requestURL.Query()
	query.Set("isEmailRequired", "true")
	query.Set("origin", kiroUsageRequestOrigin)
	query.Set("resourceType", kiroUsageResourceType)
	if trimmedARN := strings.TrimSpace(profileARN); trimmedARN != "" {
		query.Set("profileArn", trimmedARN)
	}
	if strings.TrimSpace(region) != "" {
		query.Set("region", strings.TrimSpace(region))
	}
	requestURL.RawQuery = query.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, requestURL.String(), nil)
	if err != nil {
		return kiroUsageAttemptResult{name: "codewhisperer-get", err: err}
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("User-Agent", kiroUsageUserAgent)
	req.Header.Set("x-amz-user-agent", kiroUsageAmzUserAgent)

	resp, err := k.client.Do(req)
	if err != nil {
		return kiroUsageAttemptResult{name: "codewhisperer-get", err: err}
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return kiroUsageAttemptResult{name: "codewhisperer-get", status: resp.StatusCode, err: err}
	}
	return kiroUsageAttemptResult{name: "codewhisperer-get", status: resp.StatusCode, raw: raw}
}

func (k *Kiro) fetchUsagePOSTCodeWhisperer(ctx context.Context, token, profileARN string) kiroUsageAttemptResult {
	body := map[string]string{
		"isEmailRequired": "true",
		"origin":          kiroUsageRequestOrigin,
		"resourceType":    kiroUsageResourceType,
	}
	if trimmedARN := strings.TrimSpace(profileARN); trimmedARN != "" {
		body["profileArn"] = trimmedARN
	}
	rawBody, err := json.Marshal(body)
	if err != nil {
		return kiroUsageAttemptResult{name: "codewhisperer-post", err: err}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, kiroUsageRootURL, bytes.NewReader(rawBody))
	if err != nil {
		return kiroUsageAttemptResult{name: "codewhisperer-post", err: err}
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/x-amz-json-1.0")
	req.Header.Set("x-amz-target", "AmazonCodeWhispererService.GetUsageLimits")
	resp, err := k.client.Do(req)
	if err != nil {
		return kiroUsageAttemptResult{name: "codewhisperer-post", err: err}
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return kiroUsageAttemptResult{name: "codewhisperer-post", status: resp.StatusCode, err: err}
	}
	return kiroUsageAttemptResult{name: "codewhisperer-post", status: resp.StatusCode, raw: raw}
}

func (k *Kiro) fetchUsageGETQEndpoint(ctx context.Context, token, profileARN, region string) kiroUsageAttemptResult {
	resolvedRegion := strings.TrimSpace(region)
	if resolvedRegion == "" {
		resolvedRegion = kiroauth.DefaultRegion
	}
	requestURL, err := url.Parse(fmt.Sprintf("https://q.%s.amazonaws.com/getUsageLimits", resolvedRegion))
	if err != nil {
		return kiroUsageAttemptResult{name: "q-get", err: err}
	}
	query := requestURL.Query()
	query.Set("isEmailRequired", "true")
	query.Set("origin", kiroUsageRequestOrigin)
	query.Set("resourceType", kiroUsageResourceType)
	if trimmedARN := strings.TrimSpace(profileARN); trimmedARN != "" {
		query.Set("profileArn", trimmedARN)
	}
	requestURL.RawQuery = query.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, requestURL.String(), nil)
	if err != nil {
		return kiroUsageAttemptResult{name: "q-get", err: err}
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/json")
	resp, err := k.client.Do(req)
	if err != nil {
		return kiroUsageAttemptResult{name: "q-get", err: err}
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return kiroUsageAttemptResult{name: "q-get", status: resp.StatusCode, err: err}
	}
	return kiroUsageAttemptResult{name: "q-get", status: resp.StatusCode, raw: raw}
}

func buildKiroQuotaUnavailableData(reason string) storage.QuotaData {
	now := time.Now().UTC()
	return storage.QuotaData{
		UpdatedAt: now,
		ProviderData: &storage.ProviderQuotaData{
			Models:          []storage.QuotaModel{},
			LastUpdated:     now.Format(time.RFC3339),
			IsForbidden:     false,
			PlanType:        "quota-unavailable",
			PlanDisplayName: "Quota unavailable",
			AccountLabel:    "",
		},
		Error: fmt.Sprintf("kiro_quota_profile_missing: %s", strings.TrimSpace(reason)),
	}
}

func firstPositive(values ...float64) float64 {
	for _, value := range values {
		if value > 0 {
			return value
		}
	}
	return 0
}

func firstPositiveUnixTimestamp(values ...float64) int64 {
	for _, value := range values {
		if value > 0 {
			return int64(value)
		}
	}
	return 0
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		trimmed := strings.TrimSpace(value)
		if trimmed != "" {
			return trimmed
		}
	}
	return ""
}

var curatedKiroModelIDs = []string{
	"auto",
	"claude-sonnet-4.5",
	"claude-sonnet-4",
	"claude-haiku-4.5",
	"deepseek-3.2",
	"qwen3-coder-next",
	"glm-5",
	"minimax-m2.5",
	"minimax-m2.1",
}

func CuratedKiroModelIDs() []string {
	return append([]string(nil), curatedKiroModelIDs...)
}

var kiroProviderSpecs = []Spec{
	{
		ID:             "kiro",
		OpenAICompat:   true,
		DefaultBaseURL: "https://codewhisperer.us-east-1.amazonaws.com/generateAssistantResponse",
		Quota: QuotaSpec{
			Supported: true,
			Strategy:  "kiro",
		},
		Runtime: RuntimeSpec{
			Protocols: []string{"openai"},
			OpenAI: OpenAIStrategy{
				DefaultBaseURL: "https://codewhisperer.us-east-1.amazonaws.com/generateAssistantResponse",
				AuthHeader:     "Authorization",
				AuthPrefix:     "Bearer ",
				ExtraHeaders: map[string]string{
					"Accept":           "application/vnd.amazon.eventstream",
					"X-Amz-Target":     "AmazonCodeWhispererStreamingService.GenerateAssistantResponse",
					"User-Agent":       "AWS-SDK-JS/3.0.0 kiro-ide/1.0.0",
					"X-Amz-User-Agent": "aws-sdk-js/3.0.0 kiro-ide/1.0.0",
				},
			},
		},
	},
}
