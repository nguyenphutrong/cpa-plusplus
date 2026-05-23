package providers

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/quota/storage"
)

const ampQuotaURL = "https://ampcode.com/api/internal"

var (
	ampFreePattern  = regexp.MustCompile(`(?i)Amp Free:\s*\$?([\d,]+(?:\.\d+)?)\s*/\s*\$?([\d,]+(?:\.\d+)?)\s*remaining\s*\(replenishes\s*\+\$?([\d,]+(?:\.\d+)?)\/hour\)`)
	ampCreditsExpr  = regexp.MustCompile(`(?i)Individual credits:\s*\$?([\d,]+(?:\.\d+)?)\s*remaining`)
	ampIdentityExpr = regexp.MustCompile(`(?i)Signed in as\s+([^\n(]+?)(?:\s+\(([^)]+)\))?(?:\n|$)`)
)

type ampBalanceResponse struct {
	Result *struct {
		DisplayText string `json:"displayText"`
	} `json:"result"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error"`
}

type ampDisplayUsage struct {
	AccountLabel      string
	FreeRemaining     *float64
	FreeTotal         *float64
	FreeReplenishRate *float64
	CreditsRemaining  *float64
}

type Amp struct {
	client *http.Client
}

func NewAmp(client *http.Client) *Amp {
	if client == nil {
		client = http.DefaultClient
	}
	return &Amp{client: client}
}

func (a *Amp) Provider() string {
	return "amp"
}

