package agentproxy

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"strings"

	"github.com/gorilla/websocket"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// ProxyVersion is the version of the agent-reverse-proxy.
const ProxyVersion = "0.2.3"

var upgrader = websocket.Upgrader{
	ReadBufferSize:  1024,
	WriteBufferSize: 1024,
	CheckOrigin:     func(r *http.Request) bool { return true },
}

// Config configures a reverse proxy instance.
type Config struct {
	// BasePath is the URL path prefix where this proxy is mounted.
	// Default: "/" (root, current behavior).
	BasePath string

	// Target is the fixed backend URL. When set, all requests proxy to this target.
	// When nil, dynamic mode: target URL is extracted from the request path.
	Target *url.URL

	// NoInject disables debug script injection (plain reverse proxy).
	NoInject bool

	// ToolPrefix for MCP tool names (e.g. "preview" → "preview_browser_snapshot").
	ToolPrefix string

	// ThemeCookie is the cookie name for light/dark theme on the error page.
	ThemeCookie string
}

// Proxy is an HTTP handler that reverse-proxies requests with optional
// debug script injection and MCP tool support.
type Proxy struct {
	config     Config
	basePath   string // normalized (no trailing slash, or "" for root)
	hub        *DebugHub
	mux        *http.ServeMux
	toolPrefix ToolPrefix
}

// New creates a new Proxy. Returns error if config is invalid.
func New(cfg Config) (*Proxy, error) {
	prefix, err := NewToolPrefix(cfg.ToolPrefix)
	if err != nil {
		return nil, fmt.Errorf("tool-prefix: %w", err)
	}

	// Normalize base path: strip trailing slash, "" for root
	bp := strings.TrimRight(cfg.BasePath, "/")
	if bp == "" || bp == "/" {
		bp = ""
	}

	hub := NewDebugHub()

	p := &Proxy{
		config:     cfg,
		basePath:   bp,
		hub:        hub,
		mux:        http.NewServeMux(),
		toolPrefix: prefix,
	}

	// Register debug endpoints on internal mux
	p.mux.HandleFunc("/__agent-reverse-proxy-debug__/inject.js", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/javascript")
		w.Write([]byte(debugInjectJS))
	})

	p.mux.HandleFunc("/__agent-reverse-proxy-debug__/ws", handleDebugIframeWS(hub))
	p.mux.HandleFunc("/__agent-reverse-proxy-debug__/agent", handleDebugAgentWS(hub))
	p.mux.HandleFunc("/__agent-reverse-proxy-debug__/ui", handleDebugUIObserverWS(hub))

	p.mux.HandleFunc("/__agent-reverse-proxy-debug__/open", func(w http.ResponseWriter, r *http.Request) {
		rawURL := r.URL.Query().Get("url")
		if rawURL == "" {
			http.Error(w, "missing url parameter", http.StatusBadRequest)
			return
		}
		msg, _ := json.Marshal(map[string]string{"t": "open", "url": rawURL})
		hub.SendToUIObservers(msg)
		log.Printf("[DebugHub] open → %s", rawURL)
		w.WriteHeader(http.StatusOK)
	})

	p.mux.HandleFunc("/__agent-reverse-proxy-debug__/shell", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		html := shellPageHTML
		if bp != "" {
			html = strings.ReplaceAll(html,
				"/__agent-reverse-proxy-debug__/",
				bp+"/__agent-reverse-proxy-debug__/")
			// Inject base path so inner iframe src and navigate commands
			// resolve relative to the proxy, not the host server root
			html = strings.Replace(html,
				"var _basePath = '';",
				"var _basePath = '"+bp+"';", 1)
		}
		fmt.Fprintf(w, html)
	})

	// Reverse proxy catch-all
	p.mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if cfg.Target != nil {
			// Fixed target mode — use basePath as prefix for URL rewriting
			appAddr := cfg.Target.Host
			handleProxyRequest(cfg.Target, appAddr, cfg.NoInject, cfg.ThemeCookie, p.scriptTag(), bp)(w, r)
		} else {
			// Dynamic target mode
			p.handleDynamicProxy(w, r)
		}
	})

	return p, nil
}

