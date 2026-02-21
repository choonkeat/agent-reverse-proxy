package agentproxy

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// testScriptTag is the default script tag used in tests (root base path)
const testScriptTag = `<script src="/__agent-reverse-proxy-debug__/inject.js"></script>`

// --- inject / CSP tests (from debug_inject_test.go) ---

func TestInjectDebugScript(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "inject after <head>",
			input:    `<!DOCTYPE html><html><head><title>Test</title></head><body></body></html>`,
			expected: `<!DOCTYPE html><html><head><script src="/__agent-reverse-proxy-debug__/inject.js"></script><title>Test</title></head><body></body></html>`,
		},
		{
			name:     "inject after <head> with attributes",
			input:    `<html><head lang="en"><title>Test</title></head></html>`,
			expected: `<html><head lang="en"><script src="/__agent-reverse-proxy-debug__/inject.js"></script><title>Test</title></head></html>`,
		},
		{
			name:     "inject after <body> if no head",
			input:    `<!DOCTYPE html><html><body><p>Hello</p></body></html>`,
			expected: `<!DOCTYPE html><html><body><script src="/__agent-reverse-proxy-debug__/inject.js"></script><p>Hello</p></body></html>`,
		},
		{
			name:     "case insensitive HEAD",
			input:    `<HTML><HEAD><TITLE>Test</TITLE></HEAD></HTML>`,
			expected: `<HTML><HEAD><script src="/__agent-reverse-proxy-debug__/inject.js"></script><TITLE>Test</TITLE></HEAD></HTML>`,
		},
		{
			name:     "case insensitive BODY",
			input:    `<HTML><BODY><P>Hello</P></BODY></HTML>`,
			expected: `<HTML><BODY><script src="/__agent-reverse-proxy-debug__/inject.js"></script><P>Hello</P></BODY></HTML>`,
		},
		{
			name:     "mixed case hEaD",
			input:    `<html><hEaD><title>Test</title></hEaD></html>`,
			expected: `<html><hEaD><script src="/__agent-reverse-proxy-debug__/inject.js"></script><title>Test</title></hEaD></html>`,
		},
		{
			name:     "no head or body - unchanged",
			input:    `<html><div>content</div></html>`,
			expected: `<html><div>content</div></html>`,
		},
		{
			name:     "head comes before body - only first injected",
			input:    `<head></head><body></body>`,
			expected: `<head><script src="/__agent-reverse-proxy-debug__/inject.js"></script></head><body></body>`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := injectDebugScript([]byte(tt.input), testScriptTag)
			if string(result) != tt.expected {
				t.Errorf("injectDebugScript(%q)\ngot:  %q\nwant: %q", tt.input, result, tt.expected)
			}
		})
	}
}

func TestModifyCSPHeader(t *testing.T) {
	tests := []struct {
		name     string
		csp      string
		expected string
	}{
		{
			name:     "empty CSP unchanged",
			csp:      "",
			expected: "",
		},
		{
			name:     "adds to existing script-src and adds connect-src",
			csp:      "script-src 'unsafe-inline'",
			expected: "script-src 'self' 'unsafe-inline'; connect-src 'self' ws: wss:",
		},
		{
			name:     "adds script-src and connect-src if both missing",
			csp:      "default-src 'self'",
			expected: "default-src 'self'; script-src 'self'; connect-src 'self' ws: wss:",
		},
		{
			name:     "adds to existing connect-src and adds script-src",
			csp:      "connect-src https://api.example.com",
			expected: "connect-src 'self' ws: wss: https://api.example.com; script-src 'self'",
		},
		{
			name:     "modifies both existing script-src and connect-src",
			csp:      "script-src 'self'; connect-src https://api.example.com",
			expected: "script-src 'self' 'self'; connect-src 'self' ws: wss: https://api.example.com",
		},
		{
			name:     "handles full CSP with all directives",
			csp:      "default-src 'self'; script-src 'unsafe-inline'; connect-src https://api.example.com",
			expected: "default-src 'self'; script-src 'self' 'unsafe-inline'; connect-src 'self' ws: wss: https://api.example.com",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := http.Header{}
			if tt.csp != "" {
				h.Set("Content-Security-Policy", tt.csp)
			}
			modifyCSPHeader(h)
			result := h.Get("Content-Security-Policy")
			if result != tt.expected {
				t.Errorf("modifyCSPHeader with CSP %q\ngot:  %q\nwant: %q", tt.csp, result, tt.expected)
			}
		})
	}
}

