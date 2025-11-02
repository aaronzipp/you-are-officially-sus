package handlers

import (
	"log"
	"math/rand"
	"net/http"
	"strings"

	"github.com/aaronzipp/you-are-officially-sus/internal/game"
	"github.com/aaronzipp/you-are-officially-sus/internal/models"
	"github.com/aaronzipp/you-are-officially-sus/internal/sse"
)

// HandleStartGame starts a new game in the lobby
func (ctx *Context) HandleStartGame(w http.ResponseWriter, r *http.Request) {
	log.Printf("HandleStartGame called: %s %s", r.Method, r.URL.Path)

	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	roomCode := strings.TrimPrefix(r.URL.Path, "/start-game/")

	log.Printf("HandleStartGame: roomCode=%s", roomCode)

	lobby, exists := ctx.LobbyStore.Get(roomCode)
	if !exists {
		log.Printf("HandleStartGame: lobby %s not found", roomCode)
		http.Error(w, "Lobby not found", http.StatusNotFound)
		return
	}

	// Get player ID from cookie
	cookie, err := r.Cookie("player_id")
	if err != nil {
		log.Printf("HandleStartGame: no player_id cookie")
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}
	playerID := cookie.Value

	log.Printf("HandleStartGame: playerID=%s isHost=%v", playerID, lobby.Host == playerID)

	lobby.Lock()

	// Check if player is host
	if lobby.Host != playerID {
		lobby.Unlock()
		log.Printf("HandleStartGame: player is not host")
		http.Error(w, "Only host can start game", http.StatusForbidden)
		return
	}

	if lobby.CurrentGame != nil {
		lobby.Unlock()
		log.Printf("HandleStartGame: game already in progress")
		http.Error(w, "Game already in progress", http.StatusBadRequest)
		return
	}

	if len(lobby.Players) < game.MinPlayers {
		lobby.Unlock()
		log.Printf("HandleStartGame: not enough players (%d)", len(lobby.Players))
		http.Error(w, "Need at least 3 players", http.StatusBadRequest)
		return
	}

	log.Printf("HandleStartGame: creating game for lobby %s", roomCode)

	// Create new game
	newGame := &models.Game{
		Location:         &ctx.Locations[rand.Intn(len(ctx.Locations))],
		PlayerInfo:       make(map[string]*models.GamePlayerInfo),
		Status:           models.StatusReadyCheck,
		ReadyToReveal:    make(map[string]bool),
		ReadyAfterReveal: make(map[string]bool),
		ReadyToVote:      make(map[string]bool),
		Votes:            make(map[string]string),
		VoteRound:        1,
	}
	// Pre-seed current phase readiness map with all players
	for id := range lobby.Players {
		newGame.ReadyToReveal[id] = false
	}

	// Assign spy
	playerIDs := make([]string, 0, len(lobby.Players))
	for id := range lobby.Players {
		playerIDs = append(playerIDs, id)
	}
	newGame.SpyID = playerIDs[rand.Intn(len(playerIDs))]

	// Assign challenges and roles
	shuffledChallenges := make([]string, len(ctx.Challenges))
	copy(shuffledChallenges, ctx.Challenges)
	rand.Shuffle(len(shuffledChallenges), func(i, j int) {
		shuffledChallenges[i], shuffledChallenges[j] = shuffledChallenges[j], shuffledChallenges[i]
	})

	for i, id := range playerIDs {
		newGame.PlayerInfo[id] = &models.GamePlayerInfo{
			Challenge: shuffledChallenges[i%len(shuffledChallenges)],
			IsSpy:     id == newGame.SpyID,
		}
	}

	lobby.CurrentGame = newGame
	lobby.Unlock()

	log.Printf("HandleStartGame: game created, broadcasting redirect to confirm-reveal")

	// Broadcast HTMX redirect snippet to all clients to go to confirm-reveal
	sse.Broadcast(lobby, sse.EventNavRedirect, ctx.RedirectSnippet(roomCode, game.PhasePathFor(roomCode, models.StatusReadyCheck)))

	log.Printf("HandleStartGame: complete")
	w.Header().Set("HX-Redirect", game.PhasePathFor(roomCode, models.StatusReadyCheck))
	w.WriteHeader(http.StatusOK)
}

// HandleRestartGame resets the game and returns to lobby
func (ctx *Context) HandleRestartGame(w http.ResponseWriter, r *http.Request) {
	log.Printf("HandleRestartGame called: %s %s", r.Method, r.URL.Path)

	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	roomCode := strings.TrimPrefix(r.URL.Path, "/restart-game/")

	lobby, exists := ctx.LobbyStore.Get(roomCode)
	if !exists {
		http.Error(w, "Lobby not found", http.StatusNotFound)
		return
	}

	// Get player ID from cookie
	cookie, err := r.Cookie("player_id")
	if err != nil {
		log.Printf("HandleRestartGame: no player_id cookie")
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}
	playerID := cookie.Value

	lobby.Lock()

	// Check if player is host
	if lobby.Host != playerID {
		lobby.Unlock()
		log.Printf("HandleRestartGame: player %s is not host", playerID)
		http.Error(w, "Only host can restart game", http.StatusForbidden)
		return
	}

	// Clear game
	lobby.CurrentGame = nil

	lobby.Unlock()

	log.Printf("HandleRestartGame: game cleared, broadcasting nav-redirect to lobby")

	// Broadcast restart WITHOUT holding lock
	sse.Broadcast(lobby, sse.EventNavRedirect, ctx.RedirectSnippet(roomCode, "/lobby/"+roomCode))

	log.Printf("HandleRestartGame: sending redirect response")
	w.Header().Set("HX-Redirect", "/lobby/"+roomCode)
	w.WriteHeader(http.StatusOK)
}

// HandleCloseLobby deletes the lobby
func (ctx *Context) HandleCloseLobby(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	roomCode := strings.TrimPrefix(r.URL.Path, "/close-lobby/")

	lobby, exists := ctx.LobbyStore.Get(roomCode)
	if !exists {
		http.Error(w, "Lobby not found", http.StatusNotFound)
		return
	}

	// Get player ID from cookie
	cookie, err := r.Cookie("player_id")
	if err != nil {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}
	playerID := cookie.Value

	lobby.Lock()
	if lobby.Host != playerID {
		lobby.Unlock()
		http.Error(w, "Only host can close lobby", http.StatusForbidden)
		return
	}
	lobby.Unlock()

	// Broadcast closure
	sse.Broadcast(lobby, sse.EventNavRedirect, ctx.RedirectSnippet(roomCode, "/"))

	// Delete lobby
	ctx.LobbyStore.Delete(roomCode)

	w.Header().Set("HX-Redirect", "/")
	w.WriteHeader(http.StatusOK)
}
