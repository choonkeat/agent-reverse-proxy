package main

import (
	"fmt"
	"regexp"
)

var validPrefix = regexp.MustCompile(`^[a-zA-Z][a-zA-Z0-9]*$`)

// ToolPrefix is a validated prefix for MCP tool names and resource URIs.
// The raw string is unexported to prevent direct access.
type ToolPrefix struct{ s string }

// NewToolPrefix validates and returns a ToolPrefix.
// The prefix must match [a-zA-Z][a-zA-Z0-9]* — no underscores, hyphens, or spaces.
func NewToolPrefix(raw string) (ToolPrefix, error) {
	if !validPrefix.MatchString(raw) {
		return ToolPrefix{}, fmt.Errorf("invalid tool prefix %q: must match [a-zA-Z][a-zA-Z0-9]* (letters and digits, starting with a letter)", raw)
	}
	return ToolPrefix{s: raw}, nil
}

// ToolName returns a tool name like "preview_browser_snapshot".
func (p ToolPrefix) ToolName(suffix string) string {
	return p.s + "_" + suffix
}

// ResourceURI returns a URI like "preview-browser://reference".
// Uses hyphens (not underscores) in the scheme to comply with RFC 3986.
func (p ToolPrefix) ResourceURI(name string) string {
	return p.s + "-browser://" + name
}