func TestDebugInjectJSEndpoint(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/__agent-reverse-proxy-debug__/inject.js" {
			w.Header().Set("Content-Type", "application/javascript")
			w.Write([]byte(debugInjectJS))
			return
		}
		http.NotFound(w, r)
	})

	req := httptest.NewRequest("GET", "/__agent-reverse-proxy-debug__/inject.js", nil)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", rr.Code)
	}

	if ct := rr.Header().Get("Content-Type"); ct != "application/javascript" {
		t.Errorf("expected Content-Type application/javascript, got %q", ct)
	}

	body := rr.Body.String()
	if !strings.Contains(body, "agent-reverse-proxy-debug") {
		t.Errorf("expected body to contain 'agent-reverse-proxy-debug', got %q", body)
	}
}

func TestGzipDecompression(t *testing.T) {
	original := `<!DOCTYPE html><html><head><title>Test</title></head><body>Hello</body></html>`

	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	gz.Write([]byte(original))
	gz.Close()

	gr, err := gzip.NewReader(bytes.NewReader(buf.Bytes()))
	if err != nil {
		t.Fatalf("failed to create gzip reader: %v", err)
	}
	decompressed, err := io.ReadAll(gr)
	gr.Close()
	if err != nil {
		t.Fatalf("failed to read gzip: %v", err)
	}

	if string(decompressed) != original {
		t.Errorf("gzip roundtrip failed\ngot:  %q\nwant: %q", decompressed, original)
	}

	injected := injectDebugScript(decompressed, testScriptTag)
	if !strings.Contains(string(injected), testScriptTag) {
		t.Errorf("injection into decompressed content failed")
	}
}

func TestDebugInjectJSContent(t *testing.T) {
	script := debugInjectJS

	if !strings.HasPrefix(script, "(function()") {
		t.Error("inject.js should be wrapped in IIFE")
	}

	requiredPatterns := []string{
		"'log', 'warn', 'error'",
		"window.fetch",
		"XMLHttpRequest",
		"__agent-reverse-proxy-debug__/ws",
		"WebSocket",
		"addEventListener('error'",
		"unhandledrejection",
		"console[method]",
	}

	for _, pattern := range requiredPatterns {
		if !strings.Contains(script, pattern) {
			t.Errorf("inject.js should contain %q", pattern)
		}
	}
}

func TestDebugInjectJSMessageTypes(t *testing.T) {
	script := debugInjectJS

	messageTypes := []string{
		`t: 'console'`,
		`t: 'error'`,
		`t: 'rejection'`,
		`t: 'fetch'`,
		`t: 'xhr'`,
		`t: 'init'`,
		`t: 'queryResult'`,
	}

	for _, msgType := range messageTypes {
		if !strings.Contains(script, msgType) {
			t.Errorf("inject.js should send messages with %s", msgType)
		}
	}
}

func TestDebugInjectJSSerializeFunction(t *testing.T) {
	script := debugInjectJS

	serializeChecks := []string{
		"function serialize",
		"instanceof Error",
		"instanceof HTMLElement",
		"instanceof Event",
		"Array.isArray",
		"[max depth]",
		"[undefined]",
		"[function]",
	}

	for _, check := range serializeChecks {
		if !strings.Contains(script, check) {
			t.Errorf("inject.js serialize function should handle %s", check)
		}
	}
}

// --- DebugHub tests ---

func TestDebugHubClientManagement(t *testing.T) {
	hub := NewDebugHub()

	hub.mu.RLock()
	if len(hub.iframeClients) != 0 {
		t.Error("hub should start with no clients")
	}
	if hub.agentConn != nil {
		t.Error("hub should start with no agent")
	}
	hub.mu.RUnlock()
}

