package control

import (
	"encoding/json"
	"log"
	"net"
	"sync"

	"github.com/gobwas/ws"
	"github.com/gobwas/ws/wsutil"
)

// client represents a single connected WebSocket client.
type client struct {
	hub          *Hub
	conn         net.Conn
	send         chan []byte
	done         chan struct{}
	tuiConnected bool
	tuiSessionID string
}

// Hub manages all active WebSocket connections and event broadcasting.
type Hub struct {
	clients    map[*client]struct{}
	mu         sync.RWMutex
	broadcast  chan []byte
	register   chan *client
	unregister chan *client
	stop       chan struct{}
}

// NewHub creates an initialised Hub ready to run.
func NewHub() *Hub {
	return &Hub{
		clients:    make(map[*client]struct{}),
		broadcast:  make(chan []byte, 256),
		register:   make(chan *client, 16),
		unregister: make(chan *client, 16),
		stop:       make(chan struct{}),
	}
}

// Run processes hub events; call it in its own goroutine.
// Returns when Stop is called.
func (h *Hub) Run() {
	for {
		select {
		case <-h.stop:
			h.mu.Lock()
			for c := range h.clients {
				close(c.send)
				delete(h.clients, c)
			}
			h.mu.Unlock()
			return

		case c := <-h.register:
			h.mu.Lock()
			h.clients[c] = struct{}{}
			h.mu.Unlock()
			log.Printf("[control/hub] client connected (%s)", c.conn.RemoteAddr())

		case c := <-h.unregister:
			h.mu.Lock()
			if _, ok := h.clients[c]; ok {
				delete(h.clients, c)
				close(c.send)
			}
			h.mu.Unlock()
			log.Printf("[control/hub] client disconnected (%s)", c.conn.RemoteAddr())

		case msg := <-h.broadcast:
			h.mu.RLock()
			for c := range h.clients {
				select {
				case c.send <- msg:
				default:
					// Slow client — drop the message rather than block.
				}
			}
			h.mu.RUnlock()
		}
	}
}

// Stop signals the hub run loop to exit and disconnects all clients.
func (h *Hub) Stop() {
	select {
	case h.stop <- struct{}{}:
	default:
	}
}

// Count returns the number of connected clients.
func (h *Hub) Count() int {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return len(h.clients)
}

// Emit serialises an event and broadcasts it to every connected client.
func (h *Hub) Emit(evtType string, payload interface{}) {
	evt, err := NewEvent(evtType, payload)
	if err != nil {
		log.Printf("[control/hub] failed to marshal event %s: %v", evtType, err)
		return
	}
	data, err := json.Marshal(evt)
	if err != nil {
		log.Printf("[control/hub] failed to encode event %s: %v", evtType, err)
		return
	}

	h.BroadcastRaw(data)

	// Also mirror legacy events to the TUI websocket protocol so the Bubble Tea
	// client can consume runtime updates from the main control server.
	for _, tuiMsg := range legacyEventToTUI(evtType, payload) {
		h.BroadcastTUI(tuiMsg)
	}
}

// BroadcastRaw sends a pre-encoded websocket text frame payload to all clients.
func (h *Hub) BroadcastRaw(data []byte) {
	select {
	case h.broadcast <- data:
	default:
		log.Printf("[control/hub] broadcast channel full, dropping message")
	}
}

// BroadcastTUI encodes and broadcasts a TUI protocol message to all clients.
func (h *Hub) BroadcastTUI(msg ServerMsg) {
	data, err := json.Marshal(msg)
	if err != nil {
		log.Printf("[control/hub] failed to encode TUI message %s: %v", msg.Type, err)
		return
	}
	h.BroadcastRaw(data)
}

// addClient registers c and starts its read/write pumps.
func (h *Hub) addClient(conn net.Conn, srv *Server) {
	c := &client{
		hub:  h,
		conn: conn,
		send: make(chan []byte, 64),
		done: make(chan struct{}),
	}
	h.register <- c
	go c.writePump()
	go c.readPump(srv)
}

func (c *client) writePump() {
	defer func() {
		c.conn.Close()
		close(c.done)
	}()
	for msg := range c.send {
		if err := wsutil.WriteServerText(c.conn, msg); err != nil {
			log.Printf("[control/hub] write error: %v", err)
			return
		}
	}
}

func (c *client) readPump(srv *Server) {
	defer func() {
		c.hub.unregister <- c
		c.conn.Close()
	}()
	for {
		data, op, err := wsutil.ReadClientData(c.conn)
		if err != nil {
			if !isClosedErr(err) {
				log.Printf("[control/hub] read error: %v", err)
			}
			return
		}
		if op != ws.OpText {
			continue
		}

		var tuiReq ClientMsg
		if err := json.Unmarshal(data, &tuiReq); err == nil && isTUICommand(tuiReq.Type) {
			srv.handleTUIRequest(c, tuiReq)
			continue
		}

		var req Message
		if err := json.Unmarshal(data, &req); err != nil {
			c.sendError("", "parse", "invalid JSON: "+err.Error())
			continue
		}

		resp := srv.handleRequest(req)
		if resp == nil {
			continue
		}
		c.sendMessage(*resp)
	}
}

func (c *client) sendError(id, reqType, msg string) {
	c.sendMessage(ErrorResponse(id, reqType, msg))
}

func (c *client) sendMessage(msg Message) {
	out, err := json.Marshal(msg)
	if err != nil {
		log.Printf("[control/hub] marshal response error: %v", err)
		return
	}
	select {
	case c.send <- out:
	default:
	}
}

func (c *client) sendTUIMsg(msg ServerMsg) {
	out, err := json.Marshal(msg)
	if err != nil {
		log.Printf("[control/hub] marshal TUI response error: %v", err)
		return
	}
	select {
	case c.send <- out:
	case <-c.done:
	}
}

func (c *client) sendTUIError(msg string) {
	c.sendTUIMsg(ServerMsg{Type: MsgTypeError, Message: msg})
}

func isClosedErr(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return msg == "EOF" ||
		msg == "use of closed network connection" ||
		msg == "wsutil: unexpected EOF"
}
