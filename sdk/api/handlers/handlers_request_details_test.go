package handlers

import (
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v7/sdk/config"
)

func TestGetRequestDetails_PreservesSuffix(t *testing.T) {
	modelRegistry := registry.GetGlobalRegistry()
	now := time.Now().Unix()

	modelRegistry.RegisterClient("test-request-details-gemini", "gemini", []*registry.ModelInfo{
		{ID: "gemini-2.5-pro", Created: now + 30},
		{ID: "gemini-2.5-flash", Created: now + 25},
	})
	modelRegistry.RegisterClient("test-request-details-openai", "openai", []*registry.ModelInfo{
		{ID: "gpt-5.2", Created: now + 20},
	})
	modelRegistry.RegisterClient("test-request-details-claude", "claude", []*registry.ModelInfo{
		{ID: "claude-sonnet-4-5", Created: now + 5},
	})

	// Ensure cleanup of all test registrations.
	clientIDs := []string{
		"test-request-details-gemini",
		"test-request-details-openai",
		"test-request-details-claude",
	}
	for _, clientID := range clientIDs {
		id := clientID
		t.Cleanup(func() {
			modelRegistry.UnregisterClient(id)
		})
	}

	handler := NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, coreauth.NewManager(nil, nil, nil))

	tests := []struct {
		name          string
		inputModel    string
		wantProviders []string
		wantModel     string
		wantErr       bool
	}{
		{
			name:          "numeric suffix preserved",
			inputModel:    "gemini/gemini-2.5-pro(8192)",
			wantProviders: []string{"gemini"},
			wantModel:     "gemini-2.5-pro(8192)",
			wantErr:       false,
		},
		{
			name:          "level suffix preserved",
			inputModel:    "openai/gpt-5.2(high)",
			wantProviders: []string{"openai"},
			wantModel:     "gpt-5.2(high)",
			wantErr:       false,
		},
		{
			name:          "no suffix unchanged",
			inputModel:    "claude/claude-sonnet-4-5",
			wantProviders: []string{"claude"},
			wantModel:     "claude-sonnet-4-5",
			wantErr:       false,
		},
		{
			name:          "bare concrete model resolves",
			inputModel:    "gpt-5.2(high)",
			wantProviders: []string{"openai"},
			wantModel:     "gpt-5.2(high)",
			wantErr:       false,
		},
		{
			name:          "unknown model with suffix",
			inputModel:    "unknown-model(8192)",
			wantProviders: nil,
			wantModel:     "",
			wantErr:       true,
		},
		{
			name:          "auto suffix resolved",
			inputModel:    "auto(high)",
			wantProviders: []string{"gemini"},
			wantModel:     "gemini-2.5-pro(high)",
			wantErr:       false,
		},
		{
			name:          "special suffix none preserved",
			inputModel:    "gemini/gemini-2.5-flash(none)",
			wantProviders: []string{"gemini"},
			wantModel:     "gemini-2.5-flash(none)",
			wantErr:       false,
		},
		{
			name:          "special suffix auto preserved",
			inputModel:    "claude/claude-sonnet-4-5(auto)",
			wantProviders: []string{"claude"},
			wantModel:     "claude-sonnet-4-5(auto)",
			wantErr:       false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			providers, model, errMsg := handler.getRequestDetails(tt.inputModel)
			if (errMsg != nil) != tt.wantErr {
				t.Fatalf("getRequestDetails() error = %v, wantErr %v", errMsg, tt.wantErr)
			}
			if errMsg != nil {
				return
			}
			if !reflect.DeepEqual(providers, tt.wantProviders) {
				t.Fatalf("getRequestDetails() providers = %v, want %v", providers, tt.wantProviders)
			}
			if model != tt.wantModel {
				t.Fatalf("getRequestDetails() model = %v, want %v", model, tt.wantModel)
			}
		})
	}
}

