package providers

import (
	"sort"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/quota/providerid"
)

var registry = buildRegistry()

func buildRegistry() map[string]Spec {
	entries := []Spec{}
	entries = append(entries, defaultProviderSpecs...)
	entries = append(entries, openAIProviderSpecs...)
	entries = append(entries, anthropicProviderSpecs...)
	entries = append(entries, codexProviderSpecs...)
	entries = append(entries, githubCopilotProviderSpecs...)
	entries = append(entries, kiroProviderSpecs...)
	entries = append(entries, kimiForCodingProviderSpecs...)
	entries = append(entries, antigravityProviderSpecs...)
	entries = append(entries, nvidiaProviderSpecs...)
	entries = append(entries, opencodeProviderSpecs...)
	entries = append(entries, opencodeGoProviderSpecs...)
	entries = append(entries, genericOpenAICompatProviderSpecs...)

	out := make(map[string]Spec, len(entries))
	for _, spec := range entries {
		if spec.ID == "" {
			continue
		}
		if spec.Quota.Fetch == nil {
			spec.Quota.Fetch = quotaFetchByStrategy(spec.Quota.Strategy)
		}
		out[spec.ID] = spec
	}
	return out
}

func Get(provider string) (Spec, bool) {
	id := providerid.Normalize(provider)
	spec, ok := registry[id]
	return spec, ok
}

func IsOpenAICompat(provider string) bool {
	spec, ok := Get(provider)
	return ok && spec.OpenAICompat
}

func DefaultBaseURLs() map[string]string {
	out := make(map[string]string, len(registry))
	for id, spec := range registry {
		out[id] = spec.DefaultBaseURL
	}
	return out
}

func OpenAICompatProviderIDs() []string {
	ids := make([]string, 0, len(registry))
	for _, spec := range registry {
		if spec.OpenAICompat {
			ids = append(ids, spec.ID)
		}
	}
	sort.Strings(ids)
	return ids
}

func SupportsProtocol(provider, protocol string) bool {
	spec, ok := Get(provider)
	if !ok {
		return false
	}
	for _, p := range spec.Runtime.Protocols {
		if p == protocol {
			return true
		}
	}
	return false
}

func All() []Spec {
	specs := make([]Spec, 0, len(registry))
	for _, spec := range registry {
		specs = append(specs, spec)
	}
	sort.Slice(specs, func(i, j int) bool {
		return specs[i].ID < specs[j].ID
	})
	return specs
}
