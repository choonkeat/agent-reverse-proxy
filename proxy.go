package main

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
	"strconv"
	"strings"
	"time"
)

// injectDebugScript injects the debug script tag after the FIRST <head> or <body> tag only
func injectDebugScript(body []byte) []byte {
	loc := debugInjectScriptRe.FindIndex(body)
	if loc == nil {
		return body // No match found
	}
	// Insert script tag after the first match
	result := make([]byte, 0, len(body)+len(debugScriptTag))
	result = append(result, body[:loc[1]]...)
	result = append(result, debugScriptTag...)
	result = append(result, body[loc[1]:]...)
	return result
}

// modifyCSPHeader modifies Content-Security-Policy header to allow debug script and WebSocket
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

	h.Set("Content-Security-Policy", csp)
}

// handleProxyRequest proxies requests to the user's app at the given target URL.
// If noInject is true, HTML injection is disabled (plain reverse proxy).
func handleProxyRequest(target *url.URL, appAddr string, noInject bool, themeCookie string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// WebSocket upgrade detection: relay raw bytes instead of HTTP proxy
		if strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
			handleWebSocketRelay(w, r, target)
			return
		}

		// Build the target URL with the request path
		targetURL := *target
		targetURL.Path = singleJoiningSlash(target.Path, r.URL.Path)
		targetURL.RawQuery = r.URL.RawQuery

		// Create HTTP client with TLS config that allows self-signed certs
		client := &http.Client{
			Timeout: 30 * time.Second,
			Transport: &http.Transport{
				TLSClientConfig: &tls.Config{
					InsecureSkipVerify: true,
				},
			},
			// Don't follow redirects automatically - let the browser handle them
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				return http.ErrUseLastResponse
			},
		}

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

		// Set Host header to target host
		outReq.Host = target.Host

		// Make the request
		resp, err := client.Do(outReq)
		if err != nil {
			log.Printf("Preview proxy error: %v", err)
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.WriteHeader(http.StatusBadGateway)
			fmt.Fprintf(w, previewProxyErrorPage, themeCookie, appAddr)
			return
		}
		defer resp.Body.Close()

		// Process response (inject debug script for HTML, handle cookies)
		processProxyResponse(w, r, resp, target, noInject)
	}
}

// processProxyResponse handles the response: injects debug script for HTML, strips Domain from cookies
func processProxyResponse(w http.ResponseWriter, r *http.Request, resp *http.Response, target *url.URL, noInject bool) {
	// Copy response headers, handling cookies specially
	for key, values := range resp.Header {
		if isHopByHopHeader(key) {
			continue
		}
		// Handle Set-Cookie specially to strip Domain attribute
		if strings.EqualFold(key, "Set-Cookie") {
			for _, cookie := range resp.Cookies() {
				// Strip Domain so cookie applies to proxy domain
				cookie.Domain = ""
				// Also strip Secure flag if we're proxying (allows cookies over non-HTTPS proxy)
				cookie.Secure = false
				http.SetCookie(w, cookie)
			}
			continue
		}
		// Rewrite Location header to point to proxy instead of backend
		if strings.EqualFold(key, "Location") {
			proxyScheme := "http"
			if r.TLS != nil {
				proxyScheme = "https"
			}
			backendOrigin := target.Scheme + "://" + target.Host
			proxyOrigin := proxyScheme + "://" + r.Host
			for _, value := range values {
				rewritten := strings.Replace(value, backendOrigin, proxyOrigin, 1)
				w.Header().Add(key, rewritten)
			}
			continue
		}
		for _, value := range values {
			w.Header().Add(key, value)
		}
	}

	// Check if HTML for debug script injection
	contentType := resp.Header.Get("Content-Type")
	if noInject || !strings.Contains(contentType, "text/html") {
		// Non-HTML or injection disabled: pass through as-is
		w.WriteHeader(resp.StatusCode)
		io.Copy(w, resp.Body)
		return
	}

	// HTML response: read, decompress if needed, inject script
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

	// Inject the debug script
	injected := injectDebugScript(body)

	// Modify CSP header if present
	modifyCSPHeader(w.Header())

	// Update content length and remove encoding (we decompressed)
	w.Header().Set("Content-Length", strconv.Itoa(len(injected)))
	w.Header().Del("Content-Encoding")

	w.WriteHeader(resp.StatusCode)
	w.Write(injected)
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
