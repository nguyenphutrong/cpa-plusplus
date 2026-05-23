package providers

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/quota/storage"
)

var (
	githubCopilotEntitlementURL = "https://api.github.com/copilot_internal/user"
	githubCopilotModelsURL      = "https://api.individual.githubcopilot.com/models"
)

type copilotQuotaSnapshot struct {
	Entitlement      *float64 `json:"entitlement"`
	Remaining        *float64 `json:"remaining"`
	PercentRemaining *float64 `json:"percent_remaining"`
	Unlimited        bool     `json:"unlimited"`
}

type copilotQuotaSnapshots struct {
	Chat                *copilotQuotaSnapshot `json:"chat"`
	Completions         *copilotQuotaSnapshot `json:"completions"`
	PremiumInteractions *copilotQuotaSnapshot `json:"premium_interactions"`
}

type copilotLimitedUserQuotas struct {
	Chat        *float64 `json:"chat"`
	Completions *float64 `json:"completions"`
}

type copilotEntitlement struct {
	AccessTypeSKU     string                    `json:"access_type_sku"`
	CopilotPlan       string                    `json:"copilot_plan"`
	QuotaResetDate    string                    `json:"quota_reset_date"`
	QuotaResetDateUTC string                    `json:"quota_reset_date_utc"`
	LimitedUserReset  string                    `json:"limited_user_reset_date"`
	QuotaSnapshots    *copilotQuotaSnapshots    `json:"quota_snapshots"`
	LimitedUserQuotas *copilotLimitedUserQuotas `json:"limited_user_quotas"`
	MonthlyQuotas     *copilotLimitedUserQuotas `json:"monthly_quotas"`
}

type GitHubCopilot struct {
	client *http.Client
}

type copilotPlanSpec struct {
	PlanType       string
	PlanDisplay    string
	MultiplierPlan string
}

type copilotModelEntry struct {
	ModelID     string
	DisplayName string
}

var copilotModelEntries = []copilotModelEntry{
	{ModelID: "claude-haiku-4.5", DisplayName: "Claude Haiku 4.5"},
	{ModelID: "claude-opus-4.5", DisplayName: "Claude Opus 4.5"},
	{ModelID: "claude-opus-4.6", DisplayName: "Claude Opus 4.6"},
	{ModelID: "claude-opus-4.6-fast-mode", DisplayName: "Claude Opus 4.6 (fast mode) (preview)"},
	{ModelID: "claude-opus-4.7", DisplayName: "Claude Opus 4.7"},
	{ModelID: "claude-sonnet-4", DisplayName: "Claude Sonnet 4"},
	{ModelID: "claude-sonnet-4.5", DisplayName: "Claude Sonnet 4.5"},
	{ModelID: "claude-sonnet-4.6", DisplayName: "Claude Sonnet 4.6"},
	{ModelID: "gemini-2.5-pro", DisplayName: "Gemini 2.5 Pro"},
	{ModelID: "gemini-3-flash", DisplayName: "Gemini 3 Flash"},
	{ModelID: "gemini-3.1-pro", DisplayName: "Gemini 3.1 Pro"},
	{ModelID: "gpt-4.1", DisplayName: "GPT-4.1"},
	{ModelID: "gpt-5-mini", DisplayName: "GPT-5 mini"},
	{ModelID: "gpt-5.2", DisplayName: "GPT-5.2"},
	{ModelID: "gpt-5.2-codex", DisplayName: "GPT-5.2-Codex"},
	{ModelID: "gpt-5.3-codex", DisplayName: "GPT-5.3-Codex"},
	{ModelID: "gpt-5.4", DisplayName: "GPT-5.4"},
	{ModelID: "gpt-5.4-mini", DisplayName: "GPT-5.4 mini"},
	{ModelID: "grok-code-fast-1", DisplayName: "Grok Code Fast 1"},
	{ModelID: "raptor-mini", DisplayName: "Raptor mini"},
	{ModelID: "goldeneye", DisplayName: "Goldeneye"},
}

