package providers

var opencodeProviderSpecs = []Spec{
	{
		ID:             "opencode",
		OpenAICompat:   true,
		DefaultBaseURL: "https://opencode.ai/zen",
		Runtime: RuntimeSpec{
			Protocols: []string{"anthropic"},
			Anthropic: AnthropicStrategy{
				DefaultBaseURL: "https://opencode.ai/zen",
				AuthHeader:     "x-api-key",
				AuthPrefix:     "",
				ExtraHeaders: map[string]string{
					"anthropic-version": "2023-06-01",
				},
			},
		},
	},
}