func TestDebugHubForwarding(t *testing.T) {
	hub := NewDebugHub()

	t.Run("forward to nil agent is safe", func(t *testing.T) {
		hub.ForwardToAgent([]byte(`{"t":"test"}`))
	})

	t.Run("forward to empty iframes is safe", func(t *testing.T) {
		hub.ForwardToIframes([]byte(`{"t":"test"}`))
	})
}

func TestDebugHubInProcessSubscribe(t *testing.T) {
	hub := NewDebugHub()

	sub := hub.Subscribe()
	defer hub.Unsubscribe(sub)

	// Simulate iframe message forwarded to agent (which includes in-process subs)
	testMsg := []byte(`{"t":"console","m":"log","args":["hello"]}`)
	hub.ForwardToAgent(testMsg)

	select {
	case msg := <-sub:
		if string(msg) != string(testMsg) {
			t.Errorf("expected %q, got %q", testMsg, msg)
		}
	case <-time.After(time.Second):
		t.Fatal("in-process subscriber did not receive message")
	}
}

func TestDebugHubInProcessQuery(t *testing.T) {
	hub := NewDebugHub()

	// Start an HTTP server with the actual iframe WS handler
	iframeServer := httptest.NewServer(handleDebugIframeWS(hub))
	defer iframeServer.Close()

	// Connect a mock inject.js client that responds to queries
	wsURL := "ws" + strings.TrimPrefix(iframeServer.URL, "http")
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("failed to connect mock inject.js: %v", err)
	}
	defer conn.Close()

	// Mock inject.js: read queries and respond with queryResults
	go func() {
		for {
			_, msg, err := conn.ReadMessage()
			if err != nil {
				return
			}
			var query struct {
				T        string `json:"t"`
				ID       string `json:"id"`
				Selector string `json:"selector"`
			}
			if json.Unmarshal(msg, &query) == nil && query.T == "query" {
				result, _ := json.Marshal(map[string]interface{}{
					"t":       "queryResult",
					"id":      query.ID,
					"found":   true,
					"text":    "Hello World",
					"html":    "<h1>Hello World</h1>",
					"visible": true,
				})
				conn.WriteMessage(websocket.TextMessage, result)
			}
		}
	}()

	// Give the connection time to register
	time.Sleep(50 * time.Millisecond)

	// Subscribe to in-process messages
	sub := hub.Subscribe()
	defer hub.Unsubscribe(sub)

	// Send a query via the hub (as MCP tools do)
	queryID := "test-query-1"
	query, _ := json.Marshal(map[string]string{
		"t":        "query",
		"id":       queryID,
		"selector": "h1",
	})
	hub.SendQuery(query)

	// Wait for queryResult on the in-process channel
	timeout := time.After(2 * time.Second)
	for {
		select {
		case msg := <-sub:
			var result struct {
				T  string `json:"t"`
				ID string `json:"id"`
			}
			if json.Unmarshal(msg, &result) == nil && result.T == "queryResult" && result.ID == queryID {
				if !strings.Contains(string(msg), "Hello World") {
					t.Errorf("expected 'Hello World' in result, got %s", msg)
				}
				return
			}
		case <-timeout:
			t.Fatal("timed out waiting for queryResult")
		}
	}
}

func TestDebugHubInProcessListen(t *testing.T) {
	hub := NewDebugHub()
	sub := hub.Subscribe()
	defer hub.Unsubscribe(sub)

	// Simulate a stream of iframe messages
	messages := []string{
		`{"t":"console","m":"log","args":["App started"]}`,
		`{"t":"console","m":"error","args":["Something went wrong"]}`,
		`{"t":"fetch","url":"http://localhost:3000/api","status":200}`,
	}

	go func() {
		for _, msg := range messages {
			hub.ForwardToAgent([]byte(msg))
			time.Sleep(10 * time.Millisecond)
		}
	}()

	var collected []string
	deadline := time.After(500 * time.Millisecond)
	for {
		select {
		case msg := <-sub:
			collected = append(collected, string(msg))
		case <-deadline:
			if len(collected) != 3 {
				t.Errorf("expected 3 messages, got %d", len(collected))
			}
			for _, msg := range messages {
				found := false
				for _, c := range collected {
					if c == msg {
						found = true
						break
					}
				}
				if !found {
					t.Errorf("missing message: %s", msg)
				}
			}
			return
		}
	}
}

