package main

import (
	"fmt"
	"log"
	"net/http"
	"time"

	"github.com/gorilla/websocket"
)

var upgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool { return true },
}

func main() {
	http.HandleFunc("/ws", func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			log.Printf("upgrade error: %v", err)
			return
		}
		defer conn.Close()
		log.Println("client connected")
		for {
			_, msg, err := conn.ReadMessage()
			if err != nil {
				log.Printf("read error: %v", err)
				return
			}
			reply := fmt.Sprintf("[%s] %s", time.Now().Format("15:04:05.000"), string(msg))
			log.Printf("recv: %s -> %s", string(msg), reply)
			if err := conn.WriteMessage(websocket.TextMessage, []byte(reply)); err != nil {
				log.Printf("write error: %v", err)
				return
			}
		}
	})

	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, page)
	})

	log.Println("WebSocket test server on :3001")
	log.Fatal(http.ListenAndServe(":3001", nil))
}

const page = `<!DOCTYPE html>
<html><head><title>WS Test</title></head>
<body>
<h3>WebSocket Test</h3>
<input id="msg" placeholder="type a message..." autofocus>
<button onclick="send()">Send</button>
<pre id="log"></pre>
<script>
var ws = new WebSocket("ws://" + location.host + "/ws");
var out = document.getElementById("log");
ws.onopen = function() { out.textContent += "connected\n"; };
ws.onmessage = function(e) { out.textContent += "< " + e.data + "\n"; };
ws.onclose = function() { out.textContent += "disconnected\n"; };
ws.onerror = function() { out.textContent += "error\n"; };
function send() {
  var m = document.getElementById("msg");
  if (!m.value) return;
  out.textContent += "> " + m.value + "\n";
  ws.send(m.value);
  m.value = "";
}
document.getElementById("msg").addEventListener("keydown", function(e) {
  if (e.key === "Enter") send();
});
</script>
</body></html>`
