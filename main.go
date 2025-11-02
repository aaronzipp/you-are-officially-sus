package main

import (
	"encoding/json"
	"fmt"
	"html/template"
	"log"
	"math/rand"
	"net/http"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
)

// GameStatus represents the current state of the game
type GameStatus string

const (
	StatusWaiting    GameStatus = "waiting"
	StatusReadyCheck GameStatus = "ready_check"
	StatusRoleReveal GameStatus = "role_reveal"
	StatusPlaying    GameStatus = "playing"
	StatusVoting     GameStatus = "voting"
	StatusFinished   GameStatus = "finished"
)

// Location represents a place/word with categories
type Location struct {
	Word       string   `json:"word"`
	Categories []string `json:"categories"`
}

// SSEMessage represents a message sent via Server-Sent Events
type SSEMessage struct {
	Event string // Event type (e.g., "player-update", "nav-redirect")
	Data  string // HTML content or data to send
}

// PlayerScore tracks persistent score across games
type PlayerScore struct {
	GamesWon  int
	GamesLost int
}

// Player represents a player in the lobby
type Player struct {
	ID   string
	Name string
}

// GamePlayerInfo contains game-specific player information
type GamePlayerInfo struct {
	Challenge        string
	HasConfirmedRole bool
	IsSpy            bool
}

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

// Game represents an active game session (ephemeral)
type Game struct {
	Lobby            *Lobby
	Location         *Location
	SpyID            string
	FirstQuestioner  string                     // Player ID of who asks the first question
	PlayerInfo       map[string]*GamePlayerInfo // game-specific player data
	Status           GameStatus
	StartTime        time.Time
	ReadyToReveal    map[string]bool // Phase 1: Ready to see role (all players required)
	ReadyAfterReveal map[string]bool // Phase 2: Confirmed saw role (all players required)
	ReadyToVote      map[string]bool // Phase 3: Ready to vote (>50% required)
	Votes            map[string]string
	VoteRound        int // Track voting rounds for tie-breaking
}

// Global storage
var (
	lobbies    = make(map[string]*Lobby)
	lobbiesMux sync.RWMutex
	locations  []Location
	challenges []string
	templates  *template.Template
	debug      bool
)

func init() {
	rand.Seed(time.Now().UnixNano())
	// Enable DEBUG logs when DEBUG env var is set (non-empty)
	debug = os.Getenv("DEBUG") != ""
}

func main() {
	// Load data
	if err := loadData(); err != nil {
		log.Fatal("Failed to load data:", err)
	}

	// Parse templates with custom functions
	var err error
	templates, err = template.New("").Funcs(template.FuncMap{
		"add": func(a, b int) int { return a + b },
	}).ParseGlob("templates/*.html")
	if err != nil {
		log.Fatal("Failed to parse templates:", err)
	}

	// Routes
	http.HandleFunc("/", handleIndex)
	http.HandleFunc("/create", handleCreateLobby)
	http.HandleFunc("/join", handleJoinLobby)
	http.HandleFunc("/lobby/", handleLobby)
	http.HandleFunc("/sse/", handleSSE)
	http.HandleFunc("/start-game/", handleStartGame)
	// Game multiplexer: phases (GET), actions (POST), and redirect helper
	http.HandleFunc("/game/", handleGameMux)
	// Results
	http.HandleFunc("/results/", handleResults)
	// Lobby/game lifecycle
	http.HandleFunc("/restart-game/", handleRestartGame)
	http.HandleFunc("/close-lobby/", handleCloseLobby)

	// Static files
	http.Handle("/static/", http.StripPrefix("/static/", http.FileServer(http.Dir("static"))))

	port := ":8080"
	log.Printf("Server starting on http://localhost%s", port)
	log.Fatal(http.ListenAndServe(port, nil))
}

// loadData loads locations and challenges from JSON files
func loadData() error {
	// Load locations
	locationData, err := os.ReadFile("data/places.json")
	if err != nil {
		return fmt.Errorf("reading places.json: %w", err)
	}
	if err := json.Unmarshal(locationData, &locations); err != nil {
		return fmt.Errorf("parsing places.json: %w", err)
	}

	// Load challenges
	challengeData, err := os.ReadFile("data/challenges.json")
	if err != nil {
		return fmt.Errorf("reading challenges.json: %w", err)
	}
	if err := json.Unmarshal(challengeData, &challenges); err != nil {
		return fmt.Errorf("parsing challenges.json: %w", err)
	}

	log.Printf("Loaded %d locations and %d challenges", len(locations), len(challenges))
	return nil
}

// ===== SSE Methods =====

// addSSEClient adds a new SSE client to the lobby
func (l *Lobby) addSSEClient(client chan SSEMessage, playerID string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.sseClients == nil {
		l.sseClients = make(map[chan SSEMessage]string)
	}
	// Warn if the same player has multiple SSE connections
	dup := 0
	for _, pid := range l.sseClients {
		if pid == playerID {
			dup++
		}
	}
	if dup > 0 {
		log.Printf("WARN: player %s opened %d additional SSE connection(s)", playerID, dup)
	}
	l.sseClients[client] = playerID
}

// removeSSEClient removes an SSE client from the lobby
func (l *Lobby) removeSSEClient(client chan SSEMessage) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.sseClients, client)
	close(client)
	log.Printf("removeSSEClient: client removed, now have %d total clients", len(l.sseClients))
}

