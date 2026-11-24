package storage

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	_ "modernc.org/sqlite"
)

// Store wraps a WAL-mode SQLite database for telemetry, budgets, and caching.
type Store struct {
	db *sql.DB
}

// RequestLog represents a single row in requests_log.
type RequestLog struct {
	ID               string  `json:"id"`
	TsUTC            int64   `json:"ts_utc"`
	ProjectID        string  `json:"project_id"`
	SessionID        string  `json:"session_id"`
	APIFormat        string  `json:"api_format"`
	RequestedModel   string  `json:"requested_model"`
	RoutedProvider   string  `json:"routed_provider"`
	RoutedModel      string  `json:"routed_model"`
	RouteRule        string  `json:"route_rule"`
	FallbackAttempts int     `json:"fallback_attempts"`
	IsStream         bool    `json:"is_stream"`
	StatusCode       int     `json:"status_code"`
	ErrorCode        string  `json:"error_code"`
	CacheStatus      string  `json:"cache_status"`
	CacheSimilarity  float64 `json:"cache_similarity,omitempty"`
	PromptTokens     int64   `json:"prompt_tokens"`
	CompletionTokens int64   `json:"completion_tokens"`
	CachedTokens     int64   `json:"cached_tokens"`
	EstimatedCostUSD float64 `json:"estimated_cost_usd"`
	BaselineCostUSD  float64 `json:"baseline_cost_usd"`
	SavedCostUSD     float64 `json:"saved_cost_usd"`
	SavedTokens      int64   `json:"saved_tokens"`
	TTFBMs           int64   `json:"ttfb_ms"`
	TotalMs          int64   `json:"total_ms"`
	ProxyOverheadMs  int64   `json:"proxy_overhead_ms"`
}

// BudgetUsage represents current usage for a budget scope and window.
type BudgetUsage struct {
	ScopeType      string
	ScopeKey       string
	WindowStartUTC int64
	TotalTokens    int64
	TotalCostUSD   float64
	RequestCount   int64
	UpdatedAtUTC   int64
}

// MetricsSummary aggregates telemetry for TokenHUD and /metrics/summary.
type MetricsSummary struct {
	TotalRequests    int64   `json:"total_requests"`
	CacheHits        int64   `json:"cache_hits"`
	CacheHitRate     float64 `json:"cache_hit_rate"`
	PromptTokens     int64   `json:"prompt_tokens"`
	CompletionTokens int64   `json:"completion_tokens"`
	CachedTokens     int64   `json:"cached_tokens"`
	SavedTokens      int64   `json:"saved_tokens"`
	EstimatedCostUSD float64 `json:"estimated_cost_usd"`
	SavedCostUSD     float64 `json:"saved_cost_usd"`
	P50TotalMs       int64   `json:"p50_total_ms"`
	P95TotalMs       int64   `json:"p95_total_ms"`
	P50OverheadMs    int64   `json:"p50_overhead_ms"`
	P95OverheadMs    int64   `json:"p95_overhead_ms"`
}

// Open opens (or creates) the SQLite database in WAL mode and runs idempotent migrations.
func Open(dbPath string) (*Store, error) {
	if dir := filepath.Dir(dbPath); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("create sqlite parent dir: %w", err)
		}
	}

	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, fmt.Errorf("open sqlite %q: %w", dbPath, err)
	}

	// Single writer connection avoids SQLITE_BUSY contention under WAL mode.
	db.SetMaxOpenConns(1)

	pragmas := []string{
		"PRAGMA journal_mode=WAL;",
		"PRAGMA busy_timeout=5000;",
		"PRAGMA synchronous=NORMAL;",
	}
	for _, p := range pragmas {
		if _, err := db.Exec(p); err != nil {
			_ = db.Close()
			return nil, fmt.Errorf("exec %q: %w", p, err)
		}
	}

	if err := migrate(db); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("migrate sqlite: %w", err)
	}

	return &Store{db: db}, nil
}

// Close closes the underlying SQLite connection.
func (s *Store) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}

// DB returns the underlying *sql.DB for testing or advanced queries.
func (s *Store) DB() *sql.DB {
	return s.db
}

