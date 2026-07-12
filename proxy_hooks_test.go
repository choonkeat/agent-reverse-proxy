package agentproxy

import (
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// mustParseURL parses a URL for tests, failing the test on error.
func mustParseURL(t *testing.T, raw string) *url.URL {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("parse %q: %v", raw, err)
	}
	return u
}

// TestResolveTargetPerRequest verifies that a ResolveTarget hook can select a
// per-request backend and rewrite the upstream Host header. This is the
// two-hostname model: the request is dialed to the resolved backend, but the
// Host header the backend observes is the logical vhost, not the backend addr.
func TestResolveTargetPerRequest(t *testing.T) {
	var gotHost string
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotHost = r.Host
		w.Write([]byte("backend-ok"))
	}))
	defer backend.Close()
	backendURL := mustParseURL(t, backend.URL)

	// Primary fixed target -- must NOT be hit when the hook resolves.
	primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("primary-should-not-be-hit"))
	}))
	defer primary.Close()
	primaryURL := mustParseURL(t, primary.URL)

	p, err := New(Config{
		Target:     primaryURL,
		ToolPrefix: "preview",
		NoInject:   true,
		ResolveTarget: func(inboundHost string) (*url.URL, string, bool) {
			if strings.HasPrefix(inboundHost, "app1-5000.") {
				return backendURL, "app1.lvh.me:5000", true
			}
			return nil, "", false
		},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	req := httptest.NewRequest("GET", "/", nil)
	req.Host = "app1-5000.x.sslip.io:23000"
	rr := httptest.NewRecorder()
	p.ServeHTTP(rr, req)

	if body := rr.Body.String(); body != "backend-ok" {
		t.Errorf("body = %q, want backend-ok (request should reach resolved backend)", body)
	}
	if gotHost != "app1.lvh.me:5000" {
		t.Errorf("upstream Host = %q, want app1.lvh.me:5000", gotHost)
	}
}

// TestResolveTargetFallback verifies that when ResolveTarget returns ok=false,
// the request falls through to the fixed target with today's clobbered Host.
func TestResolveTargetFallback(t *testing.T) {
	var gotHost string
	primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotHost = r.Host
		w.Write([]byte("primary-ok"))
	}))
	defer primary.Close()
	primaryURL := mustParseURL(t, primary.URL)

	p, err := New(Config{
		Target:     primaryURL,
		ToolPrefix: "preview",
		NoInject:   true,
		ResolveTarget: func(inboundHost string) (*url.URL, string, bool) {
			return nil, "", false // never resolves
		},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	req := httptest.NewRequest("GET", "/", nil)
	req.Host = "whatever.example:23000"
	rr := httptest.NewRecorder()
	p.ServeHTTP(rr, req)

	if body := rr.Body.String(); body != "primary-ok" {
		t.Errorf("body = %q, want primary-ok (fallback to fixed target)", body)
	}
	if gotHost != primaryURL.Host {
		t.Errorf("upstream Host = %q, want %q (clobbered to target)", gotHost, primaryURL.Host)
	}
}

// TestNoHookUnchanged verifies that with nil hooks, behavior is identical to
// v0.2.9: fixed target, Host clobbered to the target host.
func TestNoHookUnchanged(t *testing.T) {
	var gotHost string
	primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotHost = r.Host
		w.Write([]byte("primary-ok"))
	}))
	defer primary.Close()
	primaryURL := mustParseURL(t, primary.URL)

	p, err := New(Config{
		Target:     primaryURL,
		ToolPrefix: "preview",
		NoInject:   true,
		// no ResolveTarget, no CookieDomainRewrite
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	req := httptest.NewRequest("GET", "/", nil)
	req.Host = "anything.example:23000"
	rr := httptest.NewRecorder()
	p.ServeHTTP(rr, req)

	if gotHost != primaryURL.Host {
		t.Errorf("upstream Host = %q, want %q (clobbered to target)", gotHost, primaryURL.Host)
	}
}

// TestCookieDomainRewrite verifies that a CookieDomainRewrite hook maps the
// upstream Set-Cookie Domain (logical) to the browser-facing reach domain,
// while empty-Domain cookies keep today's strip behavior, and a nil hook
// strips Domain entirely.
func TestCookieDomainRewrite(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.SetCookie(w, &http.Cookie{Name: "sid", Value: "1", Domain: ".lvh.me", Path: "/"})
		http.SetCookie(w, &http.Cookie{Name: "nodomain", Value: "2", Path: "/"})
		w.Write([]byte("ok"))
	}))
	defer backend.Close()
	backendURL := mustParseURL(t, backend.URL)

	t.Run("rewrite", func(t *testing.T) {
		p, err := New(Config{
			Target:     backendURL,
			ToolPrefix: "preview",
			NoInject:   true,
			CookieDomainRewrite: func(inboundHost, domain string) string {
				// Derive the reach from the inbound Host (leftmost label
				// removed) instead of hardcoding it -- proves the hook is
				// per-request.
				reach := inboundHost
				if i := strings.Index(inboundHost, "."); i >= 0 {
					reach = inboundHost[i+1:]
				}
				if h, _, err := net.SplitHostPort(reach); err == nil {
					reach = h
				}
				if strings.HasSuffix(domain, "lvh.me") {
					return "." + reach
				}
				return ""
			},
		})
		if err != nil {
			t.Fatalf("New: %v", err)
		}
		req := httptest.NewRequest("GET", "/", nil)
		req.Host = "app1-5000.x.sslip.io:23000"
		rr := httptest.NewRecorder()
		p.ServeHTTP(rr, req)

		// Assert on the raw wire header: Go's cookie re-parser strips a leading
		// dot from Domain, so rr.Result().Cookies() would hide what we emit.
		var sidHdr, nodomainHdr string
		for _, h := range rr.Result().Header["Set-Cookie"] {
			if strings.HasPrefix(h, "sid=") {
				sidHdr = h
			}
			if strings.HasPrefix(h, "nodomain=") {
				nodomainHdr = h
			}
		}
		if sidHdr == "" {
			t.Fatalf("sid Set-Cookie missing; got %+v", rr.Result().Header["Set-Cookie"])
		}
		// Go's stdlib Cookie.String() always drops a leading dot, so the wire
		// form of a rewritten ".x.sslip.io" is "Domain=x.sslip.io" (semantically
		// identical per RFC 6265: domain-match still covers subdomains).
		if !strings.Contains(sidHdr, "Domain=x.sslip.io") {
			t.Errorf("sid Set-Cookie = %q, want Domain=x.sslip.io (rewritten)", sidHdr)
		}
		if nodomainHdr == "" {
			t.Fatalf("nodomain Set-Cookie missing")
		}
		// Case-sensitive "Domain=" -- avoid matching the "domain" in "nodomain=".
		if strings.Contains(nodomainHdr, "Domain=") {
			t.Errorf("nodomain Set-Cookie = %q, want no Domain attribute", nodomainHdr)
		}
	})

	t.Run("nil-hook-strips", func(t *testing.T) {
		p, err := New(Config{
			Target:     backendURL,
			ToolPrefix: "preview",
			NoInject:   true,
		})
		if err != nil {
			t.Fatalf("New: %v", err)
		}
		req := httptest.NewRequest("GET", "/", nil)
		req.Host = "app1-5000.x.sslip.io:23000"
		rr := httptest.NewRecorder()
		p.ServeHTTP(rr, req)

		for _, c := range rr.Result().Cookies() {
			if c.Name == "sid" && c.Domain != "" {
				t.Errorf("nil hook: sid Domain = %q, want stripped", c.Domain)
			}
		}
	})
}