// --- Reverse proxy tests (from websocket_proxy_test.go) ---

var testUpgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool { return true },
}

func TestWebSocketProxyRelay(t *testing.T) {
	const backendPrefix = "echo:"
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := testUpgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Logf("backend upgrade error: %v", err)
			return
		}
		defer conn.Close()
		for {
			mt, msg, err := conn.ReadMessage()
			if err != nil {
				return
			}
			if err := conn.WriteMessage(mt, append([]byte(backendPrefix), msg...)); err != nil {
				return
			}
		}
	}))
	defer backend.Close()

	backendURL, _ := url.Parse(backend.URL)
	proxyMux := http.NewServeMux()
	proxyMux.HandleFunc("/", handleProxyRequest(backendURL, backendURL.Port(), false, "test-theme", testScriptTag))
	proxy := httptest.NewServer(proxyMux)
	defer proxy.Close()

	wsURL := "ws" + strings.TrimPrefix(proxy.URL, "http") + "/"
	dialer := websocket.Dialer{}
	conn, resp, err := dialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("WebSocket dial through proxy failed: %v (resp=%v)", err, resp)
	}
	defer conn.Close()

	testMsg := "hello websocket"
	if err := conn.WriteMessage(websocket.TextMessage, []byte(testMsg)); err != nil {
		t.Fatalf("Failed to write message: %v", err)
	}

	_, msg, err := conn.ReadMessage()
	if err != nil {
		t.Fatalf("Failed to read echo message: %v", err)
	}
	expected := backendPrefix + testMsg
	if string(msg) != expected {
		t.Errorf("Expected %q, got %q", expected, string(msg))
	}
}

