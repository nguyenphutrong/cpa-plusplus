package providers

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/quota/storage"
)

const (
	antigravityBaseURLPrimary = "https://cloudcode-pa.googleapis.com"
	antigravityBaseURLDaily   = "https://daily-cloudcode-pa.googleapis.com"
	antigravityBaseURLSandbox = "https://daily-cloudcode-pa.sandbox.googleapis.com"

	antigravityLoadCodeAssistPath      = "/v1internal:loadCodeAssist"
	antigravityFetchAvailableModelsURL = "/v1internal:fetchAvailableModels"
)

var antigravityProviderSpecs = []Spec{
	{
		ID:             "antigravity",
		OpenAICompat:   false,
		DefaultBaseURL: antigravityBaseURLPrimary,
		Quota: QuotaSpec{
			Supported: true,
			Strategy:  "antigravity",
		},
		Runtime: RuntimeSpec{
			Protocols: []string{"openai"},
			OpenAI: OpenAIStrategy{
				DefaultBaseURL: antigravityBaseURLPrimary,
				AuthHeader:     "Authorization",
				AuthPrefix:     "Bearer ",
				ExtraHeaders: map[string]string{
					"Content-Type": "application/json",
				},
			},
		},
	},
}

type Antigravity struct {
	client *http.Client
}

type antigravityTier struct {
	ID          string `json:"id"`
	DisplayName string `json:"displayName"`
	Name        string `json:"name"`
}

type antigravityLoadCodeAssistResponse struct {
	PaidTier                antigravityTier `json:"paidTier"`
	CurrentTier             antigravityTier `json:"currentTier"`
	CloudAICompanionProject any             `json:"cloudaicompanionProject"`
	Project                 any             `json:"project"`
}

type antigravityModelsResponse struct {
	Models map[string]struct {
		QuotaInfo struct {
			RemainingFraction float64 `json:"remainingFraction"`
			ResetTime         string  `json:"resetTime"`
		} `json:"quotaInfo"`
	} `json:"models"`
}

func NewAntigravity(client *http.Client) *Antigravity {
	if client == nil {
		client = http.DefaultClient
	}
	return &Antigravity{client: client}
}

func (a *Antigravity) Provider() string {
	return "antigravity"
}

func (a *Antigravity) Fetch(ctx context.Context, input QuotaFetchInput) (storage.QuotaData, error) {
	loadPayload, baseURL, err := a.callLoadCodeAssist(ctx, input)
	if err != nil {
		return storage.QuotaData{}, err
	}

	var load antigravityLoadCodeAssistResponse
	if err := json.Unmarshal(loadPayload, &load); err != nil {
		return storage.QuotaData{}, fmt.Errorf("antigravity quota parse failed: %w", err)
	}

	projectID := inputString(input, "project_id", "antigravity_project_id")
	if projectID == "" {
		projectID = parseAntigravityProjectID(load.CloudAICompanionProject)
	}
	if projectID == "" {
		projectID = strings.TrimSpace(fmt.Sprintf("%v", load.Project))
	}

	modelsPayload, forbidden, err := a.callFetchAvailableModels(ctx, input, baseURL, projectID)
	if err != nil {
		return storage.QuotaData{}, err
	}

	var modelsResp antigravityModelsResponse
	if len(modelsPayload) > 0 {
		if err := json.Unmarshal(modelsPayload, &modelsResp); err != nil {
			return storage.QuotaData{}, fmt.Errorf("antigravity models quota parse failed: %w", err)
		}
	}

	now := time.Now().UTC()
	grouped := map[string]antigravityQuotaGroup{}
	for modelName, modelInfo := range modelsResp.Models {
		trimmedModel := strings.TrimSpace(modelName)
		if trimmedModel == "" {
			continue
		}
		lower := strings.ToLower(trimmedModel)
		if !strings.Contains(lower, "gemini") && !strings.Contains(lower, "claude") {
			continue
		}
		if modelInfo.QuotaInfo.RemainingFraction <= 0 && strings.TrimSpace(modelInfo.QuotaInfo.ResetTime) == "" {
			continue
		}
		groupKey := antigravityQuotaGroupKey(trimmedModel)
		if groupKey == "" {
			continue
		}
		current := antigravityQuotaGroup{
			remainingFraction: modelInfo.QuotaInfo.RemainingFraction,
			resetTime:         strings.TrimSpace(modelInfo.QuotaInfo.ResetTime),
		}
		if existing, ok := grouped[groupKey]; !ok || antigravityPreferQuotaGroup(current, existing) {
			grouped[groupKey] = current
		}
	}

	models := make([]storage.QuotaModel, 0, len(grouped))
	for groupKey, group := range grouped {
		remainingPercent, usedPercent := quotaPercentPointers(group.remainingFraction * 100)
		models = append(models, storage.QuotaModel{
			Name:             groupKey,
			DisplayName:      antigravityQuotaDisplayName(groupKey),
			RemainingPercent: remainingPercent,
			UsedPercent:      usedPercent,
			ResetTime:        group.resetTime,
			QuotaKind:        "window",
			TimeBoundaryKind: "rolling",
			DisplayUnit:      "percent",
			Source:           "antigravity_fetch_available_models",
		})
	}
	sort.Slice(models, func(i, j int) bool {
		if models[i].DisplayName == models[j].DisplayName {
			return models[i].Name < models[j].Name
		}
		return models[i].DisplayName < models[j].DisplayName
	})

	planType, planDisplayName := antigravityPlanFromLoad(load)
	data := storage.QuotaData{
		UpdatedAt: now,
		ProviderData: &storage.ProviderQuotaData{
			IsForbidden:     forbidden,
			AccountLabel:    firstNonEmpty(input.ValidationAccountID, input.OAuthAccountID, derivedAccountIdentity(input), input.Label),
			PlanType:        planType,
			PlanDisplayName: planDisplayName,
			LastUpdated:     now.Format(time.RFC3339),
			Models:          models,
		},
	}
	return data, nil
}

