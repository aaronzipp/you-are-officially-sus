package main

import (
	"encoding/json"
	"fmt"
	"html/template"
	"log"
	"math/rand"
	"net/http"
	"os"
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
	Event string // Event type (e.g., "player-update", "phase-change")
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
	VoteRound        int       // Track voting rounds for tie-breaking
	timerDone        chan bool // Signal to stop timer goroutine
}

// Global storage
var (
	lobbies    = make(map[string]*Lobby)
	lobbiesMux sync.RWMutex
	locations  []Location
	challenges []string
	templates  *template.Template
)

func init() {
	rand.Seed(time.Now().UnixNano())
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
	http.HandleFunc("/game/", handleGame)
	http.HandleFunc("/results/", handleResults)
	http.HandleFunc("/ready/", handleReady)
	http.HandleFunc("/vote/", handleVote)
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

	log.Printf("broadcastSSE: event=%s data=%s to %d clients", event, data, clientCount)

	// Send messages WITHOUT holding the lock
	msg := SSEMessage{Event: event, Data: data}
	successCount := 0
	for _, client := range clients {
		select {
		case client <- msg:
			successCount++
		case <-time.After(1 * time.Second):
			log.Printf("broadcastSSE: timeout sending to client")
		}
	}
	log.Printf("broadcastSSE: sent to %d/%d clients successfully", successCount, clientCount)
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
	html := fmt.Sprintf(`<h2>Players (%d)</h2><ul class="player-list">`, len(l.Players))
	for _, p := range l.Players {
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
	return list
}

// ===== HTTP Handlers =====

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

	// Set cookie for player ID
	http.SetCookie(w, &http.Cookie{
		Name:  "player_id",
		Value: playerID,
		Path:  "/",
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

	// Set cookie for player ID
	http.SetCookie(w, &http.Cookie{
		Name:  "player_id",
		Value: playerID,
		Path:  "/",
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
	log.Printf("handleSSE called: %s", r.URL.Path)

	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/sse/"), "/")
	if len(parts) != 2 {
		log.Printf("handleSSE: invalid URL parts=%v", parts)
		http.Error(w, "Invalid URL", http.StatusBadRequest)
		return
	}
	roomCode := parts[0]
	playerID := parts[1]

	log.Printf("handleSSE: roomCode=%s playerID=%s", roomCode, playerID)

	lobbiesMux.RLock()
	lobby, exists := lobbies[roomCode]
	lobbiesMux.RUnlock()

	if !exists {
		log.Printf("handleSSE: room %s not found, sending redirect event", roomCode)
		// Set SSE headers
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")

		// Send lobby-not-found event to trigger client-side redirect
		fmt.Fprintf(w, "event: lobby-not-found\ndata: Room not found\n\n")
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
		return
	}

	log.Printf("handleSSE: found lobby, setting up SSE for player %s", playerID)

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
	log.Printf("handleSSE: client %s connected, now have %d total clients", playerID, clientCount)

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
			for _, ready := range game.ReadyToReveal {
				if ready {
					readyCount++
				}
			}
			totalPlayers := len(lobby.Players)
			countHTML = lobby.renderReadyCountCheck(readyCount, totalPlayers)
			eventName = "ready-count-check"
		case StatusRoleReveal:
			for _, ready := range game.ReadyAfterReveal {
				if ready {
					readyCount++
				}
			}
			totalPlayers := len(lobby.Players)
			countHTML = lobby.renderReadyCountReveal(readyCount, totalPlayers)
			eventName = "ready-count-reveal"
		case StatusPlaying:
			for _, ready := range game.ReadyToVote {
				if ready {
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
		log.Printf("handleSSE: sending initial %s to player %s: %s", eventName, playerID, countHTML)
		fmt.Fprintf(w, "event: %s\ndata: %s\n\n", eventName, countHTML)
	} else {
		// No game - send lobby data
		playerListHTML := lobby.renderPlayerList()
		hostControlsHTML := lobby.renderHostControls(playerID)
		scoreTableHTML := lobby.renderScoreTable()
		lobby.mu.RUnlock()
		log.Printf("handleSSE: sending initial lobby data to player %s", playerID)
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
			log.Printf("handleSSE: sending event=%s to player %s", msg.Event, playerID)
			fmt.Fprintf(w, "event: %s\ndata: %s\n\n", msg.Event, msg.Data)
			w.(http.Flusher).Flush()
		}
	}
}

// handleGame displays the unified game page for all phases
func handleGame(w http.ResponseWriter, r *http.Request) {
	log.Printf("handleGame called: %s", r.URL.Path)

	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/game/"), "/")
	if len(parts) != 2 {
		log.Printf("handleGame: invalid URL parts=%v", parts)
		http.Error(w, "Invalid URL", http.StatusBadRequest)
		return
	}
	roomCode := parts[0]
	playerID := parts[1]

	log.Printf("handleGame: roomCode=%s playerID=%s", roomCode, playerID)

	lobbiesMux.RLock()
	lobby, exists := lobbies[roomCode]
	lobbiesMux.RUnlock()

	if !exists {
		log.Printf("handleGame: lobby not found, redirecting to home")
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}

	lobby.mu.RLock()
	defer lobby.mu.RUnlock()

	if lobby.CurrentGame == nil {
		log.Printf("handleGame: no game in progress, redirecting to lobby")
		http.Redirect(w, r, "/lobby/"+roomCode, http.StatusSeeOther)
		return
	}

	log.Printf("handleGame: rendering game page for player %s in phase %s", playerID, lobby.CurrentGame.Status)

	game := lobby.CurrentGame
	player := lobby.Players[playerID]
	if player == nil {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}

	playerInfo := game.PlayerInfo[playerID]
	if playerInfo == nil {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}

	// Calculate time remaining
	timeRemaining := "10:00"
	if game.Status == StatusPlaying {
		elapsed := time.Since(game.StartTime)
		remaining := 10*time.Minute - elapsed
		if remaining > 0 {
			minutes := int(remaining.Minutes())
			seconds := int(remaining.Seconds()) % 60
			timeRemaining = fmt.Sprintf("%d:%02d", minutes, seconds)
		} else {
			timeRemaining = "0:00"
		}
	}

	// Determine if player is ready based on current game phase
	isReady := false
	switch game.Status {
	case StatusReadyCheck:
		isReady = game.ReadyToReveal[playerID]
	case StatusRoleReveal:
		isReady = game.ReadyAfterReveal[playerID]
	case StatusPlaying:
		isReady = game.ReadyToVote[playerID]
	}

	data := struct {
		RoomCode        string
		PlayerID        string
		Status          GameStatus
		Players         []*Player
		TotalPlayers    int
		Location        *Location
		Challenge       string
		IsSpy           bool
		IsReady         bool
		HasVoted        bool
		VoteRound       int
		FirstQuestioner string
		TimeRemaining   string
	}{
		RoomCode:        roomCode,
		PlayerID:        playerID,
		Status:          game.Status,
		Players:         getPlayerList(lobby.Players),
		TotalPlayers:    len(lobby.Players),
		Location:        game.Location,
		Challenge:       playerInfo.Challenge,
		IsSpy:           playerInfo.IsSpy,
		IsReady:         isReady,
		HasVoted:        game.Votes[playerID] != "",
		VoteRound:       game.VoteRound,
		FirstQuestioner: game.FirstQuestioner,
		TimeRemaining:   timeRemaining,
	}

	templates.ExecuteTemplate(w, "game.html", data)
}

// handleResults displays the game results
func handleResults(w http.ResponseWriter, r *http.Request) {
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/results/"), "/")
	if len(parts) != 2 {
		http.Error(w, "Invalid URL", http.StatusBadRequest)
		return
	}
	roomCode := parts[0]
	playerID := parts[1]

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
		http.Redirect(w, r, "/game/"+roomCode+"/"+playerID, http.StatusSeeOther)
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
	challenges := make(map[string]string)
	for pid, info := range game.PlayerInfo {
		challenges[pid] = info.Challenge
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
		Challenges:     challenges,
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
		timerDone:        make(chan bool),
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

	log.Printf("handleStartGame: game created, broadcasting game-started event")

	// Notify all clients that game started
	lobby.broadcastSSE("game-started", roomCode)

	log.Printf("handleStartGame: complete")
	w.WriteHeader(http.StatusOK)
}

// handleReady handles all ready state toggles (unified for all phases)
func handleReady(w http.ResponseWriter, r *http.Request) {
	log.Printf("handleReady called: %s %s (full path)", r.Method, r.URL.Path)

	if r.Method != http.MethodPost {
		log.Printf("handleReady: wrong method %s", r.Method)
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/ready/"), "/")
	log.Printf("handleReady: parts = %v (len=%d)", parts, len(parts))

	if len(parts) != 2 {
		log.Printf("handleReady: invalid URL, expected 2 parts got %d", len(parts))
		http.Error(w, "Invalid URL", http.StatusBadRequest)
		return
	}
	roomCode := parts[0]
	playerID := parts[1]

	lobbiesMux.RLock()
	lobby, exists := lobbies[roomCode]
	lobbiesMux.RUnlock()

	if !exists {
		http.Error(w, "Lobby not found", http.StatusNotFound)
		return
	}

	log.Printf("handleReady: acquiring lock for room %s player %s", roomCode, playerID)

	// Prepare all broadcast messages while holding lock
	var phaseChangeMsg string
	var readyCountMsg string
	var buttonHTML string
	var shouldBroadcastPhase bool

	lobby.mu.Lock()
	game := lobby.CurrentGame
	if game == nil {
		lobby.mu.Unlock()
		http.Error(w, "No game in progress", http.StatusBadRequest)
		return
	}

	log.Printf("handleReady: toggling ready state for player %s in phase %s", playerID, game.Status)

	// Toggle ready state in the appropriate map based on current phase
	var isReady bool
	var readyStateMap map[string]bool

	switch game.Status {
	case StatusReadyCheck:
		readyStateMap = game.ReadyToReveal
		game.ReadyToReveal[playerID] = !game.ReadyToReveal[playerID]
		isReady = game.ReadyToReveal[playerID]
	case StatusRoleReveal:
		readyStateMap = game.ReadyAfterReveal
		game.ReadyAfterReveal[playerID] = !game.ReadyAfterReveal[playerID]
		isReady = game.ReadyAfterReveal[playerID]
	case StatusPlaying:
		readyStateMap = game.ReadyToVote
		game.ReadyToVote[playerID] = !game.ReadyToVote[playerID]
		isReady = game.ReadyToVote[playerID]
	default:
		lobby.mu.Unlock()
		http.Error(w, "Invalid game phase", http.StatusBadRequest)
		return
	}

	// Count ready players
	readyCount := 0
	for _, ready := range readyStateMap {
		if ready {
			readyCount++
		}
	}
	totalPlayers := len(lobby.Players)

	log.Printf("handleReady: ready count %d/%d in phase %s", readyCount, totalPlayers, game.Status)

	// Check if we should advance phase based on readiness
	shouldAdvance := false

	if game.Status == StatusReadyCheck || game.Status == StatusRoleReveal {
		// For ready check and role reveal, ALL players must be ready
		allReady := true
		for id := range lobby.Players {
			if !readyStateMap[id] {
				allReady = false
				break
			}
		}
		shouldAdvance = allReady
	} else if game.Status == StatusPlaying {
		// For voting readiness, more than 50% of players must be ready
		shouldAdvance = readyCount > totalPlayers/2
	}

	if shouldAdvance {
		log.Printf("handleReady: advancing from phase %s", game.Status)
		// Advance game state based on current status
		switch game.Status {
		case StatusReadyCheck:
			game.Status = StatusRoleReveal
			phaseChangeMsg = "role_reveal"
			shouldBroadcastPhase = true
		case StatusRoleReveal:
			game.Status = StatusPlaying
			game.StartTime = time.Now()
			// Choose random first questioner
			playerIDs := make([]string, 0, len(lobby.Players))
			for id := range lobby.Players {
				playerIDs = append(playerIDs, id)
			}
			game.FirstQuestioner = playerIDs[rand.Intn(len(playerIDs))]
			phaseChangeMsg = "playing"
			shouldBroadcastPhase = true
			// Start timer goroutine AFTER releasing lock
			go game.runTimer(lobby)
		case StatusPlaying:
			game.Status = StatusVoting
			phaseChangeMsg = "voting"
			shouldBroadcastPhase = true
		}
		log.Printf("handleReady: advanced to phase %s", game.Status)

		// IMPORTANT: Recalculate readyCount from the NEW phase's map after transition
		readyCount = 0
		switch game.Status {
		case StatusRoleReveal:
			for _, ready := range game.ReadyAfterReveal {
				if ready {
					readyCount++
				}
			}
		case StatusPlaying:
			for _, ready := range game.ReadyToVote {
				if ready {
					readyCount++
				}
			}
		case StatusVoting:
			// No ready count needed for voting phase
			readyCount = 0
		}
	}

	// Prepare ready count message with phase-specific event name
	var readyCountEventName string
	switch game.Status {
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

	// Prepare button HTML based on current game status
	buttonID := "ready-button-check"
	buttonText := "I'm Ready to See My Role"
	buttonClass := "btn btn-primary"

	switch game.Status {
	case StatusReadyCheck:
		buttonID = "ready-button-check"
		if isReady {
			buttonText = "✓ Ready - Waiting for others..."
			buttonClass = "btn btn-success"
		} else {
			buttonText = "I'm Ready to See My Role"
		}
	case StatusRoleReveal:
		buttonID = "ready-button-role"
		if isReady {
			buttonText = "✓ Waiting for others..."
			buttonClass = "btn btn-success"
		} else {
			buttonText = "I've Seen My Role ✓"
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

	buttonHTML = fmt.Sprintf(`<button id="%s" type="submit" class="%s">%s</button>`, buttonID, buttonClass, buttonText)

	lobby.mu.Unlock()
	log.Printf("handleReady: lock released, broadcasting updates")

	// Now broadcast WITHOUT holding the lock to avoid deadlock
	if shouldBroadcastPhase {
		log.Printf("handleReady: broadcasting phase-change: %s", phaseChangeMsg)
		lobby.broadcastSSE("phase-change", phaseChangeMsg)
	}

	log.Printf("handleReady: broadcasting %s: %s", readyCountEventName, readyCountMsg)
	lobby.broadcastSSE(readyCountEventName, readyCountMsg)

	log.Printf("handleReady: sending button HTML response")
	w.Header().Set("Content-Type", "text/html")
	w.Write([]byte(buttonHTML))
}

// handleVote handles player votes
func handleVote(w http.ResponseWriter, r *http.Request) {
	log.Printf("handleVote called: %s %s", r.Method, r.URL.Path)

	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/vote/"), "/")
	if len(parts) != 2 {
		http.Error(w, "Invalid URL", http.StatusBadRequest)
		return
	}
	roomCode := parts[0]
	playerID := parts[1]

	r.ParseForm()
	suspectID := r.FormValue("suspect")

	log.Printf("handleVote: roomCode=%s playerID=%s suspectID=%s", roomCode, playerID, suspectID)

	lobbiesMux.RLock()
	lobby, exists := lobbies[roomCode]
	lobbiesMux.RUnlock()

	if !exists {
		http.Error(w, "Lobby not found", http.StatusNotFound)
		return
	}

	// Prepare messages to broadcast
	var voteCountMsg string
	var shouldFinish bool
	var shouldRevote bool
	var newVoteRound int

	lobby.mu.Lock()
	game := lobby.CurrentGame
	if game == nil || game.Status != StatusVoting {
		lobby.mu.Unlock()
		http.Error(w, "Not in voting phase", http.StatusBadRequest)
		return
	}

	// Record vote
	game.Votes[playerID] = suspectID
	log.Printf("handleVote: vote recorded, total votes: %d/%d", len(game.Votes), len(lobby.Players))

	// Check if all players voted
	if len(game.Votes) == len(lobby.Players) {
		log.Printf("handleVote: all players have voted, counting results")
		// Count votes
		voteCount := make(map[string]int)
		for _, votedFor := range game.Votes {
			voteCount[votedFor]++
		}

		// Find max votes
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

		// Check for tie
		if len(playersWithMaxVotes) > 1 && game.VoteRound < 3 {
			log.Printf("handleVote: tie detected, starting revote (round %d -> %d)", game.VoteRound, game.VoteRound+1)
			// Tie - revote
			game.Votes = make(map[string]string)
			game.VoteRound++
			newVoteRound = game.VoteRound
			shouldRevote = true
		} else {
			log.Printf("handleVote: game finished, updating scores")
			// Game over - update scores
			game.Status = StatusFinished

			// Determine winner
			innocentWon := len(playersWithMaxVotes) == 1 && playersWithMaxVotes[0] == game.SpyID

			// Update scores
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

	// Prepare vote count message
	voteCountMsg = renderVoteCount(len(game.Votes), len(lobby.Players))

	lobby.mu.Unlock()

	// Broadcast vote count update WITHOUT holding lock
	log.Printf("handleVote: broadcasting vote count: %s", voteCountMsg)
	lobby.broadcastSSE("vote-count-voting", voteCountMsg)

	// Handle game end conditions
	if shouldRevote {
		log.Printf("handleVote: broadcasting vote-tie event")
		lobby.broadcastSSE("vote-tie", fmt.Sprintf("%d", newVoteRound))
	} else if shouldFinish {
		log.Printf("handleVote: broadcasting game-finished event")
		lobby.broadcastSSE("game-finished", roomCode)
	}

	// Return confirmation HTML to replace voting UI
	log.Printf("handleVote: sending voted confirmation to player %s", playerID)
	w.Header().Set("Content-Type", "text/html")
	w.Write([]byte(renderVotedConfirmation()))
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

	// Stop timer if running
	if lobby.CurrentGame != nil && lobby.CurrentGame.timerDone != nil {
		close(lobby.CurrentGame.timerDone)
	}

	// Clear game
	lobby.CurrentGame = nil

	lobby.mu.Unlock()

	log.Printf("handleRestartGame: game cleared, broadcasting game-restarted event")

	// Broadcast restart WITHOUT holding lock
	lobby.broadcastSSE("game-restarted", roomCode)

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
	lobby.broadcastSSE("lobby-closed", "")

	// Delete lobby
	lobbiesMux.Lock()
	delete(lobbies, roomCode)
	lobbiesMux.Unlock()

	w.Header().Set("HX-Redirect", "/")
	w.WriteHeader(http.StatusOK)
}

// ===== Game Methods =====

// runTimer runs the game timer and broadcasts updates
func (g *Game) runTimer(lobby *Lobby) {
	const gameDuration = 10 * time.Minute
	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-g.timerDone:
			return
		case <-ticker.C:
			elapsed := time.Since(g.StartTime)
			remaining := gameDuration - elapsed

			if remaining <= 0 {
				// Time's up - start voting
				lobby.mu.Lock()
				if g.Status == StatusPlaying {
					g.Status = StatusVoting
					lobby.broadcastSSE("phase-change", "voting")
				}
				lobby.mu.Unlock()
				return
			}

			// Broadcast time update
			minutes := int(remaining.Minutes())
			seconds := int(remaining.Seconds()) % 60
			timeStr := fmt.Sprintf("%d:%02d", minutes, seconds)
			timerHTML := renderTimerUpdate(timeStr)
			lobby.broadcastSSE("timer-update", timerHTML)
		}
	}
}
