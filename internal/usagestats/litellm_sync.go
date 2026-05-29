package usagestats

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
)

const (
	ModelPriceSyncSource  = "litellm"
	LiteLLMModelPricesURL = "https://raw.githubusercontent.com/BerriAI/litellm/main/model_prices_and_context_window.json"
)

type ModelPriceSyncResult struct {
	Source    string                `json:"source"`
	Imported  int                   `json:"imported"`
	Skipped   int                   `json:"skipped"`
	Unmatched []string              `json:"unmatched,omitempty"`
	Prices    map[string]ModelPrice `json:"prices,omitempty"`
}

type ModelPriceSyncStore interface {
	UpsertModelPrices(ctx context.Context, prices map[string]ModelPrice) (InsertResult, error)
	LoadModelPrices(ctx context.Context) (map[string]ModelPrice, error)
}

func SyncLiteLLMModelPrices(
	ctx context.Context,
	store ModelPriceSyncStore,
	client *http.Client,
	sourceURL string,
	models []string,
) (ModelPriceSyncResult, error) {
	if store == nil {
		return ModelPriceSyncResult{}, errors.New("usage stats model price store is required")
	}
	remotePrices, skipped, err := FetchLiteLLMModelPrices(ctx, client, sourceURL)
	if err != nil {
		return ModelPriceSyncResult{}, err
	}
	selectedPrices, unmatched := SelectModelPrices(remotePrices, models)
	stampSyncedModelPrices(selectedPrices, time.Now().UnixMilli())
	result, err := store.UpsertModelPrices(ctx, selectedPrices)
	if err != nil {
		return ModelPriceSyncResult{}, err
	}
	prices, err := store.LoadModelPrices(ctx)
	if err != nil {
		return ModelPriceSyncResult{}, err
	}
	return ModelPriceSyncResult{
		Source:    ModelPriceSyncSource,
		Imported:  result.Inserted,
		Skipped:   result.Skipped + skipped,
		Unmatched: unmatched,
		Prices:    prices,
	}, nil
}

func FetchLiteLLMModelPrices(ctx context.Context, client *http.Client, sourceURL string) (map[string]ModelPrice, int, error) {
	sourceURL = strings.TrimSpace(sourceURL)
	if sourceURL == "" {
		sourceURL = LiteLLMModelPricesURL
	}
	if client == nil {
		client = http.DefaultClient
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, sourceURL, nil)
	if err != nil {
		return nil, 0, err
	}
	res, err := client.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer func() {
		_ = res.Body.Close()
	}()
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return nil, 0, fmt.Errorf("model price sync failed: %s", res.Status)
	}

	var payload map[string]json.RawMessage
	if err := json.NewDecoder(res.Body).Decode(&payload); err != nil {
		return nil, 0, err
	}

	prices := make(map[string]ModelPrice, len(payload))
	skipped := 0
	for model, raw := range payload {
		model = strings.TrimSpace(model)
		if model == "" || model == "sample_spec" {
			skipped++
			continue
		}
		price, ok := liteLLMPriceFromRaw(model, raw)
		if !ok {
			skipped++
			continue
		}
		prices[model] = price
	}
	return prices, skipped, nil
}

func SelectModelPrices(prices map[string]ModelPrice, models []string) (map[string]ModelPrice, []string) {
	wanted := make([]string, 0, len(models))
	seen := map[string]struct{}{}
	for _, model := range models {
		model = strings.TrimSpace(model)
		if model == "" {
			continue
		}
		if _, ok := seen[model]; ok {
			continue
		}
		seen[model] = struct{}{}
		wanted = append(wanted, model)
	}
	if len(wanted) == 0 {
		copied := make(map[string]ModelPrice, len(prices))
		for model, price := range prices {
			copied[model] = price
		}
		return copied, nil
	}

	index := BuildModelPriceIndex(prices)
	selected := map[string]ModelPrice{}
	unmatched := []string{}
	for _, model := range wanted {
		if price, ok := LookupModelPrice(index, prices, model); ok {
			selected[model] = price
			continue
		}
		unmatched = append(unmatched, model)
	}
	return selected, unmatched
}

func liteLLMPriceFromRaw(model string, raw json.RawMessage) (ModelPrice, bool) {
	var entry map[string]any
	if err := json.Unmarshal(raw, &entry); err != nil {
		return ModelPrice{}, false
	}
	prompt, hasPrompt := readPriceFloat(entry, "input_cost_per_token")
	completion, hasCompletion := readPriceFloat(entry, "output_cost_per_token")
	cache, hasCache := readPriceFloat(entry, "cache_read_input_token_cost")
	if !hasCache {
		cache, hasCache = readPriceFloat(entry, "cache_read_cost_per_token")
	}
	if !hasPrompt && !hasCompletion {
		return ModelPrice{}, false
	}
	if !hasPrompt {
		prompt = 0
	}
	if !hasCompletion {
		completion = 0
	}
	if !hasCache {
		cache = prompt
	}
	price := ModelPrice{
		Prompt:        prompt * tokensPerPriceUnit,
		Completion:    completion * tokensPerPriceUnit,
		Cache:         cache * tokensPerPriceUnit,
		Source:        ModelPriceSyncSource,
		SourceModelID: model,
		RawJSON:       string(raw),
	}
	if err := validateModelPrice(model, price); err != nil {
		return ModelPrice{}, false
	}
	return price, true
}

func readPriceFloat(entry map[string]any, key string) (float64, bool) {
	value, ok := entry[key]
	if !ok || value == nil {
		return 0, false
	}
	switch typed := value.(type) {
	case float64:
		return typed, true
	case string:
		parsed, err := strconv.ParseFloat(strings.TrimSpace(typed), 64)
		return parsed, err == nil
	default:
		return 0, false
	}
}

func stampSyncedModelPrices(prices map[string]ModelPrice, now int64) {
	for model, price := range prices {
		if price.Source == "" {
			price.Source = ModelPriceSyncSource
		}
		if price.SourceModelID == "" {
			price.SourceModelID = model
		}
		price.UpdatedAtMS = now
		price.SyncedAtMS = &now
		prices[model] = price
	}
}
