package providers

import "testing"

func TestOpenAIConvertToQuotaDataDoesNotMarkLimitReachedAsForbidden(t *testing.T) {
	t.Parallel()

	fetcher := NewOpenAI(nil)
	data := fetcher.convertToQuotaData(openAIUsageResponse{
		PlanType: "plus",
		RateLimit: &openAIRateLimit{
			Allowed:      false,
			LimitReached: true,
			PrimaryWindow: &openAIWindow{
				UsedPercent: 100,
			},
		},
	})

	if data.ProviderData == nil {
		t.Fatal("expected provider data")
	}
	if data.ProviderData.IsForbidden {
		t.Fatal("limit_reached must not be treated as forbidden")
	}
	if len(data.ProviderData.Models) != 1 {
		t.Fatalf("models len = %d, want 1", len(data.ProviderData.Models))
	}
	if got := derefFloat(data.ProviderData.Models[0].UsedPercent); got != 100 {
		t.Fatalf("used percent = %v, want 100", got)
	}
	if got := derefFloat(data.ProviderData.Models[0].RemainingPercent); got != 0 {
		t.Fatalf("remaining percent = %v, want 0", got)
	}
}
