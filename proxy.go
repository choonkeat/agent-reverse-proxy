package agentproxy

import (
	"bytes"
	"compress/gzip"
	"crypto/tls"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// previewClient is a shared HTTP client for the preview reverse proxy.
// Using a single client avoids leaking http.Transport instances (each with its
// own TLS state and connection pool) on every proxied request.
var previewClient = &http.Client{
	Timeout: 30 * time.Second,
	Transport: &http.Transport{
		TLSClientConfig: &tls.Config{
			InsecureSkipVerify: true,
		},
		MaxIdleConnsPerHost: 10,
		IdleConnTimeout:     90 * time.Second,
	},
	CheckRedirect: func(req *http.Request, via []*http.Request) error {
		return http.ErrUseLastResponse
	},
}

// absPathRewriteRe matches absolute paths in HTML attributes and CSS url() that need
// prefixing in dynamic proxy mode. It captures the context (href=", src=", etc.) and
// the leading slash+char, avoiding protocol-relative URLs (//...).
var absPathRewriteRe = regexp.MustCompile(
	`((?:href|src|action)\s*=\s*["']|fetch\(\s*["']|url\(\s*["']?)(/[^/"'])`,
)

// rewriteAbsolutePaths inserts prefix before absolute paths in HTML and CSS content.
func rewriteAbsolutePaths(body []byte, prefix string) []byte {
	replacement := []byte("${1}" + prefix + "${2}")
	return absPathRewriteRe.ReplaceAll(body, replacement)
}

// injectDebugScript injects the given script tag after the FIRST <head> or <body> tag only
func injectDebugScript(body []byte, scriptTag string) []byte {
	loc := debugInjectScriptRe.FindIndex(body)
	if loc == nil {
		return body // No match found
	}
	// Insert script tag after the first match
	result := make([]byte, 0, len(body)+len(scriptTag))
	result = append(result, body[:loc[1]]...)
	result = append(result, scriptTag...)
	result = append(result, body[loc[1]:]...)
	return result
}

// modifyCSPHeader modifies Content-Security-Policy header to allow debug script and WebSocket,
// and removes frame-ancestors since proxied content is displayed in an iframe (shell page).
func modifyCSPHeader(h http.Header) {
	csp := h.Get("Content-Security-Policy")
	if csp == "" {
		return
	}

	// Add 'self' to script-src for our injected script
	if strings.Contains(csp, "script-src") {
		csp = strings.Replace(csp, "script-src", "script-src 'self'", 1)
	} else {
		// No script-src directive, add one
		csp = csp + "; script-src 'self'"
	}

	// Add ws:, wss:, and 'self' to connect-src for WebSocket and fetch API
	if strings.Contains(csp, "connect-src") {
		csp = strings.Replace(csp, "connect-src", "connect-src 'self' ws: wss:", 1)
	} else {
		// No connect-src directive, add one
		csp = csp + "; connect-src 'self' ws: wss:"
	}

	// Remove frame-ancestors directive — proxied content is always displayed in an
	// iframe (shell page), so any restrictive frame-ancestors would break embedding.
	csp = removeCSPDirective(csp, "frame-ancestors")

	h.Set("Content-Security-Policy", csp)
}

// removeCSPDirective removes a named directive from a CSP string.
func removeCSPDirective(csp, directive string) string {
	parts := strings.Split(csp, ";")
	filtered := parts[:0]
	for _, part := range parts {
		trimmed := strings.TrimSpace(part)
		if !strings.HasPrefix(trimmed, directive) {
			filtered = append(filtered, part)
		}
	}
	return strings.Join(filtered, ";")
}

// proxyHooks holds optional per-request customization threaded from Config.
// A zero value (both fields nil) reproduces v0.2.9 behavior exactly.
type proxyHooks struct {
	// resolveTarget selects a per-request backend + upstream Host from the
	// inbound request Host. Returns ok=false to fall through to the fixed
	// target with today's Host-clobber behavior.
	resolveTarget func(inboundHost string) (target *url.URL, upstreamHost string, ok bool)
	// cookieDomainRewrite maps an upstream Set-Cookie Domain to the
	// browser-facing domain, given the inbound request Host. Returns "" to
	// strip Domain. nil = always strip.
	cookieDomainRewrite func(inboundHost, domain string) string
}

// handleProxyRequest proxies requests to the user's app at the given target URL.
// If noInject is true, HTML injection is disabled (plain reverse proxy).
// pathPrefix is the dynamic-mode path prefix (e.g. "/http/localhost:8080") to
// prepend to absolute paths in responses; pass "" in fixed mode.
// hooks optionally customizes per-request target selection and cookie rewriting;
// a zero value reproduces v0.2.9 behavior (fixed target, Host clobbered).
func handleProxyRequest(target *url.URL, appAddr string, noInject bool, themeCookie string, scriptTag string, pathPrefix string, hooks proxyHooks) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// Resolve the per-request target and upstream Host (two-hostname model).
		// Default: fixed target, Host clobbered to the target host (v0.2.9).
		effectiveTarget := target
		upstreamHost := target.Host
		if hooks.resolveTarget != nil {
			if rt, uh, ok := hooks.resolveTarget(r.Host); ok {
				effectiveTarget = rt
				upstreamHost = uh
			}
		}

		// WebSocket upgrade detection: relay raw bytes instead of HTTP proxy
		if strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
			handleWebSocketRelay(w, r, effectiveTarget)
			return
		}

		// Build the target URL with the request path
		targetURL := *effectiveTarget
		targetURL.Path = singleJoiningSlash(effectiveTarget.Path, r.URL.Path)
		targetURL.RawQuery = r.URL.RawQuery

		// Create outgoing request
		outReq, err := http.NewRequestWithContext(r.Context(), r.Method, targetURL.String(), r.Body)
		if err != nil {
			log.Printf("Preview proxy: failed to create request: %v", err)
			http.Error(w, "Failed to create request", http.StatusInternalServerError)
			return
		}

		// Copy headers from incoming request
		for key, values := range r.Header {
			// Skip hop-by-hop headers
			if isHopByHopHeader(key) {
				continue
			}
			for _, value := range values {
				outReq.Header.Add(key, value)
			}
		}

		// Set Host header to the resolved upstream host (logical vhost when a
		// ResolveTarget hook matched, else the fixed target host as before).
		outReq.Host = upstreamHost

		// Make the request
		resp, err := previewClient.Do(outReq)
		if err != nil {
			log.Printf("Preview proxy error: %v", err)
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.WriteHeader(http.StatusBadGateway)
			spaNote := ""
			if pathPrefix != "" {
				spaNote = fmt.Sprintf(`<div class="note">%s unreachable. We can only use path-based proxy, so SPAs (React, Vue, etc.) must use hash-based routing (e.g. /#/dashboard)</div>`, appAddr)
			}
			themeValue := "dark" // default
			if c, err := r.Cookie(themeCookie); err == nil && (c.Value == "light" || c.Value == "dark") {
				themeValue = c.Value
			}
			fmt.Fprintf(w, previewProxyErrorPage, themeValue, themeCookie, appAddr, spaNote)
			return
		}
		defer resp.Body.Close()

		// Process response (inject debug script for HTML, handle cookies)
		processProxyResponse(w, r, resp, effectiveTarget, noInject, scriptTag, pathPrefix, hooks.cookieDomainRewrite)
	}
}

