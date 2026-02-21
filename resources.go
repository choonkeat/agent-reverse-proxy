package agentproxy

import (
	"context"
	_ "embed"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

//go:embed app-preview.md
var appPreviewMD string

//go:embed help.md
var helpMD string

func registerResources(server *mcp.Server, toolPrefix ToolPrefix) {
	resolve := func(s string) string {
		s = strings.ReplaceAll(s, "{{snapshot_tool}}", toolPrefix.ToolName("browser_snapshot"))
		s = strings.ReplaceAll(s, "{{console_tool}}", toolPrefix.ToolName("browser_console_messages"))
		return s
	}

	referenceURI := toolPrefix.ResourceURI("reference")
	server.AddResource(&mcp.Resource{
		URI:         referenceURI,
		Name:        "reference",
		Description: "How the App Preview works: MCP tools, debug messages, port configuration.",
		MIMEType:    "text/markdown",
	}, func(ctx context.Context, req *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
		return &mcp.ReadResourceResult{
			Contents: []*mcp.ResourceContents{
				{
					URI:      referenceURI,
					MIMEType: "text/markdown",
					Text:     resolve(appPreviewMD),
				},
			},
		}, nil
	})

	helpURI := toolPrefix.ResourceURI("help")
	server.AddResource(&mcp.Resource{
		URI:         helpURI,
		Name:        "help",
		Description: "How to debug web apps using the App Preview: tool examples, workflow, and tips.",
		MIMEType:    "text/markdown",
	}, func(ctx context.Context, req *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
		return &mcp.ReadResourceResult{
			Contents: []*mcp.ResourceContents{
				{
					URI:      helpURI,
					MIMEType: "text/markdown",
					Text:     resolve(helpMD),
				},
			},
		}, nil
	})
}
