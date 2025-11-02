package sse

import (
	"log"
	"maps"
	"os"
	"time"

	"github.com/aaronzipp/you-are-officially-sus/internal/game"
	"github.com/aaronzipp/you-are-officially-sus/internal/models"
)

var debug bool

func init() {
	debug = os.Getenv("DEBUG") != ""
}

// AddClient adds a new SSE client to the lobby
func AddClient(lobby *models.Lobby, client chan models.SSEMessage, playerID string) {
	lobby.Lock()
	defer lobby.Unlock()

	// Warn if the same player has multiple SSE connections
	dup := 0
	clients := lobby.GetSSEClients()
	for _, pid := range clients {
		if pid == playerID {
			dup++
		}
	}
	if dup > 0 {
		log.Printf("WARN: player %s opened %d additional SSE connection(s)", playerID, dup)
	}
	lobby.AddSSEClient(client, playerID)
}

// RemoveClient removes an SSE client from the lobby
func RemoveClient(lobby *models.Lobby, client chan models.SSEMessage) {
	lobby.Lock()
	defer lobby.Unlock()
	lobby.RemoveSSEClient(client)
	log.Printf("removeSSEClient: client removed, now have %d total clients", lobby.SSEClientCount())
}

// Broadcast sends a message to all connected SSE clients
func Broadcast(lobby *models.Lobby, event, data string) {
	lobby.RLock()
	// Collect all client channels while holding the lock
	clients := lobby.GetSSEClients()
	clientCount := len(clients)
	lobby.RUnlock()

	if debug {
		log.Printf("broadcastSSE: event=%s to %d clients", event, clientCount)
	}

	// Send messages WITHOUT holding the lock
	msg := models.SSEMessage{Event: event, Data: data}
	successCount := 0
	for client := range clients {
		select {
		case client <- msg:
			successCount++
		case <-time.After(time.Duration(game.SSETimeoutSeconds) * time.Second):
			if debug {
				log.Printf("broadcastSSE: timeout sending to client")
			}
		}
	}
	if debug {
		log.Printf("broadcastSSE: sent to %d/%d clients successfully", successCount, clientCount)
	}
}

// BroadcastPersonalized sends personalized messages to each client
func BroadcastPersonalized(lobby *models.Lobby, renderFunc func(playerID string) string, eventName string) {
	lobby.RLock()
	// Collect all client channels and their player IDs while holding the lock
	clientMap := maps.Clone(lobby.GetSSEClients())
	lobby.RUnlock()

	// Send personalized messages WITHOUT holding the lock
	for client, playerID := range clientMap {
		html := renderFunc(playerID)
		msg := models.SSEMessage{Event: eventName, Data: html}
		select {
		case client <- msg:
			// Message sent successfully
		case <-time.After(time.Duration(game.SSETimeoutSeconds) * time.Second):
			// Timeout - skip this client to avoid blocking
		}
	}
}
