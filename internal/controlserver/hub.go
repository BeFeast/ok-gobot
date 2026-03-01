package controlserver

import (
	"encoding/json"
	"log"
	"net"
	"sync"

	"github.com/gobwas/ws/wsutil"
)

// wsClient represents a connected WebSocket TUI client.
type wsClient struct {
	conn net.Conn
	mu   sync.Mutex
}

func (c *wsClient) send(msg ServerMsg) error {
	data, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return wsutil.WriteServerText(c.conn, data)
}

// Hub manages connected WebSocket clients and broadcasts events.
type Hub struct {
	mu      sync.RWMutex
	clients map[*wsClient]struct{}
}

// NewHub creates a new Hub.
func NewHub() *Hub {
	return &Hub{
		clients: make(map[*wsClient]struct{}),
	}
}

func (h *Hub) add(c *wsClient) {
	h.mu.Lock()
	h.clients[c] = struct{}{}
	h.mu.Unlock()
}

func (h *Hub) remove(c *wsClient) {
	h.mu.Lock()
	delete(h.clients, c)
	h.mu.Unlock()
}

// Broadcast sends a message to all connected clients.
func (h *Hub) Broadcast(msg ServerMsg) {
	h.mu.RLock()
	clients := make([]*wsClient, 0, len(h.clients))
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

// Count returns the number of connected clients.
func (h *Hub) Count() int {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return len(h.clients)
}
