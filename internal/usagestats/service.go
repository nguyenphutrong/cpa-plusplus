package usagestats

import (
	"context"
	"errors"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"time"

	coreusage "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/usage"
	log "github.com/sirupsen/logrus"
)

const (
	defaultSQLiteFileName      = "usage-statistics.sqlite"
	ModelPriceAutoSyncInterval = 24 * time.Hour
)

var ErrDisabled = errors.New("usage stats service is disabled")

type Service struct {
	mu                     sync.RWMutex
	enabled                bool
	path                   string
	store                  *SQLiteStore
	modelPriceAutoSyncStop context.CancelFunc
	modelPriceSyncMu       sync.Mutex
	modelPriceSyncStateMu  sync.RWMutex
	modelPriceSyncing      bool
	modelPriceSyncError    string
}

type ServiceStatus struct {
	Enabled                    bool   `json:"enabled"`
	Open                       bool   `json:"open"`
	Path                       string `json:"path,omitempty"`
	ModelPricesCount           int    `json:"model_prices_count"`
	ModelPricesLastSyncedAtMS  int64  `json:"model_prices_last_synced_at_ms,omitempty"`
	ModelPricesLastUpdatedAtMS int64  `json:"model_prices_last_updated_at_ms,omitempty"`
	ModelPricesSyncing         bool   `json:"model_prices_syncing,omitempty"`
	ModelPricesSyncError       string `json:"model_prices_sync_error,omitempty"`
}

func NewService() *Service {
	return &Service{}
}

func ResolveSQLitePath(configPath string, configuredPath string) string {
	path := strings.TrimSpace(configuredPath)
	if path == "" {
		path = defaultSQLiteFileName
	}
	if strings.Contains(path, ":memory:") || strings.HasPrefix(path, "file:") || filepath.IsAbs(path) {
		return path
	}
	base := strings.TrimSpace(configPath)
	if base == "" {
		return path
	}
	dir := filepath.Dir(base)
	if dir == "." || dir == "" {
		return path
	}
	return filepath.Join(dir, path)
}