func (a *Amp) Fetch(ctx context.Context, credential QuotaFetchInput) (storage.QuotaData, error) {
	trimmedToken := strings.TrimSpace(credential.Secret)
	if trimmedToken == "" {
		return storage.QuotaData{}, fmt.Errorf("amp upstream api key is empty")
	}
	requestBody := map[string]any{
		"method": "userDisplayBalanceInfo",
		"params": map[string]any{},
	}
	bodyBytes, err := json.Marshal(requestBody)
	if err != nil {
		return storage.QuotaData{}, fmt.Errorf("encode request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, ampQuotaURL, bytes.NewReader(bodyBytes))
	if err != nil {
		return storage.QuotaData{}, err
	}
	req.Header.Set("Authorization", "Bearer "+trimmedToken)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	resp, err := a.client.Do(req)
	if err != nil {
		return storage.QuotaData{}, err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return storage.QuotaData{}, ErrUnauthorized
	}
	if resp.StatusCode == http.StatusTooManyRequests {
		return storage.QuotaData{}, ErrRateLimited
	}
	if resp.StatusCode != http.StatusOK {
		return storage.QuotaData{}, fmt.Errorf("unexpected status code: %d", resp.StatusCode)
	}

	var payload ampBalanceResponse
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return storage.QuotaData{}, fmt.Errorf("decode response: %w", err)
	}
	if payload.Error != nil && strings.TrimSpace(payload.Error.Message) != "" {
		return storage.QuotaData{}, errors.New(strings.TrimSpace(payload.Error.Message))
	}

	displayText := ""
	if payload.Result != nil {
		displayText = strings.TrimSpace(payload.Result.DisplayText)
	}
	if displayText == "" {
		return storage.QuotaData{}, fmt.Errorf("missing result.displayText in amp quota response")
	}

	parsed, err := parseAmpDisplayText(displayText)
	if err != nil {
		return storage.QuotaData{}, err
	}
	return storage.QuotaData{
		UpdatedAt:    time.Now().UTC(),
		ProviderData: ampToProviderQuota(parsed),
	}, nil
}

func parseAmpDisplayText(displayText string) (ampDisplayUsage, error) {
	usage := ampDisplayUsage{AccountLabel: "Amp User"}
	if matches := ampIdentityExpr.FindStringSubmatch(displayText); len(matches) >= 2 {
		email := strings.TrimSpace(matches[1])
		username := ""
		if len(matches) > 2 {
			username = strings.TrimSpace(matches[2])
		}
		if email != "" {
			usage.AccountLabel = email
		} else if username != "" {
			usage.AccountLabel = username
		}
	}
	if matches := ampFreePattern.FindStringSubmatch(displayText); len(matches) == 4 {
		remaining, err := parseAmpMoney(matches[1])
		if err != nil {
			return ampDisplayUsage{}, fmt.Errorf("parse amp free remaining: %w", err)
		}
		total, err := parseAmpMoney(matches[2])
		if err != nil {
			return ampDisplayUsage{}, fmt.Errorf("parse amp free total: %w", err)
		}
		replenish, err := parseAmpMoney(matches[3])
		if err != nil {
			return ampDisplayUsage{}, fmt.Errorf("parse amp free replenish rate: %w", err)
		}
		usage.FreeRemaining = floatPtr(remaining)
		usage.FreeTotal = floatPtr(total)
		usage.FreeReplenishRate = floatPtr(replenish)
	}
	if matches := ampCreditsExpr.FindStringSubmatch(displayText); len(matches) == 2 {
		credits, err := parseAmpMoney(matches[1])
		if err != nil {
			return ampDisplayUsage{}, fmt.Errorf("parse amp individual credits: %w", err)
		}
		usage.CreditsRemaining = floatPtr(credits)
	}
	if usage.FreeTotal == nil && usage.CreditsRemaining == nil {
		return ampDisplayUsage{}, fmt.Errorf("could not parse amp quota usage from display text")
	}
	return usage, nil
}

func parseAmpMoney(raw string) (float64, error) {
	normalized := strings.TrimSpace(strings.ReplaceAll(raw, ",", ""))
	return strconv.ParseFloat(normalized, 64)
}

func ampToProviderQuota(usage ampDisplayUsage) *storage.ProviderQuotaData {
	data := &storage.ProviderQuotaData{
		Models:       []storage.QuotaModel{},
		LastUpdated:  time.Now().UTC().Format(time.RFC3339),
		AccountLabel: strings.TrimSpace(usage.AccountLabel),
	}
	if usage.FreeTotal != nil && *usage.FreeTotal > 0 {
		remaining := 0.0
		if usage.FreeRemaining != nil {
			remaining = *usage.FreeRemaining
		}
		total := *usage.FreeTotal
		used := max(0.0, total-remaining)
		remainingPercent, usedPercent := quotaPercentPointers((remaining / total) * 100)
		resetTime := ""
		if usage.FreeReplenishRate != nil && *usage.FreeReplenishRate > 0 && used > 0 {
			hoursToReset := used / *usage.FreeReplenishRate
			resetTime = time.Now().UTC().Add(time.Duration(hoursToReset * float64(time.Hour))).Format(time.RFC3339)
		}
		data.Models = append(data.Models, storage.QuotaModel{
			Name:                 "amp-free",
			DisplayName:          "Amp Free",
			RemainingPercent:     remainingPercent,
			UsedPercent:          usedPercent,
			Used:                 floatPtr(used),
			Limit:                floatPtr(total),
			Remaining:            floatPtr(remaining),
			ResetTime:            resetTime,
			TimeBoundaryKind:     "reset",
			QuotaKind:            "replenishing-balance",
			DisplayUnit:          "usd",
			RemainingValue:       floatPtr(remaining),
			LimitValue:           floatPtr(total),
			ReplenishRatePerHour: usage.FreeReplenishRate,
			CapValue:             floatPtr(total),
			Source:               "amp_internal_api",
		})
		data.PlanType = "Free"
		data.PlanDisplayName = "Free"
	}
	if usage.CreditsRemaining != nil {
		remainingPercent, usedPercent := quotaPercentPointers(100)
		remaining := *usage.CreditsRemaining
		data.Models = append(data.Models, storage.QuotaModel{
			Name:             "balance",
			DisplayName:      "Individual credits",
			RemainingPercent: remainingPercent,
			UsedPercent:      usedPercent,
			Used:             floatPtr(0),
			Remaining:        floatPtr(remaining),
			Limit:            floatPtr(remaining),
			QuotaKind:        "absolute-credits",
			DisplayUnit:      "usd",
			RemainingValue:   floatPtr(remaining),
			LimitValue:       floatPtr(remaining),
			Source:           "amp_internal_api",
		})
		if data.PlanType == "" {
			data.PlanType = "Credits"
			data.PlanDisplayName = "Credits"
		}
	}
	if len(data.Models) == 0 {
		data.Models = append(data.Models, storage.QuotaModel{
			Name:        "amp-usage",
			DisplayName: "Amp Usage",
			Source:      "amp_internal_api",
		})
	}
	return data
}
