package providers

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/quota/storage"
)

const (
	opencodeGoBaseURL   = "https://opencode.ai"
	opencodeGoUserAgent = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/143.0.0.0 Safari/537.36"
)

var opencodeGoAllowedCookieNames = map[string]bool{
	"auth":        true,
	"__Host-auth": true,
	"oc_locale":   true,
}

var (
	opencodeGoPercentExpr = regexp.MustCompile(`(?is)["']?(usagePercent|usedPercent|percentUsed|percent|usage_percent|used_percent|utilization|utilizationPercent|utilization_percent|usage)["']?\s*[:=]\s*([0-9]+(?:\.[0-9]+)?)`)
	opencodeGoResetInExpr = regexp.MustCompile(`(?is)["']?(resetInSec|resetInSeconds|resetSeconds|reset_sec|reset_in_sec|resetsInSec|resetsInSeconds|resetIn|resetSec)["']?\s*[:=]\s*([0-9]+)`)
	opencodeGoResetAtExpr = regexp.MustCompile(`(?is)["']?(resetAt|resetsAt|reset_at|resets_at|nextReset|next_reset|renewAt|renew_at)["']?\s*[:=]\s*["']([^"']+)["']`)
)

type OpenCodeGo struct {
	client *http.Client
	logger *slog.Logger
}

type opencodeGoWindow struct {
	name             string
	displayName      string
	resetAt          time.Time
	remainingPercent float64
	usedPercent      float64
	duration         time.Duration
}

func NewOpenCodeGo(client *http.Client, logger *slog.Logger) *OpenCodeGo {
	if client == nil {
		client = http.DefaultClient
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &OpenCodeGo{client: client, logger: logger}
}

func (o *OpenCodeGo) Provider() string {
	return "opencode-go"
}

func (o *OpenCodeGo) Fetch(ctx context.Context, credential QuotaFetchInput) (storage.QuotaData, error) {
	now := time.Now().UTC()
	workspaceID := inputString(credential, "workspace_id", "opencode_go_workspace_id")
	authCookie := inputString(credential, "auth_cookie", "opencode_go_auth_cookie", "cookie")

	if workspaceID == "" {
		o.logger.Info("opencode-go quota fetch skipped: missing workspace id", slog.String("credential_id", credential.CredentialID))
		return storage.QuotaData{
			UpdatedAt: now,
			Error:     "quota not configured: missing workspace_id auth metadata",
		}, nil
	}
	if authCookie == "" {
		o.logger.Info("opencode-go quota fetch skipped: missing auth cookie", slog.String("credential_id", credential.CredentialID))
		return storage.QuotaData{
			UpdatedAt: now,
			Error:     "quota not configured: missing auth_cookie auth metadata",
		}, nil
	}

	requestCookieHeader := normalizeOpenCodeGoCookieHeader(authCookie)
	if requestCookieHeader == "" {
		return storage.QuotaData{}, fmt.Errorf("opencode-go quota cookie header missing auth or __Host-auth cookie")
	}

	targetURL := fmt.Sprintf("%s/workspace/%s/go", opencodeGoBaseURL, url.PathEscape(workspaceID))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, targetURL, nil)
	if err != nil {
		return storage.QuotaData{}, err
	}
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8")
	req.Header.Set("Accept-Language", "en-US,en;q=0.9")
	req.Header.Set("Cache-Control", "no-cache")
	req.Header.Set("Pragma", "no-cache")
	req.Header.Set("Cookie", requestCookieHeader)
	req.Header.Set("Origin", opencodeGoBaseURL)
	req.Header.Set("User-Agent", opencodeGoUserAgent)
	req.Header.Set("Referer", targetURL)
	req.Header.Set("Upgrade-Insecure-Requests", "1")

	resp, err := o.client.Do(req)
	if err != nil {
		return storage.QuotaData{}, err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		o.logger.Warn("opencode-go quota fetch rejected", slog.String("credential_id", credential.CredentialID), slog.Int("status_code", resp.StatusCode))
		return storage.QuotaData{}, ErrUnauthorized
	}
	if resp.StatusCode != http.StatusOK {
		return storage.QuotaData{}, fmt.Errorf("opencode-go quota request failed (HTTP %d)", resp.StatusCode)
	}

	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return storage.QuotaData{}, fmt.Errorf("read response: %w", err)
	}
	body := string(bodyBytes)
	if looksSignedOut(body) {
		o.logger.Warn("opencode-go quota fetch looks signed out", slog.String("credential_id", credential.CredentialID))
		return storage.QuotaData{}, ErrUnauthorized
	}

	windows, err := parseOpenCodeGoWindows(body, now)
	if err != nil {
		snippet := strings.TrimSpace(body)
		if len(snippet) > 400 {
			snippet = snippet[:400]
		}
		o.logger.Warn(
			"opencode-go quota parse failed",
			slog.String("credential_id", credential.CredentialID),
			slog.String("error", err.Error()),
			slog.String("body_snippet", snippet),
		)
		return storage.QuotaData{}, err
	}
	o.logger.Info("opencode-go quota fetch succeeded", slog.String("credential_id", credential.CredentialID), slog.Int("window_count", len(windows)))
	return buildOpenCodeGoQuotaData(windows, now), nil
}

