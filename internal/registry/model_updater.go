package registry

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"strings"
	"sync"

	log "github.com/sirupsen/logrus"
)

//go:embed models/models.json
var embeddedModelsJSON []byte

type modelStore struct {
	mu   sync.RWMutex
	data *staticModelsJSON
}

var modelsCatalogStore = &modelStore{}

var updaterOnce sync.Once

// ModelRefreshCallback is invoked when startup model catalog repair detects changes.
// changedProviders contains the provider names whose model definitions changed.
type ModelRefreshCallback func(changedProviders []string)

var (
	refreshCallbackMu     sync.Mutex
	refreshCallback       ModelRefreshCallback
	pendingRefreshChanges []string
)

// SetModelRefreshCallback registers a callback that is invoked when model
// catalog repair detects changes. Only one callback is supported; subsequent
// calls replace the previous callback.
func SetModelRefreshCallback(cb ModelRefreshCallback) {
	refreshCallbackMu.Lock()
	refreshCallback = cb
	var pending []string
	if cb != nil && len(pendingRefreshChanges) > 0 {
		pending = append([]string(nil), pendingRefreshChanges...)
		pendingRefreshChanges = nil
	}
	refreshCallbackMu.Unlock()

	if cb != nil && len(pending) > 0 {
		cb(pending)
	}
}

func init() {
	// Load the embedded cpa-plusplus catalog as the canonical startup default.
	if err := loadModelsFromBytes(embeddedModelsJSON, "embed"); err != nil {
		log.Warnf("registry: failed to parse embedded models.json: %v", err)
	}
}

// StartModelsUpdater repairs the in-memory catalog from the embedded
// cpa-plusplus catalog once during startup. It keeps the historical name for
// caller compatibility, but it does not fetch remote model catalogs.
// Safe to call multiple times; only one repair will run.
func StartModelsUpdater(ctx context.Context) {
	_ = ctx
	updaterOnce.Do(func() {
		repairModelsFromEmbeddedCatalog("startup model catalog repair")
	})
}

func repairModelsFromEmbeddedCatalog(label string) {
	oldData := getModels()

	embedded, err := parseModelsFromBytes(embeddedModelsJSON, "embed")
	if err != nil {
		log.Warnf("%s: failed to parse embedded catalog, keeping current data: %v", label, err)
		return
	}
	repaired := overlayEmbeddedCatalogDefaults(oldData, embedded)

	// Detect changes before updating store.
	changed := detectChangedProviders(oldData, repaired)

	modelsCatalogStore.mu.Lock()
	modelsCatalogStore.data = repaired
	modelsCatalogStore.mu.Unlock()

	if len(changed) == 0 {
		log.Debugf("%s completed from embedded catalog, no changes detected", label)
		return
	}

	log.Infof("%s completed from embedded catalog, changes detected for providers: %v", label, changed)
	notifyModelRefresh(changed)
}

// detectChangedProviders compares two model catalogs and returns provider names
// whose model definitions differ. Codex tiers (free/team/plus/pro) are grouped
// under a single "codex" provider.
func detectChangedProviders(oldData, newData *staticModelsJSON) []string {
	if oldData == nil || newData == nil {
		return nil
	}

	type section struct {
		provider string
		oldList  []*ModelInfo
		newList  []*ModelInfo
	}

	sections := []section{
		{"claude", oldData.Claude, newData.Claude},
		{"gemini", oldData.Gemini, newData.Gemini},
		{"vertex", oldData.Vertex, newData.Vertex},
		{"gemini-cli", oldData.GeminiCLI, newData.GeminiCLI},
		{"aistudio", oldData.AIStudio, newData.AIStudio},
		{"codex", oldData.CodexFree, newData.CodexFree},
		{"codex", oldData.CodexTeam, newData.CodexTeam},
		{"codex", oldData.CodexPlus, newData.CodexPlus},
		{"codex", oldData.CodexPro, newData.CodexPro},
		{"kimi", oldData.Kimi, newData.Kimi},
		{"antigravity", oldData.Antigravity, newData.Antigravity},
		{"github-copilot", oldData.GitHubCopilot, newData.GitHubCopilot},
		{"kiro", oldData.Kiro, newData.Kiro},
		{"xai", oldData.XAI, newData.XAI},
	}

	seen := make(map[string]bool, len(sections))
	var changed []string
	for _, s := range sections {
		if seen[s.provider] {
			continue
		}
		if modelSectionChanged(s.oldList, s.newList) {
			changed = append(changed, s.provider)
			seen[s.provider] = true
		}
	}
	return changed
}