func TestNormalHTTPThroughProxy(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"status":"ok"}`))
	}))
	defer backend.Close()

	backendURL, _ := url.Parse(backend.URL)
	proxyMux := http.NewServeMux()
	proxyMux.HandleFunc("/", handleProxyRequest(backendURL, backendURL.Port(), false, "test-theme", testScriptTag))
	proxy := httptest.NewServer(proxyMux)
	defer proxy.Close()

	resp, err := http.Get(proxy.URL + "/")
	if err != nil {
		t.Fatalf("HTTP GET through proxy failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		t.Errorf("Expected 200, got %d", resp.StatusCode)
	}
}

func TestWebSocketProxyBackendDown(t *testing.T) {
	backendURL, _ := url.Parse("http://127.0.0.1:1")
	proxyMux := http.NewServeMux()
	proxyMux.HandleFunc("/", handleProxyRequest(backendURL, "1", false, "test-theme", testScriptTag))
	proxy := httptest.NewServer(proxyMux)
	defer proxy.Close()

	wsURL := "ws" + strings.TrimPrefix(proxy.URL, "http") + "/"
	dialer := websocket.Dialer{}
	_, resp, err := dialer.Dial(wsURL, nil)
	if err == nil {
		t.Fatal("Expected WebSocket dial to fail when backend is down")
	}
	if resp != nil && resp.StatusCode != http.StatusBadGateway {
		t.Errorf("Expected 502 Bad Gateway, got %d", resp.StatusCode)
	}
}

func TestHTMLInjectionThroughProxy(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write([]byte(`<!DOCTYPE html><html><head><title>Test</title></head><body>Hello</body></html>`))
	}))
	defer backend.Close()

	backendURL, _ := url.Parse(backend.URL)
	proxyMux := http.NewServeMux()
	proxyMux.HandleFunc("/", handleProxyRequest(backendURL, backendURL.Port(), false, "test-theme", testScriptTag))
	proxy := httptest.NewServer(proxyMux)
	defer proxy.Close()

	resp, err := http.Get(proxy.URL + "/")
	if err != nil {
		t.Fatalf("HTTP GET through proxy failed: %v", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), testScriptTag) {
		t.Errorf("expected HTML response to contain debug script tag, got: %s", body)
	}
}

func TestNoInjectionForNonHTML(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"status":"ok"}`))
	}))
	defer backend.Close()

	backendURL, _ := url.Parse(backend.URL)
	proxyMux := http.NewServeMux()
	proxyMux.HandleFunc("/", handleProxyRequest(backendURL, backendURL.Port(), false, "test-theme", testScriptTag))
	proxy := httptest.NewServer(proxyMux)
	defer proxy.Close()

	resp, err := http.Get(proxy.URL + "/")
	if err != nil {
		t.Fatalf("HTTP GET through proxy failed: %v", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	if strings.Contains(string(body), testScriptTag) {
		t.Errorf("non-HTML response should not contain debug script tag")
	}
}

func TestNoInjectFlag(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write([]byte(`<!DOCTYPE html><html><head><title>Test</title></head><body>Hello</body></html>`))
	}))
	defer backend.Close()

	backendURL, _ := url.Parse(backend.URL)
	proxyMux := http.NewServeMux()
	proxyMux.HandleFunc("/", handleProxyRequest(backendURL, backendURL.Port(), true /* noInject */, "test-theme", testScriptTag))
	proxy := httptest.NewServer(proxyMux)
	defer proxy.Close()

	resp, err := http.Get(proxy.URL + "/")
	if err != nil {
		t.Fatalf("HTTP GET through proxy failed: %v", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	if strings.Contains(string(body), testScriptTag) {
		t.Errorf("with --no-inject, HTML should not contain debug script tag")
	}
}

func TestGzipHTMLInjectionThroughProxy(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		w.Header().Set("Content-Encoding", "gzip")
		var buf bytes.Buffer
		gz := gzip.NewWriter(&buf)
		gz.Write([]byte(`<!DOCTYPE html><html><head><title>Gzipped</title></head><body>Compressed</body></html>`))
		gz.Close()
		w.Write(buf.Bytes())
	}))
	defer backend.Close()

	backendURL, _ := url.Parse(backend.URL)
	proxyMux := http.NewServeMux()
	proxyMux.HandleFunc("/", handleProxyRequest(backendURL, backendURL.Port(), false, "test-theme", testScriptTag))
	proxy := httptest.NewServer(proxyMux)
	defer proxy.Close()

	resp, err := http.Get(proxy.URL + "/")
	if err != nil {
		t.Fatalf("HTTP GET through proxy failed: %v", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), testScriptTag) {
		t.Errorf("gzip HTML should be decompressed and injected, got: %s", body)
	}
	if resp.Header.Get("Content-Encoding") != "" {
		t.Errorf("Content-Encoding should be removed after decompression, got: %s", resp.Header.Get("Content-Encoding"))
	}
}

func TestDebugEndpointPaths(t *testing.T) {
	expectedIframePath := "/__agent-reverse-proxy-debug__/ws"

	if !strings.Contains(debugInjectJS, expectedIframePath) {
		t.Errorf("inject.js should connect to %s", expectedIframePath)
	}
}

// --- Location header rewriting tests ---

func TestLocationHeaderRewriting(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Redirect using the backend's own full URL
		w.Header().Set("Location", "http://"+r.Host+"/dest")
		w.WriteHeader(http.StatusFound)
	}))
	defer backend.Close()

	backendURL, _ := url.Parse(backend.URL)
	proxyMux := http.NewServeMux()
	proxyMux.HandleFunc("/", handleProxyRequest(backendURL, backendURL.Host, true, "test-theme", testScriptTag))
	proxy := httptest.NewServer(proxyMux)
	defer proxy.Close()

	client := &http.Client{
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	resp, err := client.Get(proxy.URL + "/source")
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusFound {
		t.Fatalf("expected 302, got %d", resp.StatusCode)
	}

	loc := resp.Header.Get("Location")
	proxyURL, _ := url.Parse(proxy.URL)
	expected := "http://" + proxyURL.Host + "/dest"
	if loc != expected {
		t.Errorf("Location header not rewritten\ngot:  %s\nwant: %s", loc, expected)
	}
}