func looksSignedOut(body string) bool {
	lower := strings.ToLower(body)
	return strings.Contains(lower, "sign in") ||
		strings.Contains(lower, "log in") ||
		strings.Contains(lower, "login") ||
		strings.Contains(lower, "session expired") ||
		strings.Contains(lower, "<title>openauth</title>") ||
		strings.Contains(lower, "auth/authorize")
}

func normalizeOpenCodeGoCookieHeader(raw string) string {
	pairs := strings.Split(raw, ";")
	filtered := make([]string, 0, len(pairs))
	for _, pair := range pairs {
		trimmed := strings.TrimSpace(pair)
		if trimmed == "" {
			continue
		}
		name, _, found := strings.Cut(trimmed, "=")
		if !found {
			continue
		}
		if !opencodeGoAllowedCookieNames[strings.TrimSpace(name)] {
			continue
		}
		filtered = append(filtered, trimmed)
	}
	if len(filtered) == 0 {
		return ""
	}
	return strings.Join(filtered, "; ")
}

func parseOpenCodeGoWindows(body string, now time.Time) ([]opencodeGoWindow, error) {
	windows := make([]opencodeGoWindow, 0, 3)
	candidates := []struct {
		name        string
		displayName string
		aliases     []string
		duration    time.Duration
		required    bool
	}{
		{name: "session", displayName: "Session", aliases: []string{"rollingUsage", "rolling_usage", "rollingWindow", "rolling_window", "rolling", "5h", "5-hour"}, duration: 5 * time.Hour, required: true},
		{name: "weekly", displayName: "Weekly", aliases: []string{"weeklyUsage", "weekly_usage", "weeklyWindow", "weekly_window", "weekly", "week"}, duration: 7 * 24 * time.Hour, required: true},
		{name: "monthly", displayName: "Monthly", aliases: []string{"monthlyUsage", "monthly_usage", "monthlyWindow", "monthly_window", "monthly", "month"}, duration: 30 * 24 * time.Hour, required: false},
	}

	if parsed := parseOpenCodeGoWindowsFromRegex(body, now); len(parsed) > 0 {
		for _, candidate := range candidates {
			window, ok := parsed[candidate.name]
			if !ok {
				if candidate.required {
					return nil, fmt.Errorf("opencode-go quota payload missing %s usage window", candidate.name)
				}
				continue
			}
			window.name = candidate.name
			window.displayName = candidate.displayName
			window.duration = candidate.duration
			windows = append(windows, window)
		}
		if len(windows) >= 2 {
			return windows, nil
		}
		windows = windows[:0]
	}

	if parsed := parseOpenCodeGoWindowsFromJSON(body, now); len(parsed) > 0 {
		for _, candidate := range candidates {
			window, ok := parsed[candidate.name]
			if !ok {
				if candidate.required {
					return nil, fmt.Errorf("opencode-go quota payload missing %s usage window", candidate.name)
				}
				continue
			}
			window.name = candidate.name
			window.displayName = candidate.displayName
			window.duration = candidate.duration
			windows = append(windows, window)
		}
		if len(windows) >= 2 {
			return windows, nil
		}
		windows = windows[:0]
	}

	for _, candidate := range candidates {
		window, ok := extractOpenCodeGoWindow(body, now, candidate.aliases, candidate.name, candidate.displayName, candidate.duration)
		if !ok {
			if candidate.required {
				return nil, fmt.Errorf("opencode-go quota payload missing %s usage window", candidate.name)
			}
			continue
		}
		windows = append(windows, window)
	}
	if len(windows) < 2 {
		return nil, fmt.Errorf("opencode-go quota payload missing required usage windows")
	}
	return windows, nil
}

