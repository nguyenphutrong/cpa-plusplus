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
	miniMaxCodingPlanURLGlobal = "https://platform.minimax.io/v1/api/openplatform/coding_plan/remains"
	miniMaxCodingPlanURLChina  = "https://platform.minimaxi.com/v1/api/openplatform/coding_plan/remains"
)

type miniMaxCodingPlanResponse struct {
	BaseResp *struct {
		StatusCode int    `json:"status_code"`
		StatusMsg  string `json:"status_msg"`
	} `json:"base_resp"`
	ModelRemains []struct {
		CurrentIntervalTotalCount *float64 `json:"current_interval_total_count"`
		CurrentIntervalUsageCount *float64 `json:"current_interval_usage_count"`
		EndTime                   *int64   `json:"end_time"`
		RemainsTime               *float64 `json:"remains_time"`
		ModelName                 string   `json:"model_name"`
	} `json:"model_remains"`
}

type MiniMax struct {
	client *http.Client
}

func NewMiniMax(client *http.Client) *MiniMax {
	if client == nil {
		client = http.DefaultClient
	}
	return &MiniMax{client: client}
}

func (m *MiniMax) Provider() string {
	return "minimax"
}

func (m *MiniMax) Fetch(ctx context.Context, credential QuotaFetchInput) (storage.QuotaData, error) {
	payload, err := m.fetchFromEndpoint(ctx, credential.Secret, miniMaxCodingPlanURLGlobal)
	if err != nil {
		payload, err = m.fetchFromEndpoint(ctx, credential.Secret, miniMaxCodingPlanURLChina)
		if err != nil {
			return storage.QuotaData{}, err
		}
	}
	return storage.QuotaData{
		UpdatedAt:    time.Now().UTC(),
		ProviderData: parseMiniMaxQuota(payload),
	}, nil
}

func (m *MiniMax) fetchFromEndpoint(ctx context.Context, accessToken, url string) (miniMaxCodingPlanResponse, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return miniMaxCodingPlanResponse{}, err
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/json")

	resp, err := m.client.Do(req)
	if err != nil {
		return miniMaxCodingPlanResponse{}, err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return miniMaxCodingPlanResponse{}, ErrUnauthorized
	}
	if resp.StatusCode != http.StatusOK {
		return miniMaxCodingPlanResponse{}, fmt.Errorf("unexpected status code: %d", resp.StatusCode)
	}

	var payload miniMaxCodingPlanResponse
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return miniMaxCodingPlanResponse{}, fmt.Errorf("decode response: %w", err)
	}
	return payload, nil
}

func parseMiniMaxQuota(payload miniMaxCodingPlanResponse) *storage.ProviderQuotaData {
	data := &storage.ProviderQuotaData{
		IsForbidden:     false,
		LastUpdated:     time.Now().UTC().Format(time.RFC3339),
		PlanType:        "MiniMax",
		PlanDisplayName: "MiniMax",
		Models:          []storage.QuotaModel{},
	}

	if payload.BaseResp != nil && payload.BaseResp.StatusCode != 0 {
		if payload.BaseResp.StatusCode == http.StatusUnauthorized || payload.BaseResp.StatusCode == http.StatusForbidden {
			data.IsForbidden = true
		}
		return data
	}

	for _, remain := range payload.ModelRemains {
		if remain.CurrentIntervalTotalCount == nil || *remain.CurrentIntervalTotalCount <= 0 {
			continue
		}
		total := *remain.CurrentIntervalTotalCount
		used := 0.0
		if remain.CurrentIntervalUsageCount != nil {
			used = *remain.CurrentIntervalUsageCount
		}
		remaining := max(0.0, total-used)
		remainingPercent, usedPercent := quotaPercentPointers((remaining / total) * 100)
		data.Models = append(data.Models, storage.QuotaModel{
			Name:             firstMiniMaxString(remain.ModelName, "coding-plan"),
			DisplayName:      firstMiniMaxString(remain.ModelName, "Coding Plan"),
			RemainingPercent: remainingPercent,
			UsedPercent:      usedPercent,
			Used:             floatPtr(used),
			Limit:            floatPtr(total),
			Remaining:        floatPtr(remaining),
			ResetTime:        miniMaxResetTime(remain.EndTime, remain.RemainsTime),
			TimeBoundaryKind: "reset",
			QuotaKind:        "absolute-credits",
			DisplayUnit:      "count",
			RemainingValue:   floatPtr(remaining),
			LimitValue:       floatPtr(total),
			Source:           "minimax_coding_plan_api",
		})
	}

	return data
}

func miniMaxResetTime(endTime *int64, remainsTime *float64) string {
	if endTime != nil && *endTime > 0 {
		return time.UnixMilli(*endTime).UTC().Format(time.RFC3339)
	}
	if remainsTime != nil && *remainsTime > 0 {
		return time.Now().UTC().Add(time.Duration(*remainsTime) * time.Second).Format(time.RFC3339)
	}
	return ""
}

func firstMiniMaxString(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}
