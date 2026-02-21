package agentproxy

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// TestBridgeBasicRelay verifies a JSON-RPC request is POSTed and the JSON
// response is written to stdout.
func TestBridgeBasicRelay(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var req map[string]interface{}
		json.Unmarshal(body, &req)

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"jsonrpc": "2.0",
			"id":      req["id"],
			"result":  map[string]interface{}{"tools": []interface{}{}},
		})
	}))
	defer server.Close()

	stdin := strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{}}` + "\n")
	var stdout bytes.Buffer
	var stderr bytes.Buffer

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	err := RunBridge(ctx, server.URL, stdin, &stdout, &stderr)
	if err != nil {
		t.Fatalf("RunBridge error: %v", err)
	}

	output := strings.TrimSpace(stdout.String())
	var resp map[string]interface{}
	if err := json.Unmarshal([]byte(output), &resp); err != nil {
		t.Fatalf("invalid JSON response: %v\noutput: %s", err, output)
	}
	if resp["jsonrpc"] != "2.0" {
		t.Errorf("expected jsonrpc 2.0, got %v", resp["jsonrpc"])
	}
	if resp["id"] != float64(1) {
		t.Errorf("expected id 1, got %v", resp["id"])
	}
	if resp["result"] == nil {
		t.Error("expected result field in response")
	}
}

// TestBridgeNotification verifies that notifications (no id) get 202 and
// produce no stdout output.
func TestBridgeNotification(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusAccepted)
	}))
	defer server.Close()

	stdin := strings.NewReader(`{"jsonrpc":"2.0","method":"notifications/initialized"}` + "\n")
	var stdout bytes.Buffer
	var stderr bytes.Buffer

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	err := RunBridge(ctx, server.URL, stdin, &stdout, &stderr)
	if err != nil {
		t.Fatalf("RunBridge error: %v", err)
	}

	if stdout.Len() != 0 {
		t.Errorf("expected no stdout for notification, got: %s", stdout.String())
	}
}

// TestBridgeSSEResponse verifies the bridge can parse SSE responses and relay
// each event's data to stdout.
func TestBridgeSSEResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)

		flusher, ok := w.(http.Flusher)
		if !ok {
			t.Fatal("server does not support flushing")
		}

		// Send two SSE events.
		fmt.Fprint(w, "event: message\ndata: {\"jsonrpc\":\"2.0\",\"id\":1,\"result\":{\"tools\":[]}}\n\n")
		flusher.Flush()
		fmt.Fprint(w, "event: message\ndata: {\"jsonrpc\":\"2.0\",\"method\":\"notifications/tools/list_changed\"}\n\n")
		flusher.Flush()
	}))
	defer server.Close()

	stdin := strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{}}` + "\n")
	var stdout bytes.Buffer
	var stderr bytes.Buffer

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	err := RunBridge(ctx, server.URL, stdin, &stdout, &stderr)
	if err != nil {
		t.Fatalf("RunBridge error: %v", err)
	}

	lines := strings.Split(strings.TrimSpace(stdout.String()), "\n")
	if len(lines) != 2 {
		t.Fatalf("expected 2 lines on stdout, got %d: %q", len(lines), stdout.String())
	}

	var resp1 map[string]interface{}
	json.Unmarshal([]byte(lines[0]), &resp1)
	if resp1["id"] != float64(1) {
		t.Errorf("first event: expected id 1, got %v", resp1["id"])
	}

	var resp2 map[string]interface{}
	json.Unmarshal([]byte(lines[1]), &resp2)
	if resp2["method"] != "notifications/tools/list_changed" {
		t.Errorf("second event: expected tools/list_changed notification, got %v", resp2["method"])
	}
}

