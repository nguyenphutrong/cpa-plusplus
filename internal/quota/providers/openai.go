package providers

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/quota/storage"
)

const (
	openAIPanicUsageURL = "https://chatgpt.com/backend-api/wham/usage"
)

type openAIUsageResponse struct {
	PlanType  string           `json:"plan_type"`
	RateLimit *openAIRateLimit `json:"rate_limit"`
}

type openAIRateLimit struct {
	Allowed         bool          `json:"allowed"`
	LimitReached    bool          `json:"limit_reached"`
	PrimaryWindow   *openAIWindow `json:"primary_window"`
	SecondaryWindow *openAIWindow `json:"secondary_window"`
}

type openAIWindow struct {
	UsedPercent        float64 `json:"used_percent"`
	LimitWindowSeconds int     `json:"limit_window_seconds"`
	ResetAfterSeconds  int     `json:"reset_after_seconds"`
	ResetAt            int64   `json:"reset_at"`
}

type OpenAI struct {
	client   *http.Client
	provider string
}

func NewOpenAI(client *http.Client) *OpenAI {
	return NewOpenAIForProvider("openai", client)
}

func NewOpenAIForProvider(provider string, client *http.Client) *OpenAI {
	if client == nil {
		client = http.DefaultClient
	}
	return &OpenAI{client: client, provider: provider}
}

func (o *OpenAI) Provider() string {
	return o.provider
}

func (o *OpenAI) Fetch(ctx context.Context, credential QuotaFetchInput) (storage.QuotaData, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, openAIPanicUsageURL, nil)
	if err != nil {
		return storage.QuotaData{}, err
	}

	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Bearer "+credential.Secret)

	resp, err := o.client.Do(req)
	if err != nil {
		return storage.QuotaData{}, err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusUnauthorized {
		return storage.QuotaData{}, ErrUnauthorized
	}
	if resp.StatusCode == http.StatusTooManyRequests {
		return storage.QuotaData{}, ErrRateLimited
	}
	if resp.StatusCode != http.StatusOK {
		return storage.QuotaData{}, fmt.Errorf("unexpected status code: %d", resp.StatusCode)
	}

	var usage openAIUsageResponse
	if err := json.NewDecoder(resp.Body).Decode(&usage); err != nil {
		return storage.QuotaData{}, fmt.Errorf("decode response: %w", err)
	}

	return o.convertToQuotaData(usage), nil
}

func (o *OpenAI) convertToQuotaData(usage openAIUsageResponse) storage.QuotaData {
	pData := storage.ProviderQuotaData{
		IsForbidden: false,
		PlanType:    usage.PlanType,
		LastUpdated: time.Now().UTC().Format(time.RFC3339),
		Models:      []storage.QuotaModel{},
	}
	pData.PlanDisplayName = usage.PlanType

	if usage.RateLimit != nil {
		pData.IsForbidden = usage.RateLimit.LimitReached

		if usage.RateLimit.PrimaryWindow != nil {
			remainingPercent, usedPercent := quotaPercentPointers(100 - usage.RateLimit.PrimaryWindow.UsedPercent)
			pData.Models = append(pData.Models, storage.QuotaModel{
				Name:             "codex-session",
				DisplayName:      "Session",
				RemainingPercent: remainingPercent,
				UsedPercent:      usedPercent,
				ResetTime:        formatResetTime(usage.RateLimit.PrimaryWindow.ResetAt),
				TimeBoundaryKind: "reset",
				QuotaKind:        "window",
				DisplayUnit:      "percent",
				Source:           "chatgpt_usage_api",
			})
		}

		if usage.RateLimit.SecondaryWindow != nil {
			remainingPercent, usedPercent := quotaPercentPointers(100 - usage.RateLimit.SecondaryWindow.UsedPercent)
			pData.Models = append(pData.Models, storage.QuotaModel{
				Name:             "codex-weekly",
				DisplayName:      "Weekly",
				RemainingPercent: remainingPercent,
				UsedPercent:      usedPercent,
				ResetTime:        formatResetTime(usage.RateLimit.SecondaryWindow.ResetAt),
				TimeBoundaryKind: "reset",
				QuotaKind:        "window",
				DisplayUnit:      "percent",
				Source:           "chatgpt_usage_api",
			})
		}
	}

	return storage.QuotaData{
		UpdatedAt:    time.Now(),
		ProviderData: &pData,
	}
}

var openAIProviderSpecs = []Spec{}
