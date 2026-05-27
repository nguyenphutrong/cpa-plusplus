package config

import "testing"

func TestParseConfigBytesVirtualModels(t *testing.T) {
	cfg, err := ParseConfigBytes([]byte(`
routing:
  virtual-models-enabled: false
  virtual-model-cache-ttl: "45s"
  max-virtual-depth: 3
combo-templates:
  default:
    targets:
      - codex/gpt-5.1
virtual-models:
  fast:
    combo-template: default
    targets:
      - target: claude/claude-sonnet-4-5
        enabled: false
`))
	if err != nil {
		t.Fatalf("ParseConfigBytes() error = %v", err)
	}
	if cfg.VirtualModelsRoutingEnabled() {
		t.Fatal("VirtualModelsRoutingEnabled() = true, want false")
	}
	if got := cfg.EffectiveVirtualModelCacheTTL(); got != "45s" {
		t.Fatalf("EffectiveVirtualModelCacheTTL() = %q", got)
	}
	if got := cfg.EffectiveMaxVirtualDepth(); got != 3 {
		t.Fatalf("EffectiveMaxVirtualDepth() = %d", got)
	}
	if got := cfg.ComboTemplates["default"].Targets[0].Target; got != "codex/gpt-5.1" {
		t.Fatalf("template target = %q", got)
	}
	target := cfg.VirtualModels["fast"].Targets[0]
	if target.Target != "claude/claude-sonnet-4-5" {
		t.Fatalf("virtual target = %q", target.Target)
	}
	if target.Enabled == nil || *target.Enabled {
		t.Fatalf("virtual target enabled = %v, want false", target.Enabled)
	}
}

func TestVirtualModelDefaults(t *testing.T) {
	cfg := &Config{}
	if !cfg.VirtualModelsRoutingEnabled() {
		t.Fatal("VirtualModelsRoutingEnabled() = false, want true by default")
	}
	if got := cfg.EffectiveVirtualModelCacheTTL(); got != "30s" {
		t.Fatalf("EffectiveVirtualModelCacheTTL() = %q", got)
	}
	if got := cfg.EffectiveMaxVirtualDepth(); got != 5 {
		t.Fatalf("EffectiveMaxVirtualDepth() = %d", got)
	}
}
