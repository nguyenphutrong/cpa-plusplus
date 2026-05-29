package usagestats

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"strconv"
	"strings"
	"time"

	internallogging "github.com/router-for-me/CLIProxyAPI/v7/internal/logging"
	coreusage "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/usage"
)

func EventFromUsageRecord(ctx context.Context, record coreusage.Record) Event {
	timestamp := record.RequestedAt
	if timestamp.IsZero() {
		timestamp = time.Now()
	}
	timestamp = timestamp.UTC()

	model := strings.TrimSpace(record.Model)
	if model == "" {
		model = "unknown"
	}
	requestedModel := strings.TrimSpace(record.Alias)
	if requestedModel == "" {
		requestedModel = model
	}

	statusCode := record.Fail.StatusCode
	if statusCode <= 0 {
		statusCode = internallogging.GetResponseStatus(ctx)
	}
	failed := record.Failed || statusCode >= 400
	if statusCode <= 0 {
		if failed {
			statusCode = 500
		} else {
			statusCode = 200
		}
	}

	var latencyMS *int64
	if record.Latency > 0 {
		value := record.Latency.Milliseconds()
		if value < 0 {
			value = 0
		}
		latencyMS = &value
	}

	cachedTokens := maxInt64(record.Detail.CachedTokens, record.Detail.CacheReadTokens)
	cacheTokens := record.Detail.CacheCreationTokens
	totalTokens := record.Detail.TotalTokens
	if totalTokens == 0 {
		totalTokens = record.Detail.InputTokens +
			record.Detail.OutputTokens +
			record.Detail.ReasoningTokens +
			maxInt64(cachedTokens, cacheTokens)
	}

	endpoint := strings.TrimSpace(internallogging.GetEndpoint(ctx))
	method, path := splitEndpoint(endpoint)
	accountRaw := strings.TrimSpace(record.Source)
	apiKey := strings.TrimSpace(record.APIKey)
	provider := strings.TrimSpace(record.Provider)
	if provider == "" {
		provider = "unknown"
	}

	event := Event{
		RequestID:        strings.TrimSpace(internallogging.GetRequestID(ctx)),
		TimestampMS:      timestamp.UnixMilli(),
		Provider:         provider,
		Channel:          provider,
		Model:            requestedModel,
		RequestedModel:   requestedModel,
		ResolvedModel:    model,
		Endpoint:         endpoint,
		Method:           method,
		Path:             path,
		AuthType:         strings.TrimSpace(record.AuthType),
		AuthIndex:        strings.TrimSpace(record.AuthIndex),
		Account:          maskSource(accountRaw),
		AccountHash:      hashString(accountRaw),
		APIKeyHash:       hashString(apiKey),
		StatusCode:       statusCode,
		PromptTokens:     record.Detail.InputTokens,
		CompletionTokens: record.Detail.OutputTokens,
		ReasoningTokens:  record.Detail.ReasoningTokens,
		CachedTokens:     cachedTokens,
		CacheTokens:      cacheTokens,
		TotalTokens:      totalTokens,
		LatencyMS:        latencyMS,
		Failed:           failed,
		CreatedAtMS:      time.Now().UnixMilli(),
	}
	event.EventHash = buildEventHash(event)
	return event
}

func splitEndpoint(endpoint string) (string, string) {
	fields := strings.Fields(strings.TrimSpace(endpoint))
	if len(fields) < 2 {
		return "", ""
	}
	method := strings.ToUpper(fields[0])
	switch method {
	case "GET", "POST", "PUT", "PATCH", "DELETE", "OPTIONS", "HEAD":
		return method, fields[1]
	default:
		return "", ""
	}
}

func buildEventHash(event Event) string {
	parts := []string{
		event.RequestID,
		strconv.FormatInt(event.TimestampMS, 10),
		event.Endpoint,
		event.Model,
		event.ResolvedModel,
		event.AuthIndex,
		event.AccountHash,
		strconv.Itoa(event.StatusCode),
		strconv.FormatInt(event.PromptTokens, 10),
		strconv.FormatInt(event.CompletionTokens, 10),
		strconv.FormatInt(event.ReasoningTokens, 10),
		strconv.FormatInt(maxInt64(event.CachedTokens, event.CacheTokens), 10),
		strconv.FormatBool(event.Failed),
	}
	if event.LatencyMS != nil {
		parts = append(parts, strconv.FormatInt(*event.LatencyMS, 10))
	}
	return hashString(strings.Join(parts, "|"))
}

func hashString(value string) string {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(trimmed))
	return hex.EncodeToString(sum[:])
}

func maskSource(value string) string {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return ""
	}
	if strings.Contains(trimmed, "@") {
		parts := strings.SplitN(trimmed, "@", 2)
		prefix := parts[0]
		if len(prefix) > 3 {
			prefix = prefix[:3]
		}
		return prefix + "***@" + parts[1]
	}
	if looksSecret(trimmed) {
		if len(trimmed) <= 8 {
			return "m:****"
		}
		return "m:" + trimmed[:4] + "..." + trimmed[len(trimmed)-4:]
	}
	return trimmed
}

func looksSecret(value string) bool {
	if strings.ContainsAny(value, " /\\") {
		return false
	}
	return strings.HasPrefix(value, "sk-") || strings.HasPrefix(value, "AIza") || len(value) >= 32
}

func maxInt64(left, right int64) int64 {
	if left > right {
		return left
	}
	return right
}
