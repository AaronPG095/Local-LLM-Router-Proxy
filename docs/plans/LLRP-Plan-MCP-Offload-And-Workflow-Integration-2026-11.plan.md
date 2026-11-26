---
title: Plan - Feature: MCP-Offload-And-Workflow-Integration
ticket: local://LLRP:LLRP-Plan-MCP-Offload-And-Workflow-Integration-2026-11
status: in-progress
priority: 
state: 
due: 
appetite: 
version: 
impact: 
devTicketKey: LLRP:LLRP-Plan-MCP-Offload-And-Workflow-Integration-2026-11
agent: Gemini
branch: feat/mcp-offload-tool
worktree: Local-LLM-Router-Proxy
startedAt: 2026-11-26
---
# MCP-Offload-And-Workflow-Integration

## Summary

Add a pure-Go stdio MCP server (`bin/llm-router-mcp.exe`) exposing `offload_llm`, `router_metrics`, and `router_cache_purge` backed by `http://127.0.0.1:8787`, plus a `scripts/start-proxy.ps1` launcher, `docs/WORKFLOW_INTEGRATION.md`, and universal registration in `C:\Users\Aaron\.gemini\antigravity\mcp_config.json`.

## Context

- `llm-router.exe` already serves `/v1/chat/completions`, `/metrics/summary`, and `DELETE /cache` on `127.0.0.1:8787`.
- CLI tools (Codex, Claude Code, Gemini CLI) connect directly via `OPENAI_BASE_URL` / `ANTHROPIC_BASE_URL` / `GEMINI_API_BASE_URL`, whereas Antigravity IDE uses global MCP tools from `C:\Users\Aaron\.gemini\antigravity\mcp_config.json` across all repos.

## Open questions

None

## Acceptance criteria

- [x] AC1: Pure-Go stdio MCP server (`internal/mcp`, `cmd/router-mcp`) passes unit tests for `initialize`, `tools/list`, `offload_llm`, `router_metrics`, and `router_cache_purge`. verified: go test -v ./internal/mcp/... passed
- [x] AC2: `scripts/start-proxy.ps1` and `docs/WORKFLOW_INTEGRATION.md` + `README.md` document universal Antigravity MCP + Codex/Claude/Gemini CLI integration. verified: scripts/start-proxy.ps1, docs/WORKFLOW_INTEGRATION.md, and README.md added
- [x] AC3: `bin/llm-router-mcp.exe` builds cleanly, responds to live stdio `initialize` + `tools/list`, and is registered in `C:\Users\Aaron\.gemini\antigravity\mcp_config.json`. verified: bin/llm-router-mcp.exe built, stdio initialize/tools/list verified, registered in mcp_config.json
- [x] AC4: Full `go test -v ./...` and `go build` pass on branch and after merge to `main`. verified: go test -v ./... and go build passed

## Steps

- [x] Slice 1: Implement pure-Go stdio MCP server (`internal/mcp/`, `cmd/router-mcp/`) and unit tests (AC1)
- [x] Slice 2: Add `scripts/start-proxy.ps1`, `docs/WORKFLOW_INTEGRATION.md`, and update `README.md` (AC2)
- [x] Slice 3: Register MCP server globally, verify live stdio + `go test -v ./...`, and complete review/PR (AC3, AC4)

## Risks

- **Proxy not running when Antigravity calls `offload_llm`:** Mitigated by returning a clear `isError: true` message instructing to run `scripts/start-proxy.ps1`.
- **Corrupting existing `mcp_config.json`:** Mitigated by preserving `notion`, `fluentoverlay`, and `context-gatekeeper` entries untouched.

## Out of scope

- Auto-installing Windows scheduled tasks/services (user launches via `scripts/start-proxy.ps1` on demand).

## Notes

Approved MCP Offload & Workflow Integration Plan:
> 1. Workflow (`adw`): `adw new "MCP-Offload-And-Workflow-Integration"` -> `adw create --agent Gemini` -> branch `feat/mcp-offload-tool` -> `adw start` -> incremental commits + `adw tick` -> `adw pack --test` + independent review -> `adw check` -> PR #4 & merge.
> 2. Slice 1 — Pure-Go Stdio MCP Server (`internal/mcp/` + `cmd/router-mcp/main.go`) exposing `offload_llm`, `router_metrics`, `router_cache_purge`.
> 3. Slice 2 — Workflow Integration Scripts & Docs (`scripts/start-proxy.ps1`, `docs/WORKFLOW_INTEGRATION.md`, `README.md`).
> 4. Slice 3 — Live Registration in `C:\Users\Aaron\.gemini\antigravity\mcp_config.json` & Verification.

## Progress log

<!-- One line per session/review, added with `ticket.mjs log` or `ticket.mjs review` -->