// ServeHTTP implements http.Handler.
func (p *Proxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// Add X-Agent-Reverse-Proxy and CORS headers
	w.Header().Set("X-Agent-Reverse-Proxy", ProxyVersion)
	if origin := r.Header.Get("Origin"); origin != "" {
		w.Header().Set("Access-Control-Allow-Origin", origin)
		w.Header().Set("Access-Control-Allow-Credentials", "true")
		w.Header().Set("Access-Control-Allow-Methods", "GET, HEAD, OPTIONS")
		w.Header().Set("Access-Control-Expose-Headers", "X-Agent-Reverse-Proxy")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
	}

	// Strip base path prefix
	if p.basePath != "" {
		if !strings.HasPrefix(r.URL.Path, p.basePath+"/") && r.URL.Path != p.basePath {
			http.NotFound(w, r)
			return
		}
		r.URL.Path = strings.TrimPrefix(r.URL.Path, p.basePath)
		if r.URL.Path == "" {
			r.URL.Path = "/"
		}
		r.URL.RawPath = ""
		// Also strip from RequestURI for dynamic mode
		if strings.HasPrefix(r.RequestURI, p.basePath) {
			r.RequestURI = strings.TrimPrefix(r.RequestURI, p.basePath)
			if r.RequestURI == "" {
				r.RequestURI = "/"
			}
		}
	}

	p.mux.ServeHTTP(w, r)
}

// Hub returns the DebugHub for external use.
func (p *Proxy) Hub() *DebugHub {
	return p.hub
}

// RegisterTools registers MCP tools on an external MCP server.
func (p *Proxy) RegisterTools(server *mcp.Server) {
	registerTools(server, p.hub, p.toolPrefix)
}

// RegisterResources registers MCP resources on an external MCP server.
func (p *Proxy) RegisterResources(server *mcp.Server) {
	registerResources(server, p.toolPrefix)
}

// MCPHandler returns an HTTP handler for the MCP endpoint.
func (p *Proxy) MCPHandler(server *mcp.Server) http.Handler {
	return mcp.NewStreamableHTTPHandler(func(r *http.Request) *mcp.Server {
		return server
	}, &mcp.StreamableHTTPOptions{
		Stateless: true,
	})
}

// scriptTag returns the base-path-aware script tag for injection.
func (p *Proxy) scriptTag() string {
	return fmt.Sprintf(`<script src="%s/__agent-reverse-proxy-debug__/inject.js"></script>`, p.basePath)
}

// handleDynamicProxy extracts target URL from the request path and proxies to it.
// Path format: /{scheme}/{host:port}/{path...}
func (p *Proxy) handleDynamicProxy(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/")
	if path == "" {
		p.serveDynamicLandingPage(w, r)
		return
	}

	target, remainder, err := extractTarget(path)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	// Rewrite request path to the remainder
	r.URL.Path = remainder
	r.URL.RawPath = ""

	pathPrefix := p.basePath + "/" + target.Scheme + "/" + target.Host
	appAddr := target.Host
	handleProxyRequest(target, appAddr, p.config.NoInject, p.config.ThemeCookie, p.scriptTag(), pathPrefix)(w, r)
}

// serveDynamicLandingPage renders a landing page for dynamic proxy mode at "/".
func (p *Proxy) serveDynamicLandingPage(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprint(w, dynamicLandingHTML)
}

const dynamicLandingHTML = `<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>Agent Reverse Proxy</title>
<style>
  * { box-sizing: border-box; margin: 0; padding: 0; }
  body { font-family: system-ui, sans-serif; display: flex; align-items: center; justify-content: center; min-height: 100vh; background: #f5f5f5; }
  .card { background: #fff; border-radius: 8px; box-shadow: 0 2px 8px rgba(0,0,0,.1); padding: 2rem; max-width: 480px; width: 100%; }
  h1 { font-size: 1.25rem; margin-bottom: 1rem; }
  form { display: flex; gap: .5rem; }
  input { flex: 1; padding: .5rem; border: 1px solid #ccc; border-radius: 4px; font-size: 1rem; }
  button { padding: .5rem 1rem; background: #0057ff; color: #fff; border: none; border-radius: 4px; font-size: 1rem; cursor: pointer; }
  button:hover { background: #004ad9; }
  .err { color: #c00; margin-top: .5rem; font-size: .875rem; }
</style>
</head>
<body>
<div class="card">
  <h1>Agent Reverse Proxy</h1>
  <form id="f">
    <input id="u" type="text" value="http://localhost:3000/" placeholder="http://host:port/path" autofocus>
    <button type="submit">Go</button>
  </form>
  <div class="err" id="e"></div>
</div>
<script>
document.getElementById("f").addEventListener("submit", function(ev) {
  ev.preventDefault();
  var raw = document.getElementById("u").value.trim();
  var errEl = document.getElementById("e");
  errEl.textContent = "";
  try {
    var u = new URL(raw);
    if (u.protocol !== "http:" && u.protocol !== "https:") throw new Error("scheme must be http or https");
    var scheme = u.protocol.replace(":", "");
    var host = u.host;
    var path = u.pathname + u.search + u.hash;
    window.location.href = "/" + scheme + "/" + host + path;
  } catch (err) {
    errEl.textContent = "Invalid URL: " + err.message;
  }
});
</script>
</body>
</html>
`

