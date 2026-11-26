---
title: Plan - Feature: M3-Cache-And-Benchmark
ticket: local://LLRP:LLRP-Plan-M3-Cache-And-Benchmark-2026-11
status: in-progress
priority: 
state: 
due: 
appetite: 
version: 
impact: 
devTicketKey: LLRP:LLRP-Plan-M3-Cache-And-Benchmark-2026-11
agent: Gemini
branch: feat/m3-cache-and-benchmark
worktree: Local-LLM-Router-Proxy
startedAt: 2026-11-26
---
# M3-Cache-And-Benchmark

## Summary

Implement Milestone 3 from `ROADMAP.md`: L1 exact canonical cache with synthetic SSE frame replay, strictly opt-in L2 semantic cache using local Ollama embeddings, `DELETE /cache` invalidation, and a reproducible benchmark harness (`bench/`) comparing Direct vs. Proxied (Routed + Cached) workloads.

## Context

- `internal/storage/sqlite.go` already defines the `cache_entries` table and indexes (`idx_cache_expiry`, `idx_cache_ns_expiry`).
- `internal/config/config.go` already defines `CacheConfig`, `L1CacheConfig`, and `L2CacheConfig`.
- `internal/proxy/` handles Gemini Native, OpenAI Chat Completions, OpenAI Responses, and Anthropic Messages with zero-buffering SSE forwarding (`sse.go`).
- Per `ADR-002`, L1 exact cache applies only to deterministic requests (`temperature == 0` or `X-Router-Cache: force`), and L2 semantic cache is strictly opt-in per route/header and always disabled when tools are present.

## Open questions

None

## Acceptance criteria

- [ ] AC1: SQLite `cache_entries` CRUD, TTL expiration, LRU `max_entries` eviction, semantic candidate listing, and `PurgeCache` pass unit tests.
- [ ] AC2: L1 canonical exact cache and opt-in L2 semantic cache (Ollama embeddings + cosine similarity) pass unit tests, including bypass and `has_tools` guards.
- [ ] AC3: All 4 proxy handlers support L1/L2 cache lookup, write-through, synthetic SSE frame replay, and `DELETE /cache` invalidation with integration tests.
- [ ] AC4: Reproducible benchmark harness (`go run ./bench`) outputs Markdown + JSON report showing >0% frontier token savings and <5ms p50 pass-through proxy overhead.
- [ ] AC5: `ROADMAP.md` and `README.md` updated; `go test -v ./...` and `go build -o bin/llm-router.exe ./cmd/router` pass.

## Steps

- [x] Slice 1: Implement SQLite cache storage (`internal/storage/cache.go` + tests) (AC1)
- [x] Slice 2: Implement L1 exact & opt-in L2 semantic cache engine (`internal/cache/` + tests) (AC2)
- [ ] Slice 3: Integrate cache, synthetic SSE replay, and `DELETE /cache` into proxy (`internal/proxy/` + tests) (AC3)
- [ ] Slice 4: Create reproducible benchmark & proof harness (`bench/main.go`) (AC4)
- [ ] Slice 5: Update `ROADMAP.md` and `README.md`, run full verification, and complete review/PR (AC5)

## Risks

- **Stale/corrupt code answers from semantic cache:** Mitigated by keeping L2 strictly opt-in (`semantic_cache: true` or `X-Router-Semantic-Cache: allow`), scoping by `route_namespace`, and disabling L2 when `has_tools == true`.
- **SSE buffering regression:** Mitigated by teeing SSE lines in `forwardSSEWithCapture` with a bounded buffer while flushing each line immediately.

## Out of scope

- Distributed/Redis caching (local SQLite only per local-first architecture).
- Live network calls in unit tests or benchmarks (all tests and `bench/` use deterministic local `httptest` mocks).

## Notes

Approved Milestone 3 Plan:
> 1. Workflow (`adw` + `context-gatekeeper`): `adw new "M3-Cache-And-Benchmark"` -> `adw create <plan> --agent Gemini` -> branch `feat/m3-cache-and-benchmark` -> commit plan -> `adw start`. Regular incremental commits per slice, using `context-gatekeeper` (`run_compact` + `fetch_chunk`) for verbose `go test` / benchmark outputs. Finish with `adw pack --test`, independent review (`quick-review` + `adw review`), `adw check`, PR #3, and `adw merge`.
> 2. Slice 1 — SQLite Cache Storage (`internal/storage/cache.go` + tests).
> 3. Slice 2 — L1 Exact & Opt-In L2 Semantic Cache (`internal/cache/` + tests).
> 4. Slice 3 — Proxy Integration, Synthetic SSE Replay & Cache Invalidation (`internal/proxy/` + tests).
> 5. Slice 4 — Reproducible Benchmark & Proof Harness (`bench/`).
> 6. Slice 5 — Verification & Docs (`ROADMAP.md`, `README.md`, `go test -v ./...`, `go build`, `go run ./bench`).

## Progress log

<!-- One line per session/review, added with `ticket.mjs log` or `ticket.mjs review` -->
