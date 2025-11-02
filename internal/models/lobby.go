package models

import "sync"

// Lobby represents a persistent game lobby
type Lobby struct {
	Code        string
	Host        string
	Players     map[string]*Player      // playerID -> Player
	Scores      map[string]*PlayerScore // playerID -> PlayerScore (persistent)
	CurrentGame *Game                   // nil when in lobby
	mu          sync.RWMutex
	sseClients  map[chan SSEMessage]string // channel -> playerID
}

// SSEMessage represents a message sent via Server-Sent Events
type SSEMessage struct {
	Event string // Event type (e.g., "player-update", "nav-redirect")
	Data  string // HTML content or data to send
}

// Lock acquires the lobby's write lock
func (l *Lobby) Lock() {
	l.mu.Lock()
}

// Unlock releases the lobby's write lock
func (l *Lobby) Unlock() {
	l.mu.Unlock()
}

// RLock acquires the lobby's read lock
func (l *Lobby) RLock() {
	l.mu.RLock()
}

// RUnlock releases the lobby's read lock
func (l *Lobby) RUnlock() {
	l.mu.RUnlock()
}

// GetSSEClients returns a copy of the SSE clients map (must be called with lock held)
func (l *Lobby) GetSSEClients() map[chan SSEMessage]string {
	clients := make(map[chan SSEMessage]string, len(l.sseClients))
	for k, v := range l.sseClients {
		clients[k] = v
	}
	return clients
}

// AddSSEClient adds a new SSE client to the lobby
func (l *Lobby) AddSSEClient(client chan SSEMessage, playerID string) {
	if l.sseClients == nil {
		l.sseClients = make(map[chan SSEMessage]string)
	}
	l.sseClients[client] = playerID
}

// RemoveSSEClient removes an SSE client from the lobby
func (l *Lobby) RemoveSSEClient(client chan SSEMessage) {
	delete(l.sseClients, client)
}

// SSEClientCount returns the number of connected SSE clients
func (l *Lobby) SSEClientCount() int {
	return len(l.sseClients)
}