func extractOpenCodeGoWindow(body string, now time.Time, aliases []string, name string, displayName string, duration time.Duration) (opencodeGoWindow, bool) {
	for _, alias := range aliases {
		index := strings.Index(strings.ToLower(body), strings.ToLower(alias))
		if index < 0 {
			continue
		}
		end := index + 2400
		if end > len(body) {
			end = len(body)
		}
		segment := body[index:end]
		usedPercent, ok := extractFirstFloat(segment, opencodeGoPercentExpr)
		if !ok {
			continue
		}
		resetAt, ok := extractWindowReset(segment, now)
		if !ok {
			continue
		}
		clampedUsed := clampPercent(usedPercent)
		return opencodeGoWindow{
			name:             name,
			displayName:      displayName,
			resetAt:          resetAt,
			usedPercent:      clampedUsed,
			remainingPercent: clampPercent(100 - clampedUsed),
			duration:         duration,
		}, true
	}
	return opencodeGoWindow{}, false
}

func parseOpenCodeGoWindowsFromRegex(body string, now time.Time) map[string]opencodeGoWindow {
	result := map[string]opencodeGoWindow{}
	if session, ok := extractOpenCodeGoWindowByPattern(
		body,
		now,
		`(?is)rollingUsage[^}]*?usagePercent\s*:\s*([0-9]+(?:\.[0-9]+)?)`,
		`(?is)rollingUsage[^}]*?resetInSec\s*:\s*([0-9]+)`,
	); ok {
		result["session"] = session
	}
	if weekly, ok := extractOpenCodeGoWindowByPattern(
		body,
		now,
		`(?is)weeklyUsage[^}]*?usagePercent\s*:\s*([0-9]+(?:\.[0-9]+)?)`,
		`(?is)weeklyUsage[^}]*?resetInSec\s*:\s*([0-9]+)`,
	); ok {
		result["weekly"] = weekly
	}
	if monthly, ok := extractOpenCodeGoWindowByPattern(
		body,
		now,
		`(?is)monthlyUsage[^}]*?usagePercent\s*:\s*([0-9]+(?:\.[0-9]+)?)`,
		`(?is)monthlyUsage[^}]*?resetInSec\s*:\s*([0-9]+)`,
	); ok {
		result["monthly"] = monthly
	}
	return result
}

func extractOpenCodeGoWindowByPattern(body string, now time.Time, percentPattern string, resetPattern string) (opencodeGoWindow, bool) {
	usedPercent := extractPatternFloat(body, percentPattern)
	resetInSec := extractPatternInt(body, resetPattern)
	if usedPercent == nil || resetInSec == nil {
		return opencodeGoWindow{}, false
	}
	clampedUsed := clampPercent(*usedPercent)
	return opencodeGoWindow{
		resetAt:          now.Add(time.Duration(*resetInSec) * time.Second),
		usedPercent:      clampedUsed,
		remainingPercent: clampPercent(100 - clampedUsed),
	}, true
}

type opencodeGoWindowCandidate struct {
	window    opencodeGoWindow
	pathLower string
}

