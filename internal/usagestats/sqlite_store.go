package usagestats

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

type SQLiteStore struct {
	db *sql.DB
}

type ModelPriceStoreStatus struct {
	Count          int
	LastUpdatedMS  int64
	LastSyncedAtMS int64
}

func OpenSQLiteStore(path string) (*SQLiteStore, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return nil, errors.New("usage stats sqlite path is required")
	}
	if !strings.Contains(path, ":memory:") && !strings.HasPrefix(path, "file:") {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return nil, fmt.Errorf("create usage stats directory: %w", err)
		}
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("open usage stats sqlite: %w", err)
	}
	store := &SQLiteStore{db: db}
	if err := store.init(); err != nil {
		_ = db.Close()
		return nil, err
	}
	return store, nil
}

func (s *SQLiteStore) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}

func (s *SQLiteStore) init() error {
	statements := []string{
		`pragma journal_mode = WAL`,
		`pragma synchronous = FULL`,
		`pragma busy_timeout = 5000`,
		`create table if not exists usage_events (
			id integer primary key autoincrement,
			request_id text,
			event_hash text not null unique,
			timestamp_ms integer not null,
			provider text,
			channel text,
			model text not null,
			requested_model text,
			resolved_model text,
			endpoint text,
			method text,
			path text,
			auth_type text,
			auth_index text,
			account text,
			account_hash text,
			api_key_hash text,
			status_code integer not null,
			prompt_tokens integer not null default 0,
			completion_tokens integer not null default 0,
			reasoning_tokens integer not null default 0,
			cached_tokens integer not null default 0,
			cache_tokens integer not null default 0,
			total_tokens integer not null default 0,
			latency_ms integer,
			failed integer not null default 0,
			created_at_ms integer not null
		)`,
		`create index if not exists idx_usage_events_account_time on usage_events(account, timestamp_ms)`,
		`create index if not exists idx_usage_events_model_time on usage_events(model, timestamp_ms)`,
		`create index if not exists idx_usage_events_channel_time on usage_events(channel, timestamp_ms)`,
		`create index if not exists idx_usage_events_auth_index_time on usage_events(auth_index, timestamp_ms)`,
		`create index if not exists idx_usage_events_time on usage_events(timestamp_ms)`,
		`create table if not exists model_prices (
			model text primary key,
			prompt_per_1m real not null,
			completion_per_1m real not null,
			cache_per_1m real not null,
			source text,
			source_model_id text,
			raw_json text,
			updated_at_ms integer not null,
			synced_at_ms integer
		)`,
	}
	for _, statement := range statements {
		if _, err := s.db.Exec(statement); err != nil {
			return fmt.Errorf("init usage stats sqlite: %w", err)
		}
	}
	return nil
}

func (s *SQLiteStore) InsertEvents(ctx context.Context, events []Event) (InsertResult, error) {
	if s == nil || s.db == nil {
		return InsertResult{}, errors.New("usage stats store is closed")
	}
	if len(events) == 0 {
		return InsertResult{}, nil
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return InsertResult{}, err
	}
	defer func() {
		_ = tx.Rollback()
	}()

	stmt, err := tx.PrepareContext(ctx, `insert or ignore into usage_events (
		request_id, event_hash, timestamp_ms, provider, channel, model, requested_model, resolved_model,
		endpoint, method, path, auth_type, auth_index, account, account_hash, api_key_hash, status_code,
		prompt_tokens, completion_tokens, reasoning_tokens, cached_tokens, cache_tokens, total_tokens,
		latency_ms, failed, created_at_ms
	) values (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`)
	if err != nil {
		return InsertResult{}, err
	}
	defer stmt.Close()

	result := InsertResult{}
	for _, event := range events {
		event = normalizeEvent(event)
		failed := 0
		if event.Failed {
			failed = 1
		}
		res, errExec := stmt.ExecContext(
			ctx,
			nullString(event.RequestID),
			event.EventHash,
			event.TimestampMS,
			nullString(event.Provider),
			nullString(event.Channel),
			event.Model,
			nullString(event.RequestedModel),
			nullString(event.ResolvedModel),
			nullString(event.Endpoint),
			nullString(event.Method),
			nullString(event.Path),
			nullString(event.AuthType),
			nullString(event.AuthIndex),
			nullString(event.Account),
			nullString(event.AccountHash),
			nullString(event.APIKeyHash),
			event.StatusCode,
			event.PromptTokens,
			event.CompletionTokens,
			event.ReasoningTokens,
			event.CachedTokens,
			event.CacheTokens,
			event.TotalTokens,
			nullInt64(event.LatencyMS),
			failed,
			event.CreatedAtMS,
		)
		if errExec != nil {
			return InsertResult{}, errExec
		}
		affected, _ := res.RowsAffected()
		if affected > 0 {
			result.Inserted++
		} else {
			result.Skipped++
		}
	}
	if err := tx.Commit(); err != nil {
		return InsertResult{}, err
	}
	return result, nil
}

