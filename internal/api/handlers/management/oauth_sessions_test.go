package management

import "testing"

func TestNormalizeOAuthProviderIncludesCopilotAndKiroAliases(t *testing.T) {
	tests := map[string]string{
		"kiro":           "kiro",
		"github":         "github-copilot",
		"copilot":        "github-copilot",
		"github-copilot": "github-copilot",
		"kimi":           "kimi",
	}
	for input, want := range tests {
		got, err := NormalizeOAuthProvider(input)
		if err != nil {
			t.Fatalf("NormalizeOAuthProvider(%q) error = %v", input, err)
		}
		if got != want {
			t.Fatalf("NormalizeOAuthProvider(%q) = %q, want %q", input, got, want)
		}
	}
}
