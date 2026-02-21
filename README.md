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

## Embedding as a Go library

The root package is `agentproxy` — import it to mount the proxy inside your own server.

### Fixed target (proxy all requests to one backend)

```go
package main

import (
	"log"
	"net/http"
	"net/url"

	agentproxy "agent-reverse-proxy"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func main() {
	target, _ := url.Parse("http://localhost:3000")

	// 1. Create the proxy
	proxy, err := agentproxy.New(agentproxy.Config{
		Target:      target,
		ToolPrefix:  "preview",
		ThemeCookie: "my-theme",
	})
	if err != nil {
		log.Fatal(err)
	}

	// 2. Create an MCP server and register tools + resources
	mcpServer := mcp.NewServer(&mcp.Implementation{
		Name:    "my-app",
		Version: "1.0.0",
	}, nil)
	proxy.RegisterTools(mcpServer)
	proxy.RegisterResources(mcpServer)

	// 3. Wire up all handlers
	mux := http.NewServeMux()
	mux.Handle("/mcp", proxy.MCPHandler(mcpServer)) // MCP-over-HTTP
	mux.Handle("/", proxy)                           // reverse proxy + debug endpoints

	log.Fatal(http.ListenAndServe(":8080", mux))
}
```

`proxy` (`http.Handler`) serves:

| Path | What |
|------|------|
| `/__agent-reverse-proxy-debug__/inject.js` | Debug instrumentation script |
| `/__agent-reverse-proxy-debug__/ws` | WebSocket for iframe debug clients |
| `/__agent-reverse-proxy-debug__/agent` | WebSocket for agent connections |
| `/__agent-reverse-proxy-debug__/ui` | WebSocket for UI observers |
| `/__agent-reverse-proxy-debug__/open` | Open a URL in the Preview pane |
| `/__agent-reverse-proxy-debug__/shell` | Double-iframe shell page |
| `/*` | Reverse proxy to target (with HTML injection) |

`proxy.MCPHandler(mcpServer)` (`http.Handler`) serves the StreamableHTTP MCP endpoint. Mount it wherever you want (e.g. `/mcp`, `/api/mcp`).

### Dynamic target (target URL in the request path)

Set `Target: nil` — the proxy extracts the backend from the path as `/{scheme}/{host:port}/{path...}`:

```go
proxy, _ := agentproxy.New(agentproxy.Config{
	Target:     nil, // dynamic mode
	ToolPrefix: "preview",
})

// GET /http/localhost:3000/hello → proxies to http://localhost:3000/hello
// GET /https/api.example.com:443/v1 → proxies to https://api.example.com:443/v1
```

### Mounting at a base path

Set `BasePath` to mount the proxy under a prefix. All debug endpoints and proxy routes are served relative to this path:

```go
proxy, _ := agentproxy.New(agentproxy.Config{
	BasePath:   "/preview",
	Target:     target,
	ToolPrefix: "preview",
})

mux := http.NewServeMux()
mux.Handle("/preview/", proxy) // note: trailing slash to match sub-paths
// Now:
//   /preview/              → proxied to target /
//   /preview/hello         → proxied to target /hello
//   /preview/__agent-reverse-proxy-debug__/inject.js → debug script
//   /preview/mcp           → NOT handled (mount MCPHandler separately if needed)
```

The injected `<script src>` tag and the WebSocket URL in `inject.js` automatically include the base path — no extra configuration needed.

### API summary

```go
// Create a proxy instance
proxy, err := agentproxy.New(cfg agentproxy.Config) (*agentproxy.Proxy, error)

// http.Handler — serves reverse proxy + all debug endpoints
proxy.ServeHTTP(w, r)

// Register MCP tools (browser_snapshot, browser_console_messages)
proxy.RegisterTools(mcpServer *mcp.Server)

// Register MCP resources (reference, help)
proxy.RegisterResources(mcpServer *mcp.Server)

// Returns an http.Handler for the MCP-over-HTTP endpoint
proxy.MCPHandler(mcpServer *mcp.Server) http.Handler

// Access the DebugHub for direct message routing
proxy.Hub() *agentproxy.DebugHub
```

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
