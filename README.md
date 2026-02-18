# agent-reverse-proxy

A reverse proxy that sits in front of your web app, injecting debug instrumentation and exposing [MCP](https://modelcontextprotocol.io/) tools so AI agents can inspect the running page — query DOM elements, capture console logs, errors, and network requests.

## Install

```bash
npx @choonkeat/agent-reverse-proxy --help
```

Or install globally:

```bash
npm install -g @choonkeat/agent-reverse-proxy
```

## Usage

```bash
# Proxy requests to your app on port 3000 (default)
agent-reverse-proxy

# Specify a different app port
agent-reverse-proxy --app-port 8080

# Specify the proxy port (default: 20000 + app-port)
agent-reverse-proxy --proxy-port 9000

# Plain reverse proxy, no debug script injection
agent-reverse-proxy --no-inject

# HTTP-only mode (no stdio MCP transport)
agent-reverse-proxy --no-stdio

# Custom tool prefix (default: "proxied")
agent-reverse-proxy --tool-prefix preview
```

The proxy starts on `localhost:23000` (by default) and forwards to your app on `localhost:3000`. HTML responses get a small debug script injected that captures console output, errors, and network activity.

## How it works

```
Browser ──► agent-reverse-proxy (:23000) ──► Your app (:3000)
                   │
                   ├── Injects debug script into HTML responses
                   ├── Captures console.log/warn/error/info/debug
                   ├── Captures fetch() and XMLHttpRequest
                   ├── Captures uncaught errors and promise rejections
                   ├── Tracks SPA navigation (pushState, popstate)
                   └── Exposes MCP tools for AI agents
```

## MCP tools

The proxy registers two MCP tools (prefixed with `--tool-prefix`, default `proxied`):

### `proxied_browser_snapshot`

Query DOM elements by CSS selector. Returns text content, inner HTML, visibility, and bounding rect.

```json
{ "selector": "h1" }
```

### `proxied_browser_console_messages`

Listen for console output, errors, and network requests for a specified duration (1–30 seconds).

```json
{ "duration_seconds": 5 }
```

## MCP resources

- `proxied-browser://reference` — How the proxy works: tools, message types, port configuration
- `proxied-browser://help` — Debugging workflow and tips

## Port configuration

| Environment variable | Flag | Default |
|---|---|---|
| `APP_PORT` or `PORT` | `--app-port` | 3000 |
| `PROXY_PORT` | `--proxy-port` | 20000 + app port |

## Features

- Reverse proxies HTTP and WebSocket connections
- Injects debug script into HTML responses (handles gzip-encoded responses)
- Modifies Content-Security-Policy headers to allow debug script and WebSocket
- Strips `Domain` from `Set-Cookie` headers so cookies work through the proxy
- Auto-upgrades `ws://` to `wss://` on HTTPS pages (with warning banner)
- Shows a "waiting for app" page with auto-retry when the app isn't running
- Supports both stdio and HTTP MCP transports

## License

MIT
