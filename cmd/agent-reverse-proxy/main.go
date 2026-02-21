package main

import (
	"context"
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

	agentproxy "agent-reverse-proxy"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func main() {
	appHostFlag := flag.String("app-host", "", "Hostname of the user's app (default: $APP_HOST or localhost)")
	appPortFlag := flag.Int("app-port", 0, "Port of the user's app (default: $APP_PORT or $PORT or 3000)")
	proxyPortFlag := flag.Int("proxy-port", 0, "Port for the proxy HTTP server (default: $PROXY_PORT or 20000+app-port)")
	noInject := flag.Bool("no-inject", false, "Disable debug script injection (plain reverse proxy)")
	noStdio := flag.Bool("no-stdio", false, "Disable stdio MCP transport (HTTP MCP only)")
	toolPrefix := flag.String("tool-prefix", "proxied", "Prefix for MCP tool names (e.g. 'preview' gives preview_browser_snapshot)")
	themeCookie := flag.String("theme-cookie", "agent-reverse-proxy-theme", "Cookie name for light/dark theme on the error page")
	flag.Parse()

	appHost := resolveAppHost(*appHostFlag)
	appPort := resolveAppPort(*appPortFlag)
	proxyPort := resolveProxyPort(*proxyPortFlag, appPort)

	targetURL, err := url.Parse(fmt.Sprintf("http://%s:%d", appHost, appPort))
	if err != nil {
		log.Fatalf("invalid target URL: %v", err)
	}

	proxy, err := agentproxy.New(agentproxy.Config{
		BasePath:    "/",
		Target:      targetURL,
		NoInject:    *noInject,
		ToolPrefix:  *toolPrefix,
		ThemeCookie: *themeCookie,
	})
	if err != nil {
		log.Fatalf("failed to create proxy: %v", err)
	}

	mcpServer := mcp.NewServer(&mcp.Implementation{
		Name:    "agent-reverse-proxy",
		Version: agentproxy.ProxyVersion,
	}, nil)
	proxy.RegisterTools(mcpServer)
	proxy.RegisterResources(mcpServer)

	// Create top-level mux with MCP endpoint
	topMux := http.NewServeMux()
	topMux.Handle("/mcp", proxy.MCPHandler(mcpServer))
	topMux.Handle("/", proxy)

	addr := fmt.Sprintf("0.0.0.0:%d", proxyPort)
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		log.Fatalf("listen error: %v", err)
	}
	actualPort := ln.Addr().(*net.TCPAddr).Port

	go func() {
		if err := http.Serve(ln, topMux); err != nil {
			log.Printf("HTTP server error: %v", err)
		}
	}()

	baseURL := fmt.Sprintf("http://localhost:%d", actualPort)
	fmt.Fprintf(os.Stderr, "Reverse proxy: %s -> %s:%d\n", baseURL, appHost, appPort)
	fmt.Fprintf(os.Stderr, "MCP endpoint: POST %s/mcp\n", baseURL)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if !*noStdio {
		if err := mcpServer.Run(ctx, &mcp.StdioTransport{}); err != nil {
			log.Fatalf("mcp server error: %v", err)
		}
	} else {
		fmt.Fprintf(os.Stderr, "Running in HTTP-only mode (no stdio MCP). Press Ctrl+C to stop.\n")
		sig := make(chan os.Signal, 1)
		signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
		<-sig
	}
}

// resolveAppHost determines the app host from flag, env var, or default.
func resolveAppHost(flagVal string) string {
	if flagVal != "" {
		return flagVal
	}
	if s := os.Getenv("APP_HOST"); s != "" {
		return s
	}
	return "localhost"
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
