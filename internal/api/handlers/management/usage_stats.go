package management

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/usagestats"
)

var liteLLMModelPricesURL = usagestats.LiteLLMModelPricesURL

type usageStatsModelPricesRequest struct {
	Prices map[string]usagestats.ModelPrice `json:"prices"`
}

type usageStatsModelPricesSyncRequest struct {
	Models []string `json:"models"`
}

func (h *Handler) GetUsageStatsStatus(c *gin.Context) {
	service := h.requireUsageStats(c)
	if service == nil {
		return
	}
	c.JSON(http.StatusOK, service.Status())
}

func (h *Handler) GetUsageStatsEvents(c *gin.Context) {
	service := h.requireUsageStats(c)
	if service == nil {
		return
	}
	filter, err := parseUsageStatsFilter(c)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_query", "message": err.Error()})
		return
	}
	limit, offset, err := parseUsageStatsPagination(c)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_query", "message": err.Error()})
		return
	}
	events, err := service.QueryEvents(c.Request.Context(), filter, limit, offset)
	if err != nil {
		writeUsageStatsError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"events": events,
		"limit":  limit,
		"offset": offset,
	})
}

func (h *Handler) GetUsageStatsSummary(c *gin.Context) {
	service := h.requireUsageStats(c)
	if service == nil {
		return
	}
	filter, err := parseUsageStatsFilter(c)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_query", "message": err.Error()})
		return
	}
	includeCost := parseUsageStatsBool(c, "include_cost", true)
	summary, err := service.Summary(c.Request.Context(), filter, includeCost)
	if err != nil {
		writeUsageStatsError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"summary": summary})
}

func (h *Handler) GetUsageStatsModelPrices(c *gin.Context) {
	service := h.requireUsageStats(c)
	if service == nil {
		return
	}
	prices, err := service.LoadModelPrices(c.Request.Context())
	if err != nil {
		writeUsageStatsError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"prices": prices})
}

func (h *Handler) PutUsageStatsModelPrices(c *gin.Context) {
	service := h.requireUsageStats(c)
	if service == nil {
		return
	}
	var req usageStatsModelPricesRequest
	if err := json.NewDecoder(c.Request.Body).Decode(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_body", "message": err.Error()})
		return
	}
	if req.Prices == nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "prices_required", "message": "prices are required"})
		return
	}
	if err := service.SaveModelPrices(c.Request.Context(), req.Prices); err != nil {
		writeUsageStatsBadRequestOrServiceError(c, err)
		return
	}
	prices, err := service.LoadModelPrices(c.Request.Context())
	if err != nil {
		writeUsageStatsError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"prices": prices})
}

func (h *Handler) PostUsageStatsModelPricesSync(c *gin.Context) {
	service := h.requireUsageStats(c)
	if service == nil {
		return
	}
	var req usageStatsModelPricesSyncRequest
	if err := json.NewDecoder(c.Request.Body).Decode(&req); err != nil && !errors.Is(err, io.EOF) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_body", "message": err.Error()})
		return
	}
	result, err := service.SyncLiteLLMModelPrices(
		c.Request.Context(),
		http.DefaultClient,
		liteLLMModelPricesURL,
		req.Models,
	)
	if err != nil {
		if errors.Is(err, usagestats.ErrDisabled) {
			writeUsageStatsError(c, err)
			return
		}
		c.JSON(http.StatusBadGateway, gin.H{"error": "model_price_sync_failed", "message": err.Error()})
		return
	}
	c.JSON(http.StatusOK, result)
}

func (h *Handler) requireUsageStats(c *gin.Context) *usagestats.Service {
	if h == nil || h.usageStats == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "usage_stats_unavailable", "message": "usage stats service is unavailable"})
		return nil
	}
	return h.usageStats
}

func writeUsageStatsError(c *gin.Context, err error) {
	if errors.Is(err, usagestats.ErrDisabled) {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "usage_stats_disabled", "message": err.Error()})
		return
	}
	c.JSON(http.StatusInternalServerError, gin.H{"error": "usage_stats_failed", "message": err.Error()})
}

func writeUsageStatsBadRequestOrServiceError(c *gin.Context, err error) {
	if errors.Is(err, usagestats.ErrDisabled) {
		writeUsageStatsError(c, err)
		return
	}
	c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_model_prices", "message": err.Error()})
}

func parseUsageStatsFilter(c *gin.Context) (usagestats.SummaryFilter, error) {
	var filter usagestats.SummaryFilter
	startMS, err := parseOptionalInt64Query(c, "start_ms", "startMs")
	if err != nil {
		return filter, err
	}
	endMS, err := parseOptionalInt64Query(c, "end_ms", "endMs")
	if err != nil {
		return filter, err
	}
	filter.StartMS = startMS
	filter.EndMS = endMS
	filter.Account = strings.TrimSpace(queryFirst(c, "account"))
	filter.Model = strings.TrimSpace(queryFirst(c, "model"))
	filter.Channel = strings.TrimSpace(queryFirst(c, "channel"))
	filter.AuthIndex = strings.TrimSpace(queryFirst(c, "auth_index", "authIndex"))
	return filter, nil
}

func parseUsageStatsPagination(c *gin.Context) (int, int, error) {
	limit := 100
	if raw := strings.TrimSpace(queryFirst(c, "limit")); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed <= 0 {
			return 0, 0, errors.New("limit must be a positive integer")
		}
		limit = parsed
	}
	if limit > 1000 {
		limit = 1000
	}
	offset := 0
	if raw := strings.TrimSpace(queryFirst(c, "offset")); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 0 {
			return 0, 0, errors.New("offset must be a non-negative integer")
		}
		offset = parsed
	}
	return limit, offset, nil
}

func parseOptionalInt64Query(c *gin.Context, keys ...string) (*int64, error) {
	raw := strings.TrimSpace(queryFirst(c, keys...))
	if raw == "" {
		return nil, nil
	}
	value, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || value < 0 {
		return nil, errors.New(keys[0] + " must be a non-negative integer")
	}
	return &value, nil
}

func parseUsageStatsBool(c *gin.Context, key string, fallback bool) bool {
	raw := strings.TrimSpace(c.Query(key))
	if raw == "" {
		return fallback
	}
	switch strings.ToLower(raw) {
	case "1", "true", "yes", "on":
		return true
	case "0", "false", "no", "off":
		return false
	default:
		return fallback
	}
}

func queryFirst(c *gin.Context, keys ...string) string {
	for _, key := range keys {
		if value := c.Query(key); value != "" {
			return value
		}
	}
	return ""
}
