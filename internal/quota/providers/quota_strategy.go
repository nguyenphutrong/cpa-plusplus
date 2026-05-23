package providers

import (
	"context"
	"net/http"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/quota/storage"
)

func quotaFetchByStrategy(strategy string) QuotaFetchFunc {
	switch strings.TrimSpace(strategy) {
	case "openai":
		return fetchOpenAIQuota("openai")
	case "codex":
		return fetchOpenAIQuota("codex")
	case "anthropic":
		return fetchAnthropicQuota
	case "gemini":
		return fetchGeminiQuota
	case "github-copilot":
		return fetchGitHubCopilotQuota
	case "kiro":
		return fetchKiroQuota
	case "kimi-for-coding":
		return fetchKimiForCodingQuota
	case "z-ai":
		return fetchZAIQuota
	case "opencode-go":
		return fetchOpenCodeGoQuota
	case "amp":
		return fetchAmpQuota
	case "minimax":
		return fetchMiniMaxQuota
	case "antigravity":
		return fetchAntigravityQuota
	default:
		return nil
	}
}

func fetchOpenAIQuota(provider string) QuotaFetchFunc {
	return func(ctx context.Context, input QuotaFetchInput) (storage.QuotaData, error) {
		client := input.HTTPClient
		if client == nil {
			client = http.DefaultClient
		}
		return NewOpenAIForProvider(provider, client).Fetch(ctx, input)
	}
}

func fetchAnthropicQuota(ctx context.Context, input QuotaFetchInput) (storage.QuotaData, error) {
	client := input.HTTPClient
	if client == nil {
		client = http.DefaultClient
	}
	return NewAnthropic(client).Fetch(ctx, input)
}

func fetchGeminiQuota(ctx context.Context, input QuotaFetchInput) (storage.QuotaData, error) {
	client := input.HTTPClient
	if client == nil {
		client = http.DefaultClient
	}
	return NewGemini(client).Fetch(ctx, input)
}

func fetchGitHubCopilotQuota(ctx context.Context, input QuotaFetchInput) (storage.QuotaData, error) {
	client := input.HTTPClient
	if client == nil {
		client = http.DefaultClient
	}
	return NewGitHubCopilot(client).Fetch(ctx, input)
}

func fetchKiroQuota(ctx context.Context, input QuotaFetchInput) (storage.QuotaData, error) {
	client := input.HTTPClient
	if client == nil {
		client = http.DefaultClient
	}
	return NewKiro(client).Fetch(ctx, input)
}

func fetchKimiForCodingQuota(ctx context.Context, input QuotaFetchInput) (storage.QuotaData, error) {
	client := input.HTTPClient
	if client == nil {
		client = http.DefaultClient
	}
	return NewKimiForCoding(client).Fetch(ctx, input)
}

func fetchZAIQuota(ctx context.Context, input QuotaFetchInput) (storage.QuotaData, error) {
	client := input.HTTPClient
	if client == nil {
		client = http.DefaultClient
	}
	return NewZAI(client).Fetch(ctx, input)
}

func fetchOpenCodeGoQuota(ctx context.Context, input QuotaFetchInput) (storage.QuotaData, error) {
	client := input.HTTPClient
	if client == nil {
		client = http.DefaultClient
	}
	return NewOpenCodeGo(client, input.Logger).Fetch(ctx, input)
}

func fetchAmpQuota(ctx context.Context, input QuotaFetchInput) (storage.QuotaData, error) {
	client := input.HTTPClient
	if client == nil {
		client = http.DefaultClient
	}
	return NewAmp(client).Fetch(ctx, input)
}

func fetchMiniMaxQuota(ctx context.Context, input QuotaFetchInput) (storage.QuotaData, error) {
	client := input.HTTPClient
	if client == nil {
		client = http.DefaultClient
	}
	return NewMiniMax(client).Fetch(ctx, input)
}

func fetchAntigravityQuota(ctx context.Context, input QuotaFetchInput) (storage.QuotaData, error) {
	client := input.HTTPClient
	if client == nil {
		client = http.DefaultClient
	}
	return NewAntigravity(client).Fetch(ctx, input)
}
