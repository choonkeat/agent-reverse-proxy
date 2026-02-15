# App Preview

The App Preview shows the user's web app through a reverse proxy that injects debug instrumentation.

## How It Works

The `agent-reverse-proxy` binary runs inside the container as an MCP server. It:

1. Starts an HTTP reverse proxy on `$PROXY_PORT` (default: `20000 + $PORT`)
2. Forwards all requests to the user's app on `$PORT` (default: 3000)
3. Injects a debug script into HTML responses
4. Exposes two MCP tools for inspecting the app

Start the user's app on the assigned `$PORT`:

```bash
echo $PORT  # See your assigned port

# Example: Start a Python HTTP server
python3 -m http.server "$PORT"

# Example: Start a Node.js app
PORT=$PORT npm start
```

If no server is running on `$PORT`, the proxy shows a "Waiting for App" page with auto-retry.

## MCP Tools

### `{{prefix}}_browser_snapshot`

Capture a snapshot of the Preview content by CSS selector. Returns the text, HTML, and visibility of matching elements.

**This is the correct tool for inspecting the Preview** — `browser_snapshot` cannot see Preview content because it runs inside an iframe.

```
selector: "h1"           → Get the first <h1> element
selector: ".error"       → Get elements with class "error"
selector: "#app"         → Get the element with id "app"
```

Response:
```json
{"t":"queryResult","found":true,"text":"Page Title","html":"<h1>Page Title</h1>","visible":true,"rect":{"x":0,"y":0,"width":100,"height":50}}
```

### `{{prefix}}_browser_console_messages`

Listen for console logs, errors, and network requests from the Preview for a specified duration.

**This is the correct tool for debugging the Preview** — `browser_console_messages` cannot see Preview output.

```
duration_seconds: 5    → Listen for 5 seconds (default)
duration_seconds: 15   → Listen longer for intermittent issues
```

Returns newline-delimited JSON messages:

```json
{"t":"console","m":"log","args":["App started"],"ts":1234567890}
{"t":"error","msg":"Uncaught TypeError: ...","stack":"...","ts":1234567890}
{"t":"fetch","url":"/api/data","method":"GET","status":200,"ok":true,"ms":42,"ts":1234567890}
```

## Message Types

| Type | Field `t` | Description |
|------|-----------|-------------|
| Page load | `init` | Sent when page loads, includes URL |
| URL change | `urlchange` | SPA navigation (pushState, replaceState, popstate, hashchange) |
| Nav state | `navstate` | Back/forward button availability (`canGoBack`, `canGoForward`) |
| Console | `console` | Console.log/warn/error/info/debug output |
| Error | `error` | Uncaught exceptions with stack trace |
| Promise rejection | `rejection` | Unhandled promise rejections |
| Fetch | `fetch` | fetch() requests with status and timing |
| XHR | `xhr` | XMLHttpRequest with status and timing |
| Query result | `queryResult` | Response to DOM query |

## Port Configuration

Each session gets its own `PORT` (default range 3000-3019). The proxy port is `20000 + PORT`:

- PORT=3000 → Proxy on 23000
- PORT=3005 → Proxy on 23005

## Limitations

- Only works for web apps served through the App Preview (the session's `$PORT`)
- The user must have the preview open in their browser for messages to flow
- DOM queries return the first matching element only
- No request/response body capture (only metadata)
