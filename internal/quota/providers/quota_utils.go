package providers

import (
	"fmt"
	"strings"
	"time"
)

func clampPercent(val float64) float64 {
	if val < 0 {
		return 0
	}
	if val > 100 {
		return 100
	}
	return val
}

func formatResetTime(resetAt int64) string {
	if resetAt <= 0 {
		return ""
	}
	return time.Unix(resetAt, 0).UTC().Format(time.RFC3339)
}

func floatPtr(value float64) *float64 {
	return &value
}

func quotaPercentPointers(remaining float64) (*float64, *float64) {
	clamped := clampPercent(remaining)
	return floatPtr(clamped), floatPtr(100 - clamped)
}

func inputString(input QuotaFetchInput, keys ...string) string {
	for _, key := range keys {
		if value := strings.TrimSpace(input.Headers[key]); value != "" {
			return value
		}
		if value := strings.TrimSpace(input.Attributes[key]); value != "" {
			return value
		}
		if value := metadataString(input.Metadata, key); value != "" {
			return value
		}
	}
	return ""
}

func metadataString(metadata map[string]any, key string) string {
	if metadata == nil {
		return ""
	}
	raw, ok := metadata[key]
	if !ok || raw == nil {
		return ""
	}
	switch value := raw.(type) {
	case string:
		return strings.TrimSpace(value)
	case fmt.Stringer:
		return strings.TrimSpace(value.String())
	case float64, float32, int, int64, int32, uint, uint64, uint32:
		return strings.TrimSpace(fmt.Sprint(value))
	default:
		return ""
	}
}