func parseOpenCodeGoWindowsFromJSON(body string, now time.Time) map[string]opencodeGoWindow {
	object, ok := parseEmbeddedJSONObject(body)
	if !ok {
		return nil
	}
	candidates := make([]opencodeGoWindowCandidate, 0, 8)
	collectOpenCodeGoWindowCandidates(object, now, nil, &candidates)
	if len(candidates) == 0 {
		return nil
	}

	result := map[string]opencodeGoWindow{}
	pick := func(keys []string, fallback bool) (opencodeGoWindow, bool) {
		filtered := make([]opencodeGoWindowCandidate, 0, len(candidates))
		for _, candidate := range candidates {
			for _, key := range keys {
				if strings.Contains(candidate.pathLower, key) {
					filtered = append(filtered, candidate)
					break
				}
			}
		}
		if len(filtered) == 0 && fallback {
			filtered = candidates
		}
		if len(filtered) == 0 {
			return opencodeGoWindow{}, false
		}
		best := filtered[0]
		for _, candidate := range filtered[1:] {
			if candidate.window.resetAt.Before(best.window.resetAt) {
				best = candidate
			}
		}
		return best.window, true
	}

	if rolling, ok := pick([]string{"rolling", "hour", "5h", "5-hour"}, true); ok {
		result["session"] = rolling
	}
	if weekly, ok := pick([]string{"weekly", "week"}, false); ok {
		result["weekly"] = weekly
	}
	if monthly, ok := pick([]string{"monthly", "month"}, false); ok {
		result["monthly"] = monthly
	}
	return result
}

func extractPatternFloat(body string, pattern string) *float64 {
	expr, err := regexp.Compile(pattern)
	if err != nil {
		return nil
	}
	match := expr.FindStringSubmatch(body)
	if len(match) < 2 {
		return nil
	}
	value, err := strconv.ParseFloat(match[1], 64)
	if err != nil {
		return nil
	}
	return floatPtr(value)
}

func extractPatternInt(body string, pattern string) *int {
	expr, err := regexp.Compile(pattern)
	if err != nil {
		return nil
	}
	match := expr.FindStringSubmatch(body)
	if len(match) < 2 {
		return nil
	}
	value, err := strconv.Atoi(match[1])
	if err != nil {
		return nil
	}
	return &value
}

func parseEmbeddedJSONObject(body string) (any, bool) {
	trimmed := strings.TrimSpace(body)
	if trimmed == "" {
		return nil, false
	}
	var object any
	if json.Unmarshal([]byte(trimmed), &object) == nil {
		return object, true
	}

	start := strings.IndexFunc(trimmed, func(r rune) bool {
		return r == '{' || r == '['
	})
	if start < 0 {
		return nil, false
	}
	candidate := trimmed[start:]
	for len(candidate) > 0 {
		if json.Unmarshal([]byte(candidate), &object) == nil {
			return object, true
		}
		candidate = strings.TrimRightFunc(candidate[:len(candidate)-1], unicode.IsSpace)
	}
	return nil, false
}

func collectOpenCodeGoWindowCandidates(object any, now time.Time, path []string, out *[]opencodeGoWindowCandidate) {
	switch typed := object.(type) {
	case map[string]any:
		if window, ok := parseOpenCodeGoWindowObject(typed, now); ok {
			*out = append(*out, opencodeGoWindowCandidate{
				window:    window,
				pathLower: strings.ToLower(strings.Join(path, ".")),
			})
		}
		for key, value := range typed {
			collectOpenCodeGoWindowCandidates(value, now, append(path, key), out)
		}
	case []any:
		for index, value := range typed {
			collectOpenCodeGoWindowCandidates(value, now, append(path, strconv.Itoa(index)), out)
		}
	}
}

func parseOpenCodeGoWindowObject(dict map[string]any, now time.Time) (opencodeGoWindow, bool) {
	if usage, ok := dict["usage"].(map[string]any); ok {
		if parsed, ok := parseOpenCodeGoWindowObject(usage, now); ok {
			return parsed, true
		}
	}

	usedPercent, ok := findOpenCodeGoPercent(dict)
	if !ok {
		return opencodeGoWindow{}, false
	}
	resetAt, ok := findOpenCodeGoResetAt(dict, now)
	if !ok {
		return opencodeGoWindow{}, false
	}
	clampedUsed := clampPercent(usedPercent)
	return opencodeGoWindow{
		resetAt:          resetAt,
		usedPercent:      clampedUsed,
		remainingPercent: clampPercent(100 - clampedUsed),
	}, true
}

