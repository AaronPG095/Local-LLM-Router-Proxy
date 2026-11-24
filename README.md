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
.\bin\llm-router.exe -config router.config.yaml
```

- Health check: `GET http://127.0.0.1:8787/healthz`
- Live metrics (`TokenHUD`-compatible): `GET http://127.0.0.1:8787/metrics/summary`

## Documentation
- [ROADMAP.md](./ROADMAP.md) — Phased milestones (M0 → M3) and current progress
- [ARCHITECTURE.md](./ARCHITECTURE.md) — Streaming mechanics, cache invariants, and telemetry contract
- [AGENTS.md](./AGENTS.md) — AI development process and verification gates
- [docs/adr/](./docs/adr/) — Architecture Decision Records
