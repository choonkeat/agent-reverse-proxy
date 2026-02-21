// Command example is an HTTP server that exercises reverse proxy edge cases.
// It serves an 8-step linear flow where each page tests a specific proxy feature.
package main

import (
	"fmt"
	"log"
	"net/http"
	"os"
)

func main() {
	port := os.Getenv("PORT")
	if port == "" {
		port = "9876"
	}

	mux := http.NewServeMux()

	// Step 1: home page with relative CSS + relative link
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprint(w, `<!DOCTYPE html><html><head>
<link rel="stylesheet" href="styles/home.css">
</head><body>
<h1>Step 1: Relative CSS &amp; Links</h1>
<p id="step-info">If you can see this styled, relative CSS works.</p>
<a id="next" href="step/2">Next &rarr;</a>
</body></html>`)
	})

	// Relative CSS for step 1
	mux.HandleFunc("/styles/home.css", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/css")
		fmt.Fprint(w, `body { font-family: sans-serif; margin: 2rem; }
#step-info { color: green; }
a#next { display: inline-block; margin-top: 1rem; padding: 0.5rem 1rem; background: #007bff; color: white; text-decoration: none; border-radius: 4px; }`)
	})

	// Step 2: relative JS (src="step2.js" resolves to /step/step2.js)
	mux.HandleFunc("/step/2", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprint(w, `<!DOCTYPE html><html><head></head><body>
<h1>Step 2: Relative JS</h1>
<p id="js-status">Waiting for JS...</p>
<a id="next" href="/step/3" style="display:none">Next &rarr;</a>
<script src="step2.js"></script>
</body></html>`)
	})

	// JS file for step 2 (served at /step/step2.js)
	mux.HandleFunc("/step/step2.js", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/javascript")
		fmt.Fprint(w, `document.getElementById('js-status').textContent = 'JS_LOADED';
document.getElementById('next').style.display = '';`)
	})

	// Step 3: relative fetch (fetch('3/data') resolves to /step/3/data)
	mux.HandleFunc("/step/3", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprint(w, `<!DOCTYPE html><html><head></head><body>
<h1>Step 3: Relative Fetch</h1>
<p id="fetch-status">Fetching...</p>
<a id="next" href="/step/4" style="display:none">Next &rarr;</a>
<script>
fetch('3/data').then(r => r.json()).then(d => {
  document.getElementById('fetch-status').textContent = d.message;
  document.getElementById('next').style.display = '';
}).catch(e => {
  document.getElementById('fetch-status').textContent = 'FETCH_FAILED: ' + e.message;
});
</script>
</body></html>`)
	})

	// Data endpoint for step 3
	mux.HandleFunc("/step/3/data", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"message":"RELATIVE_FETCH_OK"}`)
	})

	// Step 4: absolute path fetch
	mux.HandleFunc("/step/4", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprint(w, `<!DOCTYPE html><html><head></head><body>
