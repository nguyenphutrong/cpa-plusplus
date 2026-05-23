package quota

import (
	"slices"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/quota/storage"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

type AccountSwitchingAccount struct {
	ID       string `json:"id"`
	Email    string `json:"email"`
	IsActive bool   `json:"is_active"`
}

type AccountSwitchingStatus struct {
	Provider  string                    `json:"provider"`
	Supported bool                      `json:"supported"`
	Accounts  []AccountSwitchingAccount `json:"accounts"`
}

type QuotaModelView struct {
	Name                 string   `json:"name"`
	DisplayName          string   `json:"display_name"`
	RemainingPercent     *float64 `json:"remaining_percent,omitempty"`
	UsedPercent          *float64 `json:"used_percent,omitempty"`
	Used                 *float64 `json:"used,omitempty"`
	Limit                *float64 `json:"limit,omitempty"`
	Remaining            *float64 `json:"remaining,omitempty"`
	ResetTime            string   `json:"reset_time,omitempty"`
	TimeBoundaryKind     string   `json:"time_boundary_kind,omitempty"`
	QuotaKind            string   `json:"quota_kind,omitempty"`
	DisplayUnit          string   `json:"display_unit,omitempty"`
	RemainingValue       *float64 `json:"remaining_value,omitempty"`
	LimitValue           *float64 `json:"limit_value,omitempty"`
	ReplenishRatePerHour *float64 `json:"replenish_rate_per_hour,omitempty"`
	CapValue             *float64 `json:"cap_value,omitempty"`
	Source               string   `json:"source,omitempty"`
	SourceDescription    string   `json:"source_description,omitempty"`
}

type CopilotChatModelView struct {
	ModelID           string `json:"model_id"`
	DisplayName       string `json:"display_name"`
	MultiplierLabel   string `json:"multiplier_label,omitempty"`
	MultiplierValue   string `json:"multiplier_value,omitempty"`
	MultiplierPlan    string `json:"multiplier_plan,omitempty"`
	MultiplierApplies bool   `json:"multiplier_applies"`
}

type QuotaAccountView struct {
	CredentialID      string                 `json:"credential_id"`
	AccountKey        string                 `json:"account_key"`
	Provider          string                 `json:"provider"`
	QuotaSupported    bool                   `json:"quota_supported"`
	QuotaStatus       string                 `json:"quota_status"`
	QuotaStatusReason string                 `json:"quota_status_reason,omitempty"`
	PlanType          string                 `json:"plan_type,omitempty"`
	PlanDisplayName   string                 `json:"plan_display_name,omitempty"`
	IsForbidden       bool                   `json:"is_forbidden"`
	IsActive          bool                   `json:"is_active"`
	LastUpdated       string                 `json:"last_updated,omitempty"`
	Error             string                 `json:"error,omitempty"`
	Models            []QuotaModelView       `json:"models"`
	CopilotChatModels []CopilotChatModelView `json:"copilot_chat_models,omitempty"`
}

type QuotaProviderView struct {
	Provider              string             `json:"provider"`
	DisplayName           string             `json:"display_name"`
	QuotaSupported        bool               `json:"quota_supported"`
	QuotaStatus           string             `json:"quota_status"`
	QuotaStatusReason     string             `json:"quota_status_reason,omitempty"`
	SupportsRefresh       bool               `json:"supports_refresh"`
	SupportsAccountSwitch bool               `json:"supports_account_switch"`
	Accounts              []QuotaAccountView `json:"accounts"`
}

type QuotaView struct {
	Providers        []QuotaProviderView    `json:"providers"`
	AccountSwitching AccountSwitchingStatus `json:"account_switching"`
}

type QuotaSummaryProvider struct {
	Provider   string `json:"provider"`
	Accounts   int    `json:"accounts"`
	StaleCount int    `json:"stale_count"`
}

type QuotaSummaryView struct {
	Providers []QuotaSummaryProvider `json:"providers"`
}

func BuildQuotaView(auths []*coreauth.Auth, supportsRefresh func(provider string) bool) QuotaView {
	providersByID := map[string]*QuotaProviderView{}
	switching := AccountSwitchingStatus{Provider: "codex", Accounts: []AccountSwitchingAccount{}}

	for _, auth := range auths {
		if auth == nil || auth.Disabled || auth.Status == coreauth.StatusDisabled {
			continue
		}

		provider := ProviderKey(auth)
		view, ok := providersByID[provider]
		if !ok {
			capability := SupportsProvider(provider)
			view = &QuotaProviderView{
				Provider:              provider,
				DisplayName:           providerDisplayName(provider),
				QuotaSupported:        capability.Supported,
				QuotaStatus:           statusForCapability(capability, ""),
				QuotaStatusReason:     capability.Reason,
				SupportsRefresh:       supportsRefresh(provider) && capability.Supported,
				SupportsAccountSwitch: provider == "codex",
				Accounts:              []QuotaAccountView{},
			}
			providersByID[provider] = view
		}

		quotaData := CachedQuotaData(auth)
		capability := SupportsProvider(provider)
		accountKey := firstNonEmpty(quotaAccountLabel(provider, quotaData), authAccountLabel(auth), auth.Label, auth.ID)
		account := QuotaAccountView{
			CredentialID:      auth.ID,
			AccountKey:        accountKey,
			Provider:          provider,
			QuotaSupported:    capability.Supported,
			QuotaStatus:       statusForCapability(capability, quotaData.Error),
			QuotaStatusReason: capability.Reason,
			IsForbidden:       quotaData.ProviderData != nil && quotaData.ProviderData.IsForbidden,
			IsActive:          true,
			Error:             quotaData.Error,
			Models:            []QuotaModelView{},
			CopilotChatModels: []CopilotChatModelView{},
		}
		if !quotaData.UpdatedAt.IsZero() {
			account.LastUpdated = quotaData.UpdatedAt.UTC().Format(time.RFC3339)
		}
		if !capability.Supported {
			account.Error = ""
			account.LastUpdated = ""
		}
		if quotaData.ProviderData != nil {
			account.PlanType = quotaData.ProviderData.PlanType
			account.PlanDisplayName = firstNonEmpty(quotaData.ProviderData.PlanDisplayName, quotaData.ProviderData.PlanType)
			account.IsForbidden = quotaData.ProviderData.IsForbidden
			account.LastUpdated = firstNonEmpty(quotaData.ProviderData.LastUpdated, account.LastUpdated)
			account.Models = quotaModelViews(quotaData.ProviderData.Models)
			account.CopilotChatModels = copilotChatModelViews(quotaData.ProviderData.CopilotChatModels)
		}
		view.Accounts = append(view.Accounts, account)

		if provider == "codex" {
			switching.Supported = true
			switching.Accounts = append(switching.Accounts, AccountSwitchingAccount{
				ID:       auth.ID,
				Email:    accountKey,
				IsActive: true,
			})
		}
	}

	result := QuotaView{Providers: []QuotaProviderView{}, AccountSwitching: switching}
	for _, provider := range providersByID {
		if provider.QuotaSupported {
			for _, account := range provider.Accounts {
				if account.QuotaStatus == string(CapabilityError) {
					provider.QuotaStatus = string(CapabilityError)
					break
				}
			}
		}
		slices.SortStableFunc(provider.Accounts, func(left, right QuotaAccountView) int {
			return strings.Compare(left.AccountKey, right.AccountKey)
		})
		result.Providers = append(result.Providers, *provider)
	}
	slices.SortFunc(result.Providers, func(left, right QuotaProviderView) int {
		return strings.Compare(left.DisplayName, right.DisplayName)
	})
	slices.SortFunc(result.AccountSwitching.Accounts, func(left, right AccountSwitchingAccount) int {
		return strings.Compare(left.Email, right.Email)
	})
	return result
}

func BuildQuotaSummaryView(view QuotaView) QuotaSummaryView {
	summary := QuotaSummaryView{Providers: make([]QuotaSummaryProvider, 0, len(view.Providers))}
	for _, provider := range view.Providers {
		entry := QuotaSummaryProvider{Provider: provider.Provider, Accounts: len(provider.Accounts)}
		for _, account := range provider.Accounts {
			if account.LastUpdated == "" || account.QuotaStatus == string(CapabilityError) {
				entry.StaleCount++
			}
		}
		summary.Providers = append(summary.Providers, entry)
	}
	return summary
}

func quotaModelViews(models []storage.QuotaModel) []QuotaModelView {
	out := make([]QuotaModelView, 0, len(models))
	for _, model := range models {
		out = append(out, QuotaModelView{
			Name:                 model.Name,
			DisplayName:          firstNonEmpty(model.DisplayName, model.Name),
			RemainingPercent:     model.RemainingPercent,
			UsedPercent:          model.UsedPercent,
			Used:                 model.Used,
			Limit:                model.Limit,
			Remaining:            model.Remaining,
			ResetTime:            model.ResetTime,
			TimeBoundaryKind:     model.TimeBoundaryKind,
			QuotaKind:            firstNonEmpty(model.QuotaKind, "window"),
			DisplayUnit:          firstNonEmpty(model.DisplayUnit, "percent"),
			RemainingValue:       model.RemainingValue,
			LimitValue:           model.LimitValue,
			ReplenishRatePerHour: model.ReplenishRatePerHour,
			CapValue:             model.CapValue,
			Source:               model.Source,
			SourceDescription:    model.SourceDescription,
		})
	}
	return out
}

func copilotChatModelViews(models []storage.CopilotChatModel) []CopilotChatModelView {
	out := make([]CopilotChatModelView, 0, len(models))
	for _, model := range models {
		out = append(out, CopilotChatModelView{
			ModelID:           model.ModelID,
			DisplayName:       firstNonEmpty(model.DisplayName, model.ModelID),
			MultiplierLabel:   model.MultiplierLabel,
			MultiplierValue:   model.MultiplierValue,
			MultiplierPlan:    model.MultiplierPlan,
			MultiplierApplies: model.MultiplierApplies,
		})
	}
	return out
}

func quotaAccountLabel(provider string, quotaData storage.QuotaData) string {
	if quotaData.ProviderData == nil {
		return ""
	}
	label := strings.ToLower(strings.TrimSpace(quotaData.ProviderData.AccountLabel))
	if label == "" || label == strings.ToLower(strings.TrimSpace(provider)) {
		return ""
	}
	if strings.EqualFold(label, strings.TrimSpace(provider)+" account") {
		return ""
	}
	return label
}

func authAccountLabel(auth *coreauth.Auth) string {
	if auth == nil {
		return ""
	}
	if kind, account := auth.AccountInfo(); strings.TrimSpace(account) != "" && !strings.EqualFold(kind, "api_key") {
		return strings.TrimSpace(account)
	}
	if auth.Metadata != nil {
		for _, key := range []string{"email", "account_id", "sub", "subject", "login", "username"} {
			if value, ok := auth.Metadata[key].(string); ok && strings.TrimSpace(value) != "" {
				return strings.TrimSpace(value)
			}
		}
	}
	return ""
}

func providerDisplayName(provider string) string {
	switch strings.ToLower(strings.TrimSpace(provider)) {
	case "anthropic":
		return "Claude"
	case "github-copilot":
		return "GitHub Copilot"
	case "gemini":
		return "Gemini"
	case "opencode-go":
		return "OpenCode Go"
	case "z-ai":
		return "Z.AI"
	default:
		trimmed := strings.TrimSpace(provider)
		if trimmed == "" {
			return "Unknown"
		}
		return strings.ToUpper(trimmed[:1]) + trimmed[1:]
	}
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}
