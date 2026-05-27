package routing

import (
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
)

func boolPtr(v bool) *bool { return &v }

func TestResolveVirtualModel(t *testing.T) {
	cfg := &config.Config{
		VirtualModels: map[string]config.VirtualModelConfig{
			"fast": {
				Targets: []config.VirtualModelTarget{
					{Target: "codex/gpt-5.1"},
					{Target: "gemini/gemini-2.5-pro", Enabled: boolPtr(false)},
					{Target: "claude/claude-sonnet-4-5"},
				},
			},
		},
	}

	got, err := ResolveVirtualModel(cfg, "fast(high)")
	if err != nil {
		t.Fatalf("ResolveVirtualModel() error = %v", err)
	}
	if !got.Matched {
		t.Fatal("ResolveVirtualModel() did not match")
	}
	want := []VirtualTarget{
		{Provider: "codex", Model: "gpt-5.1(high)"},
		{Provider: "claude", Model: "claude-sonnet-4-5(high)"},
	}
	if len(got.Targets) != len(want) {
		t.Fatalf("targets = %#v, want %#v", got.Targets, want)
	}
	for i := range want {
		if got.Targets[i] != want[i] {
			t.Fatalf("target[%d] = %#v, want %#v", i, got.Targets[i], want[i])
		}
	}
}

func TestResolveVirtualModelBypassAndDisabled(t *testing.T) {
	disabled := false
	cfg := &config.Config{
		Routing: config.RoutingConfig{VirtualModelsEnabled: &disabled},
		VirtualModels: map[string]config.VirtualModelConfig{
			"fast": {Targets: []config.VirtualModelTarget{{Target: "codex/gpt-5.1"}}},
		},
	}

	if got, err := ResolveVirtualModel(cfg, "fast"); err != nil || got.Matched {
		t.Fatalf("disabled routing = %#v, err=%v", got, err)
	}

	cfg.Routing.VirtualModelsEnabled = nil
	if got, err := ResolveVirtualModel(cfg, "codex/fast"); err != nil || got.Matched {
		t.Fatalf("provider-qualified model = %#v, err=%v", got, err)
	}
}

func TestResolveVirtualModelNested(t *testing.T) {
	cfg := &config.Config{
		VirtualModels: map[string]config.VirtualModelConfig{
			"primary":  {Targets: []config.VirtualModelTarget{{Target: "gemini/gemini-2.5-pro"}, {Target: "fallback"}}},
			"fallback": {Targets: []config.VirtualModelTarget{{Target: "claude/claude-sonnet-4-5(none)"}}},
		},
	}

	got, err := ResolveVirtualModel(cfg, "primary(high)")
	if err != nil {
		t.Fatalf("ResolveVirtualModel() error = %v", err)
	}
	want := []VirtualTarget{
		{Provider: "gemini", Model: "gemini-2.5-pro(high)"},
		{Provider: "claude", Model: "claude-sonnet-4-5(none)"},
	}
	if len(got.Targets) != len(want) {
		t.Fatalf("targets = %#v, want %#v", got.Targets, want)
	}
	for i := range want {
		if got.Targets[i] != want[i] {
			t.Fatalf("target[%d] = %#v, want %#v", i, got.Targets[i], want[i])
		}
	}
}

func TestResolveVirtualModelCycleDepthAndInvalidTarget(t *testing.T) {
	tests := []struct {
		name    string
		cfg     *config.Config
		wantErr string
	}{
		{
			name: "cycle",
			cfg: &config.Config{
				VirtualModels: map[string]config.VirtualModelConfig{
					"a": {Targets: []config.VirtualModelTarget{{Target: "b"}}},
					"b": {Targets: []config.VirtualModelTarget{{Target: "a"}}},
				},
			},
			wantErr: "cycle",
		},
		{
			name: "depth",
			cfg: &config.Config{
				Routing: config.RoutingConfig{MaxVirtualDepth: 1},
				VirtualModels: map[string]config.VirtualModelConfig{
					"a": {Targets: []config.VirtualModelTarget{{Target: "b"}}},
					"b": {Targets: []config.VirtualModelTarget{{Target: "codex/gpt-5.1"}}},
				},
			},
			wantErr: "max depth",
		},
		{
			name: "invalid",
			cfg: &config.Config{
				VirtualModels: map[string]config.VirtualModelConfig{
					"a": {Targets: []config.VirtualModelTarget{{Target: "/gpt-5.1"}}},
				},
			},
			wantErr: "expected provider/model",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := ResolveVirtualModel(tt.cfg, "a")
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("error = %v, want contains %q", err, tt.wantErr)
			}
		})
	}
}

func TestAvailableVirtualModelInfosRequiresAvailableTarget(t *testing.T) {
	reg := registry.GetGlobalRegistry()
	reg.RegisterClient("virtual-list-codex", "codex", []*registry.ModelInfo{{ID: "gpt-5.1"}})
	t.Cleanup(func() { reg.UnregisterClient("virtual-list-codex") })

	cfg := &config.Config{
		VirtualModels: map[string]config.VirtualModelConfig{
			"available": {Targets: []config.VirtualModelTarget{{Target: "codex/gpt-5.1"}}},
			"missing":   {Targets: []config.VirtualModelTarget{{Target: "claude/claude-missing"}}},
		},
	}

	infos := AvailableVirtualModelInfos(cfg, reg)
	if len(infos) != 1 {
		t.Fatalf("infos = %#v, want one available virtual model", infos)
	}
	if infos[0].ID != "available" || infos[0].OwnedBy != "cpa-plusplus" || infos[0].Type != "virtual" {
		t.Fatalf("info = %#v", infos[0])
	}
}