var copilotModelByID = func() map[string]copilotModelEntry {
	models := make(map[string]copilotModelEntry, len(copilotModelEntries))
	for _, model := range copilotModelEntries {
		models[model.ModelID] = model
	}
	return models
}()

var copilotPlanModels = map[string][]string{
	"copilot": {
		"claude-haiku-4.5",
		"goldeneye",
		"gpt-4.1",
		"gpt-5-mini",
		"grok-code-fast-1",
		"raptor-mini",
	},
	"copilot-student": {
		"claude-haiku-4.5",
		"gemini-2.5-pro",
		"gemini-3-flash",
		"gemini-3.1-pro",
		"gpt-4.1",
		"gpt-5-mini",
		"gpt-5.2",
		"gpt-5.2-codex",
		"gpt-5.3-codex",
		"gpt-5.4-mini",
		"grok-code-fast-1",
		"raptor-mini",
	},
	"copilot-pro": {
		"claude-haiku-4.5",
		"claude-opus-4.5",
		"claude-opus-4.6",
		"claude-sonnet-4",
		"claude-sonnet-4.5",
		"claude-sonnet-4.6",
		"gemini-2.5-pro",
		"gemini-3-flash",
		"gemini-3.1-pro",
		"gpt-4.1",
		"gpt-5-mini",
		"gpt-5.2",
		"gpt-5.2-codex",
		"gpt-5.3-codex",
		"gpt-5.4",
		"gpt-5.4-mini",
		"grok-code-fast-1",
		"raptor-mini",
	},
	"copilot-pro-plus": {
		"claude-haiku-4.5",
		"claude-opus-4.5",
		"claude-opus-4.6",
		"claude-opus-4.6-fast-mode",
		"claude-opus-4.7",
		"claude-sonnet-4",
		"claude-sonnet-4.5",
		"claude-sonnet-4.6",
		"gemini-2.5-pro",
		"gemini-3-flash",
		"gemini-3.1-pro",
		"gpt-4.1",
		"gpt-5-mini",
		"gpt-5.2",
		"gpt-5.2-codex",
		"gpt-5.3-codex",
		"gpt-5.4",
		"gpt-5.4-mini",
		"grok-code-fast-1",
		"raptor-mini",
	},
	"copilot-business": {
		"claude-haiku-4.5",
		"claude-opus-4.5",
		"claude-opus-4.6",
		"claude-opus-4.7",
		"claude-sonnet-4",
		"claude-sonnet-4.5",
		"claude-sonnet-4.6",
		"gemini-2.5-pro",
		"gemini-3-flash",
		"gemini-3.1-pro",
		"gpt-4.1",
		"gpt-5-mini",
		"gpt-5.2",
		"gpt-5.2-codex",
		"gpt-5.3-codex",
		"gpt-5.4",
		"gpt-5.4-mini",
		"grok-code-fast-1",
	},
	"copilot-enterprise": {
		"claude-haiku-4.5",
		"claude-opus-4.5",
		"claude-opus-4.6",
		"claude-opus-4.6-fast-mode",
		"claude-opus-4.7",
		"claude-sonnet-4",
		"claude-sonnet-4.5",
		"claude-sonnet-4.6",
		"gemini-2.5-pro",
		"gemini-3-flash",
		"gemini-3.1-pro",
		"gpt-4.1",
		"gpt-5-mini",
		"gpt-5.2",
		"gpt-5.2-codex",
		"gpt-5.3-codex",
		"gpt-5.4",
		"gpt-5.4-mini",
		"grok-code-fast-1",
	},
}

func NewGitHubCopilot(client *http.Client) *GitHubCopilot {
	if client == nil {
		client = http.DefaultClient
	}
	return &GitHubCopilot{client: client}
}

func (g *GitHubCopilot) Provider() string {
	return "github-copilot"
}

