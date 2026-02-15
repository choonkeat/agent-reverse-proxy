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
	flag.Parse()

	appPort := resolveAppPort(*appPortFlag)
	proxyPort := resolveProxyPort(*proxyPortFlag, appPort)

	hub := NewDebugHub()

	server := mcp.NewServer(&mcp.Implementation{
		Name:    "swe-swe-preview",
		Version: "0.1.0",
	}, nil)
	registerTools(server, hub)
	registerResources(server)

	targetURL, err := url.Parse(fmt.Sprintf("http://localhost:%d", appPort))
	if err != nil {
		log.Fatalf("invalid target URL: %v", err)
	}

	baseURL, ln, err := startHTTPServer(server, hub, targetURL, strconv.Itoa(appPort), proxyPort, *noInject)
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
func startHTTPServer(mcpServer *mcp.Server, hub *DebugHub, targetURL *url.URL, appPortStr string, proxyPort int, noInject bool) (string, net.Listener, error) {
	mcpHandler := mcp.NewStreamableHTTPHandler(func(r *http.Request) *mcp.Server {
		return mcpServer
	}, &mcp.StreamableHTTPOptions{
		Stateless: true,
	})

	mux := http.NewServeMux()

	// MCP over HTTP
	mux.Handle("/mcp", mcpHandler)

	// Debug endpoints
	mux.HandleFunc("/__swe-swe-debug__/inject.js", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/javascript")
		w.Write([]byte(debugInjectJS))
	})

	mux.HandleFunc("/__swe-swe-debug__/ws", handleDebugIframeWS(hub))
	mux.HandleFunc("/__swe-swe-debug__/agent", handleDebugAgentWS(hub))
	mux.HandleFunc("/__swe-swe-debug__/ui", handleDebugUIObserverWS(hub))

	mux.HandleFunc("/__swe-swe-debug__/open", func(w http.ResponseWriter, r *http.Request) {
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
	mux.HandleFunc("/__swe-swe-shell__", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprintf(w, shellPageHTML)
	})

	// Reverse proxy (catch-all)
	mux.HandleFunc("/", handleProxyRequest(targetURL, appPortStr, noInject))

	addr := fmt.Sprintf("0.0.0.0:%d", proxyPort)
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return "", nil, fmt.Errorf("listen error: %w", err)
	}
	actualPort := ln.Addr().(*net.TCPAddr).Port

	go func() {
		if err := http.Serve(ln, mux); err != nil {
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