func TestGetRequestDetails_BareConcreteModelProviderPriority(t *testing.T) {
	modelRegistry := registry.GetGlobalRegistry()
	registrations := []struct {
		clientID string
		provider string
		model    string
	}{
		{"test-request-details-priority-gpt-openai", "openai", "gpt-priority-test"},
		{"test-request-details-priority-gpt-copilot", "github-copilot", "gpt-priority-test"},
		{"test-request-details-priority-gpt-codex", "codex", "gpt-priority-test"},
		{"test-request-details-priority-gpt-compat", "openai-compatibility", "gpt-priority-test"},
		{"test-request-details-priority-claude-anthropic", "anthropic", "claude-priority-test"},
		{"test-request-details-priority-claude-copilot", "github-copilot", "claude-priority-test"},
		{"test-request-details-priority-claude-kiro", "kiro", "claude-priority-test"},
		{"test-request-details-priority-claude-code", "claude", "claude-priority-test"},
		{"test-request-details-priority-claude-vertex", "vertex-anthropic", "claude-priority-test"},
		{"test-request-details-priority-claude-compat", "openai-compatibility", "claude-priority-test"},
		{"test-request-details-priority-gemini-compat", "openai-compatibility", "gemini-priority-test"},
		{"test-request-details-priority-gemini-antigravity", "antigravity", "gemini-priority-test"},
		{"test-request-details-priority-gemini-vertex", "vertex", "gemini-priority-test"},
		{"test-request-details-priority-gemini-aistudio", "aistudio", "gemini-priority-test"},
		{"test-request-details-priority-gemini-cli", "gemini-cli", "gemini-priority-test"},
		{"test-request-details-priority-gemini", "gemini", "gemini-priority-test"},
		{"test-request-details-priority-grok-compat", "openai-compatibility", "grok-priority-test"},
		{"test-request-details-priority-grok-copilot", "github-copilot", "grok-priority-test"},
		{"test-request-details-priority-grok-xai", "xai", "grok-priority-test"},
		{"test-request-details-priority-gpt-copilot-hint-codex", "codex", "gpt-copilot-priority"},
		{"test-request-details-priority-gpt-copilot-hint-copilot", "github-copilot", "gpt-copilot-priority"},
		{"test-request-details-priority-claude-kiro-hint-claude", "claude", "claude-kiro-priority"},
		{"test-request-details-priority-claude-kiro-hint-kiro", "kiro", "claude-kiro-priority"},
	}
	for _, registration := range registrations {
		modelRegistry.RegisterClient(registration.clientID, registration.provider, []*registry.ModelInfo{{ID: registration.model}})
		clientID := registration.clientID
		t.Cleanup(func() {
			modelRegistry.UnregisterClient(clientID)
		})
	}

	handler := NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, coreauth.NewManager(nil, nil, nil))

	tests := []struct {
		name          string
		inputModel    string
		wantProviders []string
		wantModel     string
	}{
		{
			name:          "gpt family",
			inputModel:    "gpt-priority-test(high)",
			wantProviders: []string{"codex", "github-copilot", "openai", "openai-compatibility"},
			wantModel:     "gpt-priority-test(high)",
		},
		{
			name:          "claude family",
			inputModel:    "claude-priority-test",
			wantProviders: []string{"claude", "kiro", "github-copilot", "anthropic", "vertex-anthropic", "openai-compatibility"},
			wantModel:     "claude-priority-test",
		},
		{
			name:          "gemini family",
			inputModel:    "gemini-priority-test",
			wantProviders: []string{"gemini", "gemini-cli", "aistudio", "vertex", "antigravity", "openai-compatibility"},
			wantModel:     "gemini-priority-test",
		},
		{
			name:          "grok family",
			inputModel:    "grok-priority-test",
			wantProviders: []string{"xai", "github-copilot", "openai-compatibility"},
			wantModel:     "grok-priority-test",
		},
		{
			name:          "provider hint wins before family default",
			inputModel:    "gpt-copilot-priority",
			wantProviders: []string{"github-copilot", "codex"},
			wantModel:     "gpt-copilot-priority",
		},
		{
			name:          "kiro hint wins before claude family default",
			inputModel:    "claude-kiro-priority",
			wantProviders: []string{"kiro", "claude"},
			wantModel:     "claude-kiro-priority",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			details := handler.getRequestDetailsForExecution(tt.inputModel, false)
			if details.err != nil {
				t.Fatalf("getRequestDetailsForExecution() error = %v", details.err)
			}
			if !reflect.DeepEqual(details.providers, tt.wantProviders) {
				t.Fatalf("providers = %#v, want %#v", details.providers, tt.wantProviders)
			}
			if details.normalizedModel != tt.wantModel {
				t.Fatalf("normalizedModel = %q, want %q", details.normalizedModel, tt.wantModel)
			}
			if len(details.virtualTargets) != len(tt.wantProviders) {
				t.Fatalf("virtualTargets = %#v", details.virtualTargets)
			}
			for i, provider := range tt.wantProviders {
				if details.virtualTargets[i].Provider != provider {
					t.Fatalf("virtualTargets[%d].Provider = %q, want %q", i, details.virtualTargets[i].Provider, provider)
				}
				if details.virtualTargets[i].Model != tt.wantModel {
					t.Fatalf("virtualTargets[%d].Model = %q, want %q", i, details.virtualTargets[i].Model, tt.wantModel)
				}
			}
		})
	}
}