func (a *Antigravity) callLoadCodeAssist(ctx context.Context, input QuotaFetchInput) ([]byte, string, error) {
	client := input.HTTPClient
	if client == nil {
		client = a.client
	}

	body := map[string]any{
		"metadata": map[string]string{
			"ide_type":    "ANTIGRAVITY",
			"ide_version": "1.0.0",
			"ide_name":    "antigravity",
		},
	}
	rawBody, _ := json.Marshal(body)

	lastErr := error(nil)
	for _, baseURL := range antigravityBaseURLs(input.BaseURL) {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(baseURL, "/")+antigravityLoadCodeAssistPath, bytes.NewReader(rawBody))
		if err != nil {
			lastErr = err
			continue
		}
		req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(input.Secret))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("User-Agent", firstNonEmpty(strings.TrimSpace(input.Headers["User-Agent"]), "antigravity/1.0.0 darwin/arm64"))

		resp, err := client.Do(req)
		if err != nil {
			lastErr = err
			continue
		}
		payload, readErr := io.ReadAll(io.LimitReader(resp.Body, 512*1024))
		_ = resp.Body.Close()
		if readErr != nil {
			lastErr = readErr
			continue
		}

		switch resp.StatusCode {
		case http.StatusUnauthorized, http.StatusForbidden:
			return nil, "", ErrUnauthorized
		case http.StatusTooManyRequests:
			return nil, "", ErrRateLimited
		}
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			lastErr = fmt.Errorf("antigravity loadCodeAssist failed (HTTP %d): %s", resp.StatusCode, strings.TrimSpace(string(payload)))
			continue
		}
		return payload, baseURL, nil
	}

	if lastErr == nil {
		lastErr = fmt.Errorf("antigravity loadCodeAssist failed")
	}
	return nil, "", lastErr
}

func (a *Antigravity) callFetchAvailableModels(ctx context.Context, input QuotaFetchInput, preferredBaseURL, projectID string) ([]byte, bool, error) {
	client := input.HTTPClient
	if client == nil {
		client = a.client
	}

	body := map[string]any{}
	if strings.TrimSpace(projectID) != "" {
		body["project"] = strings.TrimSpace(projectID)
	}
	rawBody, _ := json.Marshal(body)

	lastErr := error(nil)
	candidates := antigravityCandidateBaseURLs(input.BaseURL, preferredBaseURL)
	for _, baseURL := range candidates {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(baseURL, "/")+antigravityFetchAvailableModelsURL, bytes.NewReader(rawBody))
		if err != nil {
			lastErr = err
			continue
		}
		req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(input.Secret))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("User-Agent", firstNonEmpty(strings.TrimSpace(input.Headers["User-Agent"]), "antigravity/1.0.0 darwin/arm64"))

		resp, err := client.Do(req)
		if err != nil {
			lastErr = err
			continue
		}
		payload, readErr := io.ReadAll(io.LimitReader(resp.Body, 512*1024))
		_ = resp.Body.Close()
		if readErr != nil {
			lastErr = readErr
			continue
		}

		switch resp.StatusCode {
		case http.StatusUnauthorized:
			return nil, false, ErrUnauthorized
		case http.StatusForbidden:
			return payload, true, nil
		case http.StatusTooManyRequests:
			return nil, false, ErrRateLimited
		}
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			lastErr = fmt.Errorf("antigravity fetchAvailableModels failed (HTTP %d): %s", resp.StatusCode, strings.TrimSpace(string(payload)))
			continue
		}
		return payload, false, nil
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("antigravity fetchAvailableModels failed")
	}
	return nil, false, lastErr
}

