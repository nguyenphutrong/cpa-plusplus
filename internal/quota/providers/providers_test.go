package providers

import (
	"reflect"
	"sort"
	"testing"
)

func TestIsOpenAICompat(t *testing.T) {
	if !IsOpenAICompat("nvidia") {
		t.Fatal("expected nvidia to be openai-compatible")
	}
	if !IsOpenAICompat("kimi") {
		t.Fatal("expected kimi alias to resolve to kimi-for-coding")
	}
	if IsOpenAICompat("codex") {
		t.Fatal("expected codex to not be included in openai-compatible validation set")
	}
}

func TestDefaultBaseURLsContainsExpectedProviders(t *testing.T) {
	baseURLs := DefaultBaseURLs()
	if got := baseURLs["nvidia"]; got != "https://integrate.api.nvidia.com" {
		t.Fatalf("nvidia baseURL = %q", got)
	}
	if got := baseURLs["kimi-for-coding"]; got != "https://api.kimi.com/coding/v1" {
		t.Fatalf("kimi-for-coding baseURL = %q", got)
	}
}

func TestOpenAICompatProviderIDsAreSorted(t *testing.T) {
	ids := OpenAICompatProviderIDs()
	sorted := append([]string(nil), ids...)
	sort.Strings(sorted)
	if !reflect.DeepEqual(ids, sorted) {
		t.Fatalf("openai compat ids must be sorted, got %v", ids)
	}
}

func TestQuotaSpecFetchWiring(t *testing.T) {
	for _, spec := range All() {
		if spec.Quota.Supported && spec.Quota.Fetch == nil {
			t.Fatalf("provider %q has quota supported but missing quota fetch function", spec.ID)
		}
		if !spec.Quota.Supported && spec.Quota.Fetch != nil {
			t.Fatalf("provider %q has quota fetch set but quota is unsupported", spec.ID)
		}
	}
}
