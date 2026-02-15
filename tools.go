package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func registerTools(server *mcp.Server, hub *DebugHub, toolPrefix string) {
	type QueryParams struct {
		Selector string `json:"selector"`
	}

	mcp.AddTool(server, &mcp.Tool{
		Name: toolPrefix + "_browser_snapshot",
		Description: "Capture a snapshot of the Preview content by CSS selector. " +
			"Returns the text, HTML, and visibility of matching elements in the Preview. " +
			"This is the correct tool for inspecting the Preview — browser_snapshot cannot see Preview content.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, params *QueryParams) (*mcp.CallToolResult, any, error) {
		if params.Selector == "" {
			return nil, nil, fmt.Errorf("selector is required")
		}

		// Subscribe to in-process messages from iframe clients
		sub := hub.Subscribe()
		defer hub.Unsubscribe(sub)

		// Generate unique query ID
		queryID := fmt.Sprintf("q%d", time.Now().UnixNano())

		// Send DOM query to all connected iframe clients
		query, _ := json.Marshal(map[string]string{
			"t":        "query",
			"id":       queryID,
			"selector": params.Selector,
		})
		hub.SendQuery(query)

		// Wait for matching queryResult with timeout
		timeout := time.After(5 * time.Second)
		for {
			select {
			case msg := <-sub:
				// Check if this is our queryResult
				var result struct {
					T  string `json:"t"`
					ID string `json:"id"`
				}
				if json.Unmarshal(msg, &result) == nil && result.T == "queryResult" && result.ID == queryID {
					return &mcp.CallToolResult{
						Content: []mcp.Content{
							&mcp.TextContent{Text: string(msg)},
						},
					}, nil, nil
				}
				// Not our result, keep waiting
			case <-timeout:
				return &mcp.CallToolResult{
					Content: []mcp.Content{
						&mcp.TextContent{Text: "No response from preview (timeout after 5s). Is the app running?"},
					},
					IsError: true,
				}, nil, nil
			case <-ctx.Done():
				return nil, nil, ctx.Err()
			}
		}
	})

	type ListenParams struct {
		DurationSeconds float64 `json:"duration_seconds"`
	}

	mcp.AddTool(server, &mcp.Tool{
		Name: toolPrefix + "_browser_console_messages",
		Description: "Returns console logs, errors, and network requests from the Preview. " +
			"Listens for the specified duration and returns all messages. " +
			"This is the correct tool for debugging the Preview — browser_console_messages cannot see Preview output.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, params *ListenParams) (*mcp.CallToolResult, any, error) {
		duration := params.DurationSeconds
		if duration <= 0 {
			duration = 5
		}
		if duration > 30 {
			duration = 30
		}

		// Subscribe to in-process messages from iframe clients
		sub := hub.Subscribe()
		defer hub.Unsubscribe(sub)

		var messages []string
		deadline := time.After(time.Duration(duration * float64(time.Second)))

		for {
			select {
			case msg := <-sub:
				messages = append(messages, string(msg))
			case <-deadline:
				if len(messages) == 0 {
					return &mcp.CallToolResult{
						Content: []mcp.Content{
							&mcp.TextContent{Text: "No messages received during listen period"},
						},
					}, nil, nil
				}
				return &mcp.CallToolResult{
					Content: []mcp.Content{
						&mcp.TextContent{Text: strings.Join(messages, "\n")},
					},
				}, nil, nil
			case <-ctx.Done():
				return nil, nil, ctx.Err()
			}
		}
	})
}
