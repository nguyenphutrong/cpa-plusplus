package usagestats

import (
	"context"
	"path/filepath"
	"testing"
)

func TestSQLiteStoreInsertQueryAndSummary(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t)
	defer store.Close()

	latency := int64(25)
	events := []Event{
		{
			RequestID:        "req-1",
			TimestampMS:      1000,
			Channel:          "codex",
			Model:            "client-gpt",
			RequestedModel:   "client-gpt",
			ResolvedModel:    "gpt-5",
			Account:          "per***@example.com",
			StatusCode:       200,
			PromptTokens:     100,
			CompletionTokens: 50,
			TotalTokens:      150,
			LatencyMS:        &latency,
		},
		{
			RequestID:        "req-2",
			TimestampMS:      2000,
			Channel:          "claude",
			Model:            "claude-sonnet",
			Account:          "team",
			StatusCode:       500,
			PromptTokens:     20,
			CompletionTokens: 10,
		},
	}
	result, err := store.InsertEvents(ctx, events)
	if err != nil {
		t.Fatalf("InsertEvents: %v", err)
	}
	if result.Inserted != 2 || result.Skipped != 0 {
		t.Fatalf("insert result = %#v", result)
	}

	result, err = store.InsertEvents(ctx, []Event{events[0]})
	if err != nil {
		t.Fatalf("InsertEvents duplicate: %v", err)
	}
	if result.Inserted != 0 || result.Skipped != 1 {
		t.Fatalf("duplicate insert result = %#v", result)
	}

	got, err := store.QueryEvents(ctx, SummaryFilter{Model: "gpt-5"}, 10, 0)
	if err != nil {
		t.Fatalf("QueryEvents: %v", err)
	}
	if len(got) != 1 || got[0].RequestID != "req-1" {
		t.Fatalf("model query = %#v", got)
	}

	start := int64(1500)
	summary, err := store.Summary(ctx, SummaryFilter{StartMS: &start}, false)
	if err != nil {
		t.Fatalf("Summary: %v", err)
	}
	if summary.TotalRequests != 1 || summary.FailureCount != 1 || summary.Tokens.TotalTokens != 30 {
		t.Fatalf("summary = %#v", summary)
	}
}

func TestSQLiteStoreModelPricesAndCostSummary(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t)
	defer store.Close()

	if err := store.SaveModelPrices(ctx, map[string]ModelPrice{
		"gpt-5": {Prompt: 10, Completion: 20, Cache: 1},
	}); err != nil {
		t.Fatalf("SaveModelPrices: %v", err)
	}
	if _, err := store.InsertEvents(ctx, []Event{{
		RequestID:        "req-1",
		TimestampMS:      1000,
		Model:            "client-gpt",
		ResolvedModel:    "gpt-5",
		StatusCode:       200,
		PromptTokens:     1_000_000,
		CompletionTokens: 500_000,
		CachedTokens:     100_000,
	}}); err != nil {
		t.Fatalf("InsertEvents: %v", err)
	}

	summary, err := store.Summary(ctx, SummaryFilter{}, true)
	if err != nil {
		t.Fatalf("Summary: %v", err)
	}
	if summary.EstimatedCostUSD != 19.1 {
		t.Fatalf("EstimatedCostUSD = %v", summary.EstimatedCostUSD)
	}

	prices, err := store.LoadModelPrices(ctx)
	if err != nil {
		t.Fatalf("LoadModelPrices: %v", err)
	}
	if prices["gpt-5"].Prompt != 10 {
		t.Fatalf("prices = %#v", prices)
	}
}

func openTestStore(t *testing.T) *SQLiteStore {
	t.Helper()

	store, err := OpenSQLiteStore(filepath.Join(t.TempDir(), "usage.sqlite"))
	if err != nil {
		t.Fatalf("OpenSQLiteStore: %v", err)
	}
	return store
}
