# ADR-001: Go Stack, Pure-Go SQLite, and Zero-Buffering SSE Streaming

## Status
Accepted

## Context
The proxy sits on the critical path of every local coding-agent LLM call. It must add minimal latency (<5ms p50 overhead), distribute as a single local binary with zero runtime bloat, persist telemetry/budgets/cache locally, and stream SSE responses transparently.

## Decision
1. **Language:** Go, compiling to a single static binary (`bin/llm-router.exe`).
2. **Database:** `modernc.org/sqlite` (pure-Go SQLite, `CGO_ENABLED=0` compatible) in `WAL` mode (`PRAGMA busy_timeout=5000; PRAGMA synchronous=NORMAL;`).
3. **SSE Forwarding:** Line-by-line forwarding using `bufio.Reader.ReadBytes('\n')` and `http.Flusher.Flush()`, never `bufio.Scanner` (which fails on lines >64 KB) and never full-body buffering.
4. **Pre-First-Byte Fallback:** Retries and provider fallbacks are permitted only before the first byte of a `200 OK` upstream response is written to the client.

## Consequences
- No Node.js/Python/.NET runtime or CGO/GCC toolchain required to build or run locally.
- Sub-millisecond SSE pass-through overhead.
- Mid-stream upstream disconnects cannot be transparently retried and must surface to the client as stream errors.
