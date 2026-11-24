# AGENTS.md — Local LLM Router & Semantic Cache Proxy

## Project Overview
A standalone, local-first **Smart Model Router & Semantic Cache Proxy** written in Go (`modernc.org/sqlite` pure-Go SQLite, zero CGO). It sits between local dev tools / coding agents and LLM providers (Gemini Native, OpenAI / Codex, Anthropic / Claude, and local Ollama) to reduce frontier token consumption via rule-based routing, exact + opt-in semantic caching, fallback chains, and per-session/per-project budget guardrails.

## Architecture Map
- `cmd/router/main.go` — CLI entrypoint, config loading, SQLite initialization, graceful shutdown.
- `internal/config/` — YAML config loader, defaults, validation, env API key resolution.
- `internal/storage/` — Pure-Go SQLite (WAL mode) migrations, `requests_log`, `budget_usage`, `cache_entries`, `/metrics/summary` queries.
- `internal/telemetry/` — Dual telemetry writer (SQLite + `TokenHUD`-compatible append-only JSONL) and cost calculation.
- `internal/budget/` — Pre-flight per-session and per-project-daily budget guardrails (`soft_warn`, `block`, `downgrade` with `never_downgrade` protection).
- `internal/proxy/` — Zero-buffering SSE streaming reverse proxy, provider handlers (Gemini Native, OpenAI Chat Completions / Ollama, and in M2: OpenAI Responses + Anthropic Messages).
- `docs/adr/` — Architecture Decision Records.
- `ROADMAP.md` — Phased milestone plan and source of truth for project progress.
- `ARCHITECTURE.md` — System design, streaming mechanics, cache invariants, and telemetry contract.

## Development process
1. **Roadmap-Driven Milestones:** `ROADMAP.md` is the authoritative tracker. Work strictly in milestone order (M0 → M1 → M2 → M3) and check off items only after verification passes.
2. **ADR-Before-Architecture-Change:** Any change to streaming semantics, fallback invariants, cache invalidation, or budget enforcement requires creating or updating an ADR in `docs/adr/` first.
3. **Test-First / Invariant Testing:**
   - All routing policies, `never_downgrade` guards, budget thresholds, cache key canonicalization, and SSE streaming/usage-extraction paths must have automated tests (`*_test.go`) using `httptest.NewServer` mocks and `t.TempDir()` SQLite databases.
4. **Verification Gate:** Before marking any milestone item complete, run and observe passing output for:
   - `go test -v ./...` (or `go test -race ./...` when CGO/gcc is available)
   - `go build -o bin/llm-router.exe ./cmd/router` (or `go build ./cmd/router` on Unix)
5. **Security & Privacy:** Never commit real API keys, `.env`, `router.config.yaml`, or live `data/` SQLite/JSONL telemetry files.
6. **Git & PR Workflow:** Keep `main` green. Develop each milestone or feature on a dedicated branch (`feat/...`, `fix/...`, `docs/...`), push, open a GitHub PR via `gh pr create`, ensure tests/CI pass, and merge cleanly into `main`.
