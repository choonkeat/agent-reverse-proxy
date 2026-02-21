package agentproxy

import (
	"log"
	"sync"

	"github.com/gorilla/websocket"
)

// DebugHub manages WebSocket connections between iframe debug scripts, agent,
// and UI observers. It also provides an in-process channel for MCP tools to
// receive iframe messages directly (without a WebSocket self-connection).
type DebugHub struct {
	iframeClients map[*websocket.Conn]bool // Connected iframe debug scripts
	agentConn     *websocket.Conn          // Connected agent (only one allowed, for backward compat)
	uiObservers   map[*websocket.Conn]bool // UI observers (receive iframe messages, read-only)
	mu            sync.RWMutex

	// In-process subscribers: MCP tools subscribe here to receive iframe messages
	inProcMu   sync.Mutex
	inProcSubs map[chan []byte]struct{}
}

// NewDebugHub creates a new DebugHub.
func NewDebugHub() *DebugHub {
	return &DebugHub{
		iframeClients: make(map[*websocket.Conn]bool),
		uiObservers:   make(map[*websocket.Conn]bool),
		inProcSubs:    make(map[chan []byte]struct{}),
	}
}

// AddIframeClient registers an iframe debug script connection.
func (h *DebugHub) AddIframeClient(conn *websocket.Conn) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.iframeClients[conn] = true
	log.Printf("[DebugHub] Iframe client connected (total: %d)", len(h.iframeClients))
}

// RemoveIframeClient unregisters an iframe debug script connection.
func (h *DebugHub) RemoveIframeClient(conn *websocket.Conn) {
	h.mu.Lock()
	defer h.mu.Unlock()
	delete(h.iframeClients, conn)
	log.Printf("[DebugHub] Iframe client disconnected (total: %d)", len(h.iframeClients))
}

// SetAgent registers the agent WebSocket connection (replaces existing if any).
func (h *DebugHub) SetAgent(conn *websocket.Conn) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.agentConn != nil {
		h.agentConn.Close()
	}
	h.agentConn = conn
	log.Printf("[DebugHub] Agent connected")
}

// RemoveAgent unregisters the agent WebSocket connection.
func (h *DebugHub) RemoveAgent(conn *websocket.Conn) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.agentConn == conn {
		h.agentConn = nil
		log.Printf("[DebugHub] Agent disconnected")
	}
}

// AddUIObserver registers a UI observer connection.
func (h *DebugHub) AddUIObserver(conn *websocket.Conn) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.uiObservers[conn] = true
	log.Printf("[DebugHub] UI observer connected (total: %d)", len(h.uiObservers))
}

// RemoveUIObserver unregisters a UI observer connection.
func (h *DebugHub) RemoveUIObserver(conn *websocket.Conn) {
	h.mu.Lock()
	defer h.mu.Unlock()
	delete(h.uiObservers, conn)
	log.Printf("[DebugHub] UI observer disconnected (total: %d)", len(h.uiObservers))
}

// ForwardToAgent sends a message from iframe to the connected WS agent,
// all UI observers, and all in-process subscribers.
func (h *DebugHub) ForwardToAgent(msg []byte) {
	h.mu.RLock()
	agent := h.agentConn
	observers := make([]*websocket.Conn, 0, len(h.uiObservers))
	for conn := range h.uiObservers {
		observers = append(observers, conn)
	}
	h.mu.RUnlock()

	if agent != nil {
		if err := agent.WriteMessage(websocket.TextMessage, msg); err != nil {
			log.Printf("[DebugHub] Error forwarding to agent: %v", err)
		}
	}

	for _, conn := range observers {
		if err := conn.WriteMessage(websocket.TextMessage, msg); err != nil {
			log.Printf("[DebugHub] Error forwarding to UI observer: %v", err)
		}
	}

	// Fan out to in-process subscribers (non-blocking)
	msgCopy := make([]byte, len(msg))
	copy(msgCopy, msg)
	h.inProcMu.Lock()
	for ch := range h.inProcSubs {
		select {
		case ch <- msgCopy:
		default:
			// subscriber channel full, drop message
		}
	}
	h.inProcMu.Unlock()
}

// SendToUIObservers sends a message to all connected UI observers only.
func (h *DebugHub) SendToUIObservers(msg []byte) {
	h.mu.RLock()
	observers := make([]*websocket.Conn, 0, len(h.uiObservers))
	for conn := range h.uiObservers {
		observers = append(observers, conn)
	}
	h.mu.RUnlock()

	for _, conn := range observers {
		if err := conn.WriteMessage(websocket.TextMessage, msg); err != nil {
			log.Printf("[DebugHub] Error sending to UI observer: %v", err)
		}
	}
}

// ForwardToIframes sends a message from agent to all connected iframes.
func (h *DebugHub) ForwardToIframes(msg []byte) {
	h.mu.RLock()
	clients := make([]*websocket.Conn, 0, len(h.iframeClients))
	for conn := range h.iframeClients {
		clients = append(clients, conn)
	}
	h.mu.RUnlock()

	for _, conn := range clients {
		if err := conn.WriteMessage(websocket.TextMessage, msg); err != nil {
			log.Printf("[DebugHub] Error forwarding to iframe: %v", err)
		}
	}
}

// Subscribe creates an in-process subscription channel that receives all
// messages forwarded from iframe clients. Call Unsubscribe when done.
func (h *DebugHub) Subscribe() chan []byte {
	ch := make(chan []byte, 64)
	h.inProcMu.Lock()
	h.inProcSubs[ch] = struct{}{}
	h.inProcMu.Unlock()
	return ch
}

// Unsubscribe removes an in-process subscription channel.
func (h *DebugHub) Unsubscribe(ch chan []byte) {
	h.inProcMu.Lock()
	delete(h.inProcSubs, ch)
	h.inProcMu.Unlock()
}

// SendQuery sends a query message to all connected iframe clients.
// Used by MCP tools to send DOM queries in-process.
func (h *DebugHub) SendQuery(msg []byte) {
	h.ForwardToIframes(msg)
}
