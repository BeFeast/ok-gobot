package control

import (
	"encoding/json"
	"log"
	"net"
	"sync"

	"github.com/gobwas/ws/wsutil"
)

// tuiClient represents a connected WebSocket TUI client.
type tuiClient struct {
	conn net.Conn
	mu   sync.Mutex
}

func (c *tuiClient) send(msg ServerMsg) error {
	data, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return wsutil.WriteServerText(c.conn, data)
}

// tuiHub manages connected WebSocket TUI clients and broadcasts events.
type tuiHub struct {
	mu      sync.RWMutex
	clients map[*tuiClient]struct{}
}

func newTUIHub() *tuiHub {
	return &tuiHub{
		clients: make(map[*tuiClient]struct{}),
	}
}

func (h *tuiHub) add(c *tuiClient) {
	h.mu.Lock()
	h.clients[c] = struct{}{}
	h.mu.Unlock()
}

func (h *tuiHub) remove(c *tuiClient) {
	h.mu.Lock()
	delete(h.clients, c)
	h.mu.Unlock()
}

// Broadcast sends a message to all connected clients.
func (h *tuiHub) Broadcast(msg ServerMsg) {
	h.mu.RLock()
	clients := make([]*tuiClient, 0, len(h.clients))
	for c := range h.clients {
		clients = append(clients, c)
	}
	h.mu.RUnlock()

	for _, c := range clients {
		if err := c.send(msg); err != nil {
			log.Printf("[hub] send error: %v", err)
		}
	}
}

func (h *tuiHub) Count() int {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return len(h.clients)
}
