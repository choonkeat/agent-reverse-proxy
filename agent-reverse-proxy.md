# agent-reverse-proxy: MCP Server Spec

A standalone MCP server that replaces `swe-swe-server --mcp` (the `swe-swe-preview` MCP server). It provides the agent with **app preview debugging tools** while simultaneously running an **HTTP reverse proxy** that injects debug instrumentation into the user's web app.

## Motivation

Currently, `swe-swe-server --mcp` is a mode of the monolithic `swe-swe-server` binary (Go, ~5000 lines). In `--mcp` mode, it only uses ~200 lines: a thin MCP stdio wrapper that connects to a WebSocket debug endpoint on the host's preview proxy. This means:

1. The full `swe-swe-server` binary must exist in the container just for 2 MCP tools
2. The host must run a separate preview proxy (with DebugHub, JS injection, reverse proxying)
3. The architecture is split: proxy logic on host, MCP logic in container, with WebSocket bridging them

The `agent-chat` package already proves a better pattern: a standalone MCP stdio server that also starts its own HTTP server. This spec applies that same pattern to the preview proxy.

## Architecture

### Current (to be replaced)

```
Container                          Host
┌─────────────────┐     WS        ┌────────────────────┐
│ Agent (Claude)   │               │ swe-swe-server     │
│       ↓ stdio    │               │                    │
│ swe-swe-server   │──────────────→│ Preview Proxy      │
│   --mcp          │  /agent       │ :20000+PORT        │
│ (thin WS client) │               │ ├─ DebugHub        │
└─────────────────┘               │ ├─ inject.js       │──→ User's App :PORT
                                   │ └─ reverse proxy   │
                Browser ──────────→│    (HTML inject)   │
                                   └────────────────────┘
```

### Proposed

```
Container
┌─────────────────────────────────────────┐
│ Agent (Claude)                          │
│       ↓ stdio MCP                       │
│ agent-reverse-proxy                     │
│ ├─ MCP tools (debug_preview, listen)    │
│ ├─ HTTP server on $PORT proxy port      │
│ │   ├─ reverse proxy → app :$APP_PORT   │
│ │   ├─ inject.js (served + injected)    │
│ │   ├─ DebugHub (3 WS endpoints)        │
│ │   └─ error page when app not running  │
│ └─ In-process debug (no WS to self)     │
└─────────────────────────────────────────┘
         ↑
    Browser (user)
```

**Key change**: The MCP server and the reverse proxy are the **same process**. DOM queries go directly to the in-process DebugHub instead of requiring a WebSocket round-trip to the host.

## MCP Interface

### Transport

- **Primary**: stdio (JSON-RPC over stdin/stdout, like today)
- **Secondary**: StreamableHTTP at `/mcp` on the HTTP server (optional, for future use)

### Server Info

```json
{
  "name": "agent-reverse-proxy",
  "version": "0.1.0"
}
```

### Tools

#### `browser_debug_preview`

Capture a snapshot of the Preview content by CSS selector.

**Input Schema:**
```json
{
  "type": "object",
  "properties": {
    "selector": {
      "type": "string",
      "description": "CSS selector (e.g. 'h1', '.error-message', '#app')"
    }
  },
  "required": ["selector"]
}
```

**Behavior:**
1. Send `{"t":"query","id":"<unique>","selector":"<selector>"}` to connected iframe WS clients via DebugHub
2. Wait up to 5 seconds for a `queryResult` response
3. Return the result as text content

**Response format (from inject.js):**
```json
{
  "t": "queryResult",
  "id": "<query-id>",
  "found": true,
  "text": "Hello World",
  "html": "<h1>Hello World</h1>",
  "visible": true,
  "rect": {"x": 0, "y": 0, "width": 100, "height": 50}
}
```

#### `browser_debug_preview_listen`

Listen for console logs, errors, and network requests from the Preview.

**Input Schema:**
```json
{
  "type": "object",
  "properties": {
    "duration_seconds": {
      "type": "number",
      "description": "How long to listen (default: 5, max: 30)"
    }
  }
}
```

**Behavior:**
1. Subscribe to DebugHub messages for the specified duration
2. Collect all messages forwarded from iframe clients (console, errors, fetch, XHR, etc.)
3. Return collected messages as newline-delimited JSON

