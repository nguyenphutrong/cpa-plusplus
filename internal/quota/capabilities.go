package quota

import (
	"github.com/router-for-me/CLIProxyAPI/v7/internal/quota/providerid"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/quota/providers"
)

type CapabilityStatus string

const (
	CapabilitySupported   CapabilityStatus = "supported"
	CapabilityError       CapabilityStatus = "error"
	CapabilityUnsupported CapabilityStatus = "unsupported"
)

type ProviderCapability struct {
	Supported bool
	Reason    string
}

var providerCapabilities = map[string]ProviderCapability{
	"amp":             {Supported: true},
	"antigravity":     {Supported: true},
	"anthropic":       {Supported: true},
	"codex":           {Supported: true},
	"gemini":          {Supported: true},
	"github-copilot":  {Supported: true},
	"kiro":            {Supported: true},
	"kimi-for-coding": {Supported: true},
	"minimax":         {Supported: true},
	"openai":          {Supported: true},
	"opencode-go":     {Supported: true},
	"z-ai":            {Supported: true},
}

func SupportsProvider(provider string) ProviderCapability {
	normalized := providerid.Normalize(provider)
	if spec, ok := providers.Get(normalized); ok {
		if spec.Quota.Supported {
			return ProviderCapability{Supported: true}
		}
		if spec.Quota.UnsupportedReason != "" {
			return ProviderCapability{Reason: spec.Quota.UnsupportedReason}
		}
	}
	if capability, ok := providerCapabilities[normalized]; ok {
		return capability
	}
	return ProviderCapability{Reason: "Quota is not supported for this provider."}
}

func statusForCapability(capability ProviderCapability, errText string) string {
	if !capability.Supported {
		return string(CapabilityUnsupported)
	}
	if errText != "" {
		return string(CapabilityError)
	}
	return string(CapabilitySupported)
}
