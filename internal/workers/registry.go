package workers

import (
	"fmt"
	"sync"
)

// Registry stores available worker adapters and the default backend.
type Registry struct {
	mu          sync.RWMutex
	adapters    map[string]Adapter
	defaultName string
}

// NewRegistry creates an empty worker registry.
func NewRegistry() *Registry {
	return &Registry{
		adapters: make(map[string]Adapter),
	}
}

// Register stores an adapter under its Name().
func (r *Registry) Register(adapter Adapter, isDefault bool) {
	if adapter == nil {
		return
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	r.adapters[adapter.Name()] = adapter
	if isDefault || r.defaultName == "" {
		r.defaultName = adapter.Name()
	}
}

// Get retrieves an adapter by name.
func (r *Registry) Get(name string) (Adapter, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	adapter, ok := r.adapters[name]
	return adapter, ok
}

// MustDefault returns the default adapter or an error if no adapter is registered.
func (r *Registry) MustDefault() (Adapter, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	if r.defaultName == "" {
		return nil, fmt.Errorf("no worker adapters registered")
	}

	adapter, ok := r.adapters[r.defaultName]
	if !ok {
		return nil, fmt.Errorf("default worker adapter %q not registered", r.defaultName)
	}
	return adapter, nil
}

// DefaultName returns the configured default worker backend name.
func (r *Registry) DefaultName() string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.defaultName
}

// List returns operator-facing worker metadata.
func (r *Registry) List() []Info {
	r.mu.RLock()
	defer r.mu.RUnlock()

	out := make([]Info, 0, len(r.adapters))
	for name, adapter := range r.adapters {
		out = append(out, Info{
			Name:        name,
			Description: adapter.Description(),
			Binary:      adapter.Binary(),
			Default:     name == r.defaultName,
		})
	}
	return out
}
