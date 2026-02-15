# Debug with App Preview

Use these MCP tools to inspect the App Preview panel and receive console logs, errors, and network requests from the user's browser.

## Prerequisites

- User must have the App Preview panel open (right side of terminal UI)
- App must be running on `$PORT` (check with `echo $PORT`)

## MCP Tools

### Query DOM Elements

Use `{{snapshot_tool}}` to query a specific element by CSS selector:

```
{{snapshot_tool}}(selector: "h1")
{{snapshot_tool}}(selector: ".error-message")
{{snapshot_tool}}(selector: "#submit-btn")
{{snapshot_tool}}(selector: "[data-testid='login-form']")
```

Response:
```json
{"t":"queryResult","found":true,"text":"Page Title","html":"<h1>Page Title</h1>","visible":true,"rect":{"x":0,"y":0,"width":500,"height":50}}
```

If not found:
```json
{"t":"queryResult","found":false}
```

### Listen for Console & Network Activity

Use `{{console_tool}}` to capture console logs, errors, and network requests for a specified duration:

```
{{console_tool}}(duration_seconds: 5)
```

Returns JSON messages collected during the listening period:
```json
{"t":"console","m":"log","args":["Hello!",{"data":123}],"ts":...}
{"t":"console","m":"warn","args":["Warning message"],"ts":...}
{"t":"console","m":"error","args":["Error occurred"],"ts":...}
{"t":"error","msg":"Uncaught TypeError: ...","stack":"...","ts":...}
{"t":"fetch","url":"/api/users","method":"GET","status":200,"ms":45,"ts":...}
{"t":"xhr","url":"/api/data","method":"POST","status":500,"ms":120,"ts":...}
```

Navigation events:
```json
{"t":"urlchange","url":"http://localhost:3000/about","ts":...}
{"t":"navstate","canGoBack":true,"canGoForward":false}
```

## Workflow

1. **Start your app** on `$PORT` (e.g., `python3 -m http.server "$PORT"`)
2. **Ask the user** to open the Preview tab in the right panel
3. **Use `{{snapshot_tool}}`** to query DOM elements and see what's on the page
4. **Use `{{console_tool}}`** to monitor console output, errors, and network requests
5. **Fix issues** based on what you observe, then ask the user to reload

## Tips

- Prefer `{{snapshot_tool}}` (DOM query) for quick page inspection — it returns immediately
- Use `{{console_tool}}` when you need to capture activity over time (e.g., trigger an action then see what happens)
- Start with short durations (2-5 seconds) for `{{console_tool}}`
- DOM queries return the FIRST matching element only
- The `visible` field in query results indicates if element is in viewport
- Network requests show timing (`ms` field) — useful for performance debugging
