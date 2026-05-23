package storage

import "time"

type QuotaModel struct {
	Name                 string   `json:"name"`
	DisplayName          string   `json:"display_name,omitempty"`
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

type CopilotChatModel struct {
	ModelID           string `json:"model_id"`
	DisplayName       string `json:"display_name"`
	MultiplierLabel   string `json:"multiplier_label,omitempty"`
	MultiplierValue   string `json:"multiplier_value,omitempty"`
	MultiplierPlan    string `json:"multiplier_plan,omitempty"`
	MultiplierApplies bool   `json:"multiplier_applies"`
}

type ProviderQuotaData struct {
	Models            []QuotaModel       `json:"models"`
	LastUpdated       string             `json:"last_updated"`
	IsForbidden       bool               `json:"is_forbidden"`
	AccountLabel      string             `json:"account_label,omitempty"`
	PlanType          string             `json:"plan_type,omitempty"`
	PlanDisplayName   string             `json:"plan_display_name,omitempty"`
	CopilotChatModels []CopilotChatModel `json:"copilot_chat_models,omitempty"`
}

type QuotaData struct {
	QuotaLimit     float64            `json:"quota_limit"`
	QuotaUsed      float64            `json:"quota_used"`
	QuotaRemaining float64            `json:"quota_remaining"`
	Percentage     float64            `json:"percentage"`
	ResetAt        time.Time          `json:"reset_at,omitempty"`
	UpdatedAt      time.Time          `json:"updated_at"`
	Error          string             `json:"error,omitempty"`
	ProviderData   *ProviderQuotaData `json:"provider_data,omitempty"`
}
