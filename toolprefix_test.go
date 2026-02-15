package main

import (
	"net/url"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestNewToolPrefix(t *testing.T) {
	valid := []string{"proxied", "preview", "a", "myApp123", "X", "abc"}
	for _, s := range valid {
		if _, err := NewToolPrefix(s); err != nil {
			t.Errorf("NewToolPrefix(%q) should succeed, got: %v", s, err)
		}
	}

	invalid := []string{"", "has_underscore", "has-hyphen", "123start", "has space", "has:colon", "a.b"}
	for _, s := range invalid {
		if _, err := NewToolPrefix(s); err == nil {
			t.Errorf("NewToolPrefix(%q) should fail", s)
		}
	}
}

func TestToolName(t *testing.T) {
	p, _ := NewToolPrefix("preview")
	if got := p.ToolName("browser_snapshot"); got != "preview_browser_snapshot" {
		t.Errorf("ToolName = %q, want %q", got, "preview_browser_snapshot")
	}
	if got := p.ToolName("browser_console_messages"); got != "preview_browser_console_messages" {
		t.Errorf("ToolName = %q, want %q", got, "preview_browser_console_messages")
	}
}

func TestResourceURI(t *testing.T) {
	p, _ := NewToolPrefix("preview")
	uri := p.ResourceURI("reference")
	if uri != "preview-browser://reference" {
		t.Errorf("ResourceURI = %q, want %q", uri, "preview-browser://reference")
	}
	if _, err := url.Parse(uri); err != nil {
		t.Errorf("url.Parse(%q) failed: %v", uri, err)
	}

	uri2 := p.ResourceURI("help")
	if uri2 != "preview-browser://help" {
		t.Errorf("ResourceURI = %q, want %q", uri2, "preview-browser://help")
	}
	if _, err := url.Parse(uri2); err != nil {
		t.Errorf("url.Parse(%q) failed: %v", uri2, err)
	}
}

func TestRegisterResourcesNoPanic(t *testing.T) {
	prefixes := []string{"proxied", "preview", "myApp"}
	for _, raw := range prefixes {
		p, err := NewToolPrefix(raw)
		if err != nil {
			t.Fatalf("NewToolPrefix(%q): %v", raw, err)
		}
		server := mcp.NewServer(&mcp.Implementation{Name: "test", Version: "0.0.1"}, nil)
		registerResources(server, p) // must not panic
	}
}

func TestRegisterToolsNoPanic(t *testing.T) {
	prefixes := []string{"proxied", "preview", "myApp"}
	for _, raw := range prefixes {
		p, err := NewToolPrefix(raw)
		if err != nil {
			t.Fatalf("NewToolPrefix(%q): %v", raw, err)
		}
		hub := NewDebugHub()
		server := mcp.NewServer(&mcp.Implementation{Name: "test", Version: "0.0.1"}, nil)
		registerTools(server, hub, p) // must not panic
	}
}
