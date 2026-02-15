package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strconv"
	"syscall"

	"github.com/gorilla/websocket"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const proxyVersion = "0.1.0"

var upgrader = websocket.Upgrader{
	ReadBufferSize:  1024,
	WriteBufferSize: 1024,
	CheckOrigin:     func(r *http.Request) bool { return true },
}

func main() {
	appPortFlag := flag.Int("app-port", 0, "Port of the user's app (default: $APP_PORT or $PORT or 3000)")
	proxyPortFlag := flag.Int("proxy-port", 0, "Port for the proxy HTTP server (default: $PROXY_PORT or 20000+app-port)")
	noInject := flag.Bool("no-inject", false, "Disable debug script injection (plain reverse proxy)")
	noStdio := flag.Bool("no-stdio", false, "Disable stdio MCP transport (HTTP MCP only)")
	toolPrefix := flag.String("tool-prefix", "proxied", "Prefix for MCP tool names (e.g. 'preview' gives preview_browser_snapshot)")
	themeCookie := flag.String("theme-cookie", "agent-reverse-proxy-theme", "Cookie name for light/dark theme on the error page")
	flag.Parse()

	appPort := resolveAppPort(*appPortFlag)
	proxyPort := resolveProxyPort(*proxyPortFlag, appPort)

	prefix, err := NewToolPrefix(*toolPrefix)
	if err != nil {
		log.Fatalf("--tool-prefix: %v", err)
	}

	hub := NewDebugHub()

	server := mcp.NewServer(&mcp.Implementation{
		Name:    "agent-reverse-proxy",
		Version: proxyVersion,
	}, nil)
	registerTools(server, hub, prefix)
	registerResources(server, prefix)

	targetURL, err := url.Parse(fmt.Sprintf("http://localhost:%d", appPort))
	if err != nil {
		log.Fatalf("invalid target URL: %v", err)
	}

	baseURL, ln, err := startHTTPServer(server, hub, targetURL, strconv.Itoa(appPort), proxyPort, *noInject, *themeCookie)
	if err != nil {
		log.Fatalf("failed to start HTTP server: %v", err)
	}
	_ = ln

	fmt.Fprintf(os.Stderr, "Reverse proxy: %s -> localhost:%d\n", baseURL, appPort)
	fmt.Fprintf(os.Stderr, "MCP endpoint: POST %s/mcp\n", baseURL)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if !*noStdio {
		if err := server.Run(ctx, &mcp.StdioTransport{}); err != nil {
			log.Fatalf("mcp server error: %v", err)
		}
	} else {
		fmt.Fprintf(os.Stderr, "Running in HTTP-only mode (no stdio MCP). Press Ctrl+C to stop.\n")
		sig := make(chan os.Signal, 1)
		signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
		<-sig
	}
}

// resolveAppPort determines the app port from flag, env vars, or default.
func resolveAppPort(flagVal int) int {
	if flagVal > 0 {
		return flagVal
	}
	if s := os.Getenv("APP_PORT"); s != "" {
		if p, err := strconv.Atoi(s); err == nil && p > 0 {
			return p
		}
	}
	if s := os.Getenv("PORT"); s != "" {
		if p, err := strconv.Atoi(s); err == nil && p > 0 {
			return p
		}
	}
	return 3000
}

// resolveProxyPort determines the proxy port from flag, env vars, or default.
func resolveProxyPort(flagVal, appPort int) int {
	if flagVal > 0 {
		return flagVal
	}
	if s := os.Getenv("PROXY_PORT"); s != "" {
		if p, err := strconv.Atoi(s); err == nil && p > 0 {
			return p
		}
	}
	return 20000 + appPort
}

// startHTTPServer starts the HTTP server with proxy, debug endpoints, and MCP.
func startHTTPServer(mcpServer *mcp.Server, hub *DebugHub, targetURL *url.URL, appPortStr string, proxyPort int, noInject bool, themeCookie string) (string, net.Listener, error) {
	mcpHandler := mcp.NewStreamableHTTPHandler(func(r *http.Request) *mcp.Server {
		return mcpServer
	}, &mcp.StreamableHTTPOptions{
		Stateless: true,
	})

	mux := http.NewServeMux()

	// MCP over HTTP
	mux.Handle("/mcp", mcpHandler)

	// Debug endpoints
	mux.HandleFunc("/__agent-reverse-proxy-debug__/inject.js", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/javascript")
		w.Write([]byte(debugInjectJS))
	})

	mux.HandleFunc("/__agent-reverse-proxy-debug__/ws", handleDebugIframeWS(hub))
	mux.HandleFunc("/__agent-reverse-proxy-debug__/agent", handleDebugAgentWS(hub))
	mux.HandleFunc("/__agent-reverse-proxy-debug__/ui", handleDebugUIObserverWS(hub))

	mux.HandleFunc("/__agent-reverse-proxy-debug__/open", func(w http.ResponseWriter, r *http.Request) {
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

	// Shell page
	mux.HandleFunc("/__agent-reverse-proxy-debug__/shell", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprintf(w, shellPageHTML)
	})

	// Reverse proxy (catch-all)
	mux.HandleFunc("/", handleProxyRequest(targetURL, appPortStr, noInject, themeCookie))

	addr := fmt.Sprintf("0.0.0.0:%d", proxyPort)
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return "", nil, fmt.Errorf("listen error: %w", err)
	}
	actualPort := ln.Addr().(*net.TCPAddr).Port

	// Every response carries X-Agent-Reverse-Proxy so the main UI's
	// cross-origin probe can tell our responses (even 502) apart from
	// Traefik's own 502 (proxy container not started yet).
	// CORS exposes the header so JS can read it on cross-origin HEAD.
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Agent-Reverse-Proxy", proxyVersion)
		if origin := r.Header.Get("Origin"); origin != "" && (r.Method == http.MethodHead || r.Method == http.MethodOptions) {
			w.Header().Set("Access-Control-Allow-Origin", origin)
			w.Header().Set("Access-Control-Allow-Credentials", "true")
			w.Header().Set("Access-Control-Allow-Methods", "HEAD, OPTIONS")
			w.Header().Set("Access-Control-Expose-Headers", "X-Agent-Reverse-Proxy")
			if r.Method == http.MethodOptions {
				w.WriteHeader(http.StatusNoContent)
				return
			}
		}
		mux.ServeHTTP(w, r)
	})

	go func() {
		if err := http.Serve(ln, handler); err != nil {
			log.Printf("HTTP server error: %v", err)
		}
	}()

	return fmt.Sprintf("http://localhost:%d", actualPort), ln, nil
}

// handleDebugIframeWS handles WebSocket connections from iframe debug scripts
func handleDebugIframeWS(hub *DebugHub) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			log.Printf("[DebugHub] Iframe WS upgrade error: %v", err)
			return
		}
		defer conn.Close()

		hub.AddIframeClient(conn)
		defer hub.RemoveIframeClient(conn)

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
			hub.ForwardToIframes(msg)
		}
	}
}