// processProxyResponse handles the response: injects debug script for HTML, strips Domain from cookies,
// and rewrites absolute paths when pathPrefix is set (dynamic proxy mode).
func processProxyResponse(w http.ResponseWriter, r *http.Request, resp *http.Response, target *url.URL, noInject bool, scriptTag string, pathPrefix string, cookieDomainRewrite func(inboundHost, domain string) string) {
	// Copy response headers, handling cookies specially
	for key, values := range resp.Header {
		if isHopByHopHeader(key) {
			continue
		}
		// Strip X-Frame-Options — proxied content is displayed in an iframe (shell page)
		if strings.EqualFold(key, "X-Frame-Options") {
			continue
		}
		// Handle Set-Cookie specially to strip Domain attribute
		if strings.EqualFold(key, "Set-Cookie") {
			for _, cookie := range resp.Cookies() {
				// Rewrite the cookie Domain logical->reach when a hook is set and
				// the cookie carried a Domain; otherwise strip it so the cookie
				// applies to the proxy domain (v0.2.9 behavior). Empty-Domain
				// cookies stay empty.
				newDomain := ""
				if cookie.Domain != "" && cookieDomainRewrite != nil {
					newDomain = cookieDomainRewrite(r.Host, cookie.Domain)
				}
				cookie.Domain = newDomain
				// Also strip Secure flag if we're proxying (allows cookies over non-HTTPS proxy)
				cookie.Secure = false
				// Prepend pathPrefix to cookie path in dynamic mode
				if pathPrefix != "" && cookie.Path != "" {
					cookie.Path = pathPrefix + cookie.Path
				}
				http.SetCookie(w, cookie)
			}
			continue
		}
		// Rewrite Location header to point to proxy instead of backend
		if strings.EqualFold(key, "Location") {
			proxyScheme := "http"
			if proto := r.Header.Get("X-Forwarded-Proto"); proto != "" {
				proxyScheme = proto
			} else if r.TLS != nil {
				proxyScheme = "https"
			}
			backendOrigin := target.Scheme + "://" + target.Host
			proxyOrigin := proxyScheme + "://" + r.Host + pathPrefix
			for _, value := range values {
				rewritten := strings.Replace(value, backendOrigin, proxyOrigin, 1)
				// In dynamic mode, also rewrite relative Location headers (e.g. "/step/8")
				if pathPrefix != "" && rewritten == value && strings.HasPrefix(value, "/") {
					rewritten = pathPrefix + value
				}
				w.Header().Add(key, rewritten)
			}
			continue
		}
		for _, value := range values {
			w.Header().Add(key, value)
		}
	}

	// Determine if we need to buffer and rewrite the body
	contentType := resp.Header.Get("Content-Type")
	isHTML := strings.Contains(contentType, "text/html")
	isCSS := strings.Contains(contentType, "text/css")
	needsRewrite := pathPrefix != "" && (isHTML || isCSS)
	needsInject := isHTML && !noInject
	needsBuffer := needsRewrite || needsInject

	if !needsBuffer {
		// Stream through as-is
		w.WriteHeader(resp.StatusCode)
		io.Copy(w, resp.Body)
		return
	}

	// Buffer the response body, decompressing if needed
	var body []byte
	var readErr error

	encoding := resp.Header.Get("Content-Encoding")
	switch encoding {
	case "gzip":
		gr, err := gzip.NewReader(resp.Body)
		if err != nil {
			log.Printf("Preview proxy: gzip decode error: %v", err)
			w.WriteHeader(resp.StatusCode)
			io.Copy(w, resp.Body)
			return
		}
		body, readErr = io.ReadAll(gr)
		gr.Close()
	case "br":
		// Brotli requires external library, pass through unchanged
		w.WriteHeader(resp.StatusCode)
		io.Copy(w, resp.Body)
		return
	default:
		body, readErr = io.ReadAll(resp.Body)
	}

	if readErr != nil {
		log.Printf("Preview proxy: read body error: %v", readErr)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}

	// Rewrite absolute paths in dynamic mode
	if needsRewrite {
		body = rewriteAbsolutePaths(body, pathPrefix)
	}

	// Inject the debug script for HTML
	if needsInject {
		body = injectDebugScript(body, scriptTag)
		modifyCSPHeader(w.Header())
	}

	// Update content length and remove encoding (we decompressed)
	w.Header().Set("Content-Length", strconv.Itoa(len(body)))
	w.Header().Del("Content-Encoding")

	w.WriteHeader(resp.StatusCode)
	w.Write(body)
}