func findOpenCodeGoPercent(dict map[string]any) (float64, bool) {
	for _, key := range []string{
		"usagePercent",
		"usedPercent",
		"percentUsed",
		"percent",
		"usage_percent",
		"used_percent",
		"utilization",
		"utilizationPercent",
		"utilization_percent",
		"usage",
	} {
		if value, ok := readOpenCodeGoFloat(dict[key]); ok {
			if value >= 0 && value <= 1 {
				value *= 100
			}
			return value, true
		}
	}

	var used float64
	var limit float64
	var hasUsed bool
	var hasLimit bool
	for _, key := range []string{"used", "consumed", "count", "usedTokens", "currentValue"} {
		if value, ok := readOpenCodeGoFloat(dict[key]); ok {
			used = value
			hasUsed = true
			break
		}
	}
	for _, key := range []string{"limit", "total", "quota", "max", "cap", "tokenLimit", "usage"} {
		if value, ok := readOpenCodeGoFloat(dict[key]); ok && value > 0 {
			limit = value
			hasLimit = true
			break
		}
	}
	if hasUsed && hasLimit {
		return (used / limit) * 100, true
	}
	return 0, false
}

func findOpenCodeGoResetAt(dict map[string]any, now time.Time) (time.Time, bool) {
	for _, key := range []string{
		"resetInSec",
		"resetInSeconds",
		"resetSeconds",
		"reset_sec",
		"reset_in_sec",
		"resetsInSec",
		"resetsInSeconds",
		"resetIn",
		"resetSec",
	} {
		if value, ok := readOpenCodeGoInt(dict[key]); ok && value >= 0 {
			return now.Add(time.Duration(value) * time.Second), true
		}
	}
	for _, key := range []string{
		"resetAt",
		"resetsAt",
		"reset_at",
		"resets_at",
		"nextReset",
		"next_reset",
		"renewAt",
		"renew_at",
	} {
		switch raw := dict[key].(type) {
		case string:
			if parsed, ok := parseOpenCodeGoResetAt(strings.TrimSpace(raw)); ok {
				return parsed, true
			}
		case float64:
			if parsed, ok := parseOpenCodeGoResetAt(strconv.FormatInt(int64(raw), 10)); ok {
				return parsed, true
			}
		case int64:
			if parsed, ok := parseOpenCodeGoResetAt(strconv.FormatInt(raw, 10)); ok {
				return parsed, true
			}
		case int:
			if parsed, ok := parseOpenCodeGoResetAt(strconv.Itoa(raw)); ok {
				return parsed, true
			}
		}
	}
	return time.Time{}, false
}

func readOpenCodeGoFloat(value any) (float64, bool) {
	switch typed := value.(type) {
	case float64:
		return typed, true
	case float32:
		return float64(typed), true
	case int:
		return float64(typed), true
	case int64:
		return float64(typed), true
	case json.Number:
		parsed, err := typed.Float64()
		return parsed, err == nil
	case string:
		parsed, err := strconv.ParseFloat(strings.TrimSpace(typed), 64)
		return parsed, err == nil
	default:
		return 0, false
	}
}

func readOpenCodeGoInt(value any) (int, bool) {
	switch typed := value.(type) {
	case int:
		return typed, true
	case int64:
		return int(typed), true
	case float64:
		return int(typed), true
	case json.Number:
		parsed, err := typed.Int64()
		return int(parsed), err == nil
	case string:
		parsed, err := strconv.Atoi(strings.TrimSpace(typed))
		return parsed, err == nil
	default:
		return 0, false
	}
}

func extractFirstFloat(input string, expr *regexp.Regexp) (float64, bool) {
	match := expr.FindStringSubmatch(input)
	if len(match) < 3 {
		return 0, false
	}
	value, err := strconv.ParseFloat(match[2], 64)
	if err != nil {
		return 0, false
	}
	return value, true
}

