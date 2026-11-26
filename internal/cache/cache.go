package cache

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/AaronPG095/Local-LLM-Router-Proxy/internal/config"
	"github.com/AaronPG095/Local-LLM-Router-Proxy/internal/storage"
)

// LookupRequest describes an incoming request eligible for L1/L2 cache lookup.
type LookupRequest struct {
	APIFormat          string
	TargetModel        string
	RouteNamespace     string
	IsStream           bool
	HasTools           bool
	RouteSemanticCache bool
	Headers            http.Header
	Body               []byte
	PromptText         string
}

// LookupResult represents the outcome of a cache lookup.
type LookupResult struct {
	Hit        bool
	Status     string // "miss" | "l1_hit" | "l2_hit"
	Similarity float64
	Entry      *storage.CacheEntry
	QueryVec   []float32 // retained on L2 miss to avoid re-embedding on Store
}

// StoreRequest contains the response data to persist in cache_entries.
type StoreRequest struct {
	Lookup           LookupRequest
	LookupRes        LookupResult
	HeadersJSON      string
	ResponseBody     []byte
	PromptTokens     int64
	CompletionTokens int64
}

// Engine implements L1 exact canonical caching and strictly opt-in L2 semantic caching.
type Engine struct {
	store    *storage.Store
	cfg      config.CacheConfig
	embedder Embedder
}

// NewEngine creates a cache Engine.
func NewEngine(store *storage.Store, cfg config.CacheConfig, embedder Embedder) *Engine {
	return &Engine{
		store:    store,
		cfg:      cfg,
		embedder: embedder,
	}
}

func (e *Engine) l2Allowed(req LookupRequest) bool {
	if req.HasTools || e.embedder == nil || strings.TrimSpace(req.PromptText) == "" {
		return false
	}
	if req.RouteSemanticCache {
		return true
	}
	if strings.EqualFold(strings.TrimSpace(req.Headers.Get("X-Router-Semantic-Cache")), "allow") {
		return true
	}
	return e.cfg.L2Semantic.Enabled && req.RouteSemanticCache
}

// Lookup checks L1 exact cache first, then opt-in L2 semantic cache.
func (e *Engine) Lookup(ctx context.Context, req LookupRequest) (LookupResult, error) {
	if e == nil || e.store == nil || ShouldBypass(req.Headers) {
		return LookupResult{Status: "miss"}, nil
	}

	nowMs := time.Now().UTC().UnixMilli()

	// L1 Exact Cache
	if e.cfg.L1Exact.Enabled && IsDeterministic(req.Body, req.Headers, e.cfg.L1Exact.RequireDeterministic) {
		key := CanonicalKey(req.APIFormat, req.TargetModel, req.IsStream, req.Body)
		entry, err := e.store.GetCacheEntry(ctx, key, nowMs)
		if err != nil {
			return LookupResult{Status: "miss"}, err
		}
		if entry != nil {
			_ = e.store.TouchCacheHit(ctx, key, nowMs)
			return LookupResult{
				Hit:    true,
				Status: "l1_hit",
				Entry:  entry,
			}, nil
		}
	}

	// L2 Semantic Cache (strictly opt-in, never when tools are present)
	if e.l2Allowed(req) {
		qVec, err := e.embedder.Embed(ctx, req.PromptText)
		if err != nil || len(qVec) == 0 {
			return LookupResult{Status: "miss"}, nil
		}

		cands, err := e.store.ListSemanticCandidates(ctx, req.RouteNamespace, nowMs, e.cfg.L2Semantic.MaxCandidates)
		if err != nil {
			return LookupResult{Status: "miss", QueryVec: qVec}, err
		}

		threshold := e.cfg.L2Semantic.SimilarityThreshold
		if threshold <= 0 {
			threshold = 0.94
		}

		var best *storage.CacheEntry
		bestSim := 0.0
		for i := range cands {
			cand := &cands[i]
			if cand.IsStream != req.IsStream || cand.APIFormat != req.APIFormat {
				continue
			}
			vec := DecodeFloat32s(cand.Embedding)
			sim := CosineSimilarity(qVec, vec)
			if sim >= threshold && sim > bestSim {
				bestSim = sim
				best = cand
			}
		}
		if best != nil {
			_ = e.store.TouchCacheHit(ctx, best.CacheKey, nowMs)
			return LookupResult{
				Hit:        true,
				Status:     "l2_hit",
				Similarity: bestSim,
				Entry:      best,
			}, nil
		}
		return LookupResult{Status: "miss", QueryVec: qVec}, nil
	}

	return LookupResult{Status: "miss"}, nil
}

// Store persists a cacheable response to SQLite.
func (e *Engine) Store(ctx context.Context, sr StoreRequest) error {
	if e == nil || e.store == nil || ShouldBypass(sr.Lookup.Headers) || len(sr.ResponseBody) == 0 {
		return nil
	}

	l1Eligible := e.cfg.L1Exact.Enabled && IsDeterministic(sr.Lookup.Body, sr.Lookup.Headers, e.cfg.L1Exact.RequireDeterministic)
	l2Eligible := e.l2Allowed(sr.Lookup)
	if !l1Eligible && !l2Eligible {
		return nil
	}

	now := time.Now().UTC()
	nowMs := now.UnixMilli()

	ttlSec := e.cfg.L1Exact.TTLSeconds
	if !l1Eligible && l2Eligible && e.cfg.L2Semantic.TTLSeconds > 0 {
		ttlSec = e.cfg.L2Semantic.TTLSeconds
	}
	if ttlSec <= 0 {
		ttlSec = 86400
	}

	var embBytes []byte
	if l2Eligible {
		vec := sr.LookupRes.QueryVec
		if len(vec) == 0 && e.embedder != nil {
			if v, err := e.embedder.Embed(ctx, sr.Lookup.PromptText); err == nil {
				vec = v
			}
		}
		embBytes = EncodeFloat32s(vec)
	}

	key := CanonicalKey(sr.Lookup.APIFormat, sr.Lookup.TargetModel, sr.Lookup.IsStream, sr.Lookup.Body)
	return e.store.PutCacheEntry(ctx, storage.CacheEntry{
		CacheKey:            key,
		RouteNamespace:      sr.Lookup.RouteNamespace,
		APIFormat:           sr.Lookup.APIFormat,
		Model:               sr.Lookup.TargetModel,
		IsStream:            sr.Lookup.IsStream,
		ResponseHeadersJSON: sr.HeadersJSON,
		ResponseBody:        sr.ResponseBody,
		PromptTokens:        sr.PromptTokens,
		CompletionTokens:    sr.CompletionTokens,
		Embedding:           embBytes,
		CreatedAtUTC:        nowMs,
		ExpiresAtUTC:        now.Add(time.Duration(ttlSec) * time.Second).UnixMilli(),
	}, e.cfg.L1Exact.MaxEntries)
}

// Purge deletes all or namespace-scoped cache entries.
func (e *Engine) Purge(ctx context.Context, namespace string) (int64, error) {
	if e == nil || e.store == nil {
		return 0, nil
	}
	return e.store.PurgeCache(ctx, namespace)
}