// handleWebSocketRelay hijacks the client connection and relays raw bytes
// to/from the backend WebSocket server. This avoids the normal HTTP proxy
// path which strips hop-by-hop headers (Upgrade, Connection) that are
// required for the WebSocket handshake.
func handleWebSocketRelay(w http.ResponseWriter, r *http.Request, target *url.URL) {
	// Determine backend address
	backendHost := target.Hostname()
	backendPort := target.Port()
	if backendPort == "" {
		if target.Scheme == "https" {
			backendPort = "443"
		} else {
			backendPort = "80"
		}
	}
	backendAddr := net.JoinHostPort(backendHost, backendPort)

	// Dial the backend
	backendConn, err := net.DialTimeout("tcp", backendAddr, 10*time.Second)
	if err != nil {
		log.Printf("Preview proxy: WebSocket backend dial error: %v", err)
		http.Error(w, "Bad Gateway", http.StatusBadGateway)
		return
	}

	// Hijack the client connection
	hijacker, ok := w.(http.Hijacker)
	if !ok {
		log.Printf("Preview proxy: ResponseWriter does not support hijacking")
		backendConn.Close()
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}
	clientConn, clientBuf, err := hijacker.Hijack()
	if err != nil {
		log.Printf("Preview proxy: hijack error: %v", err)
		backendConn.Close()
		return
	}

	// Reconstruct the HTTP upgrade request to send to backend
	reqPath := r.URL.RequestURI()
	var reqBuf bytes.Buffer
	fmt.Fprintf(&reqBuf, "%s %s HTTP/1.1\r\n", r.Method, reqPath)
	fmt.Fprintf(&reqBuf, "Host: %s\r\n", backendAddr)
	for key, values := range r.Header {
		for _, value := range values {
			fmt.Fprintf(&reqBuf, "%s: %s\r\n", key, value)
		}
	}
	reqBuf.WriteString("\r\n")

	// Send the upgrade request to backend
	if _, err := backendConn.Write(reqBuf.Bytes()); err != nil {
		log.Printf("Preview proxy: WebSocket backend write error: %v", err)
		clientConn.Close()
		backendConn.Close()
		return
	}

	// Read the backend's response and forward it to the client
	go func() {
		defer clientConn.Close()
		defer backendConn.Close()
		io.Copy(clientConn, backendConn)
	}()

	// Forward any buffered data from the client, then relay
	if clientBuf.Reader.Buffered() > 0 {
		buffered := make([]byte, clientBuf.Reader.Buffered())
		clientBuf.Read(buffered)
		backendConn.Write(buffered)
	}
	io.Copy(backendConn, clientConn)
}

// isHopByHopHeader returns true if the header is a hop-by-hop header
func isHopByHopHeader(header string) bool {
	hopByHop := map[string]bool{
		"Connection":          true,
		"Keep-Alive":          true,
		"Proxy-Authenticate":  true,
		"Proxy-Authorization": true,
		"Te":                  true,
		"Trailers":            true,
		"Transfer-Encoding":   true,
		"Upgrade":             true,
	}
	return hopByHop[http.CanonicalHeaderKey(header)]
}

// singleJoiningSlash joins two URL paths properly
func singleJoiningSlash(a, b string) string {
	aSlash := strings.HasSuffix(a, "/")
	bSlash := strings.HasPrefix(b, "/")
	switch {
	case aSlash && bSlash:
		return a + b[1:]
	case !aSlash && !bSlash:
		return a + "/" + b
	}
	return a + b
}
