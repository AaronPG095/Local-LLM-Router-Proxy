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

## Project Guardrails
1. **Roadmap-Driven Milestones:** `ROADMAP.md` is the authoritative tracker. Work strictly in milestone order (M0 → M1 → M2 → M3) and check off items only after verification passes.
2. **ADR-Before-Architecture-Change:** Any change to streaming semantics, fallback invariants, cache invalidation, or budget enforcement requires creating or updating an ADR in `docs/adr/` first.
3. **Test-First / Invariant Testing:**
   - All routing policies, `never_downgrade` guards, budget thresholds, cache key canonicalization, and SSE streaming/usage-extraction paths must have automated tests (`*_test.go`) using `httptest.NewServer` mocks and `t.TempDir()` SQLite databases.
4. **Verification Gate:** Before marking any milestone item complete, run and observe passing output for:
   - `go test -v ./...` (or `go test -race ./...` when CGO/gcc is available)
   - `go build -o bin/llm-router.exe ./cmd/router` (or `go build ./cmd/router` on Unix)
5. **Security & Privacy:** Never commit real API keys, `.env`, `router.config.yaml`, or live `data/` SQLite/JSONL telemetry files.
6. **Git & PR Workflow:** Keep `main` green. Develop each milestone or feature on a dedicated branch (`feat/...`, `fix/...`, `docs/...`), push, open a GitHub PR via `gh pr create`, ensure tests/CI pass, and merge cleanly into `main`.

## Development process (every AI: Claude, Codex, Gemini/Antigravity, Cursor)

Non-trivial changes follow these six steps. Only trivial changes with at most 40 changed lines, no new files under `src/`, and no managed workflow changes may skip them. Label those PRs `no-plan`.

1. **Plan.** Investigate read-only, propose a plan, wait for approval, and quote it under Notes (use `blueprint` for multi-PR initiatives).
2. **Plan doc.** Create `docs/plans/LLRP-Plan-<Topic>-<YYYY-MM>.plan.md` via `adw new "<Topic>"` (one PR per plan; split if larger).
3. **Ticket.** Run `adw create <plan> --agent <Claude|Codex|Gemini|Other>` (LLRP prefix, To Do, Development, stable `Dev Ticket Key` / `local://`). Preserve `devTicketKey` on rename; never run concurrent creates.
4. **Implement.** Branch, commit plan, run `adw start [<plan>]`. Use `prime`/`resume` on cold starts, `context-gatekeeper` (`run_compact` + `fetch_chunk`; `repo_map` → `symbol_outline` → `read_symbol`) for verbose output or large files, and `tick`/`defer` (never delete checkboxes). Progress stays in the plan; run `handoff` before stopping.
5. **Review.** Run `adw pack [<plan>] [--test]`, complete a mandatory independent review (`requireReview: true`, `allowSelfReview: false`) using `quick-review`, and stamp `review --agent <client> --reviewer-id <id> --verdict <pass|warn|block> --summary "..."`.
6. **Finish.** Run `adw check [<plan>]`, open/link PR (`pr` or `ship --pr <url>`), and `merge` (never mark Task Done pre-merge).

If `adw` fails, stop and report. Detailed flags: `dev-workflow` skill & `README.md`.
Pre-push guardrail: `adw hook install`. Universal CLI: `AI-Dev-Workflow`.