func (g *GitHubCopilot) Fetch(ctx context.Context, credential QuotaFetchInput) (storage.QuotaData, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, githubCopilotEntitlementURL, nil)
	if err != nil {
		return storage.QuotaData{}, err
	}
	req.Header.Set("Authorization", "Bearer "+credential.Secret)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")

	resp, err := g.client.Do(req)
	if err != nil {
		return storage.QuotaData{}, err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return storage.QuotaData{
			UpdatedAt: time.Now().UTC(),
			ProviderData: &storage.ProviderQuotaData{
				IsForbidden: true,
				LastUpdated: time.Now().UTC().Format(time.RFC3339),
			},
		}, nil
	}
	if resp.StatusCode != http.StatusOK {
		return storage.QuotaData{}, fmt.Errorf("unexpected status code: %d", resp.StatusCode)
	}

	var entitlement copilotEntitlement
	if err := json.NewDecoder(resp.Body).Decode(&entitlement); err != nil {
		return storage.QuotaData{}, fmt.Errorf("decode response: %w", err)
	}
	models, ok := g.fetchModels(ctx, credential, copilotPlanSpecFromEntitlement(entitlement))
	return storage.QuotaData{
		UpdatedAt:    time.Now().UTC(),
		ProviderData: buildCopilotQuotaData(credential, entitlement, models, ok),
	}, nil
}

func buildCopilotQuotaData(credential QuotaFetchInput, entitlement copilotEntitlement, liveModels []storage.CopilotChatModel, hasLiveModels bool) *storage.ProviderQuotaData {
	resetTime := firstString(entitlement.QuotaResetDateUTC, entitlement.QuotaResetDate, entitlement.LimitedUserReset)
	planSpec := copilotPlanSpecFromEntitlement(entitlement)
	accountLabel := normalizeAccountIdentity(firstString(
		credential.ValidationAccountID,
		oauthAccountIdentity(credential),
		credential.Label,
	))
	if !isValidAccountIdentityForProvider("github-copilot", accountLabel) {
		accountLabel = ""
	}
	data := &storage.ProviderQuotaData{
		IsForbidden:       false,
		LastUpdated:       time.Now().UTC().Format(time.RFC3339),
		PlanType:          planSpec.PlanType,
		PlanDisplayName:   planSpec.PlanDisplay,
		AccountLabel:      accountLabel,
		Models:            []storage.QuotaModel{},
		CopilotChatModels: buildCopilotChatModels(planSpec),
	}
	if hasLiveModels {
		data.CopilotChatModels = liveModels
	}

	if entitlement.QuotaSnapshots != nil {
		appendSnapshotModel(data, "copilot-chat", "Chat", entitlement.QuotaSnapshots.Chat, 50, resetTime)
		appendSnapshotModel(data, "copilot-completions", "Completions", entitlement.QuotaSnapshots.Completions, 2000, resetTime)
		appendSnapshotModel(data, "copilot-premium", "Premium", entitlement.QuotaSnapshots.PremiumInteractions, 50, resetTime)
	}

	if len(data.Models) == 0 && entitlement.LimitedUserQuotas != nil && entitlement.MonthlyQuotas != nil {
		appendFallbackQuotaModel(data, "copilot-chat", "Chat", entitlement.LimitedUserQuotas.Chat, entitlement.MonthlyQuotas.Chat, resetTime)
		appendFallbackQuotaModel(data, "copilot-completions", "Completions", entitlement.LimitedUserQuotas.Completions, entitlement.MonthlyQuotas.Completions, resetTime)
	}

	return data
}

func (g *GitHubCopilot) fetchModels(ctx context.Context, credential QuotaFetchInput, plan copilotPlanSpec) ([]storage.CopilotChatModel, bool) {
	body := []byte(`{"auto_mode":{"model_hints":["auto"]}}`)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, githubCopilotModelsURL, bytes.NewReader(body))
	if err != nil {
		return nil, false
	}
	req.Header.Set("Authorization", "Bearer "+credential.Secret)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-GitHub-Api-Version", "2026-01-09")
	req.Header.Set("Editor-Device-Id", firstString(
		inputString(credential, "editor-device-id", "editor_device_id"),
		"cpa-plusplus",
	))
	req.Header.Set("Editor-Plugin-Version", firstString(
		inputString(credential, "editor-plugin-version", "editor_plugin_version"),
		"copilot-chat/0.47.2026042902",
	))
	req.Header.Set("Editor-Version", firstString(
		inputString(credential, "editor-version", "editor_version"),
		"vscode/1.119.0-insider",
	))
	req.Header.Set("User-Agent", firstString(
		inputString(credential, "user-agent", "user_agent"),
		"GitHubCopilotChat/0.47.2026042902",
	))
	resp, err := g.client.Do(req)
	if err != nil {
		return nil, false
	}
	defer resp.Body.Close()
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return nil, false
	}
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, false
	}
	models := parseCopilotModelsResponse(raw, plan.MultiplierPlan)
	if len(models) == 0 {
		return nil, false
	}
	return models, true
}

