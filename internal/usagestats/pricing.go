package usagestats

import (
	"math"
	"regexp"
	"strings"
)

const tokensPerPriceUnit = 1_000_000

var modelDateSuffixRegex = regexp.MustCompile(`-\d{6,8}$`)

type ModelPriceIndex struct {
	exact        map[string]string
	base         map[string]string
	dateStripped map[string]string
}

func EstimateCostUSD(event Event, prices map[string]ModelPrice) float64 {
	if len(prices) == 0 {
		return 0
	}
	index := BuildModelPriceIndex(prices)
	price, ok := LookupModelPrice(index, prices, event.ResolvedModel)
	if !ok {
		price, ok = LookupModelPrice(index, prices, event.Model)
	}
	if !ok {
		price, ok = LookupModelPrice(index, prices, event.RequestedModel)
	}
	if !ok {
		return 0
	}

	cachedTokens := maxInt64(event.CachedTokens, event.CacheTokens)
	cachedTokens = maxInt64(cachedTokens, 0)
	promptTokens := maxInt64(event.PromptTokens-cachedTokens, 0)
	completionTokens := maxInt64(event.CompletionTokens, 0)

	total := (float64(promptTokens) / tokensPerPriceUnit * price.Prompt) +
		(float64(cachedTokens) / tokensPerPriceUnit * price.Cache) +
		(float64(completionTokens) / tokensPerPriceUnit * price.Completion)
	if math.IsNaN(total) || math.IsInf(total, 0) || total <= 0 {
		return 0
	}
	return total
}

func BuildModelPriceIndex(prices map[string]ModelPrice) *ModelPriceIndex {
	index := &ModelPriceIndex{
		exact:        make(map[string]string, len(prices)),
		base:         make(map[string]string),
		dateStripped: make(map[string]string),
	}
	for key := range prices {
		lower := strings.ToLower(strings.TrimSpace(key))
		if lower == "" {
			continue
		}
		setShortestKey(index.exact, lower, key)
		baseName := lastPathSegment(lower)
		setShortestKey(index.base, baseName, key)
		stripped := stripModelDateSuffix(baseName)
		if stripped != baseName {
			setShortestKey(index.dateStripped, stripped, key)
		}
	}
	return index
}

func LookupModelPrice(index *ModelPriceIndex, prices map[string]ModelPrice, model string) (ModelPrice, bool) {
	if price, ok := prices[model]; ok {
		return price, true
	}
	if index == nil {
		return ModelPrice{}, false
	}
	lower := strings.ToLower(strings.TrimSpace(model))
	if lower == "" {
		return ModelPrice{}, false
	}
	if key, ok := index.exact[lower]; ok {
		if price, ok := prices[key]; ok {
			return price, true
		}
	}
	baseName := lastPathSegment(lower)
	if key, ok := index.base[baseName]; ok {
		if price, ok := prices[key]; ok {
			return price, true
		}
	}
	stripped := stripModelDateSuffix(baseName)
	if stripped != baseName {
		if key, ok := index.base[stripped]; ok {
			if price, ok := prices[key]; ok {
				return price, true
			}
		}
		if key, ok := index.dateStripped[stripped]; ok {
			if price, ok := prices[key]; ok {
				return price, true
			}
		}
	}
	if key, ok := index.dateStripped[baseName]; ok {
		if price, ok := prices[key]; ok {
			return price, true
		}
	}
	return ModelPrice{}, false
}

func setShortestKey(target map[string]string, key string, candidate string) {
	if existing, ok := target[key]; !ok || len(candidate) < len(existing) {
		target[key] = candidate
	}
}

func lastPathSegment(value string) string {
	idx := strings.LastIndex(value, "/")
	if idx < 0 {
		return value
	}
	return value[idx+1:]
}

func stripModelDateSuffix(value string) string {
	return modelDateSuffixRegex.ReplaceAllString(value, "")
}
