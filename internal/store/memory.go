package store

import (
	"sync"

	"github.com/aaronzipp/you-are-officially-sus/internal/models"
)

// LobbyStore manages lobby storage
type LobbyStore struct {
	lobbies map[string]*models.Lobby
	mu      sync.RWMutex
}

// NewLobbyStore creates a new lobby store
func NewLobbyStore() *LobbyStore {
	return &LobbyStore{
		lobbies: make(map[string]*models.Lobby),
	}
}

// Get retrieves a lobby by code
func (s *LobbyStore) Get(code string) (*models.Lobby, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	lobby, exists := s.lobbies[code]
	return lobby, exists
}

// Set stores a lobby
func (s *LobbyStore) Set(code string, lobby *models.Lobby) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lobbies[code] = lobby
}

// Delete removes a lobby
func (s *LobbyStore) Delete(code string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.lobbies, code)
}

// Exists checks if a lobby code exists
func (s *LobbyStore) Exists(code string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	_, exists := s.lobbies[code]
	return exists
}