// broadcastSSE sends a message to all connected SSE clients
func (l *Lobby) broadcastSSE(event, data string) {
	l.mu.RLock()
	// Collect all client channels while holding the lock
	var clients []chan SSEMessage
	for client := range l.sseClients {
		clients = append(clients, client)
	}
	clientCount := len(clients)
	l.mu.RUnlock()

	if debug {
		log.Printf("broadcastSSE: event=%s to %d clients", event, clientCount)
	}

	// Send messages WITHOUT holding the lock
	msg := SSEMessage{Event: event, Data: data}
	successCount := 0
	for _, client := range clients {
		select {
		case client <- msg:
			successCount++
		case <-time.After(1 * time.Second):
			if debug {
				log.Printf("broadcastSSE: timeout sending to client")
			}
		}
	}
	if debug {
		log.Printf("broadcastSSE: sent to %d/%d clients successfully", successCount, clientCount)
	}
}

// broadcastPersonalizedControls sends personalized control updates to each client
func (l *Lobby) broadcastPersonalizedControls() {
	l.mu.RLock()
	// Collect all client channels and their player IDs while holding the lock
	clientMap := make(map[chan SSEMessage]string)
	for client, playerID := range l.sseClients {
		clientMap[client] = playerID
	}
	l.mu.RUnlock()

	// Send personalized messages WITHOUT holding the lock
	for client, playerID := range clientMap {
		controlsHTML := l.renderHostControls(playerID)
		msg := SSEMessage{Event: "controls-update", Data: controlsHTML}
		select {
		case client <- msg:
			// Message sent successfully
		case <-time.After(1 * time.Second):
			// Timeout - skip this client to avoid blocking
		}
	}
}

// ===== HTML Renderers =====

// renderPlayerList generates HTML for the player list
func (l *Lobby) renderPlayerList() string {
	players := getPlayerList(l.Players)
	html := fmt.Sprintf(`<h2>Players (%d)</h2><ul class="player-list">`, len(players))
	for _, p := range players {
		html += fmt.Sprintf(`<li class="player-item"><span class="player-name">%s</span></li>`, p.Name)
	}
	html += `</ul>`
	return html
}

// renderHostControls generates HTML for host controls
func (l *Lobby) renderHostControls(playerID string) string {
	isHost := l.Host == playerID
	playerCount := len(l.Players)
	inGame := l.CurrentGame != nil

	if inGame {
		return "" // No controls during game
	}

	if isHost {
		if playerCount >= 3 {
			return fmt.Sprintf(`<form hx-post="/start-game/%s"><button type="submit" class="btn btn-primary">Start Game</button></form><form hx-post="/close-lobby/%s" style="margin-top: 1rem;"><button type="submit" class="btn btn-secondary">Close Lobby</button></form>`, l.Code, l.Code)
		} else {
			return fmt.Sprintf(`<p>Waiting for players to join...</p><p class="text-muted">Need at least 3 players to start</p><form hx-post="/close-lobby/%s" style="margin-top: 1rem;"><button type="submit" class="btn btn-secondary">Close Lobby</button></form>`, l.Code)
		}
	}
	return `<p>Waiting for host to start the game...</p>`
}

// renderScoreTable generates HTML for the score table
func (l *Lobby) renderScoreTable() string {
	if len(l.Scores) == 0 {
		return ""
	}

	html := `<div class="score-table"><h3>Scores</h3><table><tr><th>Player</th><th>Won</th><th>Lost</th></tr>`
	for playerID, score := range l.Scores {
		if player, exists := l.Players[playerID]; exists {
			html += fmt.Sprintf(`<tr><td>%s</td><td>%d</td><td>%d</td></tr>`, player.Name, score.GamesWon, score.GamesLost)
		}
	}
	html += `</table></div>`
	return html
}

// renderReadyCountCheck generates HTML for Phase 1 ready count display
func (l *Lobby) renderReadyCountCheck(ready, total int) string {
	text := fmt.Sprintf("%d/%d players ready", ready, total)
	return fmt.Sprintf(`<p class="ready-count">%s</p>`, text)
}

// renderReadyCountReveal generates HTML for Phase 2 ready count display
func (l *Lobby) renderReadyCountReveal(ready, total int) string {
	text := fmt.Sprintf("%d/%d players ready", ready, total)
	return fmt.Sprintf(`<p class="ready-count">%s</p>`, text)
}

// renderReadyCountPlaying generates HTML for Phase 3 ready count display
func (l *Lobby) renderReadyCountPlaying(ready, total int) string {
	text := fmt.Sprintf("%d/%d players ready to vote", ready, total)
	return fmt.Sprintf(`<p class="ready-count">%s</p>`, text)
}

// renderTimerUpdate generates HTML for timer display
func renderTimerUpdate(timeStr string) string {
	return fmt.Sprintf(`<span>Time Remaining:</span>
<strong>%s</strong>`, timeStr)
}

// renderVoteCount generates HTML for vote count display
func renderVoteCount(count, total int) string {
	return fmt.Sprintf(`<p class="ready-count">%d/%d players have voted</p>`, count, total)
}

// renderVotedConfirmation generates HTML for "you voted" confirmation
func renderVotedConfirmation() string {
	return `<div class="card">
		<p class="vote-status">✓ You voted</p>
		<p class="text-muted">Waiting for other players to vote...</p>
	</div>`
}

// ===== Utility Functions =====

// generateRoomCode creates a random 6-character room code
func generateRoomCode() string {
	const chars = "ABCDEFGHJKLMNPQRSTUVWXYZ23456789" // Exclude ambiguous chars
	code := make([]byte, 6)
	for i := range code {
		code[i] = chars[rand.Intn(len(chars))]
	}
	return string(code)
}

