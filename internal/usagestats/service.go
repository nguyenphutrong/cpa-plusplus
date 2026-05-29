package usagestats

import (
	"context"
	"errors"
	"net/http"
	"path/filepath"
	"strings"
	"sync"

	coreusage "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/usage"
	log "github.com/sirupsen/logrus"
)

const defaultSQLiteFileName = "usage-statistics.sqlite"

var ErrDisabled = errors.New("usage stats service is disabled")

type Service struct {
	mu      sync.RWMutex
	enabled bool
	path    string
	store   *SQLiteStore
}

type ServiceStatus struct {
	Enabled bool   `json:"enabled"`
	Open    bool   `json:"open"`
	Path    string `json:"path,omitempty"`
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
	if s == nil {
		return ModelPriceSyncResult{}, ErrDisabled
	}
	s.mu.RLock()
	enabled := s.enabled && s.store != nil
	s.mu.RUnlock()
	if !enabled {
		return ModelPriceSyncResult{}, ErrDisabled
	}
	return SyncLiteLLMModelPrices(ctx, s, client, sourceURL, models)
}

func (s *Service) Status() ServiceStatus {
	if s == nil {
		return ServiceStatus{}
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return ServiceStatus{
		Enabled: s.enabled,
		Open:    s.store != nil,
		Path:    s.path,
	}
}

func (s *Service) Close() error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
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
