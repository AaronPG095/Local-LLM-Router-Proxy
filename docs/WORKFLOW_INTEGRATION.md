# Desktop client integration

The router runs once on `127.0.0.1:8787` and can serve several local clients.

## 1. Start the local proxy

From the router repository:

```powershell
.\scripts\start-proxy.ps1
```

The script reuses an existing healthy router. If none is running, it starts the binary with `router.config.yaml`.

### Windows credentials

Run `.\scripts\setup-credentials.ps1` to enter provider keys into masked prompts. Each accepted key is saved using Windows DPAPI in `%LOCALAPPDATA%\LocalLLMRouter\credentials.json`. This is the same store used by the earlier desktop setup. Press Enter to retain an existing key or skip an unused provider. After a rotation, restart the router to load the change.

The router loads that store once as it starts, including when started by either client's MCP server. Environment variables take precedence, which also supports vault launchers and non-Windows environments. Use `-credential-store <path>` or `LLM_ROUTER_CREDENTIAL_STORE` to specify an alternate Windows store. Windows DPAPI files are tied to the Windows user and machine.

Only run trusted router configurations. The router resolves saved keys by `api_key_env` and sends the resolved key to that provider's configured `base_url`. For mock upstreams, use a dedicated dummy environment variable or set an explicit test credential store.

## 2. MCP offload in Codex and Antigravity

Register the built MCP executable as a stdio server. It starts the local proxy if needed. Both clients expose the same tools, including `offload_llm` and `router_metrics`. Use `privacy: "local"` when calling `offload_llm` to keep that task on Ollama.

In Codex, run this command from the router repository:

```powershell
codex mcp add llm-router -- (Resolve-Path .\bin\llm-router-mcp.exe).Path
```

In Antigravity, open **MCP Servers → Manage MCP Servers → View raw config**. Add the entry below to the existing `mcpServers` object and substitute your absolute path. The installed client determines the config location. [Antigravity MCP documentation](https://antigravity.google/docs/ide-mcp)

```json
{
  "mcpServers": {
    "llm-router": {
      "command": "C:\\path\\to\\Local-LLM-Router-Proxy\\bin\\llm-router-mcp.exe",
      "args": []
    }
  }
}
```

Start a fresh client session after changing MCP settings. Call `router_metrics` to verify the connection without generating a cloud request. Agent chat still uses the provider selected by the client. CLI generation can use the router explicitly.

## 3. Route CLI and SDK generation

| Client | Router endpoint | Configuration |
| --- | --- | --- |
| Codex CLI | `POST /v1/responses` | Custom provider with a Responses base URL ending in `/v1`. |
| Claude Code | `POST /v1/messages` | `ANTHROPIC_BASE_URL` points to the router origin. Use an Anthropic API key for API-key mode. |
| Gemini CLI | `POST /v1beta/models/<model>:generateContent` or `:streamGenerateContent?alt=sse` | Select Gemini API-key authentication and set `GOOGLE_GEMINI_BASE_URL` to the router origin. |
| OpenAI-compatible SDK | `POST /v1/chat/completions` | Point the SDK at `http://127.0.0.1:8787/v1` and request Chat Completions. |

For Codex CLI, point a custom Responses provider at loopback. The router can supply its stored OpenAI key when the client omits authorization. Select an API model available to your OpenAI project. [Codex custom provider settings](https://learn.chatgpt.com/docs/config-file/config-advanced#custom-model-providers)

```powershell
codex -c 'model_provider="local-router"' `
      -c 'model_providers.local-router.name="Local LLM Router"' `
      -c 'model_providers.local-router.base_url="http://127.0.0.1:8787/v1"' `
      -c 'model_providers.local-router.wire_api="responses"'
```

For Claude Code, Gemini CLI, or an SDK, set the relevant variables in the shell that launches the client. If the client requires a provider API key, supply the actual key for that provider. The router forwards existing provider authentication on native pass-through requests. [Claude Code gateways](https://code.claude.com/docs/en/llm-gateway) · [Gemini CLI configuration](https://geminicli.com/docs/reference/configuration/)

```powershell
# OpenAI SDKs using Chat Completions
$env:OPENAI_BASE_URL = "http://127.0.0.1:8787/v1"

# Claude Code in API-key mode
$env:ANTHROPIC_BASE_URL = "http://127.0.0.1:8787"

# Gemini CLI using Gemini API-key authentication
$env:GOOGLE_GEMINI_BASE_URL = "http://127.0.0.1:8787"
$env:GOOGLE_GENAI_API_VERSION = "v1beta"
```

The table covers generation endpoints. Clients that require model discovery, token-count endpoints, managed sign-in, or WebSocket generation may need their normal provider connection or a separate integration. Check `X-Router-Provider` on an uncached response to verify routing. For local-only OpenAI Responses requests, send `X-Router-Privacy: local` and check for `ollama`.