func (s *Service) Configure(ctx context.Context, enabled bool, path string) error {
	if s == nil {
		return nil
	}
	path = strings.TrimSpace(path)
	if ctx == nil {
		ctx = context.Background()
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if !enabled {
		s.stopModelPriceAutoSyncLocked()
		return s.closeLocked(false, "")
	}
	if path == "" {
		path = defaultSQLiteFileName
	}
	if s.enabled && s.store != nil && s.path == path {
		return nil
	}

	nextStore, err := OpenSQLiteStore(path)
	if err != nil {
		_ = s.closeLocked(false, path)
		return err
	}
	if s.store != nil {
		if errClose := s.store.Close(); errClose != nil {
			log.Warnf("failed to close previous usage stats sqlite store: %v", errClose)
		}
	}
	s.store = nextStore
	s.enabled = true
	s.path = path
	return nil
}

func (s *Service) HandleUsage(ctx context.Context, record coreusage.Record) {
	if s == nil {
		return
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if !s.enabled || s.store == nil {
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}
	event := EventFromUsageRecord(ctx, record)
	if _, err := s.store.InsertEvents(ctx, []Event{event}); err != nil {
		log.Warnf("failed to persist usage stats event: %v", err)
	}
}

func (s *Service) QueryEvents(ctx context.Context, filter SummaryFilter, limit int, offset int) ([]Event, error) {
	if s == nil {
		return nil, ErrDisabled
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if !s.enabled || s.store == nil {
		return nil, ErrDisabled
	}
	return s.store.QueryEvents(ctx, filter, limit, offset)
}

func (s *Service) Summary(ctx context.Context, filter SummaryFilter, includeCost bool) (Summary, error) {
	if s == nil {
		return Summary{}, ErrDisabled
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if !s.enabled || s.store == nil {
		return Summary{}, ErrDisabled
	}
	return s.store.Summary(ctx, filter, includeCost)
}

func (s *Service) LoadModelPrices(ctx context.Context) (map[string]ModelPrice, error) {
	if s == nil {
		return nil, ErrDisabled
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if !s.enabled || s.store == nil {
		return nil, ErrDisabled
	}
	return s.store.LoadModelPrices(ctx)
}

func (s *Service) SaveModelPrices(ctx context.Context, prices map[string]ModelPrice) error {
	if s == nil {
		return ErrDisabled
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if !s.enabled || s.store == nil {
		return ErrDisabled
	}
	return s.store.SaveModelPrices(ctx, prices)
}

func (s *Service) UpsertModelPrices(ctx context.Context, prices map[string]ModelPrice) (InsertResult, error) {
	if s == nil {
		return InsertResult{}, ErrDisabled
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if !s.enabled || s.store == nil {
		return InsertResult{}, ErrDisabled
	}
	return s.store.UpsertModelPrices(ctx, prices)
}

func (s *Service) SyncLiteLLMModelPrices(
	ctx context.Context,
	client *http.Client,
	sourceURL string,
	models []string,
) (ModelPriceSyncResult, error) {
	return s.SyncLiteLLMModelPricesWithOptions(ctx, client, sourceURL, models, true)
}

func (s *Service) SyncLiteLLMModelPricesWithOptions(
	ctx context.Context,
	client *http.Client,
	sourceURL string,
	models []string,
	includePrices bool,
) (ModelPriceSyncResult, error) {
	if s == nil {
		return ModelPriceSyncResult{}, ErrDisabled
	}
	if ctx == nil {
		ctx = context.Background()
	}

	s.modelPriceSyncMu.Lock()
	defer s.modelPriceSyncMu.Unlock()

	s.setModelPriceSyncing(true)
	defer s.setModelPriceSyncing(false)

	s.mu.RLock()
	enabled := s.enabled && s.store != nil
	s.mu.RUnlock()
	if !enabled {
		s.setModelPriceSyncError(ErrDisabled)
		return ModelPriceSyncResult{}, ErrDisabled
	}

	result, err := SyncLiteLLMModelPricesWithOptions(ctx, s, client, sourceURL, models, includePrices)
	if err != nil {
		s.setModelPriceSyncError(err)
		return ModelPriceSyncResult{}, err
	}
	s.setModelPriceSyncError(nil)
	return result, nil
}

func (s *Service) Status() ServiceStatus {
	if s == nil {
		return ServiceStatus{}
	}
	s.mu.RLock()
	status := ServiceStatus{
		Enabled: s.enabled,
		Open:    s.store != nil,
		Path:    s.path,
	}
	if s.enabled && s.store != nil {
		if priceStatus, err := s.store.ModelPriceStatus(context.Background()); err == nil {
			status.ModelPricesCount = priceStatus.Count
			status.ModelPricesLastUpdatedAtMS = priceStatus.LastUpdatedMS
			status.ModelPricesLastSyncedAtMS = priceStatus.LastSyncedAtMS
		}
	}
	s.mu.RUnlock()

	s.modelPriceSyncStateMu.RLock()
	status.ModelPricesSyncing = s.modelPriceSyncing
	status.ModelPricesSyncError = s.modelPriceSyncError
	s.modelPriceSyncStateMu.RUnlock()
	return status
}

func (s *Service) StartModelPriceAutoSync(ctx context.Context, client *http.Client, sourceURL string, interval time.Duration) {
	if s == nil {
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if interval <= 0 {
		interval = ModelPriceAutoSyncInterval
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.enabled || s.store == nil || s.modelPriceAutoSyncStop != nil {
		return
	}

	workerCtx, cancel := context.WithCancel(ctx)
	s.modelPriceAutoSyncStop = cancel
	go s.runModelPriceAutoSync(workerCtx, client, sourceURL, interval)
}

func (s *Service) StopModelPriceAutoSync() {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.stopModelPriceAutoSyncLocked()
}

func (s *Service) runModelPriceAutoSync(ctx context.Context, client *http.Client, sourceURL string, interval time.Duration) {
	s.ensureModelPricesFresh(ctx, client, sourceURL, interval)

	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.ensureModelPricesFresh(ctx, client, sourceURL, interval)
		}
	}
}

func (s *Service) ensureModelPricesFresh(ctx context.Context, client *http.Client, sourceURL string, ttl time.Duration) {
	if s == nil {
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if ttl <= 0 {
		ttl = ModelPriceAutoSyncInterval
	}

	status, err := s.modelPriceStoreStatus(ctx)
	if err != nil {
		if !errors.Is(err, ErrDisabled) {
			s.setModelPriceSyncError(err)
		}
		return
	}

	now := time.Now().UnixMilli()
	staleCutoffMS := int64(ttl / time.Millisecond)
	if status.Count > 0 && status.LastSyncedAtMS > 0 && now-status.LastSyncedAtMS < staleCutoffMS {
		return
	}

	if _, err := s.SyncLiteLLMModelPricesWithOptions(ctx, client, sourceURL, nil, false); err != nil {
		log.Warnf("failed to auto-sync usage stats model prices: %v", err)
	}
}

func (s *Service) modelPriceStoreStatus(ctx context.Context) (ModelPriceStoreStatus, error) {
	if s == nil {
		return ModelPriceStoreStatus{}, ErrDisabled
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if !s.enabled || s.store == nil {
		return ModelPriceStoreStatus{}, ErrDisabled
	}
	return s.store.ModelPriceStatus(ctx)
}

func (s *Service) Close() error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.stopModelPriceAutoSyncLocked()
	return s.closeLocked(false, "")
}

func (s *Service) closeLocked(enabled bool, path string) error {
	var err error
	if s.store != nil {
		err = s.store.Close()
	}
	s.store = nil
	s.enabled = enabled
	s.path = path
	return err
}

func (s *Service) stopModelPriceAutoSyncLocked() {
	if s.modelPriceAutoSyncStop != nil {
		s.modelPriceAutoSyncStop()
		s.modelPriceAutoSyncStop = nil
	}
}

func (s *Service) setModelPriceSyncing(syncing bool) {
	s.modelPriceSyncStateMu.Lock()
	s.modelPriceSyncing = syncing
	s.modelPriceSyncStateMu.Unlock()
}

func (s *Service) setModelPriceSyncError(err error) {
	s.modelPriceSyncStateMu.Lock()
	if err == nil {
		s.modelPriceSyncError = ""
	} else {
		s.modelPriceSyncError = err.Error()
	}
	s.modelPriceSyncStateMu.Unlock()
}
