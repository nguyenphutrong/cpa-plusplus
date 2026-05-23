package providers

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/quota/storage"
)

const kimiForCodingUsageURL = "https://api.kimi.com/coding/v1/usages"

type kimiForCodingUsageResponse struct {
	Usage *struct {
		Limit     string `json:"limit"`
		Remaining string `json:"remaining"`
		ResetTime string `json:"resetTime"`
	} `json:"usage"`
	Limits []struct {
		Window struct {
			Duration float64 `json:"duration"`
			TimeUnit string  `json:"timeUnit"`
		} `json:"window"`
		Detail struct {
			Limit     string `json:"limit"`
			Remaining string `json:"remaining"`
			ResetTime string `json:"resetTime"`
		} `json:"detail"`
	} `json:"limits"`
	User struct {
		Membership struct {
			Level string `json:"level"`
		} `json:"membership"`
	} `json:"user"`
}

type kimiForCodingWindow struct {
	limit     float64
	remaining float64
	resetTime string
	period    time.Duration
}

type KimiForCoding struct {
	client *http.Client
}

func NewKimiForCoding(client *http.Client) *KimiForCoding {
	if client == nil {
		client = http.DefaultClient
	}
	return &KimiForCoding{client: client}
}

func (k *KimiForCoding) Provider() string {
	return "kimi-for-coding"
}

func (k *KimiForCoding) Fetch(ctx context.Context, credential QuotaFetchInput) (storage.QuotaData, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, kimiForCodingUsageURL, nil)
	if err != nil {
		return storage.QuotaData{}, err
	}
	req.Header.Set("Authorization", "Bearer "+credential.Secret)
	req.Header.Set("Accept", "application/json")

	resp, err := k.client.Do(req)
	if err != nil {
		return storage.QuotaData{}, err
	}
	defer resp.Body.Close()

	switch resp.StatusCode {
	case http.StatusUnauthorized, http.StatusForbidden:
		return storage.QuotaData{}, ErrUnauthorized
	case http.StatusTooManyRequests:
		return storage.QuotaData{}, ErrRateLimited
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return storage.QuotaData{}, fmt.Errorf("usage request failed (HTTP %d)", resp.StatusCode)
	}

	var payload kimiForCodingUsageResponse
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return storage.QuotaData{}, fmt.Errorf("usage response invalid: %w", err)
	}

	return storage.QuotaData{
		UpdatedAt:    time.Now().UTC(),
		ProviderData: parseKimiForCodingQuota(payload),
	}, nil
}

func parseKimiForCodingQuota(payload kimiForCodingUsageResponse) *storage.ProviderQuotaData {
	now := time.Now().UTC()
	planType, planDisplay := parseKimiForCodingPlan(payload.User.Membership.Level)
	data := &storage.ProviderQuotaData{
		IsForbidden:     false,
		LastUpdated:     now.Format(time.RFC3339),
		PlanType:        planType,
		PlanDisplayName: planDisplay,
		Models:          []storage.QuotaModel{},
	}

	windows := make([]kimiForCodingWindow, 0, len(payload.Limits))
	for _, limit := range payload.Limits {
		detail := parseKimiForCodingWindow(limit.Detail.Limit, limit.Detail.Remaining, limit.Detail.ResetTime)
		if detail.limit <= 0 {
			continue
		}
		detail.period = kimiForCodingWindowDuration(limit.Window.Duration, limit.Window.TimeUnit)
		windows = append(windows, detail)
	}

	session := pickKimiForCodingSessionWindow(windows)
	weekly := parseKimiForCodingWindow("", "", "")
	if payload.Usage != nil {
		weekly = parseKimiForCodingWindow(payload.Usage.Limit, payload.Usage.Remaining, payload.Usage.ResetTime)
	}
	if weekly.limit <= 0 {
		weekly = pickKimiForCodingLargestWindow(windows)
	}

	if session.limit > 0 {
		data.Models = append(data.Models, buildKimiForCodingQuotaModel("session", "Session", session))
	}
	if weekly.limit > 0 && !sameKimiForCodingWindow(session, weekly) {
		data.Models = append(data.Models, buildKimiForCodingQuotaModel("weekly", "Weekly", weekly))
	}

	return data
}

func buildKimiForCodingQuotaModel(name, display string, window kimiForCodingWindow) storage.QuotaModel {
	used := max(0, window.limit-window.remaining)
	remainingPercent, usedPercent := quotaPercentPointers((window.remaining / window.limit) * 100)
	return storage.QuotaModel{
		Name:             name,
		DisplayName:      display,
		Used:             floatPtr(used),
		Limit:            floatPtr(window.limit),
		Remaining:        floatPtr(window.remaining),
		RemainingPercent: remainingPercent,
		UsedPercent:      usedPercent,
		ResetTime:        window.resetTime,
		TimeBoundaryKind: "reset",
		QuotaKind:        "percent-window",
		DisplayUnit:      "percent",
		RemainingValue:   remainingPercent,
		LimitValue:       floatPtr(100),
		Source:           "kimi_usage_api",
	}
}