func oauthAccountIdentity(credential QuotaFetchInput) string {
	return strings.TrimSpace(credential.OAuthAccountID)
}

func appendSnapshotModel(data *storage.ProviderQuotaData, name, displayName string, snapshot *copilotQuotaSnapshot, defaultTotal float64, resetTime string) {
	if snapshot == nil || snapshot.Unlimited {
		return
	}
	remainingPercent := copilotPercent(snapshot, defaultTotal)
	remainingPtr, usedPtr := quotaPercentPointers(remainingPercent)
	model := storage.QuotaModel{
		Name:             name,
		DisplayName:      displayName,
		RemainingPercent: remainingPtr,
		UsedPercent:      usedPtr,
		ResetTime:        normalizeTime(resetTime),
		TimeBoundaryKind: "reset",
		QuotaKind:        "window",
		DisplayUnit:      "percent",
		Source:           "github_copilot_entitlement_api",
	}
	if snapshot.Remaining != nil {
		model.Remaining = snapshot.Remaining
		model.RemainingValue = snapshot.Remaining
	}
	if snapshot.Entitlement != nil {
		model.Limit = snapshot.Entitlement
		model.LimitValue = snapshot.Entitlement
	}
	if snapshot.Entitlement != nil && snapshot.Remaining != nil {
		used := *snapshot.Entitlement - *snapshot.Remaining
		model.Used = floatPtr(max(0.0, used))
	}
	data.Models = append(data.Models, model)
}

func appendFallbackQuotaModel(data *storage.ProviderQuotaData, name, displayName string, remaining, total *float64, resetTime string) {
	if remaining == nil || total == nil || *total <= 0 {
		return
	}
	remainingPercent := clampPercent((*remaining / *total) * 100)
	remainingPtr, usedPtr := quotaPercentPointers(remainingPercent)
	used := *total - *remaining
	data.Models = append(data.Models, storage.QuotaModel{
		Name:             name,
		DisplayName:      displayName,
		RemainingPercent: remainingPtr,
		UsedPercent:      usedPtr,
		Used:             floatPtr(max(0.0, used)),
		Limit:            total,
		Remaining:        remaining,
		ResetTime:        normalizeTime(resetTime),
		TimeBoundaryKind: "reset",
		QuotaKind:        "window",
		DisplayUnit:      "percent",
		Source:           "github_copilot_entitlement_api",
	})
}

func copilotPercent(snapshot *copilotQuotaSnapshot, defaultTotal float64) float64 {
	if snapshot == nil {
		return 0
	}
	if snapshot.PercentRemaining != nil {
		return clampPercent(*snapshot.PercentRemaining)
	}
	total := defaultTotal
	if snapshot.Entitlement != nil && *snapshot.Entitlement > 0 {
		total = *snapshot.Entitlement
	}
	if snapshot.Remaining == nil || total <= 0 {
		return 0
	}
	return clampPercent((*snapshot.Remaining / total) * 100)
}