<h1>Step 4: Absolute Path Fetch</h1>
<p id="fetch-status">Fetching...</p>
<a id="next" href="/step/5" style="display:none">Next &rarr;</a>
<script>
fetch('/api/step4').then(r => r.json()).then(d => {
  document.getElementById('fetch-status').textContent = d.message;
  document.getElementById('next').style.display = '';
}).catch(e => {
  document.getElementById('fetch-status').textContent = 'FETCH_FAILED: ' + e.message;
});
</script>
</body></html>`)
	})

	// API endpoint for step 4
	mux.HandleFunc("/api/step4", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"message":"ABSOLUTE_FETCH_OK"}`)
	})

	// Step 5: set a cookie
	mux.HandleFunc("/step/5", func(w http.ResponseWriter, r *http.Request) {
		http.SetCookie(w, &http.Cookie{
			Name:  "proxytest",
			Value: "hello123",
			Path:  "/",
		})
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprint(w, `<!DOCTYPE html><html><head></head><body>
<h1>Step 5: Set Cookie</h1>
<p id="cookie-info">Cookie "proxytest=hello123" has been set.</p>
<a id="next" href="/step/6">Next &rarr;</a>
</body></html>`)
	})

	// Step 6: read cookie
	mux.HandleFunc("/step/6", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		c, err := r.Cookie("proxytest")
		if err != nil || c.Value != "hello123" {
			fmt.Fprint(w, `<!DOCTYPE html><html><head></head><body>
<h1>Step 6: Read Cookie</h1>
<p id="cookie-status" style="color:red">COOKIE_MISSING</p>
<p>The "proxytest" cookie was not received. The proxy may not be stripping the Domain attribute.</p>
</body></html>`)
			return
		}
		fmt.Fprint(w, `<!DOCTYPE html><html><head></head><body>
<h1>Step 6: Read Cookie</h1>
<p id="cookie-status" style="color:green">COOKIE_OK</p>
<a id="next" href="/step/7">Next &rarr;</a>
</body></html>`)
	})

	// Step 7: POST form + 302 redirect with full backend URL
	mux.HandleFunc("/step/7", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			fmt.Fprint(w, `<!DOCTYPE html><html><head></head><body>
<h1>Step 7: POST + Redirect</h1>
<p>This form POSTs to the server, which responds with a 302 redirect using a full backend URL.</p>
<form id="step7form" method="POST" action="/step/7">
<input type="hidden" name="action" value="go">
<button id="next" type="submit">Next &rarr;</button>
</form>
</body></html>`)
			return
		}
		// POST handler: set a second cookie and redirect with full backend URL
		http.SetCookie(w, &http.Cookie{
			Name:  "proxytest2",
			Value: "world456",
			Path:  "/",
		})
		// Intentionally use full backend URL — proxy must rewrite this!
		// Uses the request Host so it works when accessed via IP or hostname.
		backendOrigin := "http://" + r.Host
		w.Header().Set("Location", backendOrigin+"/step/8")
		w.WriteHeader(http.StatusFound)
	})

	// Step 8: final verification — check both cookies + static assets
	mux.HandleFunc("/step/8", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		c1, err1 := r.Cookie("proxytest")
		c2, err2 := r.Cookie("proxytest2")
		cookieStatus := "COOKIES_MISSING"
		if err1 == nil && c1.Value == "hello123" && err2 == nil && c2.Value == "world456" {
			cookieStatus = "ALL_COOKIES_OK"
		}
		fmt.Fprintf(w, `<!DOCTYPE html><html><head>
<link rel="stylesheet" href="/static/final.css">
</head><body>
<h1>Step 8: Final Verification</h1>
<p id="cookie-status">%s</p>
<p id="js-check">Waiting for JS...</p>
<p id="result">PENDING</p>
<script src="/static/final.js"></script>
</body></html>`, cookieStatus)
	})

	// Static CSS for step 8
	mux.HandleFunc("/static/final.css", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/css")
		fmt.Fprint(w, `body { font-family: sans-serif; margin: 2rem; }
#result { font-size: 1.5rem; font-weight: bold; margin-top: 1rem; }`)
	})

	// Static JS for step 8 — checks cookies + asset loading before declaring success
	mux.HandleFunc("/static/final.js", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/javascript")
		fmt.Fprint(w, `(function() {
  document.getElementById('js-check').textContent = 'ASSETS_OK';
  var cookieOk = document.getElementById('cookie-status').textContent === 'ALL_COOKIES_OK';
  var assetsOk = document.getElementById('js-check').textContent === 'ASSETS_OK';
  if (cookieOk && assetsOk) {
    document.getElementById('result').textContent = 'ALL STEPS PASSED';
    document.getElementById('result').style.color = 'green';
  } else {
    document.getElementById('result').textContent = 'FAILED';
    document.getElementById('result').style.color = 'red';
  }
})();`)
	})

	log.Printf("Example server listening on :%s", port)
	if err := http.ListenAndServe(":"+port, mux); err != nil {
		log.Fatalf("server error: %v", err)
	}
}