// TestBridgeHTTPError verifies that an HTTP error synthesizes a JSON-RPC
// error response for requests (not notifications).
func TestBridgeHTTPError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
	}))
	defer server.Close()

	stdin := strings.NewReader(`{"jsonrpc":"2.0","id":42,"method":"tools/call","params":{}}` + "\n")
	var stdout bytes.Buffer
	var stderr bytes.Buffer

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	err := RunBridge(ctx, server.URL, stdin, &stdout, &stderr)
	if err != nil {
		t.Fatalf("RunBridge error: %v", err)
	}

	output := strings.TrimSpace(stdout.String())
	var resp map[string]interface{}
	if err := json.Unmarshal([]byte(output), &resp); err != nil {
		t.Fatalf("invalid JSON: %v\noutput: %s", err, output)
	}

	if resp["id"] != float64(42) {
		t.Errorf("expected id 42, got %v", resp["id"])
	}
	errObj, ok := resp["error"].(map[string]interface{})
	if !ok {
		t.Fatal("expected error object in response")
	}
	if errObj["code"] != float64(-32000) {
		t.Errorf("expected error code -32000, got %v", errObj["code"])
	}
	msg, _ := errObj["message"].(string)
	if !strings.Contains(msg, "HTTP 500") {
		t.Errorf("expected error message containing 'HTTP 500', got %q", msg)
	}
}

// TestBridgeSessionIDForwarding verifies that the bridge captures
// Mcp-Session-Id from the first response and sends it on subsequent requests.
func TestBridgeSessionIDForwarding(t *testing.T) {
	var secondRequestSessionID string
	requestCount := 0

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount++
		if requestCount == 1 {
			w.Header().Set("Mcp-Session-Id", "test-session-123")
		} else {
			secondRequestSessionID = r.Header.Get("Mcp-Session-Id")
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"jsonrpc": "2.0",
			"id":      requestCount,
			"result":  map[string]interface{}{},
		})
	}))
	defer server.Close()

	stdin := strings.NewReader(
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}` + "\n" +
			`{"jsonrpc":"2.0","id":2,"method":"tools/list","params":{}}` + "\n",
	)
	var stdout bytes.Buffer
	var stderr bytes.Buffer

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	err := RunBridge(ctx, server.URL, stdin, &stdout, &stderr)
	if err != nil {
		t.Fatalf("RunBridge error: %v", err)
	}

	if secondRequestSessionID != "test-session-123" {
		t.Errorf("expected session ID 'test-session-123' on second request, got %q", secondRequestSessionID)
	}
}

// TestBridgeProtocolVersion verifies that the bridge extracts protocolVersion
// from the initialize response and sends it as Mcp-Protocol-Version header.
func TestBridgeProtocolVersion(t *testing.T) {
	var secondRequestProtocolVersion string
	requestCount := 0

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount++
		if requestCount > 1 {
			secondRequestProtocolVersion = r.Header.Get("Mcp-Protocol-Version")
		}

		w.Header().Set("Content-Type", "application/json")
		if requestCount == 1 {
			// Initialize response with protocolVersion.
			json.NewEncoder(w).Encode(map[string]interface{}{
				"jsonrpc": "2.0",
				"id":      1,
				"result": map[string]interface{}{
					"protocolVersion": "2025-03-26",
					"capabilities":    map[string]interface{}{},
					"serverInfo":      map[string]interface{}{"name": "test", "version": "1.0"},
				},
			})
		} else {
			json.NewEncoder(w).Encode(map[string]interface{}{
				"jsonrpc": "2.0",
				"id":      requestCount,
				"result":  map[string]interface{}{},
			})
		}
	}))
	defer server.Close()

	stdin := strings.NewReader(
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-03-26","capabilities":{},"clientInfo":{"name":"test","version":"1.0"}}}` + "\n" +
			`{"jsonrpc":"2.0","id":2,"method":"tools/list","params":{}}` + "\n",
	)
	var stdout bytes.Buffer
	var stderr bytes.Buffer

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	err := RunBridge(ctx, server.URL, stdin, &stdout, &stderr)
	if err != nil {
		t.Fatalf("RunBridge error: %v", err)
	}

	if secondRequestProtocolVersion != "2025-03-26" {
		t.Errorf("expected Mcp-Protocol-Version '2025-03-26' on second request, got %q", secondRequestProtocolVersion)
	}
}

