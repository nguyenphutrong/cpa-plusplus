package providers

import "time"

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