func TestGetRequestDetails_ImageModelReturns503(t *testing.T) {
	modelRegistry := registry.GetGlobalRegistry()
	modelRegistry.RegisterClient("test-request-details-image", "openai", []*registry.ModelInfo{{ID: "gpt-image-2"}})
	t.Cleanup(func() {
		modelRegistry.UnregisterClient("test-request-details-image")
	})
	handler := NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, coreauth.NewManager(nil, nil, nil))

	_, _, errMsg := handler.getRequestDetails("openai/gpt-image-2")
	if errMsg == nil {
		t.Fatalf("expected error for openai/gpt-image-2, got nil")
	}
	if errMsg.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("unexpected status code: got %d want %d", errMsg.StatusCode, http.StatusServiceUnavailable)
	}
	if errMsg.Error == nil {
		t.Fatalf("expected error message, got nil")
	}
	msg := errMsg.Error.Error()
	if !strings.Contains(msg, "/v1/images/generations") || !strings.Contains(msg, "/v1/images/edits") {
		t.Fatalf("unexpected error message: %q", msg)
	}
}

func TestGetRequestDetails_VirtualModel(t *testing.T) {
	manager := coreauth.NewManager(nil, nil, nil)
	manager.SetConfig(&sdkconfig.Config{
		VirtualModels: map[string]sdkconfig.VirtualModelConfig{
			"fast": {
				Targets: []sdkconfig.VirtualModelTarget{
					{Target: "codex/gpt-5.1"},
					{Target: "claude/claude-sonnet-4-5"},
				},
			},
		},
	})
	handler := NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, manager)

	details := handler.getRequestDetailsForExecution("fast(high)", false)
	if details.err != nil {
		t.Fatalf("getRequestDetailsForExecution() error = %v", details.err)
	}
	if !reflect.DeepEqual(details.providers, []string{"codex", "claude"}) {
		t.Fatalf("providers = %#v", details.providers)
	}
	if details.normalizedModel != "fast(high)" {
		t.Fatalf("normalizedModel = %q", details.normalizedModel)
	}
	if len(details.virtualTargets) != 2 {
		t.Fatalf("virtualTargets = %#v", details.virtualTargets)
	}
	if details.virtualTargets[0].Model != "gpt-5.1(high)" {
		t.Fatalf("first target model = %q", details.virtualTargets[0].Model)
	}
}

func TestGetRequestDetails_VirtualModelPrecedesBareConcreteResolver(t *testing.T) {
	modelRegistry := registry.GetGlobalRegistry()
	modelRegistry.RegisterClient("test-request-details-virtual-priority-codex", "codex", []*registry.ModelInfo{{ID: "gpt-virtual-priority"}})
	modelRegistry.RegisterClient("test-request-details-virtual-priority-openai", "openai", []*registry.ModelInfo{{ID: "gpt-virtual-target"}})
	t.Cleanup(func() {
		modelRegistry.UnregisterClient("test-request-details-virtual-priority-codex")
		modelRegistry.UnregisterClient("test-request-details-virtual-priority-openai")
	})

	manager := coreauth.NewManager(nil, nil, nil)
	manager.SetConfig(&sdkconfig.Config{
		VirtualModels: map[string]sdkconfig.VirtualModelConfig{
			"gpt-virtual-priority": {
				Targets: []sdkconfig.VirtualModelTarget{{Target: "openai/gpt-virtual-target"}},
			},
		},
	})
	handler := NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, manager)

	details := handler.getRequestDetailsForExecution("gpt-virtual-priority(high)", false)
	if details.err != nil {
		t.Fatalf("getRequestDetailsForExecution() error = %v", details.err)
	}
	if !reflect.DeepEqual(details.providers, []string{"openai"}) {
		t.Fatalf("providers = %#v", details.providers)
	}
	if details.normalizedModel != "gpt-virtual-priority(high)" {
		t.Fatalf("normalizedModel = %q", details.normalizedModel)
	}
	if len(details.virtualTargets) != 1 || details.virtualTargets[0].Model != "gpt-virtual-target(high)" {
		t.Fatalf("virtualTargets = %#v", details.virtualTargets)
	}
}

func TestGetRequestDetails_VirtualModelExplicitBypass(t *testing.T) {
	manager := coreauth.NewManager(nil, nil, nil)
	manager.SetConfig(&sdkconfig.Config{
		VirtualModels: map[string]sdkconfig.VirtualModelConfig{
			"fast": {Targets: []sdkconfig.VirtualModelTarget{{Target: "codex/gpt-5.1"}}},
		},
	})
	handler := NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, manager)

	details := handler.getRequestDetailsForExecution("codex/fast", false)
	if details.err == nil {
		t.Fatal("expected unknown provider error for explicit bypass")
	}
	if len(details.virtualTargets) != 0 {
		t.Fatalf("virtualTargets = %#v, want none", details.virtualTargets)
	}
}
