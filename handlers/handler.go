// Package handlers provides pluggable packet handler support for network listeners.
//
// Packet handlers process incoming network data from UDP/TCP listeners. Each handler
// implements protocol-specific parsing (e.g., WSJT-X binary protocol, N1MM XML).
//
// Handlers are registered at init() time via Register() and instantiated by the
// listeners service based on configuration.
package handlers

import (
	"net"
	"sync"
)

// Packet represents an incoming network packet with metadata.
type Packet struct {
	// Data is the raw packet payload.
	Data []byte

	// RemoteAddr is the source address of the packet.
	RemoteAddr net.Addr

	// LocalAddr is the local address the packet was received on.
	LocalAddr net.Addr

	// Protocol is the transport protocol ("udp" or "tcp").
	Protocol string

	// ListenerName is the configured name of the listener that received this packet.
	ListenerName string
}

// PacketHandler processes incoming network packets.
// Implementations are protocol-specific (WSJT-X, N1MM, etc.)
//
// Handlers must be safe for concurrent use from multiple goroutines.
type PacketHandler interface {
	// Name returns the handler identifier (e.g., "wsjtx", "n1mm").
	Name() string

	// Handle processes a packet. Returns an error if processing fails.
	// Implementations should be idempotent where possible.
	Handle(pkt Packet) error

	// Close releases any resources held by the handler.
	// Called when the listener is stopped.
	Close() error
}

// HandlerFactory creates a new PacketHandler instance.
// The config map contains handler-specific configuration from ListenerConfig.HandlerConfig.
type HandlerFactory func(config map[string]any) (PacketHandler, error)

var (
	registryMu sync.RWMutex
	registry   = make(map[string]HandlerFactory)
)

// Register adds a handler factory to the global registry.
// This is typically called from an init() function in each handler package.
// Panics if a handler with the same name is already registered.
func Register(name string, factory HandlerFactory) {
	registryMu.Lock()
	defer registryMu.Unlock()

	if _, exists := registry[name]; exists {
		panic("handlers: duplicate handler registration: " + name)
	}
	registry[name] = factory
}

// Get retrieves a handler factory by name.
// Returns nil, false if no handler with that name is registered.
func Get(name string) (HandlerFactory, bool) {
	registryMu.RLock()
	defer registryMu.RUnlock()
	f, ok := registry[name]
	return f, ok
}

// List returns all registered handler names.
func List() []string {
	registryMu.RLock()
	defer registryMu.RUnlock()

	names := make([]string, 0, len(registry))
	for name := range registry {
		names = append(names, name)
	}
	return names
}
