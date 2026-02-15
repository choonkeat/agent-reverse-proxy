package main

import (
	"context"
	_ "embed"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

//go:embed app-preview.md
var appPreviewMD string

func registerResources(server *mcp.Server) {
	server.AddResource(&mcp.Resource{
		URI:         "preview://app-preview",
		Name:        "app-preview",
		Description: "How the App Preview works: MCP tools, debug messages, port configuration.",
		MIMEType:    "text/markdown",
	}, func(ctx context.Context, req *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
		return &mcp.ReadResourceResult{
			Contents: []*mcp.ResourceContents{
				{
					URI:      "preview://app-preview",
					MIMEType: "text/markdown",
					Text:     appPreviewMD,
				},
			},
		}, nil
	})
}
