package providers

import (
	"context"
	"testing"
)

func TestKiroFetchReturnsUnavailableForAWSDeviceWithoutProfileARN(t *testing.T) {
	t.Parallel()

	fetcher := NewKiro(nil)
	data, err := fetcher.Fetch(context.Background(), QuotaFetchInput{
		ProviderID: "kiro",
		Secret:     "token",
		Metadata: map[string]any{
			"auth_method": "aws-device",
			"region":      "us-east-1",
		},
	})
	if err != nil {
		t.Fatalf("fetch quota: %v", err)
	}
	if data.ProviderData == nil {
		t.Fatal("expected provider data")
	}
	if got := data.ProviderData.PlanType; got != "quota-unavailable" {
		t.Fatalf("plan type = %q, want quota-unavailable", got)
	}
	if data.Error == "" {
		t.Fatal("expected quota-unavailable reason")
	}
}
