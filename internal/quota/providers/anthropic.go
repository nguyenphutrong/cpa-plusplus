package providers

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/quota/storage"
)

const anthropicUsageURL = "https://api.anthropic.com/api/oauth/usage"

type anthropicUsageResponse struct {
	Type           string               `json:"type"`
	FiveHour       *anthropicQuotaUsage `json:"five_hour"`
	SevenDay       *anthropicQuotaUsage `json:"seven_day"`
	SevenDaySonnet *anthropicQuotaUsage `json:"seven_day_sonnet"`
	SevenDayOpus   *anthropicQuotaUsage `json:"seven_day_opus"`
	ExtraUsage     *anthropicExtraUsage `json:"extra_usage"`
	Error          *anthropicError      `json:"error"`
}

type anthropicError struct {
	Type    string `json:"type"`
	Message string `json:"message"`
}

type anthropicQuotaUsage struct {
	Utilization float64 `json:"utilization"`
	ResetsAt    string  `json:"resets_at"`
}

type anthropicExtraUsage struct {
	IsEnabled    bool    `json:"is_enabled"`
	MonthlyLimit float64 `json:"monthly_limit"`
	UsedCredits  float64 `json:"used_credits"`
	Utilization  float64 `json:"utilization"`
}

type Anthropic struct {
	client *http.Client
}

func NewAnthropic(client *http.Client) *Anthropic {
	if client == nil {
		client = http.DefaultClient
	}
	return &Anthropic{client: client}
}

func (a *Anthropic) Provider() string {
	return "anthropic"
}

func (a *Anthropic) Fetch(ctx context.Context, credential QuotaFetchInput) (storage.QuotaData, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, anthropicUsageURL, nil)
	if err != nil {
		return storage.QuotaData{}, err
	}

	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+credential.Secret)
	req.Header.Set("anthropic-beta", "oauth-2025-04-20")
	req.Header.Set("User-Agent", "claude-code/2.1.69")

	resp, err := a.client.Do(req)
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

	var usage anthropicUsageResponse
	if err := json.NewDecoder(resp.Body).Decode(&usage); err != nil {
		return storage.QuotaData{}, fmt.Errorf("decode response: %w", err)
	}

	if usage.Type == "error" {
		if usage.Error != nil && usage.Error.Type == "authentication_error" {
			return storage.QuotaData{}, ErrUnauthorized
		}
		msg := "API error"
		if usage.Error != nil && usage.Error.Message != "" {
			msg = usage.Error.Message
		}
		return storage.QuotaData{}, fmt.Errorf("%s", msg)
	}

	return a.convertToQuotaData(usage), nil
}

func (a *Anthropic) convertToQuotaData(usage anthropicUsageResponse) storage.QuotaData {
	pData := storage.ProviderQuotaData{
		IsForbidden: false,
		LastUpdated: time.Now().UTC().Format(time.RFC3339),
		Models:      []storage.QuotaModel{},
	}

	parseUsage := func(name string, u *anthropicQuotaUsage) {
		if u == nil {
			return
		}
		remainingPercent, usedPercent := quotaPercentPointers(100 - u.Utilization)
		pData.Models = append(pData.Models, storage.QuotaModel{
			Name:             name,
			DisplayName:      name,
			RemainingPercent: remainingPercent,
			UsedPercent:      usedPercent,
			ResetTime:        u.ResetsAt,
			TimeBoundaryKind: "reset",
			QuotaKind:        "window",
			DisplayUnit:      "percent",
			Source:           "anthropic_usage_api",
		})
	}

	parseUsage("five-hour-session", usage.FiveHour)
	parseUsage("seven-day-weekly", usage.SevenDay)
	parseUsage("seven-day-sonnet", usage.SevenDaySonnet)
	parseUsage("seven-day-opus", usage.SevenDayOpus)

	if usage.ExtraUsage != nil && usage.ExtraUsage.IsEnabled {
		remainingPercent, usedPercent := quotaPercentPointers(100 - usage.ExtraUsage.Utilization)
		model := storage.QuotaModel{
			Name:              "extra-usage",
			DisplayName:       "Extra Usage",
			RemainingPercent:  remainingPercent,
			UsedPercent:       usedPercent,
			Used:              floatPtr(usage.ExtraUsage.UsedCredits),
			Limit:             floatPtr(usage.ExtraUsage.MonthlyLimit),
			Remaining:         floatPtr(max(0.0, usage.ExtraUsage.MonthlyLimit-usage.ExtraUsage.UsedCredits)),
			QuotaKind:         "absolute-credits",
			DisplayUnit:       "usd",
			RemainingValue:    floatPtr(max(0.0, usage.ExtraUsage.MonthlyLimit-usage.ExtraUsage.UsedCredits)),
			LimitValue:        floatPtr(usage.ExtraUsage.MonthlyLimit),
			Source:            "anthropic_usage_api",
			SourceDescription: "Additional monthly spend allowance",
			TimeBoundaryKind:  "reset",
		}
		pData.Models = append(pData.Models, model)
	}

	return storage.QuotaData{
		UpdatedAt:    time.Now(),
		ProviderData: &pData,
	}
}

var anthropicProviderSpecs = []Spec{}