func (s *SQLiteStore) QueryEvents(ctx context.Context, filter SummaryFilter, limit int, offset int) ([]Event, error) {
	if s == nil || s.db == nil {
		return nil, errors.New("usage stats store is closed")
	}
	if limit <= 0 {
		limit = 100
	}
	if offset < 0 {
		offset = 0
	}
	whereClause, args := filter.whereClause()
	args = append(args, limit, offset)
	rows, err := s.db.QueryContext(ctx, `select
		id, request_id, event_hash, timestamp_ms, provider, channel, model, requested_model, resolved_model,
		endpoint, method, path, auth_type, auth_index, account, account_hash, api_key_hash, status_code,
		prompt_tokens, completion_tokens, reasoning_tokens, cached_tokens, cache_tokens, total_tokens,
		latency_ms, failed, created_at_ms
		from usage_events`+whereClause+`
		order by timestamp_ms desc, id desc limit ? offset ?`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var events []Event
	for rows.Next() {
		event, errScan := scanEvent(rows)
		if errScan != nil {
			return nil, errScan
		}
		events = append(events, event)
	}
	return events, rows.Err()
}

func (s *SQLiteStore) Summary(ctx context.Context, filter SummaryFilter, includeCost bool) (Summary, error) {
	if s == nil || s.db == nil {
		return Summary{}, errors.New("usage stats store is closed")
	}
	whereClause, args := filter.whereClause()
	var summary Summary
	err := s.db.QueryRowContext(ctx, `select
		count(*),
		coalesce(sum(case when failed = 0 then 1 else 0 end), 0),
		coalesce(sum(case when failed != 0 then 1 else 0 end), 0),
		coalesce(sum(prompt_tokens), 0),
		coalesce(sum(completion_tokens), 0),
		coalesce(sum(reasoning_tokens), 0),
		coalesce(sum(cached_tokens), 0),
		coalesce(sum(cache_tokens), 0),
		coalesce(sum(total_tokens), 0),
		coalesce(sum(latency_ms), 0),
		count(latency_ms)
		from usage_events`+whereClause, args...).Scan(
		&summary.TotalRequests,
		&summary.SuccessCount,
		&summary.FailureCount,
		&summary.Tokens.PromptTokens,
		&summary.Tokens.CompletionTokens,
		&summary.Tokens.ReasoningTokens,
		&summary.Tokens.CachedTokens,
		&summary.Tokens.CacheTokens,
		&summary.Tokens.TotalTokens,
		&summary.LatencySumMS,
		&summary.LatencyCount,
	)
	if err != nil {
		return Summary{}, err
	}
	if includeCost {
		cost, errCost := s.estimateCostForFilter(ctx, filter)
		if errCost != nil {
			return Summary{}, errCost
		}
		summary.EstimatedCostUSD = cost
	}
	return summary, nil
}

func (s *SQLiteStore) LoadModelPrices(ctx context.Context) (map[string]ModelPrice, error) {
	rows, err := s.db.QueryContext(ctx, `select
		model, prompt_per_1m, completion_per_1m, cache_per_1m, source, source_model_id, raw_json,
		updated_at_ms, synced_at_ms
		from model_prices order by model`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	prices := map[string]ModelPrice{}
	for rows.Next() {
		var model string
		var price ModelPrice
		var source, sourceModelID, rawJSON sql.NullString
		var syncedAt sql.NullInt64
		if err := rows.Scan(
			&model,
			&price.Prompt,
			&price.Completion,
			&price.Cache,
			&source,
			&sourceModelID,
			&rawJSON,
			&price.UpdatedAtMS,
			&syncedAt,
		); err != nil {
			return nil, err
		}
		price.Source = source.String
		price.SourceModelID = sourceModelID.String
		price.RawJSON = rawJSON.String
		if syncedAt.Valid {
			value := syncedAt.Int64
			price.SyncedAtMS = &value
		}
		prices[model] = price
	}
	return prices, rows.Err()
}

func (s *SQLiteStore) ModelPriceStatus(ctx context.Context) (ModelPriceStoreStatus, error) {
	var status ModelPriceStoreStatus
	var lastUpdated, lastSynced sql.NullInt64
	err := s.db.QueryRowContext(ctx, `select count(*), max(updated_at_ms), max(synced_at_ms) from model_prices`).Scan(
		&status.Count,
		&lastUpdated,
		&lastSynced,
	)
	if err != nil {
		return ModelPriceStoreStatus{}, err
	}
	if lastUpdated.Valid {
		status.LastUpdatedMS = lastUpdated.Int64
	}
	if lastSynced.Valid {
		status.LastSyncedAtMS = lastSynced.Int64
	}
	return status, nil
}

func (s *SQLiteStore) SaveModelPrices(ctx context.Context, prices map[string]ModelPrice) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() {
		_ = tx.Rollback()
	}()
	if _, err := tx.ExecContext(ctx, `delete from model_prices`); err != nil {
		return err
	}
	if len(prices) == 0 {
		return tx.Commit()
	}
	if _, err := insertModelPrices(ctx, tx, prices, false); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *SQLiteStore) UpsertModelPrices(ctx context.Context, prices map[string]ModelPrice) (InsertResult, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return InsertResult{}, err
	}
	defer func() {
		_ = tx.Rollback()
	}()
	result, err := insertModelPrices(ctx, tx, prices, true)
	if err != nil {
		return InsertResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return InsertResult{}, err
	}
	return result, nil
}