func TestLocationHeaderRelativeUnchanged(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Location", "/relative/path")
		w.WriteHeader(http.StatusFound)
	}))
	defer backend.Close()

	backendURL, _ := url.Parse(backend.URL)
	proxyMux := http.NewServeMux()
	proxyMux.HandleFunc("/", handleProxyRequest(backendURL, backendURL.Host, true, "test-theme", testScriptTag))
	proxy := httptest.NewServer(proxyMux)
	defer proxy.Close()

	client := &http.Client{
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	resp, err := client.Get(proxy.URL + "/source")
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	loc := resp.Header.Get("Location")
	if loc != "/relative/path" {
		t.Errorf("relative Location should pass through unchanged\ngot:  %s\nwant: /relative/path", loc)
	}
}

func TestLocationHeaderWithCookies(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/login" {
			http.SetCookie(w, &http.Cookie{Name: "session", Value: "abc123", Path: "/"})
			w.Header().Set("Location", "http://"+r.Host+"/dashboard")
			w.WriteHeader(http.StatusFound)
			return
		}
		if r.URL.Path == "/dashboard" {
			c, err := r.Cookie("session")
			if err != nil || c.Value != "abc123" {
				http.Error(w, "no cookie", http.StatusUnauthorized)
				return
			}
			w.Write([]byte("OK"))
		}
	}))
	defer backend.Close()

	backendURL, _ := url.Parse(backend.URL)
	proxyMux := http.NewServeMux()
	proxyMux.HandleFunc("/", handleProxyRequest(backendURL, backendURL.Host, true, "test-theme", testScriptTag))
	proxy := httptest.NewServer(proxyMux)
	defer proxy.Close()

	proxyParsed, _ := url.Parse(proxy.URL)

	// Step 1: hit /login, get cookie + rewritten Location
	client := &http.Client{
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	resp, err := client.Get(proxy.URL + "/login")
	if err != nil {
		t.Fatalf("login request failed: %v", err)
	}
	resp.Body.Close()

	// Verify redirect goes to proxy, not backend
	loc := resp.Header.Get("Location")
	if !strings.HasPrefix(loc, "http://"+proxyParsed.Host) {
		t.Fatalf("Location should point to proxy, got: %s", loc)
	}

	// Verify cookie was set
	cookies := resp.Cookies()
	if len(cookies) == 0 {
		t.Fatal("expected Set-Cookie header")
	}
	found := false
	for _, c := range cookies {
		if c.Name == "session" && c.Value == "abc123" {
			found = true
		}
	}
	if !found {
		t.Fatal("session cookie not found in response")
	}

	// Step 2: follow redirect manually with cookie
	req, _ := http.NewRequest("GET", loc, nil)
	req.AddCookie(&http.Cookie{Name: "session", Value: "abc123"})
	resp2, err := client.Do(req)
	if err != nil {
		t.Fatalf("dashboard request failed: %v", err)
	}
	defer resp2.Body.Close()

	if resp2.StatusCode != http.StatusOK {
		t.Errorf("expected 200 on dashboard, got %d", resp2.StatusCode)
	}
	body, _ := io.ReadAll(resp2.Body)
	if string(body) != "OK" {
		t.Errorf("expected OK, got %s", body)
	}
}

// --- Library API tests ---

func TestNewProxy(t *testing.T) {
	target, _ := url.Parse("http://localhost:3000")
	p, err := New(Config{
		BasePath:    "/",
		Target:      target,
		ToolPrefix:  "proxied",
		ThemeCookie: "test-theme",
	})
	if err != nil {
		t.Fatalf("New() failed: %v", err)
	}
	if p.Hub() == nil {
		t.Error("Hub() should not be nil")
	}
}

func TestNewProxyInvalidPrefix(t *testing.T) {
	target, _ := url.Parse("http://localhost:3000")
	_, err := New(Config{
		Target:     target,
		ToolPrefix: "invalid-prefix",
	})
	if err == nil {
		t.Error("expected error for invalid tool prefix")
	}
}