func antigravityBaseURLs(configured string) []string {
	if trimmed := strings.TrimSpace(configured); trimmed != "" {
		return []string{trimmed, antigravityBaseURLPrimary, antigravityBaseURLDaily, antigravityBaseURLSandbox}
	}
	return []string{antigravityBaseURLPrimary, antigravityBaseURLDaily, antigravityBaseURLSandbox}
}

func antigravityCandidateBaseURLs(configured, preferred string) []string {
	candidates := make([]string, 0, 4)
	seen := map[string]struct{}{}
	for _, raw := range append([]string{preferred}, antigravityBaseURLs(configured)...) {
		trimmed := strings.TrimSpace(raw)
		if trimmed == "" {
			continue
		}
		if _, ok := seen[trimmed]; ok {
			continue
		}
		seen[trimmed] = struct{}{}
		candidates = append(candidates, trimmed)
	}
	return candidates
}

func antigravityPlanFromLoad(payload antigravityLoadCodeAssistResponse) (string, string) {
	planType := strings.ToLower(strings.TrimSpace(payload.PaidTier.ID))
	planDisplay := strings.TrimSpace(payload.PaidTier.DisplayName)
	if planType == "" {
		planType = strings.ToLower(strings.TrimSpace(payload.PaidTier.Name))
	}
	if planDisplay == "" {
		planDisplay = strings.TrimSpace(payload.PaidTier.Name)
	}
	if planType == "" {
		planType = strings.ToLower(strings.TrimSpace(payload.CurrentTier.ID))
	}
	if planDisplay == "" {
		planDisplay = strings.TrimSpace(payload.CurrentTier.DisplayName)
	}
	if planType == "" {
		planType = strings.ToLower(strings.TrimSpace(payload.CurrentTier.Name))
	}
	if planDisplay == "" {
		planDisplay = strings.TrimSpace(payload.CurrentTier.Name)
	}
	return firstNonEmpty(planType, "antigravity"), firstNonEmpty(planDisplay, "Antigravity")
}

func parseAntigravityProjectID(raw any) string {
	switch typed := raw.(type) {
	case string:
		return strings.TrimSpace(typed)
	case map[string]any:
		return strings.TrimSpace(fmt.Sprintf("%v", typed["id"]))
	default:
		return ""
	}
}

type antigravityQuotaGroup struct {
	remainingFraction float64
	resetTime         string
}

func antigravityPreferQuotaGroup(candidate, existing antigravityQuotaGroup) bool {
	if candidate.remainingFraction < existing.remainingFraction {
		return true
	}
	if candidate.remainingFraction > existing.remainingFraction {
		return false
	}
	return candidate.resetTime < existing.resetTime
}

func antigravityQuotaGroupKey(model string) string {
	lower := strings.ToLower(strings.TrimSpace(model))
	lower = strings.ReplaceAll(lower, "_", "-")
	switch {
	case strings.HasPrefix(lower, "gemini-default"):
		return ""
	case strings.HasPrefix(lower, "gemini-3-pro"),
		strings.HasPrefix(lower, "gemini-3.1-pro-high"),
		strings.HasPrefix(lower, "gemini-3.1-pro-low"),
		strings.HasPrefix(lower, "gemini-3.1-pro"),
		strings.HasPrefix(lower, "gemini-2.5-pro"):
		return "gemini-pro"
	case strings.HasPrefix(lower, "gemini-3-flash"),
		strings.HasPrefix(lower, "gemini-3.1-flash-lite"),
		strings.HasPrefix(lower, "gemini-3.1-flash"),
		strings.HasPrefix(lower, "gemini-3-pro-image"),
		strings.HasPrefix(lower, "gemini-3-flash-image"),
		strings.HasPrefix(lower, "gemini-3.1-pro-image"),
		strings.HasPrefix(lower, "gemini-3.1-flash-image"),
		strings.HasPrefix(lower, "gemini-2.5-flash"):
		return "gemini-flash"
	case strings.Contains(lower, "claude") && strings.Contains(lower, "opus"):
		return "claude"
	case strings.Contains(lower, "claude") && (strings.Contains(lower, "thinking") || strings.Contains(lower, "reasoning")):
		return "claude"
	case strings.Contains(lower, "claude") && strings.Contains(lower, "sonnet"):
		return "claude"
	}
	return lower
}

func antigravityQuotaDisplayName(groupKey string) string {
	switch groupKey {
	case "gemini-pro":
		return "Gemini Pro"
	case "gemini-flash":
		return "Gemini Flash"
	case "claude":
		return "Claude"
	default:
		return groupKey
	}
}