func parseKimiForCodingWindow(limitRaw, remainingRaw, resetTimeRaw string) kimiForCodingWindow {
	limit, _ := parseKimiForCodingNumber(limitRaw)
	remaining, _ := parseKimiForCodingNumber(remainingRaw)
	if remaining < 0 {
		remaining = 0
	}
	if limit > 0 && remaining > limit {
		remaining = limit
	}
	return kimiForCodingWindow{
		limit:     limit,
		remaining: remaining,
		resetTime: normalizeKimiForCodingResetTime(resetTimeRaw),
	}
}

func parseKimiForCodingNumber(value string) (float64, bool) {
	var parsed float64
	if err := json.Unmarshal([]byte(value), &parsed); err != nil {
		return 0, false
	}
	if parsed < 0 {
		parsed = 0
	}
	return parsed, true
}

func normalizeKimiForCodingResetTime(value string) string {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return ""
	}
	if parsed, err := time.Parse(time.RFC3339Nano, trimmed); err == nil {
		return parsed.UTC().Format(time.RFC3339)
	}
	if parsed, err := time.Parse(time.RFC3339, trimmed); err == nil {
		return parsed.UTC().Format(time.RFC3339)
	}
	return trimmed
}

func parseKimiForCodingPlan(level string) (string, string) {
	normalized := strings.TrimSpace(strings.TrimPrefix(strings.ToUpper(level), "LEVEL_"))
	if normalized == "" {
		return "kimi-for-coding", "Kimi For Coding"
	}
	displayWords := strings.Split(strings.ToLower(strings.ReplaceAll(normalized, "_", " ")), " ")
	for i := range displayWords {
		if displayWords[i] == "" {
			continue
		}
		displayWords[i] = strings.ToUpper(displayWords[i][:1]) + displayWords[i][1:]
	}
	display := strings.Join(displayWords, " ")
	return strings.ToLower(strings.ReplaceAll(normalized, "_", "-")), display
}

func kimiForCodingWindowDuration(duration float64, timeUnit string) time.Duration {
	if duration <= 0 {
		return 0
	}
	unit := strings.ToUpper(strings.TrimSpace(timeUnit))
	switch {
	case strings.Contains(unit, "SECOND"):
		return time.Duration(duration * float64(time.Second))
	case strings.Contains(unit, "MINUTE"):
		return time.Duration(duration * float64(time.Minute))
	case strings.Contains(unit, "HOUR"):
		return time.Duration(duration * float64(time.Hour))
	case strings.Contains(unit, "DAY"):
		return time.Duration(duration * 24 * float64(time.Hour))
	default:
		return 0
	}
}

func pickKimiForCodingSessionWindow(windows []kimiForCodingWindow) kimiForCodingWindow {
	var candidate kimiForCodingWindow
	for _, window := range windows {
		if window.limit <= 0 {
			continue
		}
		if candidate.limit == 0 {
			candidate = window
			continue
		}
		if window.period > 0 && (candidate.period == 0 || window.period < candidate.period) {
			candidate = window
		}
	}
	return candidate
}

func pickKimiForCodingLargestWindow(windows []kimiForCodingWindow) kimiForCodingWindow {
	var candidate kimiForCodingWindow
	for _, window := range windows {
		if window.limit <= 0 {
			continue
		}
		if candidate.limit == 0 {
			candidate = window
			continue
		}
		if window.period > candidate.period {
			candidate = window
		}
	}
	return candidate
}

func sameKimiForCodingWindow(left, right kimiForCodingWindow) bool {
	return left.limit == right.limit &&
		left.remaining == right.remaining &&
		left.resetTime == right.resetTime
}

var kimiForCodingProviderSpecs = []Spec{
	{
		ID:             "kimi-for-coding",
		OpenAICompat:   true,
		DefaultBaseURL: "https://api.kimi.com/coding/v1",
		Quota: QuotaSpec{
			Supported: true,
			Strategy:  "kimi-for-coding",
		},
		Runtime: RuntimeSpec{
			Protocols: []string{"openai-compatible", "anthropic"},
			OpenAI: OpenAIStrategy{
				DefaultBaseURL: "https://api.kimi.com/coding/v1",
				AuthHeader:     "Authorization",
				AuthPrefix:     "Bearer ",
			},
			Anthropic: AnthropicStrategy{
				DefaultBaseURL: "https://api.kimi.com/coding",
				AuthHeader:     "x-api-key",
				AuthPrefix:     "",
				ExtraHeaders: map[string]string{
					"anthropic-version": "2023-06-01",
				},
			},
		},
	},
}