// getUniqueRoomCode generates a unique room code
func getUniqueRoomCode() string {
	lobbiesMux.RLock()
	defer lobbiesMux.RUnlock()

	for {
		code := generateRoomCode()
		if _, exists := lobbies[code]; !exists {
			return code
		}
	}
}

// getPlayerList converts map to sorted slice for templates
func getPlayerList(players map[string]*Player) []*Player {
	list := make([]*Player, 0, len(players))
	for _, p := range players {
		list = append(list, p)
	}
	sort.Slice(list, func(i, j int) bool { return strings.ToLower(list[i].Name) < strings.ToLower(list[j].Name) })
	return list
}

// getLobbyAndPlayer validates membership using session cookie
func getLobbyAndPlayer(r *http.Request, roomCode string) (*Lobby, string, error) {
	lobbiesMux.RLock()
	lobby, exists := lobbies[roomCode]
	lobbiesMux.RUnlock()
	if !exists {
		return nil, "", fmt.Errorf("lobby not found")
	}
	cookie, err := r.Cookie("player_id")
	if err != nil {
		return nil, "", fmt.Errorf("no session")
	}
	playerID := cookie.Value
	lobby.mu.RLock()
	_, member := lobby.Players[playerID]
	lobby.mu.RUnlock()
	if !member {
		return nil, "", fmt.Errorf("not a member")
	}
	return lobby, playerID, nil
}

func phasePathFor(roomCode string, status GameStatus) string {
	switch status {
	case StatusReadyCheck:
		return "/game/" + roomCode + "/confirm-reveal"
	case StatusRoleReveal:
		return "/game/" + roomCode + "/roles"
	case StatusPlaying:
		return "/game/" + roomCode + "/play"
	case StatusVoting:
		return "/game/" + roomCode + "/voting"
	case StatusFinished:
		return "/results/" + roomCode
	default:
		return "/lobby/" + roomCode
	}
}

// handleIndex serves the landing page
func handleIndex(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	templates.ExecuteTemplate(w, "index.html", nil)
}