func migrate(db *sql.DB) error {
	const schema = `
CREATE TABLE IF NOT EXISTS requests_log (
    id                 TEXT PRIMARY KEY,
    ts_utc             INTEGER NOT NULL,
    project_id         TEXT NOT NULL DEFAULT 'default',
    session_id         TEXT NOT NULL DEFAULT 'default',
    api_format         TEXT NOT NULL,
    requested_model    TEXT NOT NULL,
    routed_provider    TEXT NOT NULL,
    routed_model       TEXT NOT NULL,
    route_rule         TEXT,
    fallback_attempts  INTEGER NOT NULL DEFAULT 0,
    is_stream          INTEGER NOT NULL DEFAULT 0,
    status_code        INTEGER NOT NULL,
    error_code         TEXT,
    cache_status       TEXT NOT NULL DEFAULT 'miss',
    cache_similarity   REAL,
    prompt_tokens      INTEGER NOT NULL DEFAULT 0,
    completion_tokens  INTEGER NOT NULL DEFAULT 0,
    cached_tokens      INTEGER NOT NULL DEFAULT 0,
    estimated_cost_usd REAL NOT NULL DEFAULT 0.0,
    baseline_cost_usd  REAL NOT NULL DEFAULT 0.0,
    saved_cost_usd     REAL NOT NULL DEFAULT 0.0,
    saved_tokens       INTEGER NOT NULL DEFAULT 0,
    ttfb_ms            INTEGER NOT NULL DEFAULT 0,
    total_ms           INTEGER NOT NULL DEFAULT 0,
    proxy_overhead_ms  INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS idx_req_ts ON requests_log(ts_utc);
CREATE INDEX IF NOT EXISTS idx_req_project_ts ON requests_log(project_id, ts_utc);
CREATE INDEX IF NOT EXISTS idx_req_session_ts ON requests_log(session_id, ts_utc);

CREATE TABLE IF NOT EXISTS budget_usage (
    scope_type       TEXT NOT NULL,
    scope_key        TEXT NOT NULL,
    window_start_utc INTEGER NOT NULL,
    total_tokens     INTEGER NOT NULL DEFAULT 0,
    total_cost_usd   REAL NOT NULL DEFAULT 0.0,
    request_count    INTEGER NOT NULL DEFAULT 0,
    updated_at_utc   INTEGER NOT NULL,
    PRIMARY KEY (scope_type, scope_key, window_start_utc)
);

CREATE TABLE IF NOT EXISTS cache_entries (
    cache_key             TEXT PRIMARY KEY,
    route_namespace       TEXT NOT NULL,
    api_format            TEXT NOT NULL,
    model                 TEXT NOT NULL,
    is_stream             INTEGER NOT NULL DEFAULT 0,
    response_headers_json TEXT NOT NULL,
    response_body         BLOB NOT NULL,
    prompt_tokens         INTEGER NOT NULL DEFAULT 0,
    completion_tokens     INTEGER NOT NULL DEFAULT 0,
    embedding             BLOB,
    created_at_utc        INTEGER NOT NULL,
    expires_at_utc        INTEGER NOT NULL,
    hit_count             INTEGER NOT NULL DEFAULT 0,
    last_hit_at_utc       INTEGER
);
CREATE INDEX IF NOT EXISTS idx_cache_expiry ON cache_entries(expires_at_utc);
CREATE INDEX IF NOT EXISTS idx_cache_ns_expiry ON cache_entries(route_namespace, expires_at_utc);
`
	_, err := db.Exec(schema)
	return err
}

// InsertRequestLog writes a single request telemetry row.
func (s *Store) InsertRequestLog(ctx context.Context, r RequestLog) error {
	isStreamInt := 0
	if r.IsStream {
		isStreamInt = 1
	}
	if r.CacheStatus == "" {
		r.CacheStatus = "miss"
	}
	if r.ProjectID == "" {
		r.ProjectID = "default"
	}
	if r.SessionID == "" {
		r.SessionID = "default"
	}

	const q = `
INSERT INTO requests_log (
    id, ts_utc, project_id, session_id, api_format, requested_model,
    routed_provider, routed_model, route_rule, fallback_attempts, is_stream,
    status_code, error_code, cache_status, cache_similarity,
    prompt_tokens, completion_tokens, cached_tokens,
    estimated_cost_usd, baseline_cost_usd, saved_cost_usd, saved_tokens,
    ttfb_ms, total_ms, proxy_overhead_ms
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`

	_, err := s.db.ExecContext(ctx, q,
		r.ID, r.TsUTC, r.ProjectID, r.SessionID, r.APIFormat, r.RequestedModel,
		r.RoutedProvider, r.RoutedModel, r.RouteRule, r.FallbackAttempts, isStreamInt,
		r.StatusCode, r.ErrorCode, r.CacheStatus, r.CacheSimilarity,
		r.PromptTokens, r.CompletionTokens, r.CachedTokens,
		r.EstimatedCostUSD, r.BaselineCostUSD, r.SavedCostUSD, r.SavedTokens,
		r.TTFBMs, r.TotalMs, r.ProxyOverheadMs,
	)
	return err
}