func TestProxyServeHTTPCORS(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("ok"))
	}))
	defer backend.Close()

	backendURL, _ := url.Parse(backend.URL)
	p, err := New(Config{
		Target:      backendURL,
		ToolPrefix:  "proxied",
		ThemeCookie: "test-theme",
	})
	if err != nil {
		t.Fatalf("New() failed: %v", err)
	}

	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set("Origin", "http://example.com")
	rr := httptest.NewRecorder()
	p.ServeHTTP(rr, req)

	if got := rr.Header().Get("X-Agent-Reverse-Proxy"); got != ProxyVersion {
		t.Errorf("X-Agent-Reverse-Proxy = %q, want %q", got, ProxyVersion)
	}
	if got := rr.Header().Get("Access-Control-Allow-Origin"); got != "http://example.com" {
		t.Errorf("CORS origin = %q, want http://example.com", got)
	}
}

func TestProxyBasePath(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		w.Write([]byte("path:" + r.URL.Path))
	}))
	defer backend.Close()

	backendURL, _ := url.Parse(backend.URL)
	p, err := New(Config{
		BasePath:    "/myproxy",
		Target:      backendURL,
		NoInject:    true,
		ToolPrefix:  "proxied",
		ThemeCookie: "test-theme",
	})
	if err != nil {
		t.Fatalf("New() failed: %v", err)
	}

	server := httptest.NewServer(p)
	defer server.Close()

	// Request under base path should work
	resp, err := http.Get(server.URL + "/myproxy/hello")
	if err != nil {
		t.Fatalf("GET failed: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if string(body) != "path:/hello" {
		t.Errorf("expected path:/hello, got %s", body)
	}

	// Request outside base path should 404
	resp2, err := http.Get(server.URL + "/other")
	if err != nil {
		t.Fatalf("GET failed: %v", err)
	}
	defer resp2.Body.Close()
	if resp2.StatusCode != http.StatusNotFound {
		t.Errorf("expected 404 for path outside base, got %d", resp2.StatusCode)
	}
}

