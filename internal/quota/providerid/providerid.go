package providerid

import "strings"

func Normalize(provider string) string {
	switch strings.ToLower(strings.TrimSpace(provider)) {
	case "":
		return ""
	case "claude":
		return "anthropic"
	case "copilot":
		return "github-copilot"
	case "google":
		return "gemini"
	case "azure":
		return "azure-openai"
	case "glm", "zai", "z.ai":
		return "z-ai"
	case "kimi", "kimi-coding":
		return "kimi-for-coding"
	case "vertex (anthropic)", "vertex_anthropic":
		return "vertex-anthropic"
	case "ag":
		return "antigravity"
	default:
		return strings.ToLower(strings.TrimSpace(provider))
	}
}

func EnabledByDefault(provider string) bool {
	return Normalize(provider) != "antigravity"
}

func SupportsNativeAnthropic(provider string) bool {
	switch Normalize(provider) {
	case "anthropic", "z-ai", "vertex-anthropic":
		return true
	default:
		return false
	}
}

func SupportsOpenAIToAnthropicTranslation(provider string) bool {
	switch Normalize(provider) {
	case "anthropic", "vertex-anthropic":
		return true
	default:
		return false
	}
}
