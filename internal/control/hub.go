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
	hub  *Hub
	conn net.Conn
	send chan []byte
	done chan struct{}
}

// Hub manages all active WebSocket connections and event broadcasting.
type Hub struct {
	clients    map[*client]struct{}
	mu         sync.RWMutex
	broadcast  chan []byte
	register   chan *client
	unregister chan *client
}

// NewHub creates an initialised Hub ready to run.
func NewHub() *Hub {
	return &Hub{
		clients:    make(map[*client]struct{}),
		broadcast:  make(chan []byte, 256),
		register:   make(chan *client, 16),
		unregister: make(chan *client, 16),
	}
}

// Run processes hub events; call it in its own goroutine.
func (h *Hub) Run() {
	for {
		select {
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
	select {
	case h.broadcast <- data:
	default:
		log.Printf("[control/hub] broadcast channel full, dropping event %s", evtType)
	}
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

		var req Message
		if err := json.Unmarshal(data, &req); err != nil {
			c.sendError("", "parse", "invalid JSON: "+err.Error())
			continue
		}

		resp := srv.handleRequest(req)
		if resp == nil {
			continue
		}
		out, err := json.Marshal(resp)
		if err != nil {
			log.Printf("[control/hub] marshal response error: %v", err)
			continue
		}
		select {
		case c.send <- out:
		case <-c.done:
			return
		}
	}
}

func (c *client) sendError(id, reqType, msg string) {
	resp := ErrorResponse(id, reqType, msg)
	out, _ := json.Marshal(resp)
	select {
	case c.send <- out:
	default:
	}
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
