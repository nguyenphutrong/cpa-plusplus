package providers

var nvidiaProviderSpecs = []Spec{
	{
		ID:             "nvidia",
		OpenAICompat:   true,
		DefaultBaseURL: "https://integrate.api.nvidia.com",
		Quota: QuotaSpec{
			Supported:         false,
			UnsupportedReason: "Quota is not supported for NVIDIA NIM in Quotio yet.",
		},
		Runtime: RuntimeSpec{
			Protocols: []string{"openai"},
			OpenAI: OpenAIStrategy{
				DefaultBaseURL: "https://integrate.api.nvidia.com/v1",
				AuthHeader:     "Authorization",
				AuthPrefix:     "Bearer ",
			},
		},
	},
}
