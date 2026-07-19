# agent-reverse-proxy

## Testing

Always use `make test` to run tests. Never run `go test` or `go vet` directly.

After any change to `cmd/example/` (main.go or content files), run `make example-run EXAMPLE_PORT=3003` to verify all 11 Playwright steps pass. This is the end-to-end conformance test for the proxy.

## Verification checklist

Before considering a change complete:

1. `make test` — Go unit tests and vet must pass
2. `make example-run EXAMPLE_PORT=3003` — all 11 example app steps must pass (if `cmd/example/` was touched)

## Example app

- HTML/CSS/JS content lives in `cmd/example/content/` and is embedded at compile time via `//go:embed`
- `main.go` handles routing and server-side logic (cookies, redirects, templating); content files are served via `mustRead()`
- Steps with server-side logic: step 5 (set cookie), step 6 (read cookie, two HTML variants), step 7 (POST + redirect), step 11 (template with `{{COOKIE_STATUS}}`)
- Every step's Next button is gated — Playwright cannot advance past a broken step

### Make targets

| Target | Purpose |
|--------|---------|
| `make example-serve` | Build and run the server (blocks; for manual testing in a browser) |
| `make example-test` | Run Playwright tests against an already-running server |
| `make example-run` | Build, start server, run tests, stop server (CI) |

All accept `EXAMPLE_PORT` (default 9876) and `TARGET_URL`.
See .swe-swe/docs/AGENTS.md (if it exists) for context of this current environment