**Message types from inject.js:**
- `{"t":"console","m":"log|warn|error|info|debug","args":[...],"ts":...}`
- `{"t":"error","msg":"...","file":"...","line":...,"col":...,"stack":"...","ts":...}`
- `{"t":"rejection","reason":"...","ts":...}`
- `{"t":"fetch","url":"...","method":"GET","status":200,"ok":true,"ms":42,"ts":...}`
- `{"t":"xhr","url":"...","method":"GET","status":200,"ok":true,"ms":42,"ts":...}`
- `{"t":"urlchange","url":"...","ts":...}`
- `{"t":"navstate","canGoBack":false,"canGoForward":false}`

## HTTP Server

The HTTP server runs on a port determined by environment variables, providing the reverse proxy and debug infrastructure.

### Port Selection

```
if $PROXY_PORT is set → use it
else if $PORT is set  → use 20000 + $PORT
else                  → use random available port
```

The user's app is expected to run on `$APP_PORT` (or `$PORT` if `$APP_PORT` is not set).

**Note**: The exact port strategy should match what swe-swe-server expects. Currently swe-swe-server starts the preview proxy on `20000 + previewPort` and the app is expected on `previewPort`. The new binary should be compatible with this scheme, or the port mapping in swe-swe-server should be updated to match.

### Routes

| Path | Handler |
|------|---------|
| `/__swe-swe-debug__/inject.js` | Serve the debug instrumentation script |
| `/__swe-swe-debug__/ws` | WebSocket: iframe debug script connections |
| `/__swe-swe-debug__/agent` | WebSocket: agent connections (for backward compat) |
| `/__swe-swe-debug__/ui` | WebSocket: UI observer connections |
| `/__swe-swe-debug__/open` | HTTP: open a URL in the Preview pane |
| `/__swe-swe-shell__` | Serve the double-iframe shell page |
| `/*` | Reverse proxy to user's app |

### Reverse Proxy Behavior

1. **HTTP requests**: Forward to `http://localhost:$APP_PORT`, preserving method, headers, body
2. **HTML responses**: Inject `<script src="/__swe-swe-debug__/inject.js"></script>` after first `<head>` or `<body>` tag
3. **Gzip responses**: Decompress, inject, serve uncompressed (update Content-Length, remove Content-Encoding)
4. **Brotli responses**: Pass through unchanged (no injection)
5. **CSP headers**: Modify to allow debug script (`script-src 'self'`) and WebSocket (`connect-src 'self' ws: wss:`)
6. **Set-Cookie**: Strip `Domain` attribute so cookies apply to proxy domain
7. **WebSocket upgrade**: Relay raw bytes between client and backend (TCP-level proxy)
8. **App not running**: Return an HTML error page with auto-polling (checks every 3 seconds, reloads on 200)

### DebugHub

The DebugHub manages WebSocket connections between three parties:

```
Iframe Clients (inject.js in browser)
       ↕ messages
    DebugHub ←→ Agent (MCP tool calls, in-process)
       ↓ messages (read-only)
  UI Observers (terminal UI)
```

**Message flow:**
- **Iframe → DebugHub**: forwarded to Agent + all UI Observers
- **Agent → DebugHub**: forwarded to all Iframe Clients
- **UI Observer → DebugHub**: forwarded to all Iframe Clients (e.g., navigate commands)

**Connection rules:**
- Multiple iframe clients allowed (one per iframe/page)
- Only one agent connection at a time (new replaces old)
- Multiple UI observers allowed

**In-process optimization**: Since the MCP server and DebugHub are in the same process, `browser_debug_preview` and `browser_debug_preview_listen` can call DebugHub methods directly (no WebSocket self-connection needed). The `/agent` WebSocket endpoint should still exist for backward compatibility or external tools.

## Inject Script (`inject.js`)

The debug instrumentation script is injected into every HTML response. It:

