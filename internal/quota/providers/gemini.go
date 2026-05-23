package providers

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/quota/storage"
)

const (
	geminiLoadCodeAssistURL = "https://cloudcode-pa.googleapis.com/v1internal:loadCodeAssist"
	geminiQuotaURL          = "https://cloudcode-pa.googleapis.com/v1internal:retrieveUserQuota"
	geminiProjectsURL       = "https://cloudresourcemanager.googleapis.com/v1/projects"
)

type Gemini struct {
	client *http.Client
}

func NewGemini(client *http.Client) *Gemini {
	if client == nil {
		client = http.DefaultClient
	}
	return &Gemini{client: client}
}

func (g *Gemini) Provider() string {
	return "gemini"
}

func (g *Gemini) Fetch(ctx context.Context, credential QuotaFetchInput) (storage.QuotaData, error) {
	// First, load code assist to get tier and project
	reqBody, _ := json.Marshal(map[string]interface{}{
		"metadata": map[string]string{
			"ideType":     "GEMINI_CLI",
			"platform":    "PLATFORM_UNSPECIFIED",
			"pluginType":  "GEMINI",
			"duetProject": "default",
		},
	})

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, geminiLoadCodeAssistURL, bytes.NewReader(reqBody))
	if err != nil {
		return storage.QuotaData{}, err
	}

	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+credential.Secret)

	resp, err := g.client.Do(req)
	if err != nil {
		return storage.QuotaData{}, err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusUnauthorized {
		return storage.QuotaData{}, ErrUnauthorized
	}
	if resp.StatusCode == http.StatusForbidden {
		return storage.QuotaData{ProviderData: &storage.ProviderQuotaData{IsForbidden: true, LastUpdated: time.Now().UTC().Format(time.RFC3339)}, UpdatedAt: time.Now()}, nil
	}
	if resp.StatusCode != http.StatusOK {
		return storage.QuotaData{}, fmt.Errorf("unexpected status code from loadCodeAssist: %d", resp.StatusCode)
	}

	var loadData map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&loadData); err != nil {
		return storage.QuotaData{}, fmt.Errorf("decode loadCodeAssist: %w", err)
	}

	tierID := g.extractTierId(loadData)
	projectID := g.extractProjectId(loadData)

	if projectID == "" {
		projectID, _ = g.discoverProjectId(ctx, credential.Secret)
	}

	// Now fetch quota
	quotaBody := map[string]string{}
	if projectID != "" {
		quotaBody["project"] = projectID
	}
	quotaBodyBytes, _ := json.Marshal(quotaBody)

	qReq, err := http.NewRequestWithContext(ctx, http.MethodPost, geminiQuotaURL, bytes.NewReader(quotaBodyBytes))
	if err != nil {
		return storage.QuotaData{}, err
	}
	qReq.Header.Set("Accept", "application/json")
	qReq.Header.Set("Content-Type", "application/json")
	qReq.Header.Set("Authorization", "Bearer "+credential.Secret)

	qResp, err := g.client.Do(qReq)
	if err != nil {
		return storage.QuotaData{}, err
	}
	defer qResp.Body.Close()

	if qResp.StatusCode == http.StatusForbidden {
		planType := g.mapTierToPlan(tierID)
		return storage.QuotaData{ProviderData: &storage.ProviderQuotaData{IsForbidden: true, PlanType: planType, PlanDisplayName: planType, LastUpdated: time.Now().UTC().Format(time.RFC3339)}, UpdatedAt: time.Now()}, nil
	}
	if qResp.StatusCode == http.StatusUnauthorized {
		return storage.QuotaData{}, ErrUnauthorized
	}
	if qResp.StatusCode != http.StatusOK {
		return storage.QuotaData{}, fmt.Errorf("unexpected status code from retrieveUserQuota: %d", qResp.StatusCode)
	}

	var quotaData map[string]interface{}
	if err := json.NewDecoder(qResp.Body).Decode(&quotaData); err != nil {
		return storage.QuotaData{}, fmt.Errorf("decode retrieveUserQuota: %w", err)
	}

	return g.buildQuotaData(quotaData, g.mapTierToPlan(tierID)), nil
}

func (g *Gemini) extractTierId(data map[string]interface{}) string {
	if ct, ok := data["currentTier"].(map[string]interface{}); ok {
		if id, ok := ct["id"].(string); ok && id != "" {
			return strings.ToLower(strings.TrimSpace(id))
		}
	}
	for _, key := range []string{"tier", "userTier", "subscriptionTier"} {
		if val, ok := data[key].(string); ok && val != "" {
			return strings.ToLower(strings.TrimSpace(val))
		}
	}
	return ""
}

