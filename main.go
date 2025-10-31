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

// Player represents a game player
type Player struct {
	ID               string
	Name             string
	Challenge        string
	HasConfirmedRole bool
	IsSpy            bool
}

// Game represents a game room
type Game struct {
	RoomCode    string
	Host        string
	Players     []*Player
	Status      GameStatus
	Location    *Location
	SpyID       string
	StartTime   time.Time
	ReadyToVote map[string]bool
	Votes       map[string]string
	mu          sync.RWMutex
	clients     map[chan string]string // channel -> playerID
}

// Global storage
var (
	games      = make(map[string]*Game)
	gamesMutex sync.RWMutex
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

	// Parse templates
	var err error
	templates, err = template.ParseGlob("templates/*.html")
	if err != nil {
		log.Fatal("Failed to parse templates:", err)
	}

	// Routes
	http.HandleFunc("/", handleIndex)
	http.HandleFunc("/create", handleCreateRoom)
	http.HandleFunc("/join", handleJoinRoom)
	http.HandleFunc("/lobby/", handleLobby)
	http.HandleFunc("/lobby-updates/", handleLobbySSE)
	http.HandleFunc("/start/", handleStartGame)
	http.HandleFunc("/game/", handleGame)
	http.HandleFunc("/confirm/", handleConfirmRole)
	http.HandleFunc("/ready/", handleToggleReady)
	http.HandleFunc("/ready-status/", handleReadyStatus)
	http.HandleFunc("/vote/", handleVote)
	http.HandleFunc("/results/", handleResults)

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

// addClient adds a new SSE client to the game
func (g *Game) addClient(client chan string, playerID string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.clients == nil {
		g.clients = make(map[chan string]string)
	}
	g.clients[client] = playerID
}

// removeClient removes an SSE client from the game
func (g *Game) removeClient(client chan string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	delete(g.clients, client)
	close(client)
}

// broadcast sends a message to all connected SSE clients
func (g *Game) broadcast(message string) {
	g.mu.RLock()

	// Collect all client channels while holding the lock
	var clients []chan string
	for client := range g.clients {
		clients = append(clients, client)
	}

	g.mu.RUnlock()

	// Send messages WITHOUT holding the lock
	for _, client := range clients {
		select {
		case client <- message:
			// Message sent successfully
		case <-time.After(1 * time.Second):
			// Timeout - skip this client to avoid blocking
		}
	}
}

// broadcastPersonalized sends personalized updates to all connected SSE clients
func (g *Game) broadcastPersonalized() {
	g.mu.RLock()

	// Collect data we need while holding the lock
	playerListHTML := renderPlayerList(g.Players)

	// Build a list of messages to send to each client
	type clientMessage struct {
		client   chan string
		messages []string
	}

	var messagesToSend []clientMessage
	for client, clientPlayerID := range g.clients {
		hostControlsHTML := renderHostControls(g, clientPlayerID)
		messagesToSend = append(messagesToSend, clientMessage{
			client: client,
			messages: []string{
				fmt.Sprintf("event: players\ndata: %s", playerListHTML),
				fmt.Sprintf("event: controls\ndata: %s", hostControlsHTML),
			},
		})
	}

	g.mu.RUnlock()

	// Now send messages WITHOUT holding the lock
	for _, cm := range messagesToSend {
		for _, msg := range cm.messages {
			select {
			case cm.client <- msg:
				// Message sent successfully
			case <-time.After(1 * time.Second):
				// Timeout - skip this client to avoid blocking
			}
		}
	}
}

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
	gamesMutex.RLock()
	defer gamesMutex.RUnlock()

	for {
		code := generateRoomCode()
		if _, exists := games[code]; !exists {
			return code
		}
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

// handleCreateRoom creates a new game room
func handleCreateRoom(w http.ResponseWriter, r *http.Request) {
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

	game := &Game{
		RoomCode:    roomCode,
		Host:        playerID,
		Players:     []*Player{{ID: playerID, Name: hostName}},
		Status:      StatusWaiting,
		ReadyToVote: make(map[string]bool),
		Votes:       make(map[string]string),
	}

	gamesMutex.Lock()
	games[roomCode] = game
	gamesMutex.Unlock()

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

// handleJoinRoom allows a player to join an existing room
func handleJoinRoom(w http.ResponseWriter, r *http.Request) {
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

	gamesMutex.RLock()
	game, exists := games[roomCode]
	gamesMutex.RUnlock()

	if !exists {
		http.Error(w, "Room not found", http.StatusNotFound)
		return
	}

	game.mu.Lock()
	if game.Status != StatusWaiting {
		game.mu.Unlock()
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}

	playerID := uuid.New().String()
	game.Players = append(game.Players, &Player{
		ID:   playerID,
		Name: playerName,
	})
	game.mu.Unlock()

	// Broadcast personalized update to all clients
	game.broadcastPersonalized()

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

// handleLobby displays the lobby for a room
func handleLobby(w http.ResponseWriter, r *http.Request) {
	roomCode := strings.TrimPrefix(r.URL.Path, "/lobby/")

	gamesMutex.RLock()
	game, exists := games[roomCode]
	gamesMutex.RUnlock()

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

	game.mu.RLock()
	defer game.mu.RUnlock()

	data := struct {
		RoomCode string
		Players  []*Player
		IsHost   bool
		Status   GameStatus
	}{
		RoomCode: game.RoomCode,
		Players:  game.Players,
		IsHost:   game.Host == playerID,
		Status:   game.Status,
	}

	templates.ExecuteTemplate(w, "lobby.html", data)
}

// handleLobbySSE streams lobby updates via Server-Sent Events
func handleLobbySSE(w http.ResponseWriter, r *http.Request) {
	roomCode := strings.TrimPrefix(r.URL.Path, "/lobby-updates/")

	gamesMutex.RLock()
	game, exists := games[roomCode]
	gamesMutex.RUnlock()

	if !exists {
		http.Error(w, "Room not found", http.StatusNotFound)
		return
	}

	// Get player ID from cookie
	cookie, err := r.Cookie("player_id")
	if err != nil {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}
	playerID := cookie.Value

	// Set headers for SSE
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	// Create client channel
	clientChan := make(chan string, 10)
	game.addClient(clientChan, playerID)
	defer game.removeClient(clientChan)

	// Send initial data
	game.mu.RLock()
	playerListHTML := renderPlayerList(game.Players)
	hostControlsHTML := renderHostControls(game, playerID)
	game.mu.RUnlock()

	fmt.Fprintf(w, "event: players\ndata: %s\n\n", playerListHTML)
	fmt.Fprintf(w, "event: controls\ndata: %s\n\n", hostControlsHTML)
	w.(http.Flusher).Flush()

	// Listen for updates
	ctx := r.Context()
	for {
		select {
		case <-ctx.Done():
			return
		case msg := <-clientChan:
			fmt.Fprintf(w, "%s\n\n", msg)
			w.(http.Flusher).Flush()
		}
	}
}

// renderPlayerList generates HTML for the player list (inner content only for sse-swap)
func renderPlayerList(players []*Player) string {
	html := fmt.Sprintf(`<h2>Players (%d)</h2><ul class="player-list">`, len(players))
	for _, p := range players {
		html += fmt.Sprintf(`<li class="player-item"><span class="player-name">%s</span></li>`, p.Name)
	}
	html += `</ul>`
	return html
}

// renderHostControls generates HTML for host controls (inner content only for sse-swap)
func renderHostControls(game *Game, playerID string) string {
	isHost := game.Host == playerID
	playerCount := len(game.Players)

	if isHost {
		if playerCount >= 3 {
			return fmt.Sprintf(`<form hx-post="/start/%s"><button type="submit" class="btn btn-primary">Start Game</button></form>`, game.RoomCode)
		} else {
			return `<p>Waiting for players to join...</p><p class="text-muted">Need at least 3 players to start</p>`
		}
	}
	return `<p>Waiting for host to start the game...</p>`
}

// handleStartGame starts the game
func handleStartGame(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	roomCode := strings.TrimPrefix(r.URL.Path, "/start/")

	gamesMutex.RLock()
	game, exists := games[roomCode]
	gamesMutex.RUnlock()

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

	game.mu.Lock()

	// Check if player is host
	if game.Host != playerID {
		game.mu.Unlock()
		http.Error(w, "Only host can start game", http.StatusForbidden)
		return
	}

	if game.Status != StatusWaiting {
		game.mu.Unlock()
		http.Error(w, "Game already started", http.StatusBadRequest)
		return
	}

	if len(game.Players) < 3 {
		game.mu.Unlock()
		http.Error(w, "Need at least 3 players", http.StatusBadRequest)
		return
	}

	// Assign location
	game.Location = &locations[rand.Intn(len(locations))]

	// Assign spy
	spyIndex := rand.Intn(len(game.Players))
	game.SpyID = game.Players[spyIndex].ID
	game.Players[spyIndex].IsSpy = true

	// Assign challenges
	shuffledChallenges := make([]string, len(challenges))
	copy(shuffledChallenges, challenges)
	rand.Shuffle(len(shuffledChallenges), func(i, j int) {
		shuffledChallenges[i], shuffledChallenges[j] = shuffledChallenges[j], shuffledChallenges[i]
	})

	for i, player := range game.Players {
		player.Challenge = shuffledChallenges[i%len(shuffledChallenges)]
	}

	game.Status = StatusRoleReveal
	game.mu.Unlock()

	// Notify all connected clients to redirect
	game.broadcast("event: game-started\ndata: started")

	// Redirect to game
	w.Header().Set("HX-Redirect", fmt.Sprintf("/game/%s/%s", roomCode, playerID))
	w.WriteHeader(http.StatusOK)
}

// handleGame displays the game screen based on current status
func handleGame(w http.ResponseWriter, r *http.Request) {
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/game/"), "/")
	if len(parts) != 2 {
		http.Error(w, "Invalid URL", http.StatusBadRequest)
		return
	}
	roomCode := parts[0]
	playerID := parts[1]

	gamesMutex.RLock()
	game, exists := games[roomCode]
	gamesMutex.RUnlock()

	if !exists {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}

	game.mu.RLock()
	defer game.mu.RUnlock()

	// Find player
	var player *Player
	for _, p := range game.Players {
		if p.ID == playerID {
			player = p
			break
		}
	}
	if player == nil {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}

	// Check if voting should start
	if game.Status == StatusPlaying && game.shouldStartVoting() {
		game.mu.RUnlock()
		game.mu.Lock()
		game.Status = StatusVoting
		game.mu.Unlock()
		game.mu.RLock()
		w.Header().Set("HX-Trigger", "votingStarted")
	}

	// Render appropriate template based on status
	switch game.Status {
	case StatusRoleReveal:
		data := struct {
			RoomCode     string
			Player       *Player
			Location     *Location
			Challenge    string
			IsSpy        bool
			HasConfirmed bool
		}{
			RoomCode:     game.RoomCode,
			Player:       player,
			Location:     game.Location,
			Challenge:    player.Challenge,
			IsSpy:        player.IsSpy,
			HasConfirmed: player.HasConfirmedRole,
		}
		templates.ExecuteTemplate(w, "role-reveal.html", data)

	case StatusPlaying:
		// Check if this is an HTMX polling request from role-reveal screen
		// If the game has transitioned to playing but we're getting polled from role-reveal, redirect
		if r.Header.Get("HX-Request") == "true" {
			// Check if this looks like a polling request (not a direct navigation)
			// Polling requests come from <body hx-get="..."> with hx-swap="none"
			if r.Header.Get("HX-Target") == "" {
				// This is a polling request, send redirect
				w.Header().Set("HX-Redirect", fmt.Sprintf("/game/%s/%s", game.RoomCode, playerID))
				w.WriteHeader(http.StatusOK)
				return
			}
		}

		timeRemaining := game.timeRemaining()
		readyCount := 0
		for _, ready := range game.ReadyToVote {
			if ready {
				readyCount++
			}
		}

		data := struct {
			RoomCode      string
			Players       []*Player
			TimeRemaining string
			ReadyCount    int
			TotalPlayers  int
			IsReady       bool
			PlayerID      string
			Challenge     string
		}{
			RoomCode:      game.RoomCode,
			Players:       game.Players,
			TimeRemaining: formatDuration(timeRemaining),
			ReadyCount:    readyCount,
			TotalPlayers:  len(game.Players),
			IsReady:       game.ReadyToVote[playerID],
			PlayerID:      playerID,
			Challenge:     player.Challenge,
		}
		templates.ExecuteTemplate(w, "playing.html", data)

	case StatusVoting:
		data := struct {
			RoomCode   string
			Players    []*Player
			PlayerID   string
			HasVoted   bool
			VotedForID string
		}{
			RoomCode:   game.RoomCode,
			Players:    game.Players,
			PlayerID:   playerID,
			HasVoted:   game.Votes[playerID] != "",
			VotedForID: game.Votes[playerID],
		}
		templates.ExecuteTemplate(w, "voting.html", data)

	case StatusFinished:
		http.Redirect(w, r, "/results/"+roomCode, http.StatusSeeOther)
	}
}

// handleConfirmRole marks that a player has seen their role
func handleConfirmRole(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/confirm/"), "/")
	if len(parts) != 2 {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	roomCode := parts[0]
	playerID := parts[1]

	gamesMutex.RLock()
	game, exists := games[roomCode]
	gamesMutex.RUnlock()

	if !exists {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}

	game.mu.Lock()

	// Find and mark player as confirmed
	for _, p := range game.Players {
		if p.ID == playerID {
			p.HasConfirmedRole = true
			break
		}
	}

	// Check if all players confirmed
	allConfirmed := true
	for _, p := range game.Players {
		if !p.HasConfirmedRole {
			allConfirmed = false
			break
		}
	}

	// Start playing phase if all confirmed
	if allConfirmed && game.Status == StatusRoleReveal {
		game.Status = StatusPlaying
		game.StartTime = time.Now()
	}

	game.mu.Unlock()

	// Return updated button HTML showing confirmed state
	html := `<button id="confirm-button" type="submit" class="btn btn-success">✓ Waiting for others...</button>`
	w.Header().Set("Content-Type", "text/html")
	w.Write([]byte(html))
}

// handleToggleReady toggles a player's ready to vote status
func handleToggleReady(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/ready/"), "/")
	if len(parts) != 2 {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	roomCode := parts[0]
	playerID := parts[1]

	gamesMutex.RLock()
	game, exists := games[roomCode]
	gamesMutex.RUnlock()

	if !exists {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}

	game.mu.Lock()
	game.ReadyToVote[playerID] = !game.ReadyToVote[playerID]
	isReady := game.ReadyToVote[playerID]

	// Count ready players
	readyCount := 0
	for _, ready := range game.ReadyToVote {
		if ready {
			readyCount++
		}
	}
	totalPlayers := len(game.Players)

	// Check if all players are ready
	if readyCount == totalPlayers && totalPlayers > 0 {
		game.Status = StatusVoting
	}
	game.mu.Unlock()

	// Return just the button HTML
	buttonClass := "btn-secondary"
	buttonText := "Ready to Vote?"
	if isReady {
		buttonClass = "btn-success"
		buttonText = "✓ Ready to Vote"
	}

	html := fmt.Sprintf(`<button id="ready-button" type="submit" class="btn %s">%s</button>`, buttonClass, buttonText)

	w.Header().Set("Content-Type", "text/html")
	w.Write([]byte(html))
}

// handleReadyStatus returns the current ready count
func handleReadyStatus(w http.ResponseWriter, r *http.Request) {
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/ready-status/"), "/")
	if len(parts) != 2 {
		http.Error(w, "Invalid request", http.StatusBadRequest)
		return
	}
	roomCode := parts[0]
	playerID := parts[1]

	gamesMutex.RLock()
	game, exists := games[roomCode]
	gamesMutex.RUnlock()

	if !exists {
		http.Error(w, "Game not found", http.StatusNotFound)
		return
	}

	game.mu.RLock()
	readyCount := 0
	for _, ready := range game.ReadyToVote {
		if ready {
			readyCount++
		}
	}
	totalPlayers := len(game.Players)
	status := game.Status
	game.mu.RUnlock()

	// If all players are ready, send redirect header to voting page
	if status == StatusVoting {
		w.Header().Set("HX-Redirect", fmt.Sprintf("/vote/%s/%s", roomCode, playerID))
		w.WriteHeader(http.StatusOK)
		return
	}

	// Return just the ready count text
	html := fmt.Sprintf("%d/%d players ready to vote", readyCount, totalPlayers)
	w.Header().Set("Content-Type", "text/html")
	w.Write([]byte(html))
}

// handleVote records a player's vote for who the spy is
func handleVote(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/vote/"), "/")
	if len(parts) != 2 {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	roomCode := parts[0]
	playerID := parts[1]

	gamesMutex.RLock()
	game, exists := games[roomCode]
	gamesMutex.RUnlock()

	if !exists {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}

	r.ParseForm()
	suspectID := r.FormValue("suspect")

	game.mu.Lock()
	game.Votes[playerID] = suspectID

	// Check if all players voted
	allVoted := len(game.Votes) == len(game.Players)
	if allVoted {
		game.Status = StatusFinished
	}
	game.mu.Unlock()

	// Return updated game view or redirect to results
	if allVoted {
		w.Header().Set("HX-Redirect", "/results/"+roomCode)
	} else {
		http.Redirect(w, r, fmt.Sprintf("/game/%s/%s", roomCode, playerID), http.StatusSeeOther)
	}
}

// handleResults displays the game results
func handleResults(w http.ResponseWriter, r *http.Request) {
	roomCode := strings.TrimPrefix(r.URL.Path, "/results/")

	gamesMutex.RLock()
	game, exists := games[roomCode]
	gamesMutex.RUnlock()

	if !exists {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}

	game.mu.RLock()
	defer game.mu.RUnlock()

	// Find spy
	var spy *Player
	for _, p := range game.Players {
		if p.ID == game.SpyID {
			spy = p
			break
		}
	}

	// Count votes
	voteCount := make(map[string]int)
	votedCorrectly := make(map[string]bool)
	for voterID, suspectID := range game.Votes {
		voteCount[suspectID]++
		votedCorrectly[voterID] = (suspectID == game.SpyID)
	}

	data := struct {
		RoomCode       string
		Location       *Location
		Spy            *Player
		Players        []*Player
		Votes          map[string]string
		VoteCount      map[string]int
		VotedCorrectly map[string]bool
	}{
		RoomCode:       game.RoomCode,
		Location:       game.Location,
		Spy:            spy,
		Players:        game.Players,
		Votes:          game.Votes,
		VoteCount:      voteCount,
		VotedCorrectly: votedCorrectly,
	}

	templates.ExecuteTemplate(w, "results.html", data)
}

// Helper functions

func (g *Game) timeRemaining() time.Duration {
	const gameDuration = 10 * time.Minute
	elapsed := time.Since(g.StartTime)
	remaining := gameDuration - elapsed
	if remaining < 0 {
		return 0
	}
	return remaining
}

func (g *Game) shouldStartVoting() bool {
	// Check if time is up
	if g.timeRemaining() <= 0 {
		return true
	}

	// Check if at least 50% ready
	readyCount := 0
	for _, ready := range g.ReadyToVote {
		if ready {
			readyCount++
		}
	}

	threshold := float64(len(g.Players)) * 0.5
	return float64(readyCount) >= threshold
}

func formatDuration(d time.Duration) string {
	d = d.Round(time.Second)
	minutes := int(d.Minutes())
	seconds := int(d.Seconds()) % 60
	return fmt.Sprintf("%d:%02d", minutes, seconds)
}