// handleCreateLobby creates a new lobby
func handleCreateLobby(w http.ResponseWriter, r *http.Request) {
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
	roomCode := getUniqueRoomCode()

	lobby := &Lobby{
		Code:    roomCode,
		Host:    playerID,
		Players: make(map[string]*Player),
		Scores:  make(map[string]*PlayerScore),
	}
	lobby.Players[playerID] = &Player{ID: playerID, Name: hostName}
	lobby.Scores[playerID] = &PlayerScore{}

	lobbiesMux.Lock()
	lobbies[roomCode] = lobby
	lobbiesMux.Unlock()

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

// handleJoinLobby allows a player to join an existing lobby
func handleJoinLobby(w http.ResponseWriter, r *http.Request) {
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

	lobbiesMux.RLock()
	lobby, exists := lobbies[roomCode]
	lobbiesMux.RUnlock()

	if !exists {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}

	lobby.mu.Lock()
	if lobby.CurrentGame != nil {
		lobby.mu.Unlock()
		http.Error(w, "Game in progress", http.StatusBadRequest)
		return
	}

	playerID := uuid.New().String()
	lobby.Players[playerID] = &Player{ID: playerID, Name: playerName}
	lobby.Scores[playerID] = &PlayerScore{}
	lobby.mu.Unlock()

	log.Printf("Player joined lobby: code=%s playerID=%s name=%s", roomCode, playerID, playerName)

	// Broadcast update to all clients
	lobby.broadcastSSE("player-update", lobby.renderPlayerList())
	lobby.broadcastSSE("score-update", lobby.renderScoreTable())
	lobby.broadcastPersonalizedControls()

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

// handleLobby displays the lobby page
func handleLobby(w http.ResponseWriter, r *http.Request) {
	roomCode := strings.TrimPrefix(r.URL.Path, "/lobby/")

	lobbiesMux.RLock()
	lobby, exists := lobbies[roomCode]
	lobbiesMux.RUnlock()

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

	lobby.mu.RLock()
	defer lobby.mu.RUnlock()

	data := struct {
		RoomCode string
		PlayerID string
		Players  []*Player
		IsHost   bool
		Scores   map[string]*PlayerScore
	}{
		RoomCode: lobby.Code,
		PlayerID: playerID,
		Players:  getPlayerList(lobby.Players),
		IsHost:   lobby.Host == playerID,
		Scores:   lobby.Scores,
	}

	templates.ExecuteTemplate(w, "lobby.html", data)
}

// handleSSE handles Server-Sent Events for real-time updates
func handleSSE(w http.ResponseWriter, r *http.Request) {
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
		lobby, pid, err := getLobbyAndPlayer(r, roomCode)
		if err != nil {
			// Not authorized or lobby validation failed: instruct client to navigate home via HTMX snippet
			w.Header().Set("Content-Type", "text/event-stream")
			w.Header().Set("Cache-Control", "no-cache")
			w.Header().Set("Connection", "keep-alive")
			fmt.Fprintf(w, "event: nav-redirect\ndata: %s\n\n", redirectSnippet(roomCode, "/"))
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

	lobbiesMux.RLock()
	lobby, exists := lobbies[roomCode]
	lobbiesMux.RUnlock()

	if !exists {
		if debug {
			log.Printf("handleSSE: room %s not found, sending nav-redirect to home", roomCode)
		}
		// Set SSE headers
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")

		// Send nav-redirect snippet for HTMX to navigate home
		fmt.Fprintf(w, "event: nav-redirect\ndata: %s\n\n", redirectSnippet(roomCode, "/"))
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
	clientChan := make(chan SSEMessage, 10)
	lobby.addSSEClient(clientChan, playerID)
	defer lobby.removeSSEClient(clientChan)

	lobby.mu.RLock()
	clientCount := len(lobby.sseClients)
	lobby.mu.RUnlock()
	if debug {
		log.Printf("handleSSE: client %s connected, now have %d total clients", playerID, clientCount)
	}

	// Send initial data based on whether a game is in progress
	lobby.mu.RLock()
	gameInProgress := lobby.CurrentGame != nil
	if gameInProgress {
		// Game in progress - send ready count or vote count with phase-specific event
		game := lobby.CurrentGame
		readyCount := 0
		var countHTML string
		var eventName string

		// Count from appropriate ready state map based on game phase
		switch game.Status {
		case StatusReadyCheck:
			for id := range lobby.Players {
				if game.ReadyToReveal[id] {
					readyCount++
				}
			}
			totalPlayers := len(lobby.Players)
			countHTML = lobby.renderReadyCountCheck(readyCount, totalPlayers)
			eventName = "ready-count-check"
		case StatusRoleReveal:
			for id := range lobby.Players {
				if game.ReadyAfterReveal[id] {
					readyCount++
				}
			}
			totalPlayers := len(lobby.Players)
			countHTML = lobby.renderReadyCountReveal(readyCount, totalPlayers)
			eventName = "ready-count-reveal"
		case StatusPlaying:
			for id := range lobby.Players {
				if game.ReadyToVote[id] {
					readyCount++
				}
			}
			totalPlayers := len(lobby.Players)
			countHTML = lobby.renderReadyCountPlaying(readyCount, totalPlayers)
			eventName = "ready-count-playing"
		case StatusVoting:
			// Send vote count for voting phase
			voteCount := len(game.Votes)
			totalPlayers := len(lobby.Players)
			countHTML = renderVoteCount(voteCount, totalPlayers)
			eventName = "vote-count-voting"
		}
		lobby.mu.RUnlock()
		if debug {
			log.Printf("handleSSE: sending initial %s to player %s", eventName, playerID)
		}
		fmt.Fprintf(w, "event: %s\ndata: %s\n\n", eventName, countHTML)
	} else {
		// No game - send lobby data
		playerListHTML := lobby.renderPlayerList()
		hostControlsHTML := lobby.renderHostControls(playerID)
		scoreTableHTML := lobby.renderScoreTable()
		lobby.mu.RUnlock()
		if debug {
			log.Printf("handleSSE: sending initial lobby data to player %s", playerID)
		}
		fmt.Fprintf(w, "event: player-update\ndata: %s\n\n", playerListHTML)
		fmt.Fprintf(w, "event: controls-update\ndata: %s\n\n", hostControlsHTML)
		if scoreTableHTML != "" {
			fmt.Fprintf(w, "event: score-update\ndata: %s\n\n", scoreTableHTML)
		}
	}
	w.(http.Flusher).Flush()

	// Listen for updates
	ctx := r.Context()
	for {
		select {
		case <-ctx.Done():
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

// handleGameMux routes game subpaths by phase and actions
func handleGameMux(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/game/")
	parts := strings.Split(strings.Trim(path, "/"), "/")
	if len(parts) == 0 || parts[0] == "" {
		http.Error(w, "Invalid URL", http.StatusBadRequest)
		return
	}
	roomCode := parts[0]
	seg := ""
	if len(parts) > 1 {
		seg = parts[1]
	}

	// Reject unknown subpaths under /game/:code
	if seg != "" && seg != "confirm-reveal" && seg != "roles" && seg != "play" && seg != "voting" && seg != "ready" && seg != "vote" && seg != "redirect" {
		http.NotFound(w, r)
		return
	}

	// Redirect helper for HTMX
	if seg == "redirect" {
		to := r.URL.Query().Get("to")
		if to == "" {
			to = "/lobby/" + roomCode
		} else if !strings.HasPrefix(to, "/") {
			to = "/game/" + roomCode + "/" + to
		}
		w.Header().Set("HX-Location", to)
		w.WriteHeader(http.StatusOK)
		return
	}

	// POST actions under /game/:code
	if r.Method == http.MethodPost {
		switch seg {
		case "ready":
			gameHandleReadyCookie(w, r, roomCode)
			return
		case "vote":
			gameHandleVoteCookie(w, r, roomCode)
			return
		default:
			http.Error(w, "Not found", http.StatusNotFound)
			return
		}
	}

	// GET phase pages: confirm-reveal, roles, play, voting
	lobby, playerID, err := getLobbyAndPlayer(r, roomCode)
	if err != nil {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}

	lobby.mu.RLock()
	game := lobby.CurrentGame
	lobby.mu.RUnlock()
	if game == nil {
		http.Redirect(w, r, "/lobby/"+roomCode, http.StatusSeeOther)
		return
	}

	// Guard: ensure path matches current phase; redirect canonical path
	currentPath := phasePathFor(roomCode, game.Status)
	if seg == "" || !strings.HasSuffix(currentPath, "/"+seg) {
		if r.Header.Get("HX-Request") == "true" {
			w.Header().Set("HX-Redirect", currentPath)
			w.WriteHeader(http.StatusOK)
		} else {
			http.Redirect(w, r, currentPath, http.StatusSeeOther)
		}
		return
	}

	// Build page using per-phase template
	lobby.mu.RLock()
	g := lobby.CurrentGame
	playerInfo := g.PlayerInfo[playerID]

	// time remaining
	timeRemaining := "10:00"
	secondsRemaining := 600
	if g.Status == StatusPlaying {
		elapsed := time.Since(g.StartTime)
		remaining := 10*time.Minute - elapsed
		if remaining > 0 {
			minutes := int(remaining.Minutes())
			seconds := int(remaining.Seconds()) % 60
			timeRemaining = fmt.Sprintf("%d:%02d", minutes, seconds)
			secondsRemaining = int(remaining.Seconds())
			if secondsRemaining > 600 {
				secondsRemaining = 600
			}
		} else {
			timeRemaining = "0:00"
			secondsRemaining = 0
		}
	} else {
		secondsRemaining = 0
	}

	isReady := false
	switch g.Status {
	case StatusReadyCheck:
		isReady = g.ReadyToReveal[playerID]
	case StatusRoleReveal:
		isReady = g.ReadyAfterReveal[playerID]
	case StatusPlaying:
		isReady = g.ReadyToVote[playerID]
	}

	data := struct {
		RoomCode         string
		PlayerID         string
		Status           GameStatus
		Players          []*Player
		TotalPlayers     int
		Location         *Location
		Challenge        string
		IsSpy            bool
		IsReady          bool
		HasVoted         bool
		VoteRound        int
		FirstQuestioner  string
		TimeRemaining    string
		SecondsRemaining int
	}{
		RoomCode:         roomCode,
		PlayerID:         playerID,
		Status:           g.Status,
		Players:          getPlayerList(lobby.Players),
		TotalPlayers:     len(lobby.Players),
		Location:         g.Location,
		Challenge:        playerInfo.Challenge,
		IsSpy:            playerInfo.IsSpy,
		IsReady:          isReady,
		HasVoted:         g.Votes[playerID] != "",
		VoteRound:        g.VoteRound,
		FirstQuestioner:  g.FirstQuestioner,
		TimeRemaining:    timeRemaining,
		SecondsRemaining: secondsRemaining,
	}
	lobby.mu.RUnlock()

	// Select template by phase
	tmpl := ""
	switch g.Status {
	case StatusReadyCheck:
		tmpl = "game_confirm_reveal.html"
	case StatusRoleReveal:
		tmpl = "game_roles.html"
	case StatusPlaying:
		tmpl = "game_play.html"
	case StatusVoting:
		tmpl = "game_voting.html"
	default:
		// Should not happen due to guard; send to lobby
		w.Header().Set("HX-Redirect", "/lobby/"+roomCode)
		w.WriteHeader(http.StatusOK)
		return
	}
	templates.ExecuteTemplate(w, tmpl, data)
}

// gameHandleReadyCookie updates readiness using cookie-based player ID
func gameHandleReadyCookie(w http.ResponseWriter, r *http.Request, roomCode string) {
	lobbiesMux.RLock()
	lobby, exists := lobbies[roomCode]
	lobbiesMux.RUnlock()
	if !exists {
		http.Error(w, "Lobby not found", http.StatusNotFound)
		return
	}

	cookie, err := r.Cookie("player_id")
	if err != nil {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}
	playerID := cookie.Value

	// Values derived from server state only
	var readyCountMsg string
	var buttonHTML string
	var shouldBroadcastPhase bool
	var readyCountEventName string

	lobby.mu.Lock()
	game := lobby.CurrentGame
	if game == nil {
		lobby.mu.Unlock()
		http.Error(w, "No game in progress", http.StatusBadRequest)
		return
	}

	statusBefore := game.Status

	// Update readiness per phase rules (toggle in all phases to surface issues)
	var isReady bool
	var prev bool
	var readyStateMap map[string]bool
	switch statusBefore {
	case StatusReadyCheck:
		readyStateMap = game.ReadyToReveal
		prev = game.ReadyToReveal[playerID]
		game.ReadyToReveal[playerID] = !game.ReadyToReveal[playerID]
		isReady = game.ReadyToReveal[playerID]
	case StatusRoleReveal:
		readyStateMap = game.ReadyAfterReveal
		prev = game.ReadyAfterReveal[playerID]
		game.ReadyAfterReveal[playerID] = !game.ReadyAfterReveal[playerID]
		isReady = game.ReadyAfterReveal[playerID]
	case StatusPlaying:
		readyStateMap = game.ReadyToVote
		prev = game.ReadyToVote[playerID]
		game.ReadyToVote[playerID] = !game.ReadyToVote[playerID]
		isReady = game.ReadyToVote[playerID]
	default:
		lobby.mu.Unlock()
		http.Error(w, "Invalid game phase", http.StatusBadRequest)
		return
	}

	// Compute ready count from server state (no client math) and gather confirmed names using lobby players
	readyCount := 0
	confirmedNames := make([]string, 0)
	for id := range lobby.Players {
		if readyStateMap[id] {
			readyCount++
			if p, ok := lobby.Players[id]; ok {
				confirmedNames = append(confirmedNames, p.Name)
			} else {
				confirmedNames = append(confirmedNames, fmt.Sprintf("unknown(%s)", id))
			}
		}
	}
	totalPlayers := len(lobby.Players)

	// Actor name for logging
	actorName := "unknown"
	if p, ok := lobby.Players[playerID]; ok {
		actorName = p.Name
	}

	// Decide whether to advance based on the computed count
	shouldAdvance := false
	switch statusBefore {
	case StatusReadyCheck, StatusRoleReveal:
		shouldAdvance = readyCount == totalPlayers
	case StatusPlaying:
		shouldAdvance = readyCount > totalPlayers/2
	}

	// Prepare outgoing UI for the CURRENT (pre-advance) phase
	switch statusBefore {
	case StatusReadyCheck:
		readyCountMsg = lobby.renderReadyCountCheck(readyCount, len(lobby.Players))
		readyCountEventName = "ready-count-check"
	case StatusRoleReveal:
		readyCountMsg = lobby.renderReadyCountReveal(readyCount, len(lobby.Players))
		readyCountEventName = "ready-count-reveal"
	case StatusPlaying:
		readyCountMsg = lobby.renderReadyCountPlaying(readyCount, len(lobby.Players))
		readyCountEventName = "ready-count-playing"
	}

	buttonID := "ready-button-check"
	buttonText := "I'm Ready to See My Role"
	buttonClass := "btn btn-primary"
	buttonDisabled := false
	switch statusBefore {
	case StatusReadyCheck:
		buttonID = "ready-button-check"
		if isReady {
			buttonText = "✓ Ready - Waiting for others..."
			buttonClass = "btn btn-success"
		} else {
			buttonText = "I'm Ready to See My Role"
			buttonClass = "btn btn-primary"
		}
	case StatusRoleReveal:
		buttonID = "ready-button-role"
		if isReady {
			buttonText = "✓ Waiting for others..."
			buttonClass = "btn btn-success"
		} else {
			buttonText = "I've Seen My Role ✓"
			buttonClass = "btn btn-primary"
		}
	case StatusPlaying:
		buttonID = "ready-button-playing"
		if isReady {
			buttonText = "✓ Ready to Vote"
			buttonClass = "btn btn-success"
		} else {
			buttonText = "Ready to Vote?"
			buttonClass = "btn btn-secondary"
		}
	}
	if buttonDisabled {
		buttonHTML = fmt.Sprintf(`<button id="%s" type="submit" class="%s" disabled>%s</button>`, buttonID, buttonClass, buttonText)
	} else {
		buttonHTML = fmt.Sprintf(`<button id="%s" type="submit" class="%s">%s</button>`, buttonID, buttonClass, buttonText)
	}

	// Detailed logging for readiness change
	if debug {
		log.Printf("ready: room=%s phase=%s actor=%s(%s) prev=%v now=%v confirmed=[%s] count=%d/%d", roomCode, statusBefore, actorName, playerID, prev, isReady, strings.Join(confirmedNames, ", "), readyCount, totalPlayers)
	}

	// Advance AFTER preparing current-phase outputs
	nextPath := ""
	if shouldAdvance {
		switch statusBefore {
		case StatusReadyCheck:
			game.Status = StatusRoleReveal
			// Pre-seed next phase readiness map
			for id := range lobby.Players {
				if _, ok := game.ReadyAfterReveal[id]; !ok {
					game.ReadyAfterReveal[id] = false
				}
			}
			nextPath = phasePathFor(roomCode, game.Status)
			shouldBroadcastPhase = true
		case StatusRoleReveal:
			game.Status = StatusPlaying
			// Pre-seed next phase readiness map
			for id := range lobby.Players {
				if _, ok := game.ReadyToVote[id]; !ok {
					game.ReadyToVote[id] = false
				}
			}
			game.StartTime = time.Now()
			// Choose random first questioner
			playerIDs := make([]string, 0, len(lobby.Players))
			for id := range lobby.Players {
				playerIDs = append(playerIDs, id)
			}
			game.FirstQuestioner = playerIDs[rand.Intn(len(playerIDs))]
			nextPath = phasePathFor(roomCode, game.Status)
			shouldBroadcastPhase = true
		case StatusPlaying:
			game.Status = StatusVoting
			nextPath = phasePathFor(roomCode, game.Status)
			shouldBroadcastPhase = true
		}
	}
	lobby.mu.Unlock()

	// Broadcast the server-derived current-phase count
	lobby.broadcastSSE(readyCountEventName, readyCountMsg)

	// If phase advanced, instruct clients to navigate; no client-side math
	if shouldBroadcastPhase {
		lobby.broadcastSSE("nav-redirect", redirectSnippet(roomCode, nextPath))
		// Also ensure the initiating client navigates via HX-Redirect
		w.Header().Set("HX-Redirect", nextPath)
		w.WriteHeader(http.StatusOK)
		return
	}

	w.Header().Set("Content-Type", "text/html")
	w.Write([]byte(buttonHTML))
}

// gameHandleVoteCookie records a vote using cookie-based player ID
func gameHandleVoteCookie(w http.ResponseWriter, r *http.Request, roomCode string) {
	lobbiesMux.RLock()
	lobby, exists := lobbies[roomCode]
	lobbiesMux.RUnlock()
	if !exists {
		http.Error(w, "Lobby not found", http.StatusNotFound)
		return
	}

	cookie, err := r.Cookie("player_id")
	if err != nil {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}
	playerID := cookie.Value

	r.ParseForm()
	suspectID := r.FormValue("suspect")

	var voteCountMsg string
	var shouldFinish bool
	var shouldRevote bool
	var newVoteRound int
	_ = newVoteRound

	lobby.mu.Lock()
	game := lobby.CurrentGame
	if game == nil || game.Status != StatusVoting {
		lobby.mu.Unlock()
		http.Error(w, "Not in voting phase", http.StatusBadRequest)
		return
	}

	game.Votes[playerID] = suspectID

	if len(game.Votes) == len(lobby.Players) {
		// Count votes
		voteCount := make(map[string]int)
		for _, votedFor := range game.Votes {
			voteCount[votedFor]++
		}

		maxVotes := 0
		var playersWithMaxVotes []string
		for pID, count := range voteCount {
			if count > maxVotes {
				maxVotes = count
				playersWithMaxVotes = []string{pID}
			} else if count == maxVotes {
				playersWithMaxVotes = append(playersWithMaxVotes, pID)
			}
		}

		if len(playersWithMaxVotes) > 1 && game.VoteRound < 3 {
			// tie -> revote
			game.Votes = make(map[string]string)
			game.VoteRound++
			newVoteRound = game.VoteRound
			shouldRevote = true
		} else {
			// finish game
			game.Status = StatusFinished
			innocentWon := len(playersWithMaxVotes) == 1 && playersWithMaxVotes[0] == game.SpyID
			for id := range lobby.Players {
				if id == game.SpyID {
					if innocentWon {
						lobby.Scores[id].GamesLost++
					} else {
						lobby.Scores[id].GamesWon++
					}
				} else {
					if innocentWon {
						lobby.Scores[id].GamesWon++
					} else {
						lobby.Scores[id].GamesLost++
					}
				}
			}
			shouldFinish = true
		}
	}

	voteCountMsg = renderVoteCount(len(game.Votes), len(lobby.Players))
	lobby.mu.Unlock()

	lobby.broadcastSSE("vote-count-voting", voteCountMsg)
	if shouldRevote {
		lobby.broadcastSSE("nav-redirect", redirectSnippet(roomCode, phasePathFor(roomCode, StatusVoting)))
	} else if shouldFinish {
		lobby.broadcastSSE("nav-redirect", redirectSnippet(roomCode, phasePathFor(roomCode, StatusFinished)))
	}

	w.Header().Set("Content-Type", "text/html")
	w.Write([]byte(renderVotedConfirmation()))
}

// handleResults displays the game results
func handleResults(w http.ResponseWriter, r *http.Request) {
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/results/"), "/")
	if len(parts) < 1 || len(parts) > 2 {
		http.Error(w, "Invalid URL", http.StatusBadRequest)
		return
	}
	roomCode := parts[0]

	var playerID string
	if len(parts) == 2 {
		// legacy style with player in path
		playerID = parts[1]
	} else {
		_, pid, err := getLobbyAndPlayer(r, roomCode)
		if err != nil {
			http.Redirect(w, r, "/", http.StatusSeeOther)
			return
		}
		playerID = pid
	}

	lobbiesMux.RLock()
	lobby, exists := lobbies[roomCode]
	lobbiesMux.RUnlock()

	if !exists {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}

	lobby.mu.RLock()
	defer lobby.mu.RUnlock()

	if lobby.CurrentGame == nil {
		http.Redirect(w, r, "/lobby/"+roomCode, http.StatusSeeOther)
		return
	}

	game := lobby.CurrentGame
	if game.Status != StatusFinished {
		http.Redirect(w, r, phasePathFor(roomCode, game.Status), http.StatusSeeOther)
		return
	}

	// Calculate vote counts
	voteCount := make(map[string]int)
	for _, suspectID := range game.Votes {
		voteCount[suspectID]++
	}

	// Find most voted and check for tie
	var mostVoted string
	maxVotes := 0
	isTie := false
	voteCounts := make(map[int]int) // count -> frequency
	for _, count := range voteCount {
		voteCounts[count]++
		if count > maxVotes {
			maxVotes = count
		}
	}
	if voteCounts[maxVotes] > 1 {
		isTie = true
	} else {
		for suspectID, count := range voteCount {
			if count == maxVotes {
				mostVoted = suspectID
				break
			}
		}
	}

	innocentWon := !isTie && mostVoted == game.SpyID

	// Build challenges map
	challengesMap := make(map[string]string)
	for pid, info := range game.PlayerInfo {
		challengesMap[pid] = info.Challenge
	}

	// Build voted correctly map
	votedCorrectly := make(map[string]bool)
	for voterID, suspectID := range game.Votes {
		votedCorrectly[voterID] = suspectID == game.SpyID
	}

	spy := lobby.Players[game.SpyID]

	data := struct {
		RoomCode       string
		PlayerID       string
		IsHost         bool
		Players        []*Player
		Spy            *Player
		Location       *Location
		Challenges     map[string]string
		Votes          map[string]string
		VoteCount      map[string]int
		VotedCorrectly map[string]bool
		VoteRounds     int
		MostVoted      string
		IsTie          bool
		InnocentWon    bool
	}{
		RoomCode:       roomCode,
		PlayerID:       playerID,
		IsHost:         lobby.Host == playerID,
		Players:        getPlayerList(lobby.Players),
		Spy:            spy,
		Location:       game.Location,
		Challenges:     challengesMap,
		Votes:          game.Votes,
		VoteCount:      voteCount,
		VotedCorrectly: votedCorrectly,
		VoteRounds:     game.VoteRound,
		MostVoted:      mostVoted,
		IsTie:          isTie,
		InnocentWon:    innocentWon,
	}

	templates.ExecuteTemplate(w, "results.html", data)
}

// handleStartGame starts a new game in the lobby
func handleStartGame(w http.ResponseWriter, r *http.Request) {
	log.Printf("handleStartGame called: %s %s", r.Method, r.URL.Path)

	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	roomCode := strings.TrimPrefix(r.URL.Path, "/start-game/")

	log.Printf("handleStartGame: roomCode=%s", roomCode)

	lobbiesMux.RLock()
	lobby, exists := lobbies[roomCode]
	lobbiesMux.RUnlock()

	if !exists {
		log.Printf("handleStartGame: lobby %s not found", roomCode)
		http.Error(w, "Lobby not found", http.StatusNotFound)
		return
	}

	// Get player ID from cookie
	cookie, err := r.Cookie("player_id")
	if err != nil {
		log.Printf("handleStartGame: no player_id cookie")
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}
	playerID := cookie.Value

	log.Printf("handleStartGame: playerID=%s isHost=%v", playerID, lobby.Host == playerID)

	lobby.mu.Lock()

	// Check if player is host
	if lobby.Host != playerID {
		lobby.mu.Unlock()
		log.Printf("handleStartGame: player is not host")
		http.Error(w, "Only host can start game", http.StatusForbidden)
		return
	}

	if lobby.CurrentGame != nil {
		lobby.mu.Unlock()
		log.Printf("handleStartGame: game already in progress")
		http.Error(w, "Game already in progress", http.StatusBadRequest)
		return
	}

	if len(lobby.Players) < 3 {
		lobby.mu.Unlock()
		log.Printf("handleStartGame: not enough players (%d)", len(lobby.Players))
		http.Error(w, "Need at least 3 players", http.StatusBadRequest)
		return
	}

	log.Printf("handleStartGame: creating game for lobby %s", roomCode)

	// Create new game
	game := &Game{
		Lobby:            lobby,
		Location:         &locations[rand.Intn(len(locations))],
		PlayerInfo:       make(map[string]*GamePlayerInfo),
		Status:           StatusReadyCheck,
		ReadyToReveal:    make(map[string]bool),
		ReadyAfterReveal: make(map[string]bool),
		ReadyToVote:      make(map[string]bool),
		Votes:            make(map[string]string),
		VoteRound:        1,
	}
	// Pre-seed current phase readiness map with all players
	for id := range lobby.Players {
		game.ReadyToReveal[id] = false
	}

	// Assign spy
	playerIDs := make([]string, 0, len(lobby.Players))
	for id := range lobby.Players {
		playerIDs = append(playerIDs, id)
	}
	game.SpyID = playerIDs[rand.Intn(len(playerIDs))]

	// Assign challenges and roles
	shuffledChallenges := make([]string, len(challenges))
	copy(shuffledChallenges, challenges)
	rand.Shuffle(len(shuffledChallenges), func(i, j int) {
		shuffledChallenges[i], shuffledChallenges[j] = shuffledChallenges[j], shuffledChallenges[i]
	})

	for i, id := range playerIDs {
		game.PlayerInfo[id] = &GamePlayerInfo{
			Challenge: shuffledChallenges[i%len(shuffledChallenges)],
			IsSpy:     id == game.SpyID,
		}
	}

	lobby.CurrentGame = game
	lobby.mu.Unlock()

	log.Printf("handleStartGame: game created, broadcasting redirect to confirm-reveal")

	// Broadcast HTMX redirect snippet to all clients to go to confirm-reveal
	lobby.broadcastSSE("nav-redirect", redirectSnippet(roomCode, phasePathFor(roomCode, StatusReadyCheck)))

	log.Printf("handleStartGame: complete")
	w.Header().Set("HX-Redirect", phasePathFor(roomCode, StatusReadyCheck))
	w.WriteHeader(http.StatusOK)
}

// handleRestartGame resets the game and returns to lobby
func handleRestartGame(w http.ResponseWriter, r *http.Request) {
	log.Printf("handleRestartGame called: %s %s", r.Method, r.URL.Path)

	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	roomCode := strings.TrimPrefix(r.URL.Path, "/restart-game/")

	lobbiesMux.RLock()
	lobby, exists := lobbies[roomCode]
	lobbiesMux.RUnlock()

	if !exists {
		http.Error(w, "Lobby not found", http.StatusNotFound)
		return
	}

	// Get player ID from cookie
	cookie, err := r.Cookie("player_id")
	if err != nil {
		log.Printf("handleRestartGame: no player_id cookie")
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}
	playerID := cookie.Value

	lobby.mu.Lock()

	// Check if player is host
	if lobby.Host != playerID {
		lobby.mu.Unlock()
		log.Printf("handleRestartGame: player %s is not host", playerID)
		http.Error(w, "Only host can restart game", http.StatusForbidden)
		return
	}

	// Clear game
	lobby.CurrentGame = nil

	lobby.mu.Unlock()

	log.Printf("handleRestartGame: game cleared, broadcasting nav-redirect to lobby")

	// Broadcast restart WITHOUT holding lock
	lobby.broadcastSSE("nav-redirect", redirectSnippet(roomCode, "/lobby/"+roomCode))

	log.Printf("handleRestartGame: sending redirect response")
	w.Header().Set("HX-Redirect", "/lobby/"+roomCode)
	w.WriteHeader(http.StatusOK)
}

// handleCloseLobby deletes the lobby
func handleCloseLobby(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	roomCode := strings.TrimPrefix(r.URL.Path, "/close-lobby/")

	lobbiesMux.RLock()
	lobby, exists := lobbies[roomCode]
	lobbiesMux.RUnlock()

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

	lobby.mu.Lock()
	if lobby.Host != playerID {
		lobby.mu.Unlock()
		http.Error(w, "Only host can close lobby", http.StatusForbidden)
		return
	}
	lobby.mu.Unlock()

	// Broadcast closure
	lobby.broadcastSSE("nav-redirect", redirectSnippet(roomCode, "/"))

	// Delete lobby
	lobbiesMux.Lock()
	delete(lobbies, roomCode)
	lobbiesMux.Unlock()

	w.Header().Set("HX-Redirect", "/")
	w.WriteHeader(http.StatusOK)
}

// ===== Game Methods =====

// redirectSnippet returns an HTMX snippet that triggers a client-side redirect
// by issuing a GET to /game/:code/redirect which replies with HX-Location.
func redirectSnippet(roomCode, to string) string {
	return fmt.Sprintf(`<div hx-get="/game/%s/redirect?to=%s" hx-trigger="load" hx-swap="none"></div>`, roomCode, to)
}
