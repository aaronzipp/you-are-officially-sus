package handlers

import (
	"fmt"
	"log"
	"net/http"
	"strings"

	"github.com/aaronzipp/you-are-officially-sus/internal/game"
	"github.com/aaronzipp/you-are-officially-sus/internal/models"
	"github.com/aaronzipp/you-are-officially-sus/internal/sse"
)

// HandleSSE handles Server-Sent Events for real-time updates
func (ctx *Context) HandleSSE(w http.ResponseWriter, r *http.Request) {
	if debug {
		log.Printf("handleSSE called: %s", r.URL.Path)
	}

	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/sse/"), "/")
	if len(parts) < 1 || len(parts) > 2 {
		if debug {
			log.Printf("handleSSE: invalid URL parts=%v", parts)
		}
		http.Error(w, "Invalid URL", http.StatusBadRequest)
		return
	}
	roomCode := parts[0]
	var playerID string

	if len(parts) == 2 {
		// Legacy style: /sse/:room/:player
		playerID = parts[1]
	} else {
		// Cookie-based: /sse/:room
		lobby, pid, err := ctx.getLobbyAndPlayer(r, roomCode)
		if err != nil {
			// Not authorized or lobby validation failed: instruct client to navigate home via HTMX snippet
			w.Header().Set("Content-Type", "text/event-stream")
			w.Header().Set("Cache-Control", "no-cache")
			w.Header().Set("Connection", "keep-alive")
			fmt.Fprintf(w, "event: %s\ndata: %s\n\n", sse.EventNavRedirect, ctx.RedirectSnippet(roomCode, "/"))
			if flusher, ok := w.(http.Flusher); ok {
				flusher.Flush()
			}
			return
		}
		_ = lobby // validated but not needed yet
		playerID = pid
	}

	if debug {
		log.Printf("handleSSE: roomCode=%s playerID=%s", roomCode, playerID)
	}

	lobby, exists := ctx.LobbyStore.Get(roomCode)
	if !exists {
		if debug {
			log.Printf("handleSSE: room %s not found, sending nav-redirect to home", roomCode)
		}
		// Set SSE headers
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")

		// Send nav-redirect snippet for HTMX to navigate home
		fmt.Fprintf(w, "event: %s\ndata: %s\n\n", sse.EventNavRedirect, ctx.RedirectSnippet(roomCode, "/"))
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
		return
	}

	if debug {
		log.Printf("handleSSE: found lobby, setting up SSE for player %s", playerID)
	}

	// Set headers for SSE
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no") // Disable buffering in nginx/proxies

	// Immediately flush headers to establish SSE connection
	if flusher, ok := w.(http.Flusher); ok {
		flusher.Flush()
	}

	// Create client channel
	clientChan := make(chan models.SSEMessage, game.SSEBufferSize)
	sse.AddClient(lobby, clientChan, playerID)
	defer sse.RemoveClient(lobby, clientChan)

	lobby.RLock()
	clientCount := lobby.SSEClientCount()
	lobby.RUnlock()
	if debug {
		log.Printf("handleSSE: client %s connected, now have %d total clients", playerID, clientCount)
	}

	// Send initial data based on whether a game is in progress
	lobby.RLock()
	gameInProgress := lobby.CurrentGame != nil
	if gameInProgress {
		// Game in progress - send ready count or vote count with phase-specific event
		g := lobby.CurrentGame
		readyCount := 0
		var countHTML string
		var eventName string

		// Count from appropriate ready state map based on game phase
		switch g.Status {
		case models.StatusReadyCheck:
			for id := range lobby.Players {
				if g.ReadyToReveal[id] {
					readyCount++
				}
			}
			totalPlayers := len(lobby.Players)
			countHTML = ctx.ReadyCount(readyCount, totalPlayers, "players ready")
			eventName = "ready-count-check"
		case models.StatusRoleReveal:
			for id := range lobby.Players {
				if g.ReadyAfterReveal[id] {
					readyCount++
				}
			}
			totalPlayers := len(lobby.Players)
			countHTML = ctx.ReadyCount(readyCount, totalPlayers, "players ready")
			eventName = "ready-count-reveal"
		case models.StatusPlaying:
			for id := range lobby.Players {
				if g.ReadyToVote[id] {
					readyCount++
				}
			}
			totalPlayers := len(lobby.Players)
			countHTML = ctx.ReadyCount(readyCount, totalPlayers, "players ready to vote")
			eventName = "ready-count-playing"
		case models.StatusVoting:
			// Send vote count for voting phase
			voteCount := len(g.Votes)
			totalPlayers := len(lobby.Players)
			countHTML = ctx.VoteCount(voteCount, totalPlayers)
			eventName = "vote-count-voting"
		}
		lobby.RUnlock()
		if debug {
			log.Printf("handleSSE: sending initial %s to player %s", eventName, playerID)
		}
		fmt.Fprintf(w, "event: %s\ndata: %s\n\n", eventName, countHTML)
	} else {
		// No game - send lobby data
		playerListHTML := ctx.PlayerList(lobby.Players)
		hostControlsHTML := ctx.HostControls(lobby, playerID)
		scoreTableHTML := ctx.ScoreTable(lobby)
		lobby.RUnlock()
		if debug {
			log.Printf("handleSSE: sending initial lobby data to player %s", playerID)
		}
		fmt.Fprintf(w, "event: %s\ndata: %s\n\n", sse.EventPlayerUpdate, playerListHTML)
		fmt.Fprintf(w, "event: %s\ndata: %s\n\n", sse.EventControlsUpdate, hostControlsHTML)
		if scoreTableHTML != "" {
			fmt.Fprintf(w, "event: %s\ndata: %s\n\n", sse.EventScoreUpdate, scoreTableHTML)
		}
	}
	w.(http.Flusher).Flush()

	// Listen for updates
	reqCtx := r.Context()
	for {
		select {
		case <-reqCtx.Done():
			log.Printf("handleSSE: client %s disconnected", playerID)
			return
		case msg := <-clientChan:
			if debug {
				log.Printf("handleSSE: sending event=%s to player %s", msg.Event, playerID)
			}
			fmt.Fprintf(w, "event: %s\ndata: %s\n\n", msg.Event, msg.Data)
			w.(http.Flusher).Flush()
		}
	}
}
