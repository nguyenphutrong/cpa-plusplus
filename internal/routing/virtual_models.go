package routing

import (
	"fmt"
	"sort"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/thinking"
)

// VirtualTarget is a concrete provider/model target in a virtual-model chain.
type VirtualTarget struct {
	Provider string `json:"provider"`
	Model    string `json:"model"`
}

// VirtualResolveResult describes a virtual-model resolution attempt.
type VirtualResolveResult struct {
	Matched bool            `json:"matched"`
	Model   string          `json:"model,omitempty"`
	Targets []VirtualTarget `json:"targets,omitempty"`
}

// ResolveVirtualModel expands a bare requested model into ordered concrete provider/model targets.
func ResolveVirtualModel(cfg *config.Config, requestedModel string) (VirtualResolveResult, error) {
	result := VirtualResolveResult{}
	requestedModel = strings.TrimSpace(requestedModel)
	if cfg == nil || !cfg.VirtualModelsRoutingEnabled() || requestedModel == "" {
		return result, nil
	}

	parsed := thinking.ParseSuffix(requestedModel)
	baseModel := strings.TrimSpace(parsed.ModelName)
	if baseModel == "" || isProviderQualified(baseModel) {
		return result, nil
	}

	entry, ok := cfg.VirtualModels[baseModel]
	if !ok || !enabled(entry.Enabled) {
		return result, nil
	}

	resolver := virtualResolver{
		cfg:             cfg,
		requestedSuffix: parsed,
		maxDepth:        cfg.EffectiveMaxVirtualDepth(),
	}
	targets, err := resolver.expandVirtual(baseModel, 0, map[string]struct{}{})
	if err != nil {
		return result, err
	}
	if len(targets) == 0 {
		return result, fmt.Errorf("virtual model %q has no enabled targets", baseModel)
	}
	return VirtualResolveResult{Matched: true, Model: baseModel, Targets: targets}, nil
}

// AvailableVirtualModelInfos returns virtual models with at least one available concrete target.
func AvailableVirtualModelInfos(cfg *config.Config, reg *registry.ModelRegistry) []*registry.ModelInfo {
	if cfg == nil || reg == nil || !cfg.VirtualModelsRoutingEnabled() || len(cfg.VirtualModels) == 0 {
		return nil
	}
	names := make([]string, 0, len(cfg.VirtualModels))
	for name := range cfg.VirtualModels {
		names = append(names, name)
	}
	sort.Strings(names)

	out := make([]*registry.ModelInfo, 0, len(names))
	for _, name := range names {
		resolved, err := ResolveVirtualModel(cfg, name)
		if err != nil || !resolved.Matched {
			continue
		}
		if !hasAvailableVirtualTarget(reg, resolved.Targets) {
			continue
		}
		out = append(out, &registry.ModelInfo{
			ID:          name,
			Object:      "model",
			OwnedBy:     "cpa-plusplus",
			Type:        "virtual",
			DisplayName: name,
		})
	}
	return out
}

func hasAvailableVirtualTarget(reg *registry.ModelRegistry, targets []VirtualTarget) bool {
	for _, target := range targets {
		provider := strings.TrimSpace(strings.ToLower(target.Provider))
		model := strings.TrimSpace(thinking.ParseSuffix(target.Model).ModelName)
		if provider == "" || model == "" {
			continue
		}
		for _, availableProvider := range reg.GetModelProviders(model) {
			if strings.EqualFold(availableProvider, provider) {
				return true
			}
		}
	}
	return false
}

type virtualResolver struct {
	cfg             *config.Config
	requestedSuffix thinking.SuffixResult
	maxDepth        int
}

func (r virtualResolver) expandVirtual(name string, depth int, visiting map[string]struct{}) ([]VirtualTarget, error) {
	if depth >= r.maxDepth {
		return nil, fmt.Errorf("virtual model %q exceeds max depth %d", name, r.maxDepth)
	}
	if _, seen := visiting[name]; seen {
		return nil, fmt.Errorf("virtual model cycle detected at %q", name)
	}
	entry, ok := r.cfg.VirtualModels[name]
	if !ok || !enabled(entry.Enabled) {
		return nil, nil
	}

	visiting[name] = struct{}{}
	defer delete(visiting, name)

	rawTargets := make([]config.VirtualModelTarget, 0, len(entry.Targets))
	if templateName := strings.TrimSpace(entry.ComboTemplate); templateName != "" {
		template, okTemplate := r.cfg.ComboTemplates[templateName]
		if !okTemplate {
			return nil, fmt.Errorf("virtual model %q references unknown combo template %q", name, templateName)
		}
		if enabled(template.Enabled) {
			rawTargets = append(rawTargets, template.Targets...)
		}
	}
	rawTargets = append(rawTargets, entry.Targets...)
	if len(rawTargets) == 0 {
		return nil, fmt.Errorf("virtual model %q has no targets", name)
	}

	var out []VirtualTarget
	for _, target := range rawTargets {
		if !enabled(target.Enabled) {
			continue
		}
		ref := strings.TrimSpace(target.Target)
		if ref == "" {
			continue
		}
		if strings.Contains(ref, "/") {
			concrete, err := r.parseConcreteTarget(ref)
			if err != nil {
				return nil, err
			}
			out = append(out, concrete)
			continue
		}
		nested, err := r.expandVirtual(ref, depth+1, visiting)
		if err != nil {
			return nil, err
		}
		out = append(out, nested...)
	}
	return out, nil
}

func (r virtualResolver) parseConcreteTarget(target string) (VirtualTarget, error) {
	provider, model, ok := strings.Cut(strings.TrimSpace(target), "/")
	provider = strings.TrimSpace(provider)
	model = strings.TrimSpace(model)
	if !ok || provider == "" || model == "" {
		return VirtualTarget{}, fmt.Errorf("invalid virtual target %q; expected provider/model", target)
	}
	if r.requestedSuffix.HasSuffix && !thinking.ParseSuffix(model).HasSuffix {
		model = fmt.Sprintf("%s(%s)", model, r.requestedSuffix.RawSuffix)
	}
	return VirtualTarget{Provider: strings.ToLower(provider), Model: model}, nil
}

func enabled(value *bool) bool {
	return value == nil || *value
}

func isProviderQualified(model string) bool {
	provider, rest, ok := strings.Cut(strings.TrimSpace(model), "/")
	return ok && strings.TrimSpace(provider) != "" && strings.TrimSpace(rest) != ""
}