func overlayEmbeddedCatalogDefaults(current, embedded *staticModelsJSON) *staticModelsJSON {
	if embedded == nil {
		return cloneStaticModelsJSON(current)
	}
	if current == nil {
		return cloneStaticModelsJSON(embedded)
	}

	repaired := cloneStaticModelsJSON(current)
	overlayModelSectionDefaults(&repaired.Claude, embedded.Claude)
	overlayModelSectionDefaults(&repaired.Gemini, embedded.Gemini)
	overlayModelSectionDefaults(&repaired.Vertex, embedded.Vertex)
	overlayModelSectionDefaults(&repaired.GeminiCLI, embedded.GeminiCLI)
	overlayModelSectionDefaults(&repaired.AIStudio, embedded.AIStudio)
	overlayModelSectionDefaults(&repaired.CodexFree, embedded.CodexFree)
	overlayModelSectionDefaults(&repaired.CodexTeam, embedded.CodexTeam)
	overlayModelSectionDefaults(&repaired.CodexPlus, embedded.CodexPlus)
	overlayModelSectionDefaults(&repaired.CodexPro, embedded.CodexPro)
	overlayModelSectionDefaults(&repaired.Kimi, embedded.Kimi)
	overlayModelSectionDefaults(&repaired.Antigravity, embedded.Antigravity)
	overlayModelSectionDefaults(&repaired.GitHubCopilot, embedded.GitHubCopilot)
	overlayModelSectionDefaults(&repaired.Kiro, embedded.Kiro)
	overlayModelSectionDefaults(&repaired.XAI, embedded.XAI)
	return repaired
}

func overlayModelSectionDefaults(section *[]*ModelInfo, defaults []*ModelInfo) {
	if len(defaults) == 0 {
		return
	}
	if len(*section) == 0 {
		*section = cloneModelInfos(defaults)
		return
	}

	seen := make(map[string]struct{}, len(*section)+len(defaults))
	out := make([]*ModelInfo, 0, len(*section)+len(defaults))
	for _, model := range *section {
		if model == nil || strings.TrimSpace(model.ID) == "" {
			continue
		}
		key := strings.ToLower(strings.TrimSpace(model.ID))
		seen[key] = struct{}{}
		out = append(out, cloneModelInfo(model))
	}
	for _, model := range defaults {
		if model == nil || strings.TrimSpace(model.ID) == "" {
			continue
		}
		key := strings.ToLower(strings.TrimSpace(model.ID))
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, cloneModelInfo(model))
	}
	*section = out
}

// modelSectionChanged reports whether two model slices differ.
func modelSectionChanged(a, b []*ModelInfo) bool {
	if len(a) != len(b) {
		return true
	}
	if len(a) == 0 {
		return false
	}
	aj, err1 := json.Marshal(a)
	bj, err2 := json.Marshal(b)
	if err1 != nil || err2 != nil {
		return true
	}
	return string(aj) != string(bj)
}

func notifyModelRefresh(changedProviders []string) {
	if len(changedProviders) == 0 {
		return
	}

	refreshCallbackMu.Lock()
	cb := refreshCallback
	if cb == nil {
		pendingRefreshChanges = mergeProviderNames(pendingRefreshChanges, changedProviders)
		refreshCallbackMu.Unlock()
		return
	}
	refreshCallbackMu.Unlock()
	cb(changedProviders)
}

func mergeProviderNames(existing, incoming []string) []string {
	if len(incoming) == 0 {
		return existing
	}
	seen := make(map[string]struct{}, len(existing)+len(incoming))
	merged := make([]string, 0, len(existing)+len(incoming))
	for _, provider := range existing {
		name := strings.ToLower(strings.TrimSpace(provider))
		if name == "" {
			continue
		}
		if _, ok := seen[name]; ok {
			continue
		}
		seen[name] = struct{}{}
		merged = append(merged, name)
	}
	for _, provider := range incoming {
		name := strings.ToLower(strings.TrimSpace(provider))
		if name == "" {
			continue
		}
		if _, ok := seen[name]; ok {
			continue
		}
		seen[name] = struct{}{}
		merged = append(merged, name)
	}
	return merged
}