func insertModelPrices(ctx context.Context, tx *sql.Tx, prices map[string]ModelPrice, upsert bool) (InsertResult, error) {
	query := `insert into model_prices (
		model, prompt_per_1m, completion_per_1m, cache_per_1m, source, source_model_id,
		raw_json, updated_at_ms, synced_at_ms
	) values (?, ?, ?, ?, ?, ?, ?, ?, ?)`
	if upsert {
		query += ` on conflict(model) do update set
			prompt_per_1m = excluded.prompt_per_1m,
			completion_per_1m = excluded.completion_per_1m,
			cache_per_1m = excluded.cache_per_1m,
			source = excluded.source,
			source_model_id = excluded.source_model_id,
			raw_json = excluded.raw_json,
			updated_at_ms = excluded.updated_at_ms,
			synced_at_ms = excluded.synced_at_ms`
	}
	stmt, err := tx.PrepareContext(ctx, query)
	if err != nil {
		return InsertResult{}, err
	}
	defer stmt.Close()

	now := time.Now().UnixMilli()
	result := InsertResult{}
	for model, price := range prices {
		if err := validateModelPrice(model, price); err != nil {
			if !upsert {
				return InsertResult{}, err
			}
			result.Skipped++
			continue
		}
		if price.UpdatedAtMS <= 0 {
			price.UpdatedAtMS = now
		}
		res, errExec := stmt.ExecContext(ctx,
			model,
			price.Prompt,
			price.Completion,
			price.Cache,
			nullString(price.Source),
			nullString(price.SourceModelID),
			nullString(price.RawJSON),
			price.UpdatedAtMS,
			nullInt64(price.SyncedAtMS),
		)
		if errExec != nil {
			return InsertResult{}, errExec
		}
		affected, _ := res.RowsAffected()
		if affected > 0 {
			result.Inserted++
		} else {
			result.Skipped++
		}
	}
	return result, nil
}

func (s *SQLiteStore) estimateCostForFilter(ctx context.Context, filter SummaryFilter) (float64, error) {
	prices, err := s.LoadModelPrices(ctx)
	if err != nil {
		return 0, err
	}
	if len(prices) == 0 {
		return 0, nil
	}
	index := BuildModelPriceIndex(prices)
	events, err := s.QueryEvents(ctx, filter, int(^uint(0)>>1), 0)
	if err != nil {
		return 0, err
	}
	var total float64
	for _, event := range events {
		if cost, ok := EstimateCostUSDWithIndex(event, prices, index); ok {
			total += cost
		}
	}
	return total, nil
}