// AddBudgetUsage atomically increments token, cost, and request counters for a budget scope.
func (s *Store) AddBudgetUsage(ctx context.Context, scopeType, scopeKey string, windowStartUTC, tokens int64, costUSD float64) error {
	now := time.Now().UTC().UnixMilli()
	const q = `
INSERT INTO budget_usage (scope_type, scope_key, window_start_utc, total_tokens, total_cost_usd, request_count, updated_at_utc)
VALUES (?, ?, ?, ?, ?, 1, ?)
ON CONFLICT(scope_type, scope_key, window_start_utc) DO UPDATE SET
    total_tokens   = total_tokens + excluded.total_tokens,
    total_cost_usd = total_cost_usd + excluded.total_cost_usd,
    request_count  = request_count + 1,
    updated_at_utc = excluded.updated_at_utc`

	_, err := s.db.ExecContext(ctx, q, scopeType, scopeKey, windowStartUTC, tokens, costUSD, now)
	return err
}

// GetBudgetUsage returns the current usage for a scope and window, or a zero-value struct if absent.
func (s *Store) GetBudgetUsage(ctx context.Context, scopeType, scopeKey string, windowStartUTC int64) (BudgetUsage, error) {
	const q = `
SELECT scope_type, scope_key, window_start_utc, total_tokens, total_cost_usd, request_count, updated_at_utc
FROM budget_usage
WHERE scope_type = ? AND scope_key = ? AND window_start_utc = ?`

	var u BudgetUsage
	err := s.db.QueryRowContext(ctx, q, scopeType, scopeKey, windowStartUTC).Scan(
		&u.ScopeType, &u.ScopeKey, &u.WindowStartUTC,
		&u.TotalTokens, &u.TotalCostUSD, &u.RequestCount, &u.UpdatedAtUTC,
	)
	if err == sql.ErrNoRows {
		return BudgetUsage{
			ScopeType:      scopeType,
			ScopeKey:       scopeKey,
			WindowStartUTC: windowStartUTC,
		}, nil
	}
	return u, err
}

// GetMetricsSummary computes aggregate telemetry and latency percentiles since sinceUTC (0 = all time).
func (s *Store) GetMetricsSummary(ctx context.Context, sinceUTC int64) (MetricsSummary, error) {
	const aggQ = `
SELECT
    COUNT(*),
    COALESCE(SUM(CASE WHEN cache_status IN ('l1_hit', 'l2_hit') THEN 1 ELSE 0 END), 0),
    COALESCE(SUM(prompt_tokens), 0),
    COALESCE(SUM(completion_tokens), 0),
    COALESCE(SUM(cached_tokens), 0),
    COALESCE(SUM(saved_tokens), 0),
    COALESCE(SUM(estimated_cost_usd), 0.0),
    COALESCE(SUM(saved_cost_usd), 0.0)
FROM requests_log
WHERE ts_utc >= ?`

	var sum MetricsSummary
	err := s.db.QueryRowContext(ctx, aggQ, sinceUTC).Scan(
		&sum.TotalRequests,
		&sum.CacheHits,
		&sum.PromptTokens,
		&sum.CompletionTokens,
		&sum.CachedTokens,
		&sum.SavedTokens,
		&sum.EstimatedCostUSD,
		&sum.SavedCostUSD,
	)
	if err != nil {
		return MetricsSummary{}, err
	}
	if sum.TotalRequests > 0 {
		sum.CacheHitRate = float64(sum.CacheHits) / float64(sum.TotalRequests)
	}

	const latQ = `SELECT total_ms, proxy_overhead_ms FROM requests_log WHERE ts_utc >= ?`
	rows, err := s.db.QueryContext(ctx, latQ, sinceUTC)
	if err != nil {
		return MetricsSummary{}, err
	}
	defer rows.Close()

	var totals, overheads []int64
	for rows.Next() {
		var t, o int64
		if err := rows.Scan(&t, &o); err != nil {
			return MetricsSummary{}, err
		}
		totals = append(totals, t)
		overheads = append(overheads, o)
	}
	if err := rows.Err(); err != nil {
		return MetricsSummary{}, err
	}

	sum.P50TotalMs = percentile(totals, 0.50)
	sum.P95TotalMs = percentile(totals, 0.95)
	sum.P50OverheadMs = percentile(overheads, 0.50)
	sum.P95OverheadMs = percentile(overheads, 0.95)
	return sum, nil
}

func percentile(vals []int64, p float64) int64 {
	if len(vals) == 0 {
		return 0
	}
	sorted := append([]int64(nil), vals...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	idx := int(float64(len(sorted)-1) * p)
	if idx < 0 {
		idx = 0
	}
	if idx >= len(sorted) {
		idx = len(sorted) - 1
	}
	return sorted[idx]
}