func TestProxyBasePathScriptTag(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write([]byte(`<!DOCTYPE html><html><head><title>Test</title></head><body>Hello</body></html>`))
	}))
	defer backend.Close()

	backendURL, _ := url.Parse(backend.URL)
	p, err := New(Config{
		BasePath:    "/myproxy",
		Target:      backendURL,
		ToolPrefix:  "proxied",
		ThemeCookie: "test-theme",
	})
	if err != nil {
		t.Fatalf("New() failed: %v", err)
	}

	server := httptest.NewServer(p)
	defer server.Close()

	resp, err := http.Get(server.URL + "/myproxy/")
	if err != nil {
		t.Fatalf("GET failed: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)

	expectedTag := `<script src="/myproxy/__agent-reverse-proxy-debug__/inject.js"></script>`
	if !strings.Contains(string(body), expectedTag) {
		t.Errorf("expected HTML to contain base-path-aware script tag %q, got: %s", expectedTag, body)
	}
}

func TestProxyBasePathDebugEndpoints(t *testing.T) {
	target, _ := url.Parse("http://localhost:1")
	p, err := New(Config{
		BasePath:    "/myproxy",
		Target:      target,
		ToolPrefix:  "proxied",
		ThemeCookie: "test-theme",
	})
	if err != nil {
		t.Fatalf("New() failed: %v", err)
	}

	server := httptest.NewServer(p)
	defer server.Close()

	// inject.js should be served under base path
	resp, err := http.Get(server.URL + "/myproxy/__agent-reverse-proxy-debug__/inject.js")
	if err != nil {
		t.Fatalf("GET inject.js failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("inject.js: expected 200, got %d", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "application/javascript" {
		t.Errorf("inject.js: expected Content-Type application/javascript, got %q", ct)
	}
}

// --- Dynamic target mode tests ---

func TestExtractTarget(t *testing.T) {
	tests := []struct {
		path      string
		wantURL   string
		wantPath  string
		wantError bool
	}{
		{
			path:     "http/localhost:8080/hello/world",
			wantURL:  "http://localhost:8080",
			wantPath: "/hello/world",
		},
		{
			path:     "https/example.com:443/api/v1",
			wantURL:  "https://example.com:443",
			wantPath: "/api/v1",
		},
		{
			path:     "http/localhost:3000",
			wantURL:  "http://localhost:3000",
			wantPath: "/",
		},
		{
			path:      "ftp/localhost:21/file",
			wantError: true,
		},
		{
			path:      "http",
			wantError: true,
		},
		{
			path:      "http/",
			wantError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			target, remainder, err := extractTarget(tt.path)
			if tt.wantError {
				if err == nil {
					t.Errorf("expected error, got target=%v remainder=%q", target, remainder)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if target.String() != tt.wantURL {
				t.Errorf("target URL = %q, want %q", target.String(), tt.wantURL)
			}
			if remainder != tt.wantPath {
				t.Errorf("remainder = %q, want %q", remainder, tt.wantPath)
			}
		})
	}
}

func TestDynamicTargetProxy(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		w.Write([]byte("dynamic:" + r.URL.Path))
	}))
	defer backend.Close()

	backendURL, _ := url.Parse(backend.URL)
	host := backendURL.Host

	p, err := New(Config{
		Target:      nil, // dynamic mode
		NoInject:    true,
		ToolPrefix:  "proxied",
		ThemeCookie: "test-theme",
	})
	if err != nil {
		t.Fatalf("New() failed: %v", err)
	}

	server := httptest.NewServer(p)
	defer server.Close()

	resp, err := http.Get(server.URL + "/http/" + host + "/hello/world")
	if err != nil {
		t.Fatalf("GET failed: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if string(body) != "dynamic:/hello/world" {
		t.Errorf("expected dynamic:/hello/world, got %s", body)
	}
}

func TestDynamicTargetProxyWithBasePath(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		w.Write([]byte("dynamic:" + r.URL.Path))
	}))
	defer backend.Close()

	backendURL, _ := url.Parse(backend.URL)
	host := backendURL.Host

	p, err := New(Config{
		BasePath:    "/proxy123",
		Target:      nil, // dynamic mode
		NoInject:    true,
		ToolPrefix:  "proxied",
		ThemeCookie: "test-theme",
	})
	if err != nil {
		t.Fatalf("New() failed: %v", err)
	}

	server := httptest.NewServer(p)
	defer server.Close()

	resp, err := http.Get(server.URL + "/proxy123/http/" + host + "/hello")
	if err != nil {
		t.Fatalf("GET failed: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if string(body) != "dynamic:/hello" {
		t.Errorf("expected dynamic:/hello, got %s", body)
	}
}

func TestInjectJSBasePathDetection(t *testing.T) {
	// The inject.js should contain the basePath auto-detection code
	if !strings.Contains(debugInjectJS, "_basePath") {
		t.Error("inject.js should contain _basePath auto-detection")
	}
	if !strings.Contains(debugInjectJS, "document.currentScript") {
		t.Error("inject.js should use document.currentScript for base path detection")
	}
	if !strings.Contains(debugInjectJS, "_basePath + '/__agent-reverse-proxy-debug__/ws'") {
		t.Error("inject.js should use _basePath in WS URL construction")
	}
}

func TestShellPageBasePathRewrite(t *testing.T) {
	target, _ := url.Parse("http://localhost:1")
	p, err := New(Config{
		BasePath:    "/myproxy",
		Target:      target,
		ToolPrefix:  "proxied",
		ThemeCookie: "test-theme",
	})
	if err != nil {
		t.Fatalf("New() failed: %v", err)
	}

	server := httptest.NewServer(p)
	defer server.Close()

	resp, err := http.Get(server.URL + "/myproxy/__agent-reverse-proxy-debug__/shell")
	if err != nil {
		t.Fatalf("GET shell failed: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)

	if !strings.Contains(string(body), "/myproxy/__agent-reverse-proxy-debug__/ws") {
		t.Error("shell page should contain base-path-aware WS URL")
	}
}