func normalizeEvent(event Event) Event {
	now := time.Now().UTC()
	if event.TimestampMS <= 0 {
		event.TimestampMS = now.UnixMilli()
	}
	if strings.TrimSpace(event.Model) == "" {
		event.Model = "unknown"
	}
	if event.TotalTokens == 0 {
		event.TotalTokens = event.PromptTokens + event.CompletionTokens + event.ReasoningTokens + maxInt64(event.CachedTokens, event.CacheTokens)
	}
	if event.StatusCode <= 0 {
		if event.Failed {
			event.StatusCode = 500
		} else {
			event.StatusCode = 200
		}
	}
	if event.StatusCode >= 400 {
		event.Failed = true
	}
	if event.CreatedAtMS <= 0 {
		event.CreatedAtMS = now.UnixMilli()
	}
	if strings.TrimSpace(event.EventHash) == "" {
		event.EventHash = buildEventHash(event)
	}
	return event
}

func validateModelPrice(model string, price ModelPrice) error {
	if strings.TrimSpace(model) == "" {
		return errors.New("model is required")
	}
	if !validPriceValue(price.Prompt) || !validPriceValue(price.Completion) || !validPriceValue(price.Cache) {
		return fmt.Errorf("invalid model price for %s", model)
	}
	return nil
}

func validPriceValue(value float64) bool {
	return value >= 0 && !math.IsNaN(value) && !math.IsInf(value, 0)
}

func (filter SummaryFilter) whereClause() (string, []any) {
	clauses := make([]string, 0, 6)
	args := make([]any, 0, 6)
	if filter.StartMS != nil {
		clauses = append(clauses, "timestamp_ms >= ?")
		args = append(args, *filter.StartMS)
	}
	if filter.EndMS != nil {
		clauses = append(clauses, "timestamp_ms <= ?")
		args = append(args, *filter.EndMS)
	}
	if strings.TrimSpace(filter.Account) != "" {
		clauses = append(clauses, "lower(coalesce(account, '')) = ?")
		args = append(args, strings.ToLower(strings.TrimSpace(filter.Account)))
	}
	if strings.TrimSpace(filter.Model) != "" {
		clauses = append(clauses, "(model = ? or resolved_model = ? or requested_model = ?)")
		model := strings.TrimSpace(filter.Model)
		args = append(args, model, model, model)
	}
	if strings.TrimSpace(filter.Channel) != "" {
		clauses = append(clauses, "lower(coalesce(channel, '')) = ?")
		args = append(args, strings.ToLower(strings.TrimSpace(filter.Channel)))
	}
	if strings.TrimSpace(filter.AuthIndex) != "" {
		clauses = append(clauses, "auth_index = ?")
		args = append(args, strings.TrimSpace(filter.AuthIndex))
	}
	if len(clauses) == 0 {
		return "", args
	}
	return " where " + strings.Join(clauses, " and "), args
}

type eventScanner interface {
	Scan(dest ...any) error
}

func scanEvent(row eventScanner) (Event, error) {
	var event Event
	var requestID, provider, channel, requestedModel, resolvedModel, endpoint, method, path sql.NullString
	var authType, authIndex, account, accountHash, apiKeyHash sql.NullString
	var latency sql.NullInt64
	var failed int
	err := row.Scan(
		&event.ID,
		&requestID,
		&event.EventHash,
		&event.TimestampMS,
		&provider,
		&channel,
		&event.Model,
		&requestedModel,
		&resolvedModel,
		&endpoint,
		&method,
		&path,
		&authType,
		&authIndex,
		&account,
		&accountHash,
		&apiKeyHash,
		&event.StatusCode,
		&event.PromptTokens,
		&event.CompletionTokens,
		&event.ReasoningTokens,
		&event.CachedTokens,
		&event.CacheTokens,
		&event.TotalTokens,
		&latency,
		&failed,
		&event.CreatedAtMS,
	)
	if err != nil {
		return Event{}, err
	}
	event.RequestID = requestID.String
	event.Provider = provider.String
	event.Channel = channel.String
	event.RequestedModel = requestedModel.String
	event.ResolvedModel = resolvedModel.String
	event.Endpoint = endpoint.String
	event.Method = method.String
	event.Path = path.String
	event.AuthType = authType.String
	event.AuthIndex = authIndex.String
	event.Account = account.String
	event.AccountHash = accountHash.String
	event.APIKeyHash = apiKeyHash.String
	if latency.Valid {
		value := latency.Int64
		event.LatencyMS = &value
	}
	event.Failed = failed != 0
	return event, nil
}

func nullString(value string) any {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	return value
}

func nullInt64(value *int64) any {
	if value == nil {
		return nil
	}
	return *value
}