func loadModelsFromBytes(data []byte, source string) error {
	parsed, err := parseModelsFromBytes(data, source)
	if err != nil {
		return err
	}

	modelsCatalogStore.mu.Lock()
	modelsCatalogStore.data = parsed
	modelsCatalogStore.mu.Unlock()
	return nil
}

func parseModelsFromBytes(data []byte, source string) (*staticModelsJSON, error) {
	var parsed staticModelsJSON
	if err := json.Unmarshal(data, &parsed); err != nil {
		return nil, fmt.Errorf("%s: decode models catalog: %w", source, err)
	}
	if err := validateModelsCatalog(&parsed); err != nil {
		return nil, fmt.Errorf("%s: validate models catalog: %w", source, err)
	}
	return &parsed, nil
}

func getModels() *staticModelsJSON {
	modelsCatalogStore.mu.RLock()
	defer modelsCatalogStore.mu.RUnlock()
	if modelsCatalogStore.data == nil {
		return &staticModelsJSON{}
	}
	return modelsCatalogStore.data
}

func cloneStaticModelsJSON(data *staticModelsJSON) *staticModelsJSON {
	if data == nil {
		return &staticModelsJSON{}
	}
	return &staticModelsJSON{
		Claude:        cloneModelInfos(data.Claude),
		Gemini:        cloneModelInfos(data.Gemini),
		Vertex:        cloneModelInfos(data.Vertex),
		GeminiCLI:     cloneModelInfos(data.GeminiCLI),
		AIStudio:      cloneModelInfos(data.AIStudio),
		CodexFree:     cloneModelInfos(data.CodexFree),
		CodexTeam:     cloneModelInfos(data.CodexTeam),
		CodexPlus:     cloneModelInfos(data.CodexPlus),
		CodexPro:      cloneModelInfos(data.CodexPro),
		Kimi:          cloneModelInfos(data.Kimi),
		Antigravity:   cloneModelInfos(data.Antigravity),
		GitHubCopilot: cloneModelInfos(data.GitHubCopilot),
		Kiro:          cloneModelInfos(data.Kiro),
		XAI:           cloneModelInfos(data.XAI),
	}
}

func validateModelsCatalog(data *staticModelsJSON) error {
	if data == nil {
		return fmt.Errorf("catalog is nil")
	}

	requiredSections := []struct {
		name   string
		models []*ModelInfo
	}{
		{name: "claude", models: data.Claude},
		{name: "gemini", models: data.Gemini},
		{name: "vertex", models: data.Vertex},
		{name: "gemini-cli", models: data.GeminiCLI},
		{name: "aistudio", models: data.AIStudio},
		{name: "codex-free", models: data.CodexFree},
		{name: "codex-team", models: data.CodexTeam},
		{name: "codex-plus", models: data.CodexPlus},
		{name: "codex-pro", models: data.CodexPro},
		{name: "kimi", models: data.Kimi},
		{name: "antigravity", models: data.Antigravity},
		{name: "github-copilot", models: data.GitHubCopilot},
		{name: "kiro", models: data.Kiro},
		{name: "xai", models: data.XAI},
	}

	for _, section := range requiredSections {
		if err := validateModelSection(section.name, section.models); err != nil {
			return err
		}
	}
	return nil
}

func validateModelSection(section string, models []*ModelInfo) error {
	if len(models) == 0 {
		log.Warnf("models catalog: %s section is empty, continuing without those model definitions", section)
		return nil
	}

	seen := make(map[string]struct{}, len(models))
	for i, model := range models {
		if model == nil {
			return fmt.Errorf("%s[%d] is null", section, i)
		}
		modelID := strings.TrimSpace(model.ID)
		if modelID == "" {
			return fmt.Errorf("%s[%d] has empty id", section, i)
		}
		if _, exists := seen[modelID]; exists {
			return fmt.Errorf("%s contains duplicate model id %q", section, modelID)
		}
		seen[modelID] = struct{}{}
	}
	return nil
}
