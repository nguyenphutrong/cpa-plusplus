package usagestats

import "testing"

func TestEstimateCostUSDUsesResolvedThenRequestedModel(t *testing.T) {
	prices := map[string]ModelPrice{
		"gpt-5": {Prompt: 10, Completion: 20, Cache: 1},
		"alias": {Prompt: 100, Completion: 200, Cache: 10},
	}

	got := EstimateCostUSD(Event{
		Model:            "alias",
		RequestedModel:   "alias",
		ResolvedModel:    "gpt-5",
		PromptTokens:     1_000_000,
		CompletionTokens: 500_000,
		CachedTokens:     100_000,
	}, prices)
	want := 19.1
	if got != want {
		t.Fatalf("EstimateCostUSD() = %v, want %v", got, want)
	}

	index := BuildModelPriceIndex(prices)
	indexed, ok := EstimateCostUSDWithIndex(Event{
		Model:            "alias",
		RequestedModel:   "alias",
		ResolvedModel:    "gpt-5",
		PromptTokens:     1_000_000,
		CompletionTokens: 500_000,
		CachedTokens:     100_000,
	}, prices, index)
	if !ok || indexed != want {
		t.Fatalf("EstimateCostUSDWithIndex() = %v, %v; want %v, true", indexed, ok, want)
	}

	_, ok = EstimateCostUSDWithIndex(Event{ResolvedModel: "missing"}, prices, index)
	if ok {
		t.Fatal("missing model should not return cost")
	}
}

func TestLookupModelPriceMatchesProviderPrefixAndDateSuffix(t *testing.T) {
	prices := map[string]ModelPrice{
		"openai/gpt-5": {Prompt: 1},
		"claude-4":     {Prompt: 2},
	}
	index := BuildModelPriceIndex(prices)

	price, ok := LookupModelPrice(index, prices, "gpt-5")
	if !ok || price.Prompt != 1 {
		t.Fatalf("provider prefix lookup = %#v, %v", price, ok)
	}

	price, ok = LookupModelPrice(index, prices, "claude-4-20260530")
	if !ok || price.Prompt != 2 {
		t.Fatalf("date suffix lookup = %#v, %v", price, ok)
	}
}
