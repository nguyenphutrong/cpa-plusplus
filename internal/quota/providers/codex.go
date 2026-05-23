package providers

var codexProviderSpecs = []Spec{
	{
		ID:           "codex",
		OpenAICompat: false,
		Quota: QuotaSpec{
			Supported: true,
			Strategy:  "codex",
		},
		Runtime: RuntimeSpec{
			Protocols: []string{"openai"},
			OpenAI: OpenAIStrategy{
				DefaultBaseURL: "https://chatgpt.com/backend-api/codex",
				AuthHeader:     "Authorization",
				AuthPrefix:     "Bearer ",
			},
		},
	},
}
