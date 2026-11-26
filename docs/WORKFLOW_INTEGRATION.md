# Universal Workflow Integration Guide

`Local-LLM-Router-Proxy` runs once on `127.0.0.1:8787` and serves **every repository** on your machine.

---

## 1. Start the Local Proxy
From this repository (or any terminal):

```powershell
.\scripts\start-proxy.ps1
```

If `llm-router.exe` is already listening on `http://127.0.0.1:8787/healthz`, the script exits immediately; otherwise it starts the proxy in the background using `router.config.yaml`.

---

## 2. Antigravity IDE (Universal Across All Repos)
Antigravity loads MCP servers globally from `C:\Users\Aaron\.gemini\antigravity\mcp_config.json`.
Register `bin\llm-router-mcp.exe` once:

```json
{
  "mcpServers": {
    "llm-router": {
      "command": "C:\\Users\\Aaron\\Desktop\\CODING\\Local-LLM-Router-Proxy\\bin\\llm-router-mcp.exe",
      "args": []
    }
  }
}
```

- **Universal availability:** Every workspace/repo you open in Antigravity automatically has `offload_llm`, `router_metrics`, and `router_cache_purge`.
- **Single-point updates:** Whenever you update this repo and rebuild `bin/llm-router-mcp.exe` (`go build -o bin/llm-router-mcp.exe ./cmd/router-mcp`), all repos pick up the updated binary automatically.

---

## 3. CLI Coding Agents (Codex, Claude Code, Gemini CLI)
External CLIs and SDKs do **not** need the MCP server — point their base URL env var at `http://127.0.0.1:8787` (for example in your PowerShell `$PROFILE`):

```powershell
# Codex CLI / OpenAI SDKs
$env:OPENAI_BASE_URL = "http://127.0.0.1:8787/v1"

# Claude Code / Anthropic SDKs
$env:ANTHROPIC_BASE_URL = "http://127.0.0.1:8787"

# Gemini CLI / Google GenAI SDKs
$env:GEMINI_API_BASE_URL = "http://127.0.0.1:8787"
```

All requests from every repo are routed, cached, budget-checked, and logged to `data/router.db` and `data/telemetry.jsonl` (for `TokenHUD`).