func (g *Gemini) extractProjectId(data map[string]interface{}) string {
	if direct, ok := data["cloudaicompanionProject"].(string); ok && strings.TrimSpace(direct) != "" {
		return strings.TrimSpace(direct)
	}
	if nested, ok := data["cloudaicompanionProject"].(map[string]interface{}); ok {
		if id, ok := nested["id"].(string); ok && strings.TrimSpace(id) != "" {
			return strings.TrimSpace(id)
		}
		if pid, ok := nested["projectId"].(string); ok && strings.TrimSpace(pid) != "" {
			return strings.TrimSpace(pid)
		}
	}
	return ""
}

func (g *Gemini) discoverProjectId(ctx context.Context, accessToken string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, geminiProjectsURL, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Bearer "+accessToken)

	resp, err := g.client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("bad status: %d", resp.StatusCode)
	}

	var payload struct {
		Projects []struct {
			ProjectID string            `json:"projectId"`
			Labels    map[string]string `json:"labels"`
		} `json:"projects"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return "", err
	}

	for _, p := range payload.Projects {
		pid := strings.TrimSpace(p.ProjectID)
		if pid == "" {
			continue
		}
		if strings.HasPrefix(pid, "gen-lang-client") {
			return pid, nil
		}
		if _, hasLabel := p.Labels["generative-language"]; hasLabel {
			return pid, nil
		}
	}
	return "", nil
}

func (g *Gemini) mapTierToPlan(tierID string) string {
	switch tierID {
	case "standard-tier":
		return "Paid"
	case "legacy-tier":
		return "Legacy"
	case "free-tier":
		return "Free"
	}
	return ""
}

type geminiQuotaBucket struct {
	ModelID           string
	RemainingFraction float64
	ResetTime         string
}

func (g *Gemini) buildQuotaData(quotaPayload map[string]interface{}, planType string) storage.QuotaData {
	var buckets []geminiQuotaBucket
	g.collectQuotaBuckets(quotaPayload, &buckets)

	bestByModel := make(map[string]geminiQuotaBucket)
	for _, b := range buckets {
		if !strings.Contains(strings.ToLower(b.ModelID), "gemini") {
			continue
		}
		existing, ok := bestByModel[b.ModelID]
		if !ok || b.RemainingFraction < existing.RemainingFraction {
			bestByModel[b.ModelID] = b
		}
	}

	var models []storage.QuotaModel
	for _, b := range bestByModel {
		remainingPercent, usedPercent := quotaPercentPointers(b.RemainingFraction * 100)
		models = append(models, storage.QuotaModel{
			Name:             b.ModelID,
			DisplayName:      b.ModelID,
			RemainingPercent: remainingPercent,
			UsedPercent:      usedPercent,
			ResetTime:        b.ResetTime,
			TimeBoundaryKind: "reset",
			QuotaKind:        "window",
			DisplayUnit:      "percent",
			Source:           "gemini_quota_api",
		})
	}

	sort.Slice(models, func(i, j int) bool {
		return models[i].Name < models[j].Name
	})

	pData := storage.ProviderQuotaData{
		IsForbidden:     false,
		PlanType:        planType,
		PlanDisplayName: planType,
		LastUpdated:     time.Now().UTC().Format(time.RFC3339),
		Models:          models,
	}

	return storage.QuotaData{
		UpdatedAt:    time.Now(),
		ProviderData: &pData,
	}
}

func (g *Gemini) collectQuotaBuckets(val interface{}, out *[]geminiQuotaBucket) {
	if slice, ok := val.([]interface{}); ok {
		for _, item := range slice {
			g.collectQuotaBuckets(item, out)
		}
		return
	}

	m, ok := val.(map[string]interface{})
	if !ok {
		return
	}

	if frac, ok := m["remainingFraction"].(float64); ok {
		modelID := "unknown"
		if mid, ok := m["modelId"].(string); ok {
			modelID = mid
		} else if mid, ok := m["model_id"].(string); ok {
			modelID = mid
		}

		resetTime := ""
		if rt, ok := m["resetTime"].(string); ok {
			resetTime = rt
		} else if rt, ok := m["reset_time"].(string); ok {
			resetTime = rt
		}

		*out = append(*out, geminiQuotaBucket{
			ModelID:           modelID,
			RemainingFraction: frac,
			ResetTime:         resetTime,
		})
	}

	for _, nested := range m {
		g.collectQuotaBuckets(nested, out)
	}
}
