# Local LLM Router & Semantic Cache Proxy

A standalone, local-first **Smart Model Router & Semantic Cache Proxy** written in Go (`modernc.org/sqlite` pure-Go SQLite, zero CGO). It sits between local dev tools / coding agents and LLM providers to reduce frontier token consumption via rule-based routing, exact + opt-in semantic caching, fallback chains, and per-session/per-project budget guardrails — while keeping SSE streaming fast and transparent.

## Upstream API Priority
1. **Gemini Native API:** `/v1beta/models/{model}:generateContent` & `:streamGenerateContent?alt=sse`
2. **OpenAI / Codex:** `/v1/chat/completions` (OpenAI + local Ollama) and `/v1/responses` *(Milestone 2)*
3. **Anthropic / Claude:** `/v1/messages` *(Milestone 2)*
4. **Local Ollama:** `http://127.0.0.1:11434/v1` for mechanical sub-tasks and local embeddings

## Quickstart

```powershell
# 1. Copy example config and set your API keys
cp router.config.example.yaml router.config.yaml
$env:GEMINI_API_KEY = "your-key"

# 2. Build and run
go build -o bin/llm-router.exe ./cmd/router
go build -o bin/llm-router-mcp.exe ./cmd/router-mcp
.\scripts\start-proxy.ps1
```

On Windows, run `.\scripts\setup-credentials.ps1` to store provider keys with Windows DPAPI.
The router loads saved keys at startup, and environment variables take precedence.
Run setup again to rotate one key. Press Enter at every other prompt to keep those keys.

- Health check: `GET http://127.0.0.1:8787/healthz`
- Live metrics (`TokenHUD`-compatible): `GET http://127.0.0.1:8787/metrics/summary`
- Cache invalidation: `DELETE http://127.0.0.1:8787/cache` (optional `?namespace=...`)
- Cache control headers: `X-Router-Cache: bypass | force`, `X-Router-Semantic-Cache: allow`
- Universal Antigravity MCP + Codex/Claude/Gemini CLI setup: [docs/WORKFLOW_INTEGRATION.md](./docs/WORKFLOW_INTEGRATION.md)

## Benchmark & Proof Harness
Run the reproducible benchmark comparing Direct frontier calls vs. Proxied (Routed + L1/L2 Cached) calls:

```powershell
go run ./bench
```

## Documentation
- [ROADMAP.md](./ROADMAP.md) — Phased milestones (M0 → M3) and current progress
- [ARCHITECTURE.md](./ARCHITECTURE.md) — Streaming mechanics, cache invariants, and telemetry contract
- [AGENTS.md](./AGENTS.md) — AI development process and verification gates
- [docs/adr/](./docs/adr/) — Architecture Decision Records
