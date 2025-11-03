package handlers

import (
	"log"
	"math/rand"
	"net/http"
	"strings"
	"time"

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
	spyID := playerIDs[rand.Intn(len(playerIDs))]
	newGame.SpyID = spyID
	newGame.SpyName = lobby.Players[spyID].Name

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

// HandleLeaveLobby allows a player to leave the lobby/game
func (ctx *Context) HandleLeaveLobby(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	roomCode := strings.TrimPrefix(r.URL.Path, "/leave-lobby/")

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

	lobby.RLock()
	isHost := lobby.Host == playerID
	playerCount := len(lobby.Players)
	lobby.RUnlock()

	// If host and there are other players, redirect to host selection page
	if isHost && playerCount > 1 {
		w.Header().Set("HX-Redirect", "/select-host/"+roomCode)
		w.WriteHeader(http.StatusOK)
		return
	}

	// Otherwise, proceed with normal leave (auto-assign or last player)
	ctx.handleLeaveLogic(w, r, roomCode, playerID, "")
}

// HandleSelectHost shows the host selection page
func (ctx *Context) HandleSelectHost(w http.ResponseWriter, r *http.Request) {
	roomCode := strings.TrimPrefix(r.URL.Path, "/select-host/")

	lobby, exists := ctx.LobbyStore.Get(roomCode)
	if !exists {
		http.Error(w, "Lobby not found", http.StatusNotFound)
		return
	}

	// Get player ID from cookie
	cookie, err := r.Cookie("player_id")
	if err != nil {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	playerID := cookie.Value

	lobby.RLock()
	// Check if player is host
	if lobby.Host != playerID {
		lobby.RUnlock()
		http.Redirect(w, r, "/lobby/"+roomCode, http.StatusSeeOther)
		return
	}

	// Get other players (excluding current host)
	type Player struct {
		ID   string
		Name string
	}
	otherPlayers := []Player{}
	for id, player := range lobby.Players {
		if id != playerID {
			otherPlayers = append(otherPlayers, Player{ID: id, Name: player.Name})
		}
	}
	lobby.RUnlock()

	// If no other players, just leave
	if len(otherPlayers) == 0 {
		ctx.handleLeaveLogic(w, r, roomCode, playerID, "")
		return
	}

	data := struct {
		RoomCode     string
		OtherPlayers []Player
	}{
		RoomCode:     roomCode,
		OtherPlayers: otherPlayers,
	}

	ctx.Templates.ExecuteTemplate(w, "select_host.html", data)
}

// HandleLeaveLobbyWithHost allows a host to leave after selecting a new host
func (ctx *Context) HandleLeaveLobbyWithHost(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	roomCode := strings.TrimPrefix(r.URL.Path, "/leave-lobby-with-host/")

	// Parse form to get new host selection
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Invalid form", http.StatusBadRequest)
		return
	}
	newHostID := r.FormValue("new_host")
	if newHostID == "" {
		http.Error(w, "New host not selected", http.StatusBadRequest)
		return
	}

	// Get player ID from cookie
	cookie, err := r.Cookie("player_id")
	if err != nil {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}
	playerID := cookie.Value

	ctx.handleLeaveLogic(w, r, roomCode, playerID, newHostID)
}

// handleLeaveLogic contains the shared logic for leaving a lobby
// If newHostID is provided, it will be used instead of auto-assignment
func (ctx *Context) handleLeaveLogic(w http.ResponseWriter, r *http.Request, roomCode, playerID, newHostID string) {
	lobby, exists := ctx.LobbyStore.Get(roomCode)
	if !exists {
		http.Error(w, "Lobby not found", http.StatusNotFound)
		return
	}

	lobby.Lock()

	// Check if player is in lobby
	player, exists := lobby.Players[playerID]
	if !exists {
		lobby.Unlock()
		http.Error(w, "Player not in lobby", http.StatusBadRequest)
		return
	}

	wasHost := lobby.Host == playerID
	playerName := player.Name

	log.Printf("Player leaving: code=%s playerID=%s name=%s wasHost=%v", roomCode, playerID, playerName, wasHost)

	// Remove player from lobby
	delete(lobby.Players, playerID)
	delete(lobby.Scores, playerID)

	// Check if this was the last player
	if len(lobby.Players) == 0 {
		lobby.Unlock()
		log.Printf("Last player left, deleting lobby: code=%s", roomCode)
		ctx.LobbyStore.Delete(roomCode)
		w.Header().Set("HX-Redirect", "/")
		w.WriteHeader(http.StatusOK)
		return
	}

	// Reassign host if necessary
	assignedHostID := ""
	autoAssigned := false
	if wasHost {
		if newHostID != "" {
			// Use the provided host ID (manual selection)
			lobby.Host = newHostID
			assignedHostID = newHostID
			log.Printf("Host manually assigned: code=%s newHost=%s", roomCode, newHostID)
		} else {
			// Auto-assign new host
			assignNewHost(lobby)
			assignedHostID = lobby.Host
			autoAssigned = true
			log.Printf("Host auto-assigned: code=%s newHost=%s", roomCode, assignedHostID)
		}
	}

	// Handle game state if game is in progress
	gameEnded := false
	innocentsWon := false
	phaseAdvanced := false
	if lobby.CurrentGame != nil {
		g := lobby.CurrentGame

		// Check if spy left
		spyLeft := g.SpyID == playerID

		// Remove player from game state
		removePlayerFromGame(g, playerID)

		// Check if game should end
		if spyLeft {
			// Spy left - innocents win
			log.Printf("Spy left the game: code=%s spyName=%s", roomCode, g.SpyName)
			g.Status = models.StatusFinished
			g.SpyForfeited = true
			innocentsWon = true
			gameEnded = true

			// Update scores for remaining players (they all win)
			for id := range lobby.Players {
				lobby.Scores[id].GamesWon++
			}
		} else if len(lobby.Players) < game.MinPlayers {
			// Too few players - end game
			log.Printf("Too few players remaining: code=%s count=%d", roomCode, len(lobby.Players))
			lobby.CurrentGame = nil
			gameEnded = true
		} else {
			// Game continues - check if phase should advance now that player is removed
			phaseAdvanced = checkAndAdvancePhase(ctx, lobby, roomCode)
		}
	}

	lobby.Unlock()

	// Send notification to new host if host was auto-assigned (not manually selected)
	if assignedHostID != "" && autoAssigned {
		hostNotification := ctx.HostNotification()
		sse.BroadcastToPlayer(lobby, assignedHostID, sse.EventHostChanged, hostNotification)
	}

	// Broadcast updates to remaining players
	if gameEnded {
		if innocentsWon {
			// Redirect to results page
			sse.Broadcast(lobby, sse.EventNavRedirect, ctx.RedirectSnippet(roomCode, game.PhasePathFor(roomCode, models.StatusFinished)))
		} else {
			// Game cancelled due to insufficient players - show warning then redirect
			abortMsg := ctx.GameAbortedMessage("Not enough players remaining (minimum 3 required)")
			sse.Broadcast(lobby, sse.EventErrorMessage, abortMsg)

			// Wait a moment, then redirect to lobby
			go func() {
				time.Sleep(3 * time.Second)
				sse.Broadcast(lobby, sse.EventNavRedirect, ctx.RedirectSnippet(roomCode, "/lobby/"+roomCode))
			}()
		}
	} else {
		// Update player list and scores
		sse.Broadcast(lobby, sse.EventPlayerUpdate, ctx.PlayerList(lobby.Players))
		sse.Broadcast(lobby, sse.EventScoreUpdate, ctx.ScoreTable(lobby))
		sse.BroadcastPersonalized(lobby, func(pid string) string {
			return ctx.HostControls(lobby, pid)
		}, sse.EventControlsUpdate)

		// If phase advanced, redirect all players to new phase
		if phaseAdvanced {
			lobby.RLock()
			newPhase := lobby.CurrentGame.Status
			lobby.RUnlock()
			nextPath := game.PhasePathFor(roomCode, newPhase)
			log.Printf("Broadcasting phase transition after player leave: code=%s path=%s", roomCode, nextPath)
			sse.Broadcast(lobby, sse.EventNavRedirect, ctx.RedirectSnippet(roomCode, nextPath))
		} else if lobby.CurrentGame != nil {
			// Update ready/vote counts if in game and phase didn't advance
			lobby.RLock()
			g := lobby.CurrentGame
			switch g.Status {
			case models.StatusReadyCheck:
				readyCount := 0
				for id := range lobby.Players {
					if g.ReadyToReveal[id] {
						readyCount++
					}
				}
				lobby.RUnlock()
				sse.Broadcast(lobby, "ready-count-check", ctx.ReadyCount(readyCount, len(lobby.Players), "players ready"))
			case models.StatusRoleReveal:
				readyCount := 0
				for id := range lobby.Players {
					if g.ReadyAfterReveal[id] {
						readyCount++
					}
				}
				lobby.RUnlock()
				sse.Broadcast(lobby, "ready-count-reveal", ctx.ReadyCount(readyCount, len(lobby.Players), "players ready"))
			case models.StatusPlaying:
				readyCount := 0
				for id := range lobby.Players {
					if g.ReadyToVote[id] {
						readyCount++
					}
				}
				lobby.RUnlock()
				sse.Broadcast(lobby, "ready-count-playing", ctx.ReadyCount(readyCount, len(lobby.Players), "players ready to vote"))
			case models.StatusVoting:
				voteCount := len(g.Votes)
				lobby.RUnlock()
				sse.Broadcast(lobby, "vote-count-voting", ctx.VoteCount(voteCount, len(lobby.Players)))
			default:
				lobby.RUnlock()
			}
		}
	}

	// Redirect leaving player to home
	w.Header().Set("HX-Redirect", "/")
	w.WriteHeader(http.StatusOK)
}

// assignNewHost assigns a new host to the lobby (first player by ID)
func assignNewHost(lobby *models.Lobby) {
	// Find first player by ID (deterministic)
	var firstID string
	for id := range lobby.Players {
		if firstID == "" || id < firstID {
			firstID = id
		}
	}
	lobby.Host = firstID
}

// removePlayerFromGame removes a player from all game state maps
func removePlayerFromGame(g *models.Game, playerID string) {
	delete(g.PlayerInfo, playerID)
	delete(g.ReadyToReveal, playerID)
	delete(g.ReadyAfterReveal, playerID)
	delete(g.ReadyToVote, playerID)
	delete(g.Votes, playerID)

	// Update first questioner if it was the leaving player
	if g.FirstQuestioner == playerID {
		g.FirstQuestioner = ""
	}
}

// checkAndAdvancePhase checks if the game should advance to the next phase after a player leaves
// Returns true if phase advanced, false otherwise
// Caller must hold lobby lock
func checkAndAdvancePhase(ctx *Context, lobby *models.Lobby, roomCode string) bool {
	if lobby.CurrentGame == nil {
		return false
	}

	g := lobby.CurrentGame
	totalPlayers := len(lobby.Players)
	shouldAdvance := false

	switch g.Status {
	case models.StatusReadyCheck:
		readyCount := 0
		for id := range lobby.Players {
			if g.ReadyToReveal[id] {
				readyCount++
			}
		}
		shouldAdvance = readyCount == totalPlayers
		if shouldAdvance {
			log.Printf("Phase advancement after player leave: code=%s phase=%s->%s readyCount=%d/%d", roomCode, g.Status, models.StatusRoleReveal, readyCount, totalPlayers)
			g.Status = models.StatusRoleReveal
			// Pre-seed next phase readiness map
			for id := range lobby.Players {
				if _, ok := g.ReadyAfterReveal[id]; !ok {
					g.ReadyAfterReveal[id] = false
				}
			}
		}

	case models.StatusRoleReveal:
		readyCount := 0
		for id := range lobby.Players {
			if g.ReadyAfterReveal[id] {
				readyCount++
			}
		}
		shouldAdvance = readyCount == totalPlayers
		if shouldAdvance {
			log.Printf("Phase advancement after player leave: code=%s phase=%s->%s readyCount=%d/%d", roomCode, g.Status, models.StatusPlaying, readyCount, totalPlayers)
			g.Status = models.StatusPlaying
			// Record when playing phase started
			g.PlayStartedAt = time.Now()
			// Pre-seed next phase readiness map
			for id := range lobby.Players {
				if _, ok := g.ReadyToVote[id]; !ok {
					g.ReadyToVote[id] = false
				}
			}
			// Choose random first questioner if not set
			if g.FirstQuestioner == "" {
				playerIDs := make([]string, 0, len(lobby.Players))
				for id := range lobby.Players {
					playerIDs = append(playerIDs, id)
				}
				g.FirstQuestioner = playerIDs[rand.Intn(len(playerIDs))]
			}
		}

	case models.StatusPlaying:
		readyCount := 0
		for id := range lobby.Players {
			if g.ReadyToVote[id] {
				readyCount++
			}
		}
		shouldAdvance = readyCount > totalPlayers/2
		if shouldAdvance {
			log.Printf("Phase advancement after player leave: code=%s phase=%s->%s readyCount=%d/%d", roomCode, g.Status, models.StatusVoting, readyCount, totalPlayers)
			g.Status = models.StatusVoting
		}

	case models.StatusVoting:
		voteCount := len(g.Votes)
		shouldAdvance = voteCount == totalPlayers
		if shouldAdvance {
			log.Printf("All votes collected after player leave: code=%s votes=%d/%d", roomCode, voteCount, totalPlayers)
			// Vote calculation is handled separately in gameHandleVoteCookie
			// Here we just note that all votes are in
		}
	}

	return shouldAdvance
}

// handlePlayerDisconnect is called when a player's SSE connection is lost (browser closed/refreshed)
// This handles automatic cleanup without the player explicitly clicking "Leave"
func (ctx *Context) handlePlayerDisconnect(roomCode, playerID string) {
	lobby, exists := ctx.LobbyStore.Get(roomCode)
	if !exists {
		return
	}

	lobby.Lock()

	// Check if player is still in lobby
	player, exists := lobby.Players[playerID]
	if !exists {
		lobby.Unlock()
		return
	}

	wasHost := lobby.Host == playerID
	playerName := player.Name

	log.Printf("Player disconnected: code=%s playerID=%s name=%s wasHost=%v", roomCode, playerID, playerName, wasHost)

	// Remove player from lobby
	delete(lobby.Players, playerID)
	delete(lobby.Scores, playerID)

	// Check if this was the last player
	if len(lobby.Players) == 0 {
		lobby.Unlock()
		log.Printf("Last player disconnected, deleting lobby: code=%s", roomCode)
		ctx.LobbyStore.Delete(roomCode)
		return
	}

	// Reassign host if necessary (auto-assign on disconnect)
	newHostID := ""
	if wasHost {
		assignNewHost(lobby)
		newHostID = lobby.Host
		log.Printf("Host disconnected, reassigned: code=%s newHost=%s", roomCode, newHostID)
	}

	// Handle game state if game is in progress
	gameEnded := false
	innocentsWon := false
	if lobby.CurrentGame != nil {
		g := lobby.CurrentGame

		// Check if spy disconnected
		spyLeft := g.SpyID == playerID

		// Remove player from game state
		removePlayerFromGame(g, playerID)

		// Check if game should end
		if spyLeft {
			// Spy left - innocents win
			log.Printf("Spy disconnected from game: code=%s spyName=%s", roomCode, g.SpyName)
			g.Status = models.StatusFinished
			g.SpyForfeited = true
			innocentsWon = true
			gameEnded = true

			// Update scores for remaining players (they all win)
			for id := range lobby.Players {
				lobby.Scores[id].GamesWon++
			}
		} else if len(lobby.Players) < game.MinPlayers {
			// Too few players - end game
			log.Printf("Too few players remaining after disconnect: code=%s count=%d", roomCode, len(lobby.Players))
			lobby.CurrentGame = nil
			gameEnded = true
		}
	}

	lobby.Unlock()

	// Send notification to new host if host changed
	if newHostID != "" {
		hostNotification := ctx.HostNotification()
		sse.BroadcastToPlayer(lobby, newHostID, sse.EventHostChanged, hostNotification)
	}

	// Broadcast updates to remaining players
	if gameEnded {
		if innocentsWon {
			// Redirect to results page
			sse.Broadcast(lobby, sse.EventNavRedirect, ctx.RedirectSnippet(roomCode, game.PhasePathFor(roomCode, models.StatusFinished)))
		} else {
			// Game cancelled due to insufficient players - show warning then redirect
			abortMsg := ctx.GameAbortedMessage("Not enough players remaining (minimum 3 required)")
			sse.Broadcast(lobby, sse.EventErrorMessage, abortMsg)

			// Wait a moment, then redirect to lobby
			go func() {
				time.Sleep(3 * time.Second)
				sse.Broadcast(lobby, sse.EventNavRedirect, ctx.RedirectSnippet(roomCode, "/lobby/"+roomCode))
			}()
		}
	} else {
		// Update player list and scores
		sse.Broadcast(lobby, sse.EventPlayerUpdate, ctx.PlayerList(lobby.Players))
		sse.Broadcast(lobby, sse.EventScoreUpdate, ctx.ScoreTable(lobby))
		sse.BroadcastPersonalized(lobby, func(pid string) string {
			return ctx.HostControls(lobby, pid)
		}, sse.EventControlsUpdate)

		// Update ready/vote counts if in game
		if lobby.CurrentGame != nil {
			g := lobby.CurrentGame
			switch g.Status {
			case models.StatusReadyCheck:
				readyCount := 0
				for id := range lobby.Players {
					if g.ReadyToReveal[id] {
						readyCount++
					}
				}
				sse.Broadcast(lobby, "ready-count-check", ctx.ReadyCount(readyCount, len(lobby.Players), "players ready"))
			case models.StatusRoleReveal:
				readyCount := 0
				for id := range lobby.Players {
					if g.ReadyAfterReveal[id] {
						readyCount++
					}
				}
				sse.Broadcast(lobby, "ready-count-reveal", ctx.ReadyCount(readyCount, len(lobby.Players), "players ready"))
			case models.StatusPlaying:
				readyCount := 0
				for id := range lobby.Players {
					if g.ReadyToVote[id] {
						readyCount++
					}
				}
				sse.Broadcast(lobby, "ready-count-playing", ctx.ReadyCount(readyCount, len(lobby.Players), "players ready to vote"))
			case models.StatusVoting:
				sse.Broadcast(lobby, "vote-count-voting", ctx.VoteCount(len(g.Votes), len(lobby.Players)))
			}
		}
	}
}
