package session

import "sync"

// Transport identifies the delivery channel for a session.
type Transport string

const (
	TransportTelegram Transport = "telegram"
	TransportInternal Transport = "internal"
)

// DeliveryRoute holds the information needed to deliver a reply to the originating chat.
type DeliveryRoute struct {
	Channel  Transport
	ChatID   int64
	ThreadID int   // 0 when the message is not inside a forum topic
	UserID   int64 // Telegram user ID; 0 for group chats without per-user scope
}

// RouteStore maps canonical session keys to their delivery routes.
// It is safe for concurrent use.
type RouteStore struct {
	mu     sync.RWMutex
	routes map[string]DeliveryRoute
}

// NewRouteStore creates an empty RouteStore.
func NewRouteStore() *RouteStore {
	return &RouteStore{routes: make(map[string]DeliveryRoute)}
}

// Set stores the delivery route for the given session key, overwriting any
// existing entry.
func (rs *RouteStore) Set(key string, route DeliveryRoute) {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	rs.routes[key] = route
}

// Get retrieves the delivery route for the given session key.
// The boolean return value is false when the key is not found.
func (rs *RouteStore) Get(key string) (DeliveryRoute, bool) {
	rs.mu.RLock()
	defer rs.mu.RUnlock()
	r, ok := rs.routes[key]
	return r, ok
}

// Delete removes the route for the given session key.
func (rs *RouteStore) Delete(key string) {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	delete(rs.routes, key)
}
