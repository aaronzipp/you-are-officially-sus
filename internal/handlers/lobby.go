package handlers

import (
	"encoding/base64"
	"fmt"
	"html/template"
	"log"
	"net/http"
	"strings"

	"github.com/aaronzipp/you-are-officially-sus/internal/game"
	"github.com/aaronzipp/you-are-officially-sus/internal/models"
	"github.com/aaronzipp/you-are-officially-sus/internal/render"
	"github.com/aaronzipp/you-are-officially-sus/internal/sse"
	"github.com/google/uuid"
	"github.com/skip2/go-qrcode"
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

	// Room code can come from URL path (/join/ABCD) or form field (legacy)
	roomCode := strings.TrimPrefix(r.URL.Path, "/join/")
	if roomCode == "" || roomCode == "/join" {
		// Fall back to form field if not in URL
		roomCode = strings.TrimSpace(r.FormValue("code"))
	}
	roomCode = strings.ToUpper(roomCode)

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

	// Check if browser already has a player_id cookie
	var playerID string
	var isRejoin bool
	cookie, err := r.Cookie("player_id")
	if err == nil && cookie.Value != "" {
		existingPlayerID := cookie.Value
		// Check if this player is already in the lobby
		if _, exists := lobby.Players[existingPlayerID]; exists {
			lobby.Unlock()
			log.Printf("Player already in lobby: code=%s playerID=%s", roomCode, existingPlayerID)
			// Already joined - just redirect to lobby
			w.Header().Set("HX-Redirect", "/lobby/"+roomCode)
			w.WriteHeader(http.StatusOK)
			return
		}
		// Cookie exists but player not in lobby - rejoin with same ID
		playerID = existingPlayerID
		isRejoin = true
	} else {
		// No cookie - create new player ID
		playerID = uuid.New().String()
		isRejoin = false
	}

	// Check if name is already taken by another player
	if isNameTaken(lobby.Players, playerName, playerID) {
		lobby.Unlock()
		log.Printf("Name already taken: code=%s name=%s playerID=%s", roomCode, playerName, playerID)
		// Use HTMX response headers to retarget the error message
		w.Header().Set("Content-Type", "text/html")
		w.Header().Set("HX-Retarget", "#join-error")
		w.Header().Set("HX-Reswap", "outerHTML")
		w.WriteHeader(http.StatusOK)
		errorMsg := fmt.Sprintf("The name \"%s\" is already taken. Please choose a different name.", playerName)
		w.Write([]byte(ctx.ErrorMessage(errorMsg)))
		return
	}

	// Log the successful join/rejoin
	if isRejoin {
		log.Printf("Player rejoined lobby: code=%s playerID=%s name=%s", roomCode, playerID, playerName)
	} else {
		log.Printf("Player joined lobby: code=%s playerID=%s name=%s", roomCode, playerID, playerName)
	}

	// Add/re-add player to lobby
	lobby.Players[playerID] = &models.Player{ID: playerID, Name: playerName}
	if _, scoreExists := lobby.Scores[playerID]; !scoreExists {
		lobby.Scores[playerID] = &models.PlayerScore{}
	}
	lobby.Unlock()

	// Broadcast update to all clients
	sse.Broadcast(lobby, sse.EventPlayerUpdate, ctx.PlayerList(lobby.Players))
	sse.Broadcast(lobby, sse.EventScoreUpdate, ctx.ScoreTable(lobby))
	sse.BroadcastPersonalized(lobby, func(pid string) string {
		return ctx.HostControls(lobby, pid)
	}, sse.EventControlsUpdate)

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
		// No cookie - redirect to join screen for this lobby
		http.Redirect(w, r, "/join/"+roomCode, http.StatusSeeOther)
		return
	}
	playerID := cookie.Value

	lobby.RLock()
	defer lobby.RUnlock()

	// Generate QR code for lobby URL (only if BASE_URL is configured)
	var qrDataURL template.URL
	if ctx.BaseURL != "" {
		lobbyURL := fmt.Sprintf("%s/lobby/%s", ctx.BaseURL, roomCode)
		png, err := qrcode.Encode(lobbyURL, qrcode.Medium, 256)
		if err != nil {
			log.Printf("Failed to generate QR code for lobby %s: %v", roomCode, err)
		} else {
			qrDataURL = template.URL(fmt.Sprintf("data:image/png;base64,%s", base64.StdEncoding.EncodeToString(png)))
		}
	}

	data := struct {
		RoomCode      string
		PlayerID      string
		Players       []*models.Player
		IsHost        bool
		Scores        map[string]*models.PlayerScore
		QRCodeDataURL template.URL
	}{
		RoomCode:      lobby.Code,
		PlayerID:      playerID,
		Players:       render.GetPlayerList(lobby.Players),
		IsHost:        lobby.Host == playerID,
		Scores:        lobby.Scores,
		QRCodeDataURL: qrDataURL,
	}

	ctx.Templates.ExecuteTemplate(w, "lobby.html", data)
}

// HandleJoinLobbyScreen displays the join screen for entering name when scanning QR code
func (ctx *Context) HandleJoinLobbyScreen(w http.ResponseWriter, r *http.Request) {
	roomCode := strings.TrimPrefix(r.URL.Path, "/join/")

	// Verify the lobby exists
	_, exists := ctx.LobbyStore.Get(roomCode)
	if !exists {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}

	// Check if user already has a cookie - if so, redirect directly to lobby
	cookie, err := r.Cookie("player_id")
	if err == nil && cookie.Value != "" {
		// Has cookie - redirect to lobby (HandleLobby will handle the rest)
		http.Redirect(w, r, "/lobby/"+roomCode, http.StatusSeeOther)
		return
	}

	// No cookie - show join screen
	data := struct {
		RoomCode string
	}{
		RoomCode: roomCode,
	}

	ctx.Templates.ExecuteTemplate(w, "join_lobby.html", data)
}

// HandleJoinMux multiplexes between GET (join screen) and POST (join action) for /join/{roomCode}
func (ctx *Context) HandleJoinMux(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		ctx.HandleJoinLobbyScreen(w, r)
	} else if r.Method == http.MethodPost {
		ctx.HandleJoinLobby(w, r)
	} else {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}