func copilotPlanSpecFromEntitlement(entitlement copilotEntitlement) copilotPlanSpec {
	sku := strings.ToLower(strings.TrimSpace(entitlement.AccessTypeSKU))
	plan := strings.ToLower(strings.TrimSpace(entitlement.CopilotPlan))

	unknownPlan := firstString(entitlement.CopilotPlan, entitlement.AccessTypeSKU, "Unknown")
	unknownPlan = strings.TrimSpace(unknownPlan)
	if unknownPlan == "" {
		unknownPlan = "Unknown"
	}

	switch {
	case strings.Contains(sku, "enterprise") || plan == "enterprise":
		return copilotPlanSpec{
			PlanType:       "copilot-enterprise",
			PlanDisplay:    "Copilot Enterprise",
			MultiplierPlan: "paid",
		}
	case strings.Contains(sku, "business") || plan == "business":
		return copilotPlanSpec{
			PlanType:       "copilot-business",
			PlanDisplay:    "Copilot Business",
			MultiplierPlan: "paid",
		}
	case strings.Contains(sku, "educational") || strings.Contains(sku, "student") || strings.Contains(plan, "student"):
		return copilotPlanSpec{
			PlanType:       "copilot-student",
			PlanDisplay:    "Copilot Student",
			MultiplierPlan: "paid",
		}
	case strings.Contains(sku, "pro+") || strings.Contains(sku, "pro_plus") || strings.Contains(sku, "pro-plus") || strings.Contains(plan, "pro+") || strings.Contains(plan, "pro_plus") || strings.Contains(plan, "pro-plus"):
		return copilotPlanSpec{
			PlanType:       "copilot-pro-plus",
			PlanDisplay:    "Copilot Pro+",
			MultiplierPlan: "paid",
		}
	case strings.Contains(sku, "pro") || strings.Contains(plan, "pro") || plan == "individual":
		return copilotPlanSpec{
			PlanType:       "copilot-pro",
			PlanDisplay:    "Copilot Pro",
			MultiplierPlan: "paid",
		}
	case strings.Contains(sku, "free") || strings.Contains(plan, "free"):
		return copilotPlanSpec{
			PlanType:       "copilot",
			PlanDisplay:    "Copilot",
			MultiplierPlan: "free",
		}
	default:
		return copilotPlanSpec{
			PlanType:       strings.ToLower(strings.ReplaceAll(unknownPlan, " ", "-")),
			PlanDisplay:    unknownPlan,
			MultiplierPlan: "paid",
		}
	}
}

func CopilotPlanSpecFromStoredQuota(data *storage.ProviderQuotaData) (string, string, string, bool) {
	if data == nil {
		return "", "", "", false
	}
	planType := strings.TrimSpace(data.PlanType)
	planDisplay := strings.TrimSpace(data.PlanDisplayName)
	if planType == "" || planDisplay == "" {
		return "", "", "", false
	}
	multiplierPlan := "paid"
	if planType == "copilot" {
		multiplierPlan = "free"
	}
	return planType, planDisplay, multiplierPlan, true
}

func CopilotChatModelsForPlan(planType, multiplierPlan string) []storage.CopilotChatModel {
	return buildCopilotChatModels(copilotPlanSpec{
		PlanType:       strings.TrimSpace(planType),
		MultiplierPlan: strings.TrimSpace(multiplierPlan),
	})
}

func buildCopilotChatModels(plan copilotPlanSpec) []storage.CopilotChatModel {
	modelIDs, ok := copilotPlanModels[plan.PlanType]
	if !ok || len(modelIDs) == 0 {
		return []storage.CopilotChatModel{}
	}
	models := make([]storage.CopilotChatModel, 0, len(modelIDs))
	for _, modelID := range modelIDs {
		entry, ok := copilotModelByID[modelID]
		if !ok {
			continue
		}
		models = append(models, storage.CopilotChatModel{
			ModelID:           entry.ModelID,
			DisplayName:       entry.DisplayName,
			MultiplierLabel:   "",
			MultiplierValue:   "",
			MultiplierPlan:    plan.MultiplierPlan,
			MultiplierApplies: false,
		})
	}
	return models
}

