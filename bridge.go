package agentproxy

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// RunBridge relays newline-delimited JSON-RPC messages from stdin to an HTTP
// MCP endpoint and writes responses back to stdout. It implements a transparent
// stdio-to-HTTP MCP bridge — it does not interpret MCP protocol messages.
func RunBridge(ctx context.Context, endpoint string, stdin io.Reader, stdout io.Writer, stderr io.Writer) error {
	client := &http.Client{}
	var sessionID string
	var protocolVersion string

	scanner := bufio.NewScanner(stdin)
	scanner.Buffer(make([]byte, 1024*1024), 1024*1024) // 1MB buffer

	for scanner.Scan() {
		line := scanner.Bytes()
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}

		// Detect if this is a notification (no "id" field, or "id" is null).
		var msg json.RawMessage
		if err := json.Unmarshal(line, &msg); err != nil {
			fmt.Fprintf(stderr, "bridge: invalid JSON: %v\n", err)
			continue
		}
		isNotification := isJSONRPCNotification(line)
		requestID := extractRequestID(line)

		// Build HTTP request.
		req, err := http.NewRequestWithContext(ctx, "POST", endpoint, bytes.NewReader(line))
		if err != nil {
			return fmt.Errorf("creating request: %w", err)
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json, text/event-stream")
		if sessionID != "" {
			req.Header.Set("Mcp-Session-Id", sessionID)
		}
		if protocolVersion != "" {
			req.Header.Set("Mcp-Protocol-Version", protocolVersion)
		}

		resp, err := client.Do(req)
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			if !isNotification && requestID != nil {
				errResp := synthesizeErrorResponse(requestID, 0, err.Error())
				stdout.Write(errResp)
				stdout.Write([]byte("\n"))
			}
			fmt.Fprintf(stderr, "bridge: HTTP error: %v\n", err)
			continue
		}

		// Capture session ID from response.
		if sid := resp.Header.Get("Mcp-Session-Id"); sid != "" {
			sessionID = sid
		}

		// Handle notification acknowledgements.
		if resp.StatusCode == http.StatusAccepted || resp.StatusCode == http.StatusNoContent {
			resp.Body.Close()
			continue
		}

		// Handle HTTP errors.
		if resp.StatusCode >= 400 {
			body, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			if !isNotification && requestID != nil {
				msg := fmt.Sprintf("HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
				errResp := synthesizeErrorResponse(requestID, resp.StatusCode, msg)
				stdout.Write(errResp)
				stdout.Write([]byte("\n"))
			}
			continue
		}

		ct := resp.Header.Get("Content-Type")
		switch {
		case strings.HasPrefix(ct, "text/event-stream"):
			err = relaySSE(resp.Body, func(data []byte) error {
				// Extract protocol version from initialize response.
				maybeExtractProtocolVersion(data, &protocolVersion)
				stdout.Write(data)
				_, err := stdout.Write([]byte("\n"))
				return err
			})
			resp.Body.Close()
			if err != nil {
				fmt.Fprintf(stderr, "bridge: SSE relay error: %v\n", err)
			}
		default:
			// application/json or other — read full body.
			body, err := io.ReadAll(resp.Body)
			resp.Body.Close()
			if err != nil {
				fmt.Fprintf(stderr, "bridge: read response error: %v\n", err)
				continue
			}
			body = bytes.TrimSpace(body)
			if len(body) > 0 {
				maybeExtractProtocolVersion(body, &protocolVersion)
				stdout.Write(body)
				stdout.Write([]byte("\n"))
			}
		}
	}

	if err := scanner.Err(); err != nil {
		if ctx.Err() != nil {
			return nil
		}
		return fmt.Errorf("reading stdin: %w", err)
	}
	return nil // EOF — clean exit
}

// relaySSE parses a Server-Sent Events stream and calls writeFunc for each
// "message" event's data payload.
func relaySSE(reader io.Reader, writeFunc func([]byte) error) error {
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 1024*1024), 1024*1024)

	var eventType string
	var dataBuf bytes.Buffer

	for scanner.Scan() {
		line := scanner.Text()

		if line == "" {
			// Blank line = event dispatch.
			if dataBuf.Len() > 0 && (eventType == "" || eventType == "message") {
				data := bytes.TrimRight(dataBuf.Bytes(), "\n")
				if len(data) > 0 {
					if err := writeFunc(data); err != nil {
						return err
					}
				}
			}
			eventType = ""
			dataBuf.Reset()
			continue
		}

		if strings.HasPrefix(line, ":") {
			// Comment line — skip.
			continue
		}

		if strings.HasPrefix(line, "event:") {
			eventType = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
		} else if strings.HasPrefix(line, "data:") {
			data := strings.TrimPrefix(line, "data:")
			if len(data) > 0 && data[0] == ' ' {
				data = data[1:]
			}
			if dataBuf.Len() > 0 {
				dataBuf.WriteByte('\n')
			}
			dataBuf.WriteString(data)
		}
	}

	// Flush any trailing event without final blank line.
	if dataBuf.Len() > 0 && (eventType == "" || eventType == "message") {
		data := bytes.TrimRight(dataBuf.Bytes(), "\n")
		if len(data) > 0 {
			if err := writeFunc(data); err != nil {
				return err
			}
		}
	}

	return scanner.Err()
}

// synthesizeErrorResponse creates a JSON-RPC error response for a given
// request ID when the HTTP relay fails.
func synthesizeErrorResponse(id json.RawMessage, httpStatus int, message string) []byte {
	resp := map[string]interface{}{
		"jsonrpc": "2.0",
		"id":      id,
		"error": map[string]interface{}{
			"code":    -32000,
			"message": message,
		},
	}
	b, _ := json.Marshal(resp)
	return b
}

// isJSONRPCNotification returns true if the JSON-RPC message has no "id" field
// or if "id" is null.
func isJSONRPCNotification(data []byte) bool {
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(data, &obj); err != nil {
		return false
	}
	idRaw, exists := obj["id"]
	if !exists {
		return true
	}
	return string(idRaw) == "null"
}

// extractRequestID returns the "id" field from a JSON-RPC message, or nil if
// not present or null.
func extractRequestID(data []byte) json.RawMessage {
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(data, &obj); err != nil {
		return nil
	}
	idRaw, exists := obj["id"]
	if !exists || string(idRaw) == "null" {
		return nil
	}
	return idRaw
}

// maybeExtractProtocolVersion extracts the protocolVersion from an initialize
// response's result field and stores it for subsequent requests.
func maybeExtractProtocolVersion(data []byte, version *string) {
	if *version != "" {
		return // already extracted
	}
	var resp struct {
		Result struct {
			ProtocolVersion string `json:"protocolVersion"`
		} `json:"result"`
	}
	if json.Unmarshal(data, &resp) == nil && resp.Result.ProtocolVersion != "" {
		*version = resp.Result.ProtocolVersion
	}
}