func extractWindowReset(input string, now time.Time) (time.Time, bool) {
	if match := opencodeGoResetAtExpr.FindStringSubmatch(input); len(match) >= 3 {
		resetAt := strings.TrimSpace(match[2])
		if parsed, ok := parseOpenCodeGoResetAt(resetAt); ok {
			return parsed, true
		}
	}
	if match := opencodeGoResetInExpr.FindStringSubmatch(input); len(match) >= 3 {
		seconds, err := strconv.Atoi(match[2])
		if err == nil && seconds > 0 {
			return now.Add(time.Duration(seconds) * time.Second), true
		}
	}
	return time.Time{}, false
}

func parseOpenCodeGoResetAt(value string) (time.Time, bool) {
	if value == "" {
		return time.Time{}, false
	}
	if millis, err := strconv.ParseInt(value, 10, 64); err == nil && millis > 0 {
		if millis > 1_000_000_000_000 {
			return time.UnixMilli(millis).UTC(), true
		}
		return time.Unix(millis, 0).UTC(), true
	}
	formats := []string{time.RFC3339, time.RFC3339Nano}
	for _, format := range formats {
		if parsed, err := time.Parse(format, value); err == nil {
			return parsed.UTC(), true
		}
	}
	return time.Time{}, false
}

func buildOpenCodeGoQuotaData(windows []opencodeGoWindow, now time.Time) storage.QuotaData {
	models := make([]storage.QuotaModel, 0, len(windows))
	var selected *opencodeGoWindow

	for i := range windows {
		window := windows[i]
		if window.resetAt.IsZero() {
			continue
		}
		if selected == nil || window.resetAt.Before(selected.resetAt) {
			selected = &windows[i]
		}
		limit := 100.0
		used := window.usedPercent
		remaining := window.remainingPercent
		models = append(models, storage.QuotaModel{
			Name:                 window.name,
			DisplayName:          window.displayName,
			RemainingPercent:     floatPtr(remaining),
			UsedPercent:          floatPtr(used),
			Used:                 floatPtr(used),
			Limit:                floatPtr(limit),
			Remaining:            floatPtr(remaining),
			ResetTime:            window.resetAt.UTC().Format(time.RFC3339),
			TimeBoundaryKind:     "reset",
			QuotaKind:            "percent-window",
			DisplayUnit:          "percent",
			RemainingValue:       floatPtr(remaining),
			LimitValue:           floatPtr(limit),
			ReplenishRatePerHour: floatPtr(limit / window.duration.Hours()),
			Source:               "opencode_go_web_usage",
			SourceDescription:    "OpenCode Go workspace usage page",
		})
	}

	quota := storage.QuotaData{
		UpdatedAt: now,
		ProviderData: &storage.ProviderQuotaData{
			LastUpdated: now.Format(time.RFC3339),
			PlanType:    "Go",
			Models:      models,
		},
	}
	quota.ProviderData.PlanDisplayName = "OpenCode Go"
	if selected != nil {
		quota.QuotaLimit = 100
		quota.QuotaUsed = selected.usedPercent
		quota.QuotaRemaining = selected.remainingPercent
		quota.Percentage = selected.usedPercent
		quota.ResetAt = selected.resetAt.UTC()
	}
	return quota
}

var opencodeGoProviderSpecs = []Spec{
	{
		ID:             "opencode-go",
		OpenAICompat:   true,
		DefaultBaseURL: "https://opencode.ai/zen/go",
		Quota: QuotaSpec{
			Supported: true,
			Strategy:  "opencode-go",
		},
		Runtime: RuntimeSpec{
			Protocols: []string{"openai", "anthropic"},
			OpenAI: OpenAIStrategy{
				DefaultBaseURL: "https://opencode.ai/zen/go",
				AuthHeader:     "Authorization",
				AuthPrefix:     "Bearer ",
			},
			Anthropic: AnthropicStrategy{
				DefaultBaseURL: "https://opencode.ai/zen/go",
				AuthHeader:     "x-api-key",
				AuthPrefix:     "",
				ExtraHeaders: map[string]string{
					"anthropic-version": "2023-06-01",
				},
			},
		},
	},
}