// TestBridgeEOF verifies that RunBridge returns nil when stdin is closed.
func TestBridgeEOF(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("server should not receive any requests")
	}))
	defer server.Close()

	// Empty stdin — immediate EOF.
	stdin := strings.NewReader("")
	var stdout bytes.Buffer
	var stderr bytes.Buffer

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	err := RunBridge(ctx, server.URL, stdin, &stdout, &stderr)
	if err != nil {
		t.Fatalf("expected nil error on EOF, got: %v", err)
	}

	if stdout.Len() != 0 {
		t.Errorf("expected no stdout output, got: %s", stdout.String())
	}
}

// TestBridgeWithRealMCPServer is an integration test that wires a real MCP
// server (via StreamableHTTPHandler) to the bridge, then drives it with an
// MCP client over IOTransport.
func TestBridgeWithRealMCPServer(t *testing.T) {
	// 1. Create a real MCP server with a tool.
	type EchoParams struct {
		Message string `json:"message"`
	}
	mcpServer := mcp.NewServer(&mcp.Implementation{
		Name:    "test-server",
		Version: "1.0.0",
	}, nil)
	mcp.AddTool(mcpServer, &mcp.Tool{
		Name:        "echo",
		Description: "Echo a message back",
	}, func(ctx context.Context, req *mcp.CallToolRequest, params EchoParams) (*mcp.CallToolResult, any, error) {
		return &mcp.CallToolResult{
			Content: []mcp.Content{
				&mcp.TextContent{Text: "echo: " + params.Message},
			},
		}, nil, nil
	})

	// 2. Serve it over HTTP with StreamableHTTPHandler.
	handler := mcp.NewStreamableHTTPHandler(
		func(r *http.Request) *mcp.Server { return mcpServer },
		&mcp.StreamableHTTPOptions{
			Stateless:    true,
			JSONResponse: true,
		},
	)
	httpServer := httptest.NewServer(handler)
	defer httpServer.Close()

	// 3. Create pipes: client ↔ bridge.
	// clientWriter → bridgeStdin (bridge reads)
	// bridgeStdout → clientReader (client reads)
	bridgeStdinR, clientWriter := io.Pipe()
	clientReader, bridgeStdoutW := io.Pipe()

	var stderr bytes.Buffer

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// 4. Run bridge in background.
	bridgeDone := make(chan error, 1)
	go func() {
		bridgeDone <- RunBridge(ctx, httpServer.URL, bridgeStdinR, bridgeStdoutW, &stderr)
		bridgeStdoutW.Close()
	}()

	// 5. Connect MCP client through the bridge via IOTransport.
	mcpClient := mcp.NewClient(&mcp.Implementation{
		Name:    "test-client",
		Version: "1.0.0",
	}, nil)

	transport := &mcp.IOTransport{
		Reader: clientReader,
		Writer: clientWriter,
	}

	session, err := mcpClient.Connect(ctx, transport, nil)
	if err != nil {
		t.Fatalf("client connect failed: %v (stderr: %s)", err, stderr.String())
	}

	// 6. Connect handles initialize handshake automatically.
	initResult := session.InitializeResult()
	if initResult.ServerInfo.Name != "test-server" {
		t.Errorf("expected server name 'test-server', got %q", initResult.ServerInfo.Name)
	}

	// 7. Call the echo tool through the bridge.
	callResult, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name: "echo",
		Arguments: map[string]interface{}{
			"message": "hello from bridge",
		},
	})
	if err != nil {
		t.Fatalf("call_tool failed: %v (stderr: %s)", err, stderr.String())
	}

	if len(callResult.Content) == 0 {
		t.Fatal("expected tool result content")
	}
	textContent, ok := callResult.Content[0].(*mcp.TextContent)
	if !ok {
		t.Fatalf("expected TextContent, got %T", callResult.Content[0])
	}
	if textContent.Text != "echo: hello from bridge" {
		t.Errorf("expected 'echo: hello from bridge', got %q", textContent.Text)
	}

	// 8. Clean shutdown: close client writer (EOF) → bridge exits.
	clientWriter.Close()

	select {
	case err := <-bridgeDone:
		if err != nil {
			t.Errorf("bridge error: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("bridge did not exit after stdin closed")
	}
}
