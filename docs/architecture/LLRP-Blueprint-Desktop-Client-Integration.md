---
title: Blueprint: Desktop Client Integration
status: active
plannerModel: gemini-4-neon-medium
created: 2026-11-27
---
# Architecture Blueprint: Desktop Client Integration

## Goal & Non-Goals

Make desktop router startup repeatable across client interfaces. Keep the existing Windows-encrypted credentials, and give each client a documented and tested way to use the router. Cloud-hosted client interception and vault migration are outside this blueprint.

## Architecture & Boundaries

The Windows user owns the encrypted store. The router decrypts the keys it needs at startup and serves its existing loopback HTTP interfaces. MCP clients start or reuse the router through the existing local MCP binary. CLI and SDK clients use their supported API base URL and authentication settings.

## Contracts & Invariants

Support the existing DPAPI-encrypted credential file and explicit environment-based credential injection. Keep key material out of repository files, client registrations, diagnostics, and test fixtures. Maintain existing routing, budget, streaming, cache, and privacy behavior. Treat MCP offload and native protocol routing as separate client entry points with separate verification.

## PR Slice Breakdown

1. Shared Desktop Credentials. Version the Windows credential setup and move store decryption into the router startup path. Verify rotation, cold start, reuse, and behavior with absent or unusable stores.
2. Desktop Client Compatibility. Verify Codex CLI, Claude Code, Gemini CLI and OpenAI-compatible requests against mock upstreams. Validate the Codex and Antigravity IDE MCP paths and publish a client setup matrix.

## Living Updates & ADRs

Shared Desktop Credentials is the first implementation slice. A portable vault can supply the router's existing environment variables. Choosing or migrating to a vault is a separate decision.
