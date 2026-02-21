// Command example is an HTTP server that exercises reverse proxy edge cases.
// It serves an 8-step linear flow where each page tests a specific proxy feature.
//
// HTML, CSS, and JS content lives in the content/ directory and is embedded at
// compile time so the binary remains self-contained.
package main

import (
	"bytes"
	"embed"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"log"
	"net/http"
	"os"
	"strings"
)

//go:embed content
var content embed.FS

var greenPNG []byte

func init() {
	img := image.NewRGBA(image.Rect(0, 0, 1, 1))
	img.Set(0, 0, color.RGBA{0, 255, 0, 255})
	var buf bytes.Buffer
	png.Encode(&buf, img)
	greenPNG = buf.Bytes()
}

func mustRead(name string) []byte {
	data, err := content.ReadFile("content/" + name)
	if err != nil {
		panic(fmt.Sprintf("missing embedded file %s: %v", name, err))
	}
	return data
}

func serveHTML(w http.ResponseWriter, name string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write(mustRead(name))
}

// responseLogger wraps http.ResponseWriter to capture status code and headers for logging.
type responseLogger struct {
	http.ResponseWriter
	status int
}

func (rl *responseLogger) WriteHeader(code int) {
	rl.status = code
	rl.ResponseWriter.WriteHeader(code)
}

func (rl *responseLogger) Write(b []byte) (int, error) {
	if rl.status == 0 {
		rl.status = 200
	}
	return rl.ResponseWriter.Write(b)
}

func withLogging(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rl := &responseLogger{ResponseWriter: w}
		h.ServeHTTP(rl, r)
		if rl.status == 0 {
			rl.status = 200
		}
		loc := rl.Header().Get("Location")
		cookie := rl.Header().Get("Set-Cookie")
		extra := ""
		if loc != "" {
			extra += fmt.Sprintf(" Location: %s", loc)
		}
		if cookie != "" {
			extra += fmt.Sprintf(" Set-Cookie: %s", cookie)
		}
		log.Printf("%s %s -> %d%s", r.Method, r.URL.Path, rl.status, extra)
	})
}

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
		serveHTML(w, "step1.html")
	})

	// Relative CSS for step 1 (url(bg-check.png) resolves to /styles/bg-check.png)
	mux.HandleFunc("/styles/home.css", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/css")
		w.Write(mustRead("home.css"))
	})

	// Step 2: relative JS (src="step2.js" resolves to /step/step2.js)
	mux.HandleFunc("/step/2", func(w http.ResponseWriter, r *http.Request) {
		serveHTML(w, "step2.html")
	})

	// JS file for step 2 (served at /step/step2.js)
	mux.HandleFunc("/step/step2.js", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/javascript")
		w.Write(mustRead("step2.js"))
	})

	// Step 3: relative fetch (fetch('3/data') resolves to /step/3/data)
	mux.HandleFunc("/step/3", func(w http.ResponseWriter, r *http.Request) {
		serveHTML(w, "step3.html")
	})

	// Data endpoint for step 3
	mux.HandleFunc("/step/3/data", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"message":"RELATIVE_FETCH_OK"}`)
	})

	// Step 4: absolute path fetch
	mux.HandleFunc("/step/4", func(w http.ResponseWriter, r *http.Request) {
		serveHTML(w, "step4.html")
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
		serveHTML(w, "step5.html")
	})

	// Step 6: read cookie
	mux.HandleFunc("/step/6", func(w http.ResponseWriter, r *http.Request) {
		c, err := r.Cookie("proxytest")
		if err != nil || c.Value != "hello123" {
			serveHTML(w, "step6-missing.html")
			return
		}
		serveHTML(w, "step6-ok.html")
	})

	// Step 7: POST form + 302 redirect with full backend URL
	mux.HandleFunc("/step/7", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			serveHTML(w, "step7.html")
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
		c1, err1 := r.Cookie("proxytest")
		c2, err2 := r.Cookie("proxytest2")
		cookieStatus := "COOKIES_MISSING"
		if err1 == nil && c1.Value == "hello123" && err2 == nil && c2.Value == "world456" {
			cookieStatus = "ALL_COOKIES_OK"
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		html := strings.ReplaceAll(string(mustRead("step8.html")), "{{COOKIE_STATUS}}", cookieStatus)
		fmt.Fprint(w, html)
	})

	// Static CSS for step 8 (url(final-bg.png) resolves to /static/final-bg.png)
	mux.HandleFunc("/static/final.css", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/css")
		w.Write(mustRead("final.css"))
	})

	// Static JS for step 8 — checks cookies + asset loading before declaring success
	mux.HandleFunc("/static/final.js", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/javascript")
		w.Write(mustRead("final.js"))
	})

	// 1x1 green PNG for CSS url() tests
	servePNG := func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		w.Write(greenPNG)
	}
	mux.HandleFunc("/styles/bg-check.png", servePNG)  // relative url() from /styles/home.css
	mux.HandleFunc("/static/check.png", servePNG)      // absolute url() from /styles/home.css
	mux.HandleFunc("/static/final-bg.png", servePNG)   // relative url() from /static/final.css

	log.Printf("Example server listening on :%s", port)
	if err := http.ListenAndServe(":"+port, withLogging(mux)); err != nil {
		log.Fatalf("server error: %v", err)
	}
}
