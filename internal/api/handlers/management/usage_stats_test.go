package management

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/usagestats"
	coreusage "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/usage"
)

func TestUsageStatsEventsAndSummary(t *testing.T) {
	handler, service := newUsageStatsTestHandler(t)
	if err := service.SaveModelPrices(context.Background(), map[string]usagestats.ModelPrice{
		"gpt-5": {Prompt: 10, Completion: 20, Cache: 1},
	}); err != nil {
		t.Fatalf("SaveModelPrices: %v", err)
	}
	service.HandleUsage(context.Background(), coreusage.Record{
		Provider:    "codex",
		Model:       "gpt-5",
		Alias:       "client-gpt",
		RequestedAt: time.UnixMilli(1000),
		Source:      "person@example.com",
		Detail:      coreusage.Detail{InputTokens: 1000, OutputTokens: 500, TotalTokens: 1500},
	})

	rec := performUsageStatsRequest(t, handler.GetUsageStatsEvents, http.MethodGet, "/v0/management/usage-stats/events?model=gpt-5", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("events status = %d body=%s", rec.Code, rec.Body.String())
	}
	var eventsBody struct {
		Events []usagestats.Event `json:"events"`
		Limit  int                `json:"limit"`
		Offset int                `json:"offset"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &eventsBody); err != nil {
		t.Fatalf("decode events: %v", err)
	}
	if len(eventsBody.Events) != 1 || eventsBody.Events[0].ResolvedModel != "gpt-5" {
		t.Fatalf("events body = %#v", eventsBody)
	}

	rec = performUsageStatsRequest(t, handler.GetUsageStatsSummary, http.MethodGet, "/v0/management/usage-stats/summary?model=gpt-5", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("summary status = %d body=%s", rec.Code, rec.Body.String())
	}
	var summaryBody struct {
		Summary usagestats.Summary `json:"summary"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &summaryBody); err != nil {
		t.Fatalf("decode summary: %v", err)
	}
	if summaryBody.Summary.TotalRequests != 1 || summaryBody.Summary.Tokens.TotalTokens != 1500 || summaryBody.Summary.EstimatedCostUSD <= 0 {
		t.Fatalf("summary body = %#v", summaryBody)
	}
}

func TestUsageStatsModelPricesAndSync(t *testing.T) {
	handler, _ := newUsageStatsTestHandler(t)

	rec := performUsageStatsRequest(
		t,
		handler.PutUsageStatsModelPrices,
		http.MethodPut,
		"/v0/management/model-prices",
		[]byte(`{"prices":{"manual-model":{"prompt":1.25,"completion":2.5,"cache":0.1}}}`),
	)
	if rec.Code != http.StatusOK {
		t.Fatalf("put prices status = %d body=%s", rec.Code, rec.Body.String())
	}
	var pricesBody struct {
		Prices map[string]usagestats.ModelPrice `json:"prices"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &pricesBody); err != nil {
		t.Fatalf("decode prices: %v", err)
	}
	if price := pricesBody.Prices["manual-model"]; price.Prompt != 1.25 || price.Completion != 2.5 || price.Cache != 0.1 {
		t.Fatalf("manual price = %#v", price)
	}

	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"gpt-test": {
				"input_cost_per_token": 0.000001,
				"output_cost_per_token": 0.000002
			},
			"image-only": {
				"output_cost_per_image": 0.04
			}
		}`))
	}))
	t.Cleanup(source.Close)
	oldURL := liteLLMModelPricesURL
	liteLLMModelPricesURL = source.URL
	t.Cleanup(func() {
		liteLLMModelPricesURL = oldURL
	})

	rec = performUsageStatsRequest(
		t,
		handler.PostUsageStatsModelPricesSync,
		http.MethodPost,
		"/v0/management/model-prices/sync",
		[]byte(`{"models":["gpt-test","missing-model"]}`),
	)
	if rec.Code != http.StatusOK {
		t.Fatalf("sync status = %d body=%s", rec.Code, rec.Body.String())
	}
	var syncBody usagestats.ModelPriceSyncResult
	if err := json.Unmarshal(rec.Body.Bytes(), &syncBody); err != nil {
		t.Fatalf("decode sync: %v", err)
	}
	if syncBody.Source != usagestats.ModelPriceSyncSource || syncBody.Imported != 1 || syncBody.Skipped != 1 {
		t.Fatalf("sync body = %#v", syncBody)
	}
	if len(syncBody.Unmatched) != 1 || syncBody.Unmatched[0] != "missing-model" {
		t.Fatalf("unmatched = %#v", syncBody.Unmatched)
	}
	if price := syncBody.Prices["gpt-test"]; price.Source != usagestats.ModelPriceSyncSource || price.SourceModelID != "gpt-test" || price.SyncedAtMS == nil {
		t.Fatalf("synced price = %#v", price)
	}
}

func TestUsageStatsDisabledReturnsServiceUnavailable(t *testing.T) {
	handler := NewHandler(&config.Config{}, "", nil)
	rec := performUsageStatsRequest(t, handler.GetUsageStatsSummary, http.MethodGet, "/v0/management/usage-stats/summary", nil)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
}

func newUsageStatsTestHandler(t *testing.T) (*Handler, *usagestats.Service) {
	t.Helper()
	service := usagestats.NewService()
	if err := service.Configure(context.Background(), true, filepath.Join(t.TempDir(), "usage.sqlite")); err != nil {
		t.Fatalf("Configure usage stats: %v", err)
	}
	t.Cleanup(func() {
		if err := service.Close(); err != nil {
			t.Fatalf("Close usage stats: %v", err)
		}
	})
	handler := NewHandler(&config.Config{UsageStatisticsEnabled: true}, "", nil)
	handler.SetUsageStatsService(service)
	return handler, service
}

func performUsageStatsRequest(
	t *testing.T,
	fn gin.HandlerFunc,
	method string,
	target string,
	body []byte,
) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	ginCtx, _ := gin.CreateTestContext(rec)
	ginCtx.Request = httptest.NewRequest(method, target, bytes.NewReader(body))
	fn(ginCtx)
	return rec
}
