package storage

import (
	"context"
	"database/sql"
	"time"
)

// CacheEntry represents a row in the cache_entries table.
type CacheEntry struct {
	CacheKey            string
	RouteNamespace      string
	APIFormat           string
	Model               string
	IsStream            bool
	ResponseHeadersJSON string
	ResponseBody        []byte
	PromptTokens        int64
	CompletionTokens    int64
	Embedding           []byte
	CreatedAtUTC        int64
	ExpiresAtUTC        int64
	HitCount            int64
	LastHitAtUTC        int64
}

// GetCacheEntry fetches a non-expired cache entry by cacheKey, or returns (nil, nil) on miss/expiry.
func (s *Store) GetCacheEntry(ctx context.Context, cacheKey string, nowMs int64) (*CacheEntry, error) {
	if nowMs == 0 {
		nowMs = time.Now().UTC().UnixMilli()
	}
	const q = `
SELECT cache_key, route_namespace, api_format, model, is_stream,
       response_headers_json, response_body, prompt_tokens, completion_tokens,
       embedding, created_at_utc, expires_at_utc, hit_count, COALESCE(last_hit_at_utc, 0)
FROM cache_entries
WHERE cache_key = ? AND expires_at_utc > ?`

	var e CacheEntry
	var isStreamInt int
	err := s.db.QueryRowContext(ctx, q, cacheKey, nowMs).Scan(
		&e.CacheKey, &e.RouteNamespace, &e.APIFormat, &e.Model, &isStreamInt,
		&e.ResponseHeadersJSON, &e.ResponseBody, &e.PromptTokens, &e.CompletionTokens,
		&e.Embedding, &e.CreatedAtUTC, &e.ExpiresAtUTC, &e.HitCount, &e.LastHitAtUTC,
	)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	e.IsStream = isStreamInt != 0
	return &e, nil
}

// PutCacheEntry upserts a cache entry, deletes expired entries, and prunes oldest entries beyond maxEntries.
func (s *Store) PutCacheEntry(ctx context.Context, entry CacheEntry, maxEntries int) error {
	nowMs := entry.CreatedAtUTC
	if nowMs == 0 {
		nowMs = time.Now().UTC().UnixMilli()
		entry.CreatedAtUTC = nowMs
	}
	isStreamInt := 0
	if entry.IsStream {
		isStreamInt = 1
	}

	const upsertQ = `
INSERT INTO cache_entries (
    cache_key, route_namespace, api_format, model, is_stream,
    response_headers_json, response_body, prompt_tokens, completion_tokens,
    embedding, created_at_utc, expires_at_utc, hit_count, last_hit_at_utc
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 0, NULL)
ON CONFLICT(cache_key) DO UPDATE SET
    route_namespace       = excluded.route_namespace,
    api_format            = excluded.api_format,
    model                 = excluded.model,
    is_stream             = excluded.is_stream,
    response_headers_json = excluded.response_headers_json,
    response_body         = excluded.response_body,
    prompt_tokens         = excluded.prompt_tokens,
    completion_tokens     = excluded.completion_tokens,
    embedding             = excluded.embedding,
    created_at_utc        = excluded.created_at_utc,
    expires_at_utc        = excluded.expires_at_utc`

	if _, err := s.db.ExecContext(ctx, upsertQ,
		entry.CacheKey, entry.RouteNamespace, entry.APIFormat, entry.Model, isStreamInt,
		entry.ResponseHeadersJSON, entry.ResponseBody, entry.PromptTokens, entry.CompletionTokens,
		entry.Embedding, entry.CreatedAtUTC, entry.ExpiresAtUTC,
	); err != nil {
		return err
	}

	// Clean expired entries opportunistically.
	_, _ = s.db.ExecContext(ctx, `DELETE FROM cache_entries WHERE expires_at_utc <= ?`, nowMs)

	if maxEntries > 0 {
		const pruneQ = `
DELETE FROM cache_entries
WHERE cache_key IN (
    SELECT cache_key FROM cache_entries
    ORDER BY COALESCE(last_hit_at_utc, created_at_utc) DESC
    LIMIT -1 OFFSET ?
)`
		if _, err := s.db.ExecContext(ctx, pruneQ, maxEntries); err != nil {
			return err
		}
	}
	return nil
}

// ListSemanticCandidates returns non-expired cache entries in the given namespace that have embeddings.
func (s *Store) ListSemanticCandidates(ctx context.Context, namespace string, nowMs int64, limit int) ([]CacheEntry, error) {
	if nowMs == 0 {
		nowMs = time.Now().UTC().UnixMilli()
	}
	if limit <= 0 {
		limit = 500
	}
	const q = `
SELECT cache_key, route_namespace, api_format, model, is_stream,
       response_headers_json, response_body, prompt_tokens, completion_tokens,
       embedding, created_at_utc, expires_at_utc, hit_count, COALESCE(last_hit_at_utc, 0)
FROM cache_entries
WHERE route_namespace = ? AND expires_at_utc > ? AND embedding IS NOT NULL AND length(embedding) > 0
ORDER BY created_at_utc DESC
LIMIT ?`

	rows, err := s.db.QueryContext(ctx, q, namespace, nowMs, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []CacheEntry
	for rows.Next() {
		var e CacheEntry
		var isStreamInt int
		if err := rows.Scan(
			&e.CacheKey, &e.RouteNamespace, &e.APIFormat, &e.Model, &isStreamInt,
			&e.ResponseHeadersJSON, &e.ResponseBody, &e.PromptTokens, &e.CompletionTokens,
			&e.Embedding, &e.CreatedAtUTC, &e.ExpiresAtUTC, &e.HitCount, &e.LastHitAtUTC,
		); err != nil {
			return nil, err
		}
		e.IsStream = isStreamInt != 0
		out = append(out, e)
	}
	return out, rows.Err()
}

// TouchCacheHit increments hit_count and updates last_hit_at_utc.
func (s *Store) TouchCacheHit(ctx context.Context, cacheKey string, nowMs int64) error {
	if nowMs == 0 {
		nowMs = time.Now().UTC().UnixMilli()
	}
	_, err := s.db.ExecContext(ctx,
		`UPDATE cache_entries SET hit_count = hit_count + 1, last_hit_at_utc = ? WHERE cache_key = ?`,
		nowMs, cacheKey,
	)
	return err
}

// PurgeCache deletes all cache entries (if namespace == "") or entries for a specific namespace.
func (s *Store) PurgeCache(ctx context.Context, namespace string) (int64, error) {
	var res sql.Result
	var err error
	if namespace == "" {
		res, err = s.db.ExecContext(ctx, `DELETE FROM cache_entries`)
	} else {
		res, err = s.db.ExecContext(ctx, `DELETE FROM cache_entries WHERE route_namespace = ?`, namespace)
	}
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}
