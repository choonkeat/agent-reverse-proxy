# ADR-001: Selective Routing and Nav Message Consolidation

**Status:** Accepted
**Date:** 2026-02-21
**Commits:** `4c17887`, `4d0879b`

## Context

A tdspec audit (`/workspace/tdspec/src/DebugHub.elm`, `DebugProtocol.elm`) found two places where the code diverged from the spec's cleaner design:

1. **DebugHub broadcast everything to everyone.** The code used a single `iframeClients` pool and `ForwardToIframes()` which sent all commands to both shell page and inject.js. The tdspec shows selective routing: Navigate/Reload to shell page only, Query to inject.js only.

2. **Both shell page and inject.js sent nav messages (`init`/`urlchange`/`navstate`) directly to the hub.** The tdspec's `DebugProtocol.elm` classifies these as `ShellPageDebugMsg` — messages that only the shell page should send. In reality, inject.js sent them for SPA navigations (pushState/popstate/hashchange) while the shell page sent them for full-page navigations (inner.onload). Neither was redundant, but having two sources of nav messages on two different WS connections complicated routing and made the protocol ambiguous.

## Decision

### Part 1: Selective Routing

Split `iframeClients` into two pools (`shellClients`, `injectClients`) distinguished by a `?role=` query parameter on the `/ws` WebSocket endpoint:

- Shell page connects with `?role=shell` (default for backward compat)
- inject.js connects with `?role=inject`

Add `RouteCommand(msg)` which parses the `t` field and routes:
- `navigate`, `reload` → `ForwardToShellClients` only
- `query` → `ForwardToInjectClients` only
- unknown → both pools

The UI observer handler (`handleDebugUIObserverWS`) now calls `RouteCommand` instead of `ForwardToIframes`. The agent handler still uses `ForwardToIframes` (backward compat — agent commands are rare and may target either pool).

`SendQuery` (used by MCP tools) now calls `ForwardToInjectClients` directly.

### Part 2: Nav Message Consolidation

inject.js no longer sends `init`/`urlchange`/`navstate` directly over its WS connection. Instead it uses `window.parent.postMessage({__arpNav: true, t: '...', ...}, '*')` to relay these to the shell page.

The shell page adds a `message` event listener that relays `__arpNav`-marked postMessages to the hub over its own WS connection. This makes the shell page the single source of all nav messages, matching the tdspec's `ShellPageDebugMsg` type.

inject.js keeps direct WS sends for telemetry messages: `console`, `error`, `rejection`, `fetch`, `xhr`, `ws-upgrade`, `queryResult`.

## Consequences

- **Matches tdspec:** The code now implements the routing rules described in `DebugHub.elm` and the message ownership described in `DebugProtocol.elm`.
- **Prepares for library embedding:** When agent-reverse-proxy is embedded as a library inside swe-swe-server with path-based routing, having distinct client pools simplifies routing through the parent server's multiplexer.
- **No wire format changes:** The JSON messages on the wire are identical. The only new parameter is `?role=shell|inject` on the WS URL.
- **Backward compat preserved:** `AddIframeClient`/`RemoveIframeClient`/`ForwardToIframes` still exist as wrappers. Unrecognized roles default to the shell pool.
