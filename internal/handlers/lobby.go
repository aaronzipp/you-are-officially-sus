package handlers

import (
	"log"
	"net/http"
	"strings"

	"github.com/aaronzipp/you-are-officially-sus/internal/game"
	"github.com/aaronzipp/you-are-officially-sus/internal/models"
	"github.com/aaronzipp/you-are-officially-sus/internal/render"
	"github.com/aaronzipp/you-are-officially-sus/internal/sse"
	"github.com/google/uuid"
)

// HandleCreateLobby creates a new lobby
func (ctx *Context) HandleCreateLobby(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	r.ParseForm()
	hostName := strings.TrimSpace(r.FormValue("name"))
	if hostName == "" {
		http.Error(w, "Name is required", http.StatusBadRequest)
		return
	}

	playerID := uuid.New().String()
	roomCode := game.GetUniqueRoomCode(ctx.LobbyStore)

	lobby := &models.Lobby{
		Code:    roomCode,
		Host:    playerID,
		Players: make(map[string]*models.Player),
		Scores:  make(map[string]*models.PlayerScore),
	}
	lobby.Players[playerID] = &models.Player{ID: playerID, Name: hostName}
	lobby.Scores[playerID] = &models.PlayerScore{}

	ctx.LobbyStore.Set(roomCode, lobby)

	log.Printf("Created lobby: code=%s host=%s", roomCode, playerID)

	// Set cookie for player ID (session)
	http.SetCookie(w, &http.Cookie{
		Name:     "player_id",
		Value:    playerID,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		// Secure: true, // enable when serving over HTTPS
	})

	// Redirect to lobby
	w.Header().Set("HX-Redirect", "/lobby/"+roomCode)
	w.WriteHeader(http.StatusOK)
}

// HandleJoinLobby allows a player to join an existing lobby
func (ctx *Context) HandleJoinLobby(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	r.ParseForm()
	roomCode := strings.ToUpper(strings.TrimSpace(r.FormValue("code")))
	playerName := strings.TrimSpace(r.FormValue("name"))

	if roomCode == "" || playerName == "" {
		http.Error(w, "Room code and name are required", http.StatusBadRequest)
		return
	}

	lobby, exists := ctx.LobbyStore.Get(roomCode)
	if !exists {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}

	lobby.Lock()
	if lobby.CurrentGame != nil {
		lobby.Unlock()
		http.Error(w, "Game in progress", http.StatusBadRequest)
		return
	}

	playerID := uuid.New().String()
	lobby.Players[playerID] = &models.Player{ID: playerID, Name: playerName}
	lobby.Scores[playerID] = &models.PlayerScore{}
	lobby.Unlock()

	log.Printf("Player joined lobby: code=%s playerID=%s name=%s", roomCode, playerID, playerName)

	// Broadcast update to all clients
	sse.Broadcast(lobby, "player-update", render.PlayerList(lobby.Players))
	sse.Broadcast(lobby, "score-update", render.ScoreTable(lobby))
	sse.BroadcastPersonalized(lobby, func(pid string) string {
		return render.HostControls(lobby, pid)
	}, "controls-update")

	// Set cookie for player ID (session)
	http.SetCookie(w, &http.Cookie{
		Name:     "player_id",
		Value:    playerID,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		// Secure: true, // enable when serving over HTTPS
	})

	// Redirect to lobby
	w.Header().Set("HX-Redirect", "/lobby/"+roomCode)
	w.WriteHeader(http.StatusOK)
}

// HandleLobby displays the lobby page
func (ctx *Context) HandleLobby(w http.ResponseWriter, r *http.Request) {
	roomCode := strings.TrimPrefix(r.URL.Path, "/lobby/")

	lobby, exists := ctx.LobbyStore.Get(roomCode)
	if !exists {
		http.Redirect(w, r, "/", http.StatusSeeOther)
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
	defer lobby.RUnlock()

	data := struct {
		RoomCode string
		PlayerID string
		Players  []*models.Player
		IsHost   bool
		Scores   map[string]*models.PlayerScore
	}{
		RoomCode: lobby.Code,
		PlayerID: playerID,
		Players:  getPlayerList(lobby.Players),
		IsHost:   lobby.Host == playerID,
		Scores:   lobby.Scores,
	}

	ctx.Templates.ExecuteTemplate(w, "lobby.html", data)
}
