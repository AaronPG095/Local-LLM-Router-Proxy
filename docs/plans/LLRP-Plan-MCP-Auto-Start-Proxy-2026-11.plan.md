---
title: Plan - Feature: MCP-Auto-Start-Proxy
ticket: local://LLRP:LLRP-Plan-MCP-Auto-Start-Proxy-2026-11
status: in-progress
priority: 
state: 
due: 
appetite: 
version: 
impact: 
devTicketKey: LLRP:LLRP-Plan-MCP-Auto-Start-Proxy-2026-11
agent: Gemini
branch: feat/mcp-auto-start-proxy
worktree: Local-LLM-Router-Proxy
startedAt: 2026-11-26
reviewedBy: Gemini
reviewVerdict: pass
reviewSession: 5bf5585a-2374-46ee-ab3e-e29347927fdd
baseBranch: main
reviewFingerprint: 490f47341f98cc0f40bcd201f5b7ff2b22d741bb82cce4bdd706104bf604e5b4
---
# MCP-Auto-Start-Proxy

## Summary

Make `llm-router-mcp.exe` automatically start `llm-router.exe` detached in the background on MCP startup and self-heal on tool calls (`offload_llm`, `router_metrics`, `router_cache_purge`) so `127.0.0.1:8787` never requires manual startup.

## Context

- `cmd/router-mcp/main.go` and `internal/mcp/server.go` currently return an error if `http://127.0.0.1:8787` is not already running.
- Antigravity automatically launches `bin/llm-router-mcp.exe` from `C:\Users\Aaron\.gemini\antigravity\mcp_config.json`.

## Open questions

None

## Acceptance criteria

- [x] AC1: Cross-platform detached spawner (`proc_windows.go`, `proc_other.go`) and `EnsureProxyRunning` pass unit tests (`internal/mcp/autostart_test.go`). verified: go test -v ./internal/mcp/... passed (3/3 tests)
- [x] AC2: `cmd/router-mcp/main.go` and `internal/mcp/server.go` trigger `EnsureProxyRunning` on startup and before tool calls; `go test -v ./...` passes. verified: go test -v ./... passed
- [x] AC3: `bin/llm-router-mcp.exe` builds cleanly and live stdio `initialize` + `router_metrics` auto-starts `llm-router.exe` on `127.0.0.1:8787`. verified: bin/llm-router-mcp.exe built and auto-started llm-router.exe on 127.0.0.1:8787

## Steps

- [x] Step 1: Implement detached spawner, `EnsureProxyRunning`, startup + tool hooks, and unit tests (AC1, AC2)
- [x] Step 2: Rebuild `bin/llm-router-mcp.exe`, verify live auto-start, and complete review/PR (AC3)

## Risks

- **Windows binary lock while Antigravity runs `llm-router-mcp.exe`:** Handled by stopping any running `llm-router-mcp` process before overwriting `bin/llm-router-mcp.exe`.
- **Non-local `LLM_ROUTER_URL`:** `isLocalURL` guards against spawning a local binary when pointing at a remote URL.

## Out of scope

- Windows Service / Task Scheduler registration.

## Notes

Approved Plan:
> 1. Auto-Start on MCP Startup + Lazy Self-Heal (`internal/mcp/autostart.go` + `server.go` + `cmd/router-mcp/main.go`): resolve `bin/llm-router.exe` and `router.config.yaml` relative to `os.Executable()`, check `/healthz`, spawn detached (`CREATE_NO_WINDOW`) if down, poll until ready.
> 2. Tests & Verification: unit tests in `autostart_test.go`, rebuild `bin/llm-router-mcp.exe`, `go test -v ./...`, live end-to-end verification.
> 3. Workflow (`adw`): `feat/mcp-auto-start-proxy` -> PR #5 & merge.

## Progress log

<!-- One line per session/review, added with `ticket.mjs log` or `ticket.mjs review` -->
- 2026-11-26 21:00 UTC, Gemini (review: pass): Auto-start implementation is clean and thread-safe: windows/!windows build tags pass cross-platform go vet, startMu with double-checked isHealthy prevents duplicate spawns, isLocalURL restricts auto-start to loopback hosts, and unit tests cover healthy no-op and down-to-healthy startup. [reviewer: 5bf5585a-2374-46ee-ab3e-e29347927fdd]