func parseCopilotModelsResponse(raw []byte, multiplierPlan string) []storage.CopilotChatModel {
	var payload any
	if err := json.Unmarshal(raw, &payload); err != nil {
		return []storage.CopilotChatModel{}
	}
	var nodes []map[string]any
	switch typed := payload.(type) {
	case []any:
		nodes = appendModelNodes(nodes, typed)
	case map[string]any:
		if data, ok := typed["data"].([]any); ok {
			nodes = appendModelNodes(nodes, data)
		}
		if models, ok := typed["models"].([]any); ok {
			nodes = appendModelNodes(nodes, models)
		}
	}
	if len(nodes) == 0 {
		return []storage.CopilotChatModel{}
	}
	seen := map[string]struct{}{}
	entries := make([]storage.CopilotChatModel, 0, len(nodes))
	for _, node := range nodes {
		modelID := firstNonEmptyMapString(node, "id", "model", "model_id", "name")
		modelID = strings.TrimSpace(modelID)
		if modelID == "" {
			continue
		}
		if _, ok := seen[modelID]; ok {
			continue
		}
		seen[modelID] = struct{}{}
		display := firstNonEmptyMapString(node, "display_name", "name")
		if strings.TrimSpace(display) == "" {
			display = modelID
		}
		label := ""
		value := ""
		applies := false
		if rawValue, ok := firstMultiplierValue(node); ok {
			if strings.EqualFold(rawValue, "not applicable") {
				label, value, applies = "N/A", "", false
			} else {
				label, value, applies = "x"+rawValue, rawValue, true
			}
		}
		entries = append(entries, storage.CopilotChatModel{
			ModelID:           modelID,
			DisplayName:       display,
			MultiplierLabel:   label,
			MultiplierValue:   value,
			MultiplierPlan:    multiplierPlan,
			MultiplierApplies: applies,
		})
	}
	return entries
}

func appendModelNodes(dst []map[string]any, src []any) []map[string]any {
	for _, item := range src {
		node, ok := item.(map[string]any)
		if !ok {
			continue
		}
		dst = append(dst, node)
	}
	return dst
}

func firstNonEmptyMapString(node map[string]any, keys ...string) string {
	for _, key := range keys {
		value, ok := node[key]
		if !ok {
			continue
		}
		str := strings.TrimSpace(fmt.Sprint(value))
		if str == "" || str == "<nil>" {
			continue
		}
		return str
	}
	return ""
}

func firstMultiplierValue(node map[string]any) (string, bool) {
	if billingRaw, ok := node["billing"]; ok {
		if billing, ok := billingRaw.(map[string]any); ok {
			if raw, ok := billing["multiplier"]; ok {
				switch typed := raw.(type) {
				case float64:
					return strconv.FormatFloat(typed, 'f', -1, 64), true
				case string:
					trimmed := strings.TrimSpace(typed)
					if trimmed != "" {
						return trimmed, true
					}
				}
			}
		}
	}
	for _, key := range []string{"multiplier", "premium_multiplier", "billing_multiplier"} {
		raw, ok := node[key]
		if !ok {
			continue
		}
		switch typed := raw.(type) {
		case float64:
			return strconv.FormatFloat(typed, 'f', -1, 64), true
		case string:
			trimmed := strings.TrimSpace(typed)
			if trimmed != "" {
				return trimmed, true
			}
		}
	}
	return "", false
}

func normalizeTime(value string) string {
	if strings.TrimSpace(value) == "" {
		return ""
	}
	if parsed, err := time.Parse(time.RFC3339, value); err == nil {
		return parsed.UTC().Format(time.RFC3339)
	}
	if parsed, err := time.Parse(time.RFC3339Nano, value); err == nil {
		return parsed.UTC().Format(time.RFC3339)
	}
	return value
}

func firstString(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

var githubCopilotProviderSpecs = []Spec{
	{
		ID: "github-copilot",
		Quota: QuotaSpec{
			Supported: true,
			Strategy:  "github-copilot",
		},
		Runtime: RuntimeSpec{
			Protocols: []string{"openai"},
			OpenAI: OpenAIStrategy{
				DefaultBaseURL: "https://api.githubcopilot.com",
				AuthHeader:     "Authorization",
				AuthPrefix:     "Bearer ",
			},
		},
	},
}