// extractTarget parses a path like "http/localhost:8080/hello/world" into
// a target URL (http://localhost:8080) and remainder path (/hello/world).
func extractTarget(path string) (*url.URL, string, error) {
	// Split off scheme
	slashIdx := strings.Index(path, "/")
	if slashIdx < 0 {
		return nil, "", fmt.Errorf("invalid target path: missing host after scheme")
	}
	scheme := path[:slashIdx]
	if scheme != "http" && scheme != "https" {
		return nil, "", fmt.Errorf("invalid scheme %q: must be http or https", scheme)
	}
	rest := path[slashIdx+1:]

	// Split host from remaining path
	var host, remainder string
	hostEnd := strings.Index(rest, "/")
	if hostEnd < 0 {
		host = rest
		remainder = "/"
	} else {
		host = rest[:hostEnd]
		remainder = rest[hostEnd:]
	}

	if host == "" {
		return nil, "", fmt.Errorf("invalid target path: empty host")
	}

	target, err := url.Parse(scheme + "://" + host)
	if err != nil {
		return nil, "", fmt.Errorf("invalid target URL: %w", err)
	}

	return target, remainder, nil
}

// handleDebugIframeWS handles WebSocket connections from iframe debug scripts.
// The ?role query parameter determines which pool the client joins:
//   - "shell" (default): shell page — receives navigate/reload commands
//   - "inject": inject.js — receives query commands
func handleDebugIframeWS(hub *DebugHub) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			log.Printf("[DebugHub] Iframe WS upgrade error: %v", err)
			return
		}
		defer conn.Close()

		role := r.URL.Query().Get("role")
		switch role {
		case "inject":
			hub.AddInjectClient(conn)
			defer hub.RemoveInjectClient(conn)
		default: // "shell" or unspecified
			hub.AddShellClient(conn)
			defer hub.RemoveShellClient(conn)
		}

		for {
			_, msg, err := conn.ReadMessage()
			if err != nil {
				if websocket.IsCloseError(err, websocket.CloseNormalClosure, websocket.CloseGoingAway, websocket.CloseNoStatusReceived) {
					break
				}
				log.Printf("[DebugHub] Iframe read error: %v", err)
				break
			}
			hub.ForwardToAgent(msg)
		}
	}
}

// handleDebugAgentWS handles WebSocket connections from the agent (backward compat)
func handleDebugAgentWS(hub *DebugHub) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			log.Printf("[DebugHub] Agent WS upgrade error: %v", err)
			return
		}
		defer conn.Close()

		hub.SetAgent(conn)
		defer hub.RemoveAgent(conn)

		for {
			_, msg, err := conn.ReadMessage()
			if err != nil {
				if websocket.IsCloseError(err, websocket.CloseNormalClosure, websocket.CloseGoingAway, websocket.CloseNoStatusReceived) {
					break
				}
				log.Printf("[DebugHub] Agent read error: %v", err)
				break
			}
			hub.ForwardToIframes(msg)
		}
	}
}

// handleDebugUIObserverWS handles WebSocket connections from the terminal UI
func handleDebugUIObserverWS(hub *DebugHub) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			log.Printf("[DebugHub] UI observer WS upgrade error: %v", err)
			return
		}
		defer conn.Close()

		hub.AddUIObserver(conn)
		defer hub.RemoveUIObserver(conn)

		for {
			_, msg, err := conn.ReadMessage()
			if err != nil {
				break
			}
			hub.RouteCommand(msg)
		}
	}
}
