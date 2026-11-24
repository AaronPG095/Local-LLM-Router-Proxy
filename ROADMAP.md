# ROADMAP — Local LLM Router & Semantic Cache Proxy

## Upstream API Priority Order
1. **Priority 1 — Gemini Native API:** `/v1beta/models/{model}:generateContent` & `:streamGenerateContent?alt=sse` (used by Gemini CLI & Google GenAI SDKs).
2. **Priority 2 — OpenAI / Codex:** `/v1/chat/completions` (OpenAI + local Ollama) and `/v1/responses` (Codex CLI).
3. **Priority 3 — Anthropic / Claude:** `/v1/messages` (Claude Code / Anthropic SDKs).
4. **Local Offload:** Local Ollama (`http://127.0.0.1:11434/v1`) for mechanical sub-tasks & local embeddings.

---

## Milestone 0 — Scaffold, AI Dev Workflow, Config & Storage
**Goal:** Establish project structure, architectural invariants, YAML config, and WAL-mode SQLite persistence.
- [x] Initialize Go module, `.gitignore`, and dependencies (`yaml.v3`, `modernc.org/sqlite`).
- [x] Create `AGENTS.md` with `Development process`, `ROADMAP.md`, and `ARCHITECTURE.md`.
- [x] Write core ADRs (`ADR-001` streaming proxy, `ADR-002` cache invalidation & opt-in semantic cache, `ADR-003` budget guardrails & `never_downgrade`).
- [x] Create `router.config.example.yaml` and `internal/config` loader + validator + unit tests.
- [x] Create `internal/storage` SQLite WAL runner, idempotent migrations (`requests_log`, `budget_usage`, `cache_entries`), and unit tests.

**Exit Criteria:** `go test ./internal/config/... ./internal/storage/...` passes against a temp WAL SQLite DB.

---

## Milestone 1 — Transparent SSE Proxy, Dual Telemetry & Budget Guardrails
**Goal:** Working local proxy for Gemini Native + OpenAI Chat Completions / Ollama with zero-buffering SSE, `TokenHUD`-ready telemetry, and hard/soft budget caps.
- [x] Zero-buffering line-by-line SSE forwarder (`internal/proxy/sse.go`) using `bufio.Reader` + `http.Flusher`.
- [x] Gemini Native handler (`:generateContent` & `:streamGenerateContent?alt=sse`) with `usageMetadata` extraction and `thinkingConfig` detection.
- [x] OpenAI Chat Completions handler (`/v1/chat/completions`) with automatic `stream_options.include_usage` injection and `usage` extraction.
- [x] Dual telemetry writer (`internal/telemetry`): SQLite `requests_log` + append-only `telemetry.jsonl` + `GET /metrics/summary` endpoint.
- [x] Per-session and per-project-daily budget guardrails (`internal/budget`): `X-Router-Budget-Warning` on soft cap, `429` block or safe downgrade on hard cap, respecting `never_downgrade`.
- [x] `cmd/router/main.go` entrypoint with graceful shutdown & integration tests (`internal/proxy/proxy_test.go`).

**Exit Criteria:** `go test -v ./...` passes; `bin/llm-router.exe` builds cleanly and proxies both streaming and non-streaming Gemini & OpenAI/Ollama calls.

---

## Milestone 2 — Policy Routing, Fallback Chains & Full Multi-Provider Support
**Goal:** Automatically offload mechanical tasks to cheap/local models, recover from `429`/`529` errors transparently, and add OpenAI Responses (Codex) + Anthropic Messages adapters.
- [ ] Deterministic policy routing engine (headers, model aliases, estimated token threshold, system-prompt regex, tool-call awareness).
- [ ] Strict `never_downgrade` enforcement across all routing and budget-downgrade paths.
- [ ] Pre-first-byte `429`/`529`/`5xx` retry & fallback chains across providers.
- [ ] Cross-protocol translation for local Ollama offload (Gemini Native ↔ Ollama, OpenAI Responses ↔ Ollama).
- [ ] **OpenAI Responses API (`/v1/responses`)** adapter for Codex CLI.
- [ ] **Anthropic Messages API (`/v1/messages`)** adapter with Anthropic SSE framing and prompt-cache token accounting.

**Exit Criteria:** Mocked `429`/`529` upstreams fail over cleanly before first byte; Gemini & Codex mechanical prompts offload to local Ollama seamlessly.

---

## Milestone 3 — L1 Exact Cache, Opt-In L2 Semantic Cache & Benchmark Harness
**Goal:** Eliminate redundant calls with local caching and prove token/cost/latency savings with a reproducible benchmark suite.
- [ ] L1 Exact/Prefix Cache (SHA-256 canonicalized request tuple) with synthetic **SSE frame replay** for streaming clients.
- [ ] Opt-in L2 Semantic Cache (local Ollama embeddings + cosine similarity + namespace isolation + TTL + manual invalidation endpoint).
- [ ] Reproducible Benchmark & Proof Harness (`bench/`): compares direct API calls vs. routed + cached workload, reporting cache hit rate, p50/p95 latency overhead (ms), and % frontier tokens/cost saved.

**Exit Criteria:** Benchmark script produces reproducible markdown/JSON report demonstrating >0% frontier token savings with <5ms p50 proxy overhead on pass-through.
