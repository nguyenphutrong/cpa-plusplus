package providers

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"testing"
)

func TestKiroFetchReturnsUnavailableForAWSDeviceWithoutProfileARN(t *testing.T) {
	t.Parallel()

	transport := &kiroQuotaHeaderTransport{}
	fetcher := NewKiro(&http.Client{Transport: transport})
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
	if len(transport.requests) != 3 {
		t.Fatalf("requests = %d, want 3", len(transport.requests))
	}
	for _, req := range transport.requests {
		if got := req.Header.Get("x-amzn-kiro-agent-mode"); got != "vibe" {
			t.Fatalf("x-amzn-kiro-agent-mode = %q, want vibe", got)
		}
		if got := req.Header.Get("Amz-Sdk-Invocation-Id"); got == "" {
			t.Fatal("missing Amz-Sdk-Invocation-Id")
		}
	}
}

type kiroQuotaHeaderTransport struct {
	requests []*http.Request
}

func (t *kiroQuotaHeaderTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	t.requests = append(t.requests, req.Clone(req.Context()))
	return &http.Response{
		StatusCode: http.StatusForbidden,
		Header:     make(http.Header),
		Body:       io.NopCloser(bytes.NewBufferString(`{"message":"forbidden"}`)),
		Request:    req,
	}, nil
}
