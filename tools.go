package agentproxy

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// sendAndWait sends a command to inject.js clients and waits for a response
// with matching type and ID. Returns the raw JSON response or a timeout error.
func sendAndWait(ctx context.Context, hub *DebugHub, cmd map[string]interface{}, resultType string) (*mcp.CallToolResult, any, error) {
	sub := hub.Subscribe()
	defer hub.Unsubscribe(sub)

	queryID := fmt.Sprintf("q%d", time.Now().UnixNano())
	cmd["id"] = queryID

	data, _ := json.Marshal(cmd)
	hub.SendQuery(data)

	timeout := time.After(5 * time.Second)
	for {
		select {
		case msg := <-sub:
			var result struct {
				T  string `json:"t"`
				ID string `json:"id"`
			}
			if json.Unmarshal(msg, &result) == nil && result.T == resultType && result.ID == queryID {
				return &mcp.CallToolResult{
					Content: []mcp.Content{
						&mcp.TextContent{Text: string(msg)},
					},
				}, nil, nil
			}
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
}

func registerTools(server *mcp.Server, hub *DebugHub, toolPrefix ToolPrefix) {
	type QueryParams struct {
		Selector string `json:"selector"`
	}

	mcp.AddTool(server, &mcp.Tool{
		Name: toolPrefix.ToolName("browser_snapshot"),
		Description: "Capture a snapshot of the Preview content by CSS selector. " +
			"Returns the text, HTML, and visibility of matching elements in the Preview. " +
			"This is the correct tool for inspecting the Preview — browser_snapshot cannot see Preview content.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, params *QueryParams) (*mcp.CallToolResult, any, error) {
		if params.Selector == "" {
			return nil, nil, fmt.Errorf("selector is required")
		}
		return sendAndWait(ctx, hub, map[string]interface{}{
			"t":        "query",
			"selector": params.Selector,
		}, "queryResult")
	})

	type ListenParams struct {
		DurationSeconds float64 `json:"duration_seconds"`
	}

	mcp.AddTool(server, &mcp.Tool{
		Name: toolPrefix.ToolName("browser_console_messages"),
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

	// browser_click — click an element by CSS selector
	type ClickParams struct {
		Selector string `json:"selector"`
	}

	mcp.AddTool(server, &mcp.Tool{
		Name: toolPrefix.ToolName("browser_click"),
		Description: "Click an element in the Preview by CSS selector. " +
			"Returns whether the click succeeded and the element's tag/text.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, params *ClickParams) (*mcp.CallToolResult, any, error) {
		if params.Selector == "" {
			return nil, nil, fmt.Errorf("selector is required")
		}
		return sendAndWait(ctx, hub, map[string]interface{}{
			"t":        "click",
			"selector": params.Selector,
		}, "clickResult")
	})

	// browser_type — type text into an element
	type TypeParams struct {
		Selector string `json:"selector"`
		Text     string `json:"text"`
		Clear    bool   `json:"clear"`
		Submit   bool   `json:"submit"`
	}

	mcp.AddTool(server, &mcp.Tool{
		Name: toolPrefix.ToolName("browser_type"),
		Description: "Type text into an element in the Preview. " +
			"If selector is omitted, types into the currently focused element. " +
			"Set clear=true to clear existing value first. Set submit=true to press Enter after typing.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, params *TypeParams) (*mcp.CallToolResult, any, error) {
		if params.Text == "" {
			return nil, nil, fmt.Errorf("text is required")
		}
		cmd := map[string]interface{}{
			"t":    "type",
			"text": params.Text,
		}
		if params.Selector != "" {
			cmd["selector"] = params.Selector
		}
		if params.Clear {
			cmd["clear"] = true
		}
		if params.Submit {
			cmd["submit"] = true
		}
		return sendAndWait(ctx, hub, cmd, "typeResult")
	})

	// browser_fill_form — fill multiple form fields at once
	type FillFormField struct {
		Selector string `json:"selector"`
		Value    string `json:"value"`
	}
	type FillFormParams struct {
		Fields []FillFormField `json:"fields"`
	}

	mcp.AddTool(server, &mcp.Tool{
		Name: toolPrefix.ToolName("browser_fill_form"),
		Description: "Fill multiple form fields in the Preview. " +
			"Each field is identified by CSS selector and set to the given value. " +
			"Supports text inputs, checkboxes (value: \"true\"/\"false\"), radio buttons, and select dropdowns.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, params *FillFormParams) (*mcp.CallToolResult, any, error) {
		if len(params.Fields) == 0 {
			return nil, nil, fmt.Errorf("fields is required and must not be empty")
		}
		fields := make([]map[string]string, len(params.Fields))
		for i, f := range params.Fields {
			if f.Selector == "" {
				return nil, nil, fmt.Errorf("field %d: selector is required", i)
			}
			fields[i] = map[string]string{"selector": f.Selector, "value": f.Value}
		}
		return sendAndWait(ctx, hub, map[string]interface{}{
			"t":      "fillForm",
			"fields": fields,
		}, "fillFormResult")
	})

	// browser_press_key — press a key on an element or the active element
	type PressKeyParams struct {
		Selector string `json:"selector"`
		Key      string `json:"key"`
	}

	mcp.AddTool(server, &mcp.Tool{
		Name: toolPrefix.ToolName("browser_press_key"),
		Description: "Press a key in the Preview. " +
			"If selector is omitted, sends the key to the currently focused element. " +
			"Key names follow the KeyboardEvent.key spec (e.g. \"Enter\", \"Escape\", \"Tab\", \"ArrowDown\", \"a\").",
	}, func(ctx context.Context, req *mcp.CallToolRequest, params *PressKeyParams) (*mcp.CallToolResult, any, error) {
		if params.Key == "" {
			return nil, nil, fmt.Errorf("key is required")
		}
		cmd := map[string]interface{}{
			"t":   "pressKey",
			"key": params.Key,
		}
		if params.Selector != "" {
			cmd["selector"] = params.Selector
		}
		return sendAndWait(ctx, hub, cmd, "pressKeyResult")
	})

	// browser_evaluate — evaluate JavaScript in the Preview
	type EvaluateParams struct {
		Expression string `json:"expression"`
	}

	mcp.AddTool(server, &mcp.Tool{
		Name: toolPrefix.ToolName("browser_evaluate"),
		Description: "Evaluate a JavaScript expression in the Preview and return the result. " +
			"The expression is evaluated in the page context and has access to the DOM. " +
			"Example: \"document.title\" or \"document.querySelectorAll('li').length\".",
	}, func(ctx context.Context, req *mcp.CallToolRequest, params *EvaluateParams) (*mcp.CallToolResult, any, error) {
		if params.Expression == "" {
			return nil, nil, fmt.Errorf("expression is required")
		}
		return sendAndWait(ctx, hub, map[string]interface{}{
			"t":          "evaluate",
			"expression": params.Expression,
		}, "evaluateResult")
	})
}
