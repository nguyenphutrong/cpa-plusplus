package usagestats

import (
	"context"
	"encoding/json"
	"math"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestFetchLiteLLMModelPricesParsesTokenPrices(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"sample_spec": {},
			"gpt-test": {
				"input_cost_per_token": 0.00000125,
				"output_cost_per_token": "0.0000025",
				"cache_read_input_token_cost": 0.0000001,
				"mode": "chat"
			},
			"completion-only": {
				"output_cost_per_token": 0.000003
			},
			"image-only": {
				"output_cost_per_image": 0.04,
				"mode": "image_generation"
			}
		}`))
	}))
	t.Cleanup(server.Close)

	prices, skipped, err := FetchLiteLLMModelPrices(context.Background(), server.Client(), server.URL)
	if err != nil {
		t.Fatalf("FetchLiteLLMModelPrices: %v", err)
	}
	if skipped != 2 {
		t.Fatalf("skipped = %d, want 2", skipped)
	}
	price, ok := prices["gpt-test"]
	if !ok {
		t.Fatalf("missing gpt-test price: %#v", prices)
	}
	if !closePrice(price.Prompt, 1.25) || !closePrice(price.Completion, 2.5) || !closePrice(price.Cache, 0.1) {
		t.Fatalf("price = %#v", price)
	}
	if price.Source != ModelPriceSyncSource || price.SourceModelID != "gpt-test" {
		t.Fatalf("metadata = %#v", price)
	}
	if !json.Valid([]byte(price.RawJSON)) {
		t.Fatalf("raw json is not valid: %q", price.RawJSON)
	}
	if got := prices["completion-only"]; !closePrice(got.Prompt, 0) || !closePrice(got.Completion, 3) {
		t.Fatalf("completion-only price = %#v", got)
	}
}

func TestSelectModelPricesMatchesPriorityAndCopiesAll(t *testing.T) {
	prices := map[string]ModelPrice{
		"gpt-4o-2024-08-06":                      {Prompt: 2.5, Completion: 10},
		"anthropic/claude-3.5-sonnet":            {Prompt: 3, Completion: 15},
		"openrouter/anthropic/claude-3.5-sonnet": {Prompt: 3.1, Completion: 15.1},
		"gemini/gemini-2.5-flash":                {Prompt: 0.075, Completion: 0.3},
		"claude-sonnet-4-5-20250929":             {Prompt: 3.2, Completion: 16},
	}

	selected, unmatched := SelectModelPrices(prices, []string{
		"gpt-4o-2024-08-06",
		"GEMINI/Gemini-2.5-Flash",
		"claude-3.5-sonnet",
		"claude-sonnet-4-5",
		"unknown-model-xyz",
		"unknown-model-xyz",
	})

	if got := selected["gpt-4o-2024-08-06"].Prompt; got != 2.5 {
		t.Fatalf("exact prompt = %v", got)
	}
	if got := selected["GEMINI/Gemini-2.5-Flash"].Prompt; got != 0.075 {
		t.Fatalf("case-insensitive prompt = %v", got)
	}
	if got := selected["claude-3.5-sonnet"].Prompt; got != 3 {
		t.Fatalf("basename should prefer shortest key, got prompt = %v", got)
	}
	if got := selected["claude-sonnet-4-5"].Prompt; got != 3.2 {
		t.Fatalf("date-stripped prompt = %v", got)
	}
	if len(unmatched) != 1 || unmatched[0] != "unknown-model-xyz" {
		t.Fatalf("unmatched = %#v", unmatched)
	}

	all, unmatched := SelectModelPrices(prices, nil)
	if len(all) != len(prices) {
		t.Fatalf("all count = %d, want %d", len(all), len(prices))
	}
	if len(unmatched) != 0 {
		t.Fatalf("unmatched for all = %#v", unmatched)
	}
	all["gpt-4o-2024-08-06"] = ModelPrice{Prompt: 999}
	if prices["gpt-4o-2024-08-06"].Prompt == 999 {
		t.Fatalf("all selection should be a copy")
	}
}

func TestSyncLiteLLMModelPricesPersistsSelectedPrices(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"gpt-test": {
				"input_cost_per_token": 0.00000125,
				"output_cost_per_token": 0.0000025
			},
			"other-model": {
				"input_cost_per_token": 0.000009,
				"output_cost_per_token": 0.000010
			}
		}`))
	}))
	t.Cleanup(server.Close)
	store := openTestStore(t)
	defer store.Close()

	result, err := SyncLiteLLMModelPrices(
		context.Background(),
		store,
		server.Client(),
		server.URL,
		[]string{"gpt-test", "missing"},
	)
	if err != nil {
		t.Fatalf("SyncLiteLLMModelPrices: %v", err)
	}
	if result.Source != ModelPriceSyncSource || result.Imported != 1 || result.Skipped != 0 {
		t.Fatalf("result = %#v", result)
	}
	if len(result.Unmatched) != 1 || result.Unmatched[0] != "missing" {
		t.Fatalf("unmatched = %#v", result.Unmatched)
	}
	price, ok := result.Prices["gpt-test"]
	if !ok {
		t.Fatalf("missing persisted gpt-test price: %#v", result.Prices)
	}
	if price.SyncedAtMS == nil || *price.SyncedAtMS <= 0 || price.UpdatedAtMS <= 0 {
		t.Fatalf("missing sync timestamps: %#v", price)
	}
	if _, ok := result.Prices["other-model"]; ok {
		t.Fatalf("unselected model was persisted: %#v", result.Prices)
	}
}

func closePrice(left, right float64) bool {
	return math.Abs(left-right) < 0.0000001
}