1. **Captures console**: Wraps `console.log/warn/error/info/debug` to forward calls via WebSocket
2. **Captures errors**: Listens for `window.error` and `unhandledrejection` events
3. **Captures network**: Wraps `fetch()` and `XMLHttpRequest` to log request/response metadata
4. **Handles DOM queries**: Responds to `{"t":"query"}` messages with `querySelector` results
5. **Tracks navigation**: Wraps `history.pushState/replaceState`, listens for `popstate/hashchange`
6. **Reconnects**: Auto-reconnects WebSocket with exponential backoff (max 5 attempts)
7. **Queues messages**: Buffers up to 100 messages when WebSocket is disconnected

**Source**: The inject.js from the current `swe-swe-server` should be extracted as-is.

## Shell Page (`/__swe-swe-shell__`)

A wrapper HTML page with a double-iframe architecture:
- Outer page connects to `/__swe-swe-debug__/ws` as an iframe client
- Inner iframe loads the user's app content
- Handles navigation commands (`back`, `forward`, `reload`) from the UI via WebSocket
- Tracks navigation state at shell level for full-page (non-SPA) navigations

## Environment Variables

| Variable | Description | Default |
|----------|-------------|---------|
| `PORT` | Port the user's app listens on | 3000 |
| `APP_PORT` | Explicit app port (overrides PORT for app target) | same as PORT |
| `PROXY_PORT` | Port for the reverse proxy HTTP server | 20000 + PORT |

## CLI Flags

```
agent-reverse-proxy [flags]

Flags:
  --app-port int     Port of the user's app (default: $APP_PORT or $PORT or 3000)
  --proxy-port int   Port for the proxy HTTP server (default: $PROXY_PORT or 20000+app-port)
  --no-inject        Disable debug script injection (plain reverse proxy)
  --no-stdio         Disable stdio MCP transport (HTTP MCP only)
```

## MCP Configuration

In the container's `.mcp.json`:

```json
{
  "mcpServers": {
    "swe-swe-preview": {
      "command": "agent-reverse-proxy",
      "args": []
    }
  }
}
```

Or via npx:

```json
{
  "mcpServers": {
    "swe-swe-preview": {
      "command": "npx",
      "args": ["-y", "@choonkeat/agent-reverse-proxy"]
    }
  }
}
```

## Implementation Notes

### Language Choice

Go is recommended (matching `agent-chat`'s pattern of a Go binary with `npx` wrapper). The inject.js and shell page HTML are embedded as string constants.

### Key Dependencies

- `github.com/modelcontextprotocol/go-sdk/mcp` — MCP protocol handling
- `github.com/gorilla/websocket` — WebSocket for DebugHub
- Standard library `net/http` — reverse proxy and HTTP server

### What to Extract from swe-swe-server

The following code from `swe-swe-server/main.go` should be extracted:

1. **DebugHub** (lines ~1434-1548): WebSocket connection management
2. **debugInjectJS** (lines ~1631-1900): The inject.js script constant
3. **shellPageHTML** (lines ~1282-1386): The shell page HTML
4. **injectDebugScript** (lines ~1394-1406): HTML injection logic
5. **modifyCSPHeader** (lines ~1408-1432): CSP header modification
6. **handleProxyRequest** (lines ~2170-2240): Reverse proxy handler
7. **processProxyResponse** (lines ~2242-2317): Response processing with injection
8. **handleWebSocketRelay** (lines ~2323+): WebSocket relay for app WS connections
9. **previewProxyErrorPage** (lines ~1163-1280): Error page HTML
10. **MCP tool definitions and handlers** (lines ~2463-2636): browser_debug_preview, browser_debug_preview_listen

### What Changes in swe-swe-server

Once `agent-reverse-proxy` is deployed:

1. Remove `--mcp` mode from swe-swe-server
2. Remove host-side `startPreviewProxy`, `acquirePreviewProxyServer`, `releasePreviewProxyServer`
3. Remove host-side `previewProxyPort()` function and port 20000+ mapping
4. Update `.mcp.json` template to use `agent-reverse-proxy` instead of `swe-swe-server --mcp`
5. The container's `PORT` env var continues to tell the app which port to use
6. swe-swe-server only needs to know the proxy port for embedding the preview iframe

### Testing Strategy

1. **Unit tests**: MCP protocol handling, HTML injection, CSP modification, DebugHub message routing
2. **Integration tests**: Start proxy, start a simple HTTP app, verify injection and debug queries work
3. **Golden tests**: Update swe-swe golden tests for new `.mcp.json` configuration
