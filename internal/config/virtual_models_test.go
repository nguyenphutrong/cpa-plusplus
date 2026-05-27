package config

import "testing"

func TestParseConfigBytesVirtualModels(t *testing.T) {
	cfg, err := ParseConfigBytes([]byte(`
routing:
  virtual-models-enabled: false
  virtual-model-cache-ttl: "45s"
  max-virtual-depth: 3
virtual-models:
  fast:
    targets:
      - codex/gpt-5.1
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
	targets := cfg.VirtualModels["fast"].Targets
	if len(targets) != 2 {
		t.Fatalf("targets len = %d, want 2", len(targets))
	}
	if got := targets[0].Target; got != "codex/gpt-5.1" {
		t.Fatalf("first target = %q", got)
	}
	target := targets[1]
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
