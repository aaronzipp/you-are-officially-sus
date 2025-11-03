package main

import (
	"encoding/json"
	"fmt"
	"html/template"
	"log"
	"net/http"
	"os"

	"github.com/aaronzipp/you-are-officially-sus/internal/handlers"
	"github.com/aaronzipp/you-are-officially-sus/internal/models"
	"github.com/aaronzipp/you-are-officially-sus/internal/store"
)

var (
	debug bool
)

func init() {
	// Enable DEBUG logs when DEBUG env var is set (non-empty)
	debug = os.Getenv("DEBUG") != ""
	// Note: In Go 1.20+, math/rand is automatically seeded
}

func main() {
	// Load data
	locations, challenges, err := loadData()
	if err != nil {
		log.Fatal("Failed to load data:", err)
	}

	// Parse templates with custom functions
	tmpl := template.New("").Funcs(template.FuncMap{
		"add": func(a, b int) int { return a + b },
	})

	// Parse main templates and partials
	templates, err := tmpl.ParseGlob("templates/*.html")
	if err != nil {
		log.Fatal("Failed to parse templates:", err)
	}
	templates, err = templates.ParseGlob("templates/partials/*.html")
	if err != nil {
		log.Fatal("Failed to parse template partials:", err)
	}

	// Initialize handler context
	ctx := &handlers.Context{
		LobbyStore: store.NewLobbyStore(),
		Templates:  templates,
		Locations:  locations,
		Challenges: challenges,
	}

	// Routes
	http.HandleFunc("/", ctx.HandleIndex)
	http.HandleFunc("/create", ctx.HandleCreateLobby)
	http.HandleFunc("/join", ctx.HandleJoinLobby)
	http.HandleFunc("/lobby/", ctx.HandleLobby)
	http.HandleFunc("/sse/", ctx.HandleSSE)
	http.HandleFunc("/start-game/", ctx.HandleStartGame)
	// Game multiplexer: phases (GET), actions (POST), and redirect helper
	http.HandleFunc("/game/", ctx.HandleGameMux)
	// Results
	http.HandleFunc("/results/", ctx.HandleResults)
	// Lobby/game lifecycle
	http.HandleFunc("/restart-game/", ctx.HandleRestartGame)
	http.HandleFunc("/close-lobby/", ctx.HandleCloseLobby)
	http.HandleFunc("/leave-lobby/", ctx.HandleLeaveLobby)
	http.HandleFunc("/select-host/", ctx.HandleSelectHost)
	http.HandleFunc("/leave-lobby-with-host/", ctx.HandleLeaveLobbyWithHost)

	// Static files
	http.Handle("/static/", http.StripPrefix("/static/", http.FileServer(http.Dir("static"))))

	port := ":8080"
	log.Printf("Server starting on %s", port)
	log.Fatal(http.ListenAndServe(port, nil))
}

// loadData loads locations and challenges from JSON files
func loadData() ([]models.Location, []string, error) {
	// Load locations
	var locations []models.Location
	locationData, err := os.ReadFile("data/places.json")
	if err != nil {
		return nil, nil, fmt.Errorf("reading places.json: %w", err)
	}
	if err := json.Unmarshal(locationData, &locations); err != nil {
		return nil, nil, fmt.Errorf("parsing places.json: %w", err)
	}

	// Load challenges
	var challenges []string
	challengeData, err := os.ReadFile("data/challenges.json")
	if err != nil {
		return nil, nil, fmt.Errorf("reading challenges.json: %w", err)
	}
	if err := json.Unmarshal(challengeData, &challenges); err != nil {
		return nil, nil, fmt.Errorf("parsing challenges.json: %w", err)
	}

	log.Printf("Loaded %d locations and %d challenges", len(locations), len(challenges))
	return locations, challenges, nil
}
