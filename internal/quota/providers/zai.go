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
	zaiSubscriptionURL = "https://api.z.ai/api/biz/subscription/list"
	zaiQuotaURL        = "https://api.z.ai/api/monitor/usage/quota/limit"
	zaiSessionWindow   = 5 * time.Hour
	zaiWeeklyWindow    = 7 * 24 * time.Hour
	zaiMonthlyWindow   = 30 * 24 * time.Hour
)

type ZAI struct {
	client *http.Client
}

func NewZAI(client *http.Client) *ZAI {
	if client == nil {
		client = http.DefaultClient
	}
	return &ZAI{client: client}
}

func (z *ZAI) Provider() string {
	return "z-ai"
}

func (z *ZAI) Fetch(ctx context.Context, credential QuotaFetchInput) (storage.QuotaData, error) {
	planName, _ := z.fetchSubscription(ctx, credential.Secret)
	quotaPayload, err := z.fetchQuota(ctx, credential.Secret)
	if err != nil {
		return storage.QuotaData{}, err
	}
	return storage.QuotaData{
		UpdatedAt:    time.Now().UTC(),
		ProviderData: parseZAIQuota(planName, quotaPayload),
	}, nil
}

func (z *ZAI) fetchSubscription(ctx context.Context, accessToken string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, zaiSubscriptionURL, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Accept", "application/json")
	resp, err := z.client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("subscription request failed: %d", resp.StatusCode)
	}
	var payload struct {
		Data []struct {
			ProductName string `json:"productName"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return "", err
	}
	if len(payload.Data) == 0 {
		return "", nil
	}
	return payload.Data[0].ProductName, nil
}

func (z *ZAI) fetchQuota(ctx context.Context, accessToken string) (map[string]any, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, zaiQuotaURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Accept", "application/json")
	resp, err := z.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return nil, ErrUnauthorized
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("usage request failed (HTTP %d)", resp.StatusCode)
	}
	var payload map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return nil, fmt.Errorf("usage response invalid: %w", err)
	}
	return payload, nil
}

func parseZAIQuota(planName string, payload map[string]any) *storage.ProviderQuotaData {
	now := time.Now().UTC()
	data := &storage.ProviderQuotaData{
		IsForbidden:     false,
		LastUpdated:     now.Format(time.RFC3339),
		PlanType:        firstZAIString(planName, "z-ai"),
		PlanDisplayName: firstZAIString(planName, "Z.AI"),
		Models:          []storage.QuotaModel{},
	}

	container := payload
	if nested, ok := payload["data"].(map[string]any); ok {
		container = nested
	}
	rawLimits, ok := container["limits"]
	if !ok {
		return data
	}
	limits, ok := rawLimits.([]any)
	if !ok {
		return data
	}

	if session := findZAILimit(limits, "TOKENS_LIMIT", 3); session != nil {
		usedPercent := readFloat(session["percentage"])
		resetTime := formatEpochMillis(session["nextResetTime"])
		remainingPercent, usedPercentPtr := quotaPercentPointers(100 - usedPercent)
		model := storage.QuotaModel{
			Name:                 "session",
			DisplayName:          "Session",
			RemainingPercent:     remainingPercent,
			UsedPercent:          usedPercentPtr,
			Limit:                floatPtr(100),
			Used:                 floatPtr(usedPercent),
			ResetTime:            resetTime,
			TimeBoundaryKind:     "reset",
			QuotaKind:            "percent-window",
			DisplayUnit:          "percent",
			RemainingValue:       remainingPercent,
			LimitValue:           floatPtr(100),
			Source:               "zai_quota_api",
			SourceDescription:    "Z.AI coding plan session token usage",
			ReplenishRatePerHour: floatPtr(100 / zaiSessionWindow.Hours()),
		}
		data.Models = append(data.Models, model)
	}

	if weekly := findZAILimit(limits, "TOKENS_LIMIT", 6); weekly != nil {
		usedPercent := readFloat(weekly["percentage"])
		resetTime := formatEpochMillis(weekly["nextResetTime"])
		remainingPercent, usedPercentPtr := quotaPercentPointers(100 - usedPercent)
		model := storage.QuotaModel{
			Name:                 "weekly",
			DisplayName:          "Weekly",
			RemainingPercent:     remainingPercent,
			UsedPercent:          usedPercentPtr,
			Limit:                floatPtr(100),
			Used:                 floatPtr(usedPercent),
			ResetTime:            resetTime,
			TimeBoundaryKind:     "reset",
			QuotaKind:            "percent-window",
			DisplayUnit:          "percent",
			RemainingValue:       remainingPercent,
			LimitValue:           floatPtr(100),
			Source:               "zai_quota_api",
			SourceDescription:    "Z.AI coding plan rolling weekly token usage",
			ReplenishRatePerHour: floatPtr(100 / zaiWeeklyWindow.Hours()),
		}
		data.Models = append(data.Models, model)
	}

	if web := findZAILimit(limits, "TIME_LIMIT", 0); web != nil {
		used := readFloat(web["currentValue"])
		limit := readFloat(web["usage"])
		if limit > 0 {
			resetTime := formatEpochMillis(web["nextResetTime"])
			if resetTime == "" {
				nextMonth := time.Date(now.Year(), now.Month()+1, 1, 0, 0, 0, 0, time.UTC)
				resetTime = nextMonth.Format(time.RFC3339)
			}
			data.Models = append(data.Models, storage.QuotaModel{
				Name:                 "web-searches",
				DisplayName:          "Web Searches",
				Used:                 floatPtr(used),
				Limit:                floatPtr(limit),
				Remaining:            floatPtr(max(0.0, limit-used)),
				ResetTime:            resetTime,
				TimeBoundaryKind:     "reset",
				QuotaKind:            "absolute-credits",
				DisplayUnit:          "count",
				RemainingValue:       floatPtr(max(0.0, limit-used)),
				LimitValue:           floatPtr(limit),
				Source:               "zai_quota_api",
				SourceDescription:    "Z.AI coding plan monthly web searches",
				ReplenishRatePerHour: floatPtr(limit / zaiMonthlyWindow.Hours()),
			})
		}
	}

	return data
}

func findZAILimit(limits []any, kind string, unit float64) map[string]any {
	for _, raw := range limits {
		item, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		if firstZAIString(readString(item["type"]), readString(item["name"])) != kind {
			continue
		}
		if unit == 0 || readFloat(item["unit"]) == unit {
			return item
		}
	}
	return nil
}

func readFloat(value any) float64 {
	switch typed := value.(type) {
	case float64:
		return typed
	case int:
		return float64(typed)
	case int64:
		return float64(typed)
	case json.Number:
		parsed, _ := typed.Float64()
		return parsed
	default:
		return 0
	}
}

func readString(value any) string {
	if typed, ok := value.(string); ok {
		return typed
	}
	return ""
}

func firstZAIString(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

func formatEpochMillis(value any) string {
	millis := int64(readFloat(value))
	if millis <= 0 {
		return ""
	}
	return time.UnixMilli(millis).UTC().Format(time.RFC3339)
}
