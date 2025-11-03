package handlers

import (
	"log"
	"math/rand"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/aaronzipp/you-are-officially-sus/internal/game"
	"github.com/aaronzipp/you-are-officially-sus/internal/models"
	"github.com/aaronzipp/you-are-officially-sus/internal/render"
	"github.com/aaronzipp/you-are-officially-sus/internal/sse"
)

var debug bool

func init() {
	debug = os.Getenv("DEBUG") != ""
}

// HandleGameMux routes game subpaths by phase and actions
func (ctx *Context) HandleGameMux(w http.ResponseWriter, r *http.Request) {
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
			ctx.gameHandleReadyCookie(w, r, roomCode)
			return
		case "vote":
			ctx.gameHandleVoteCookie(w, r, roomCode)
			return
		default:
			http.Error(w, "Not found", http.StatusNotFound)
			return
		}
	}

	// GET phase pages: confirm-reveal, roles, play, voting
	lobby, playerID, err := ctx.getLobbyAndPlayer(r, roomCode)
	if err != nil {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}

	lobby.RLock()
	g := lobby.CurrentGame
	lobby.RUnlock()
	if g == nil {
		http.Redirect(w, r, "/lobby/"+roomCode, http.StatusSeeOther)
		return
	}

	// Guard: ensure path matches current phase; redirect canonical path
	currentPath := game.PhasePathFor(roomCode, g.Status)
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
	lobby.RLock()
	g = lobby.CurrentGame
	playerInfo := g.PlayerInfo[playerID]

	isReady := false
	switch g.Status {
	case models.StatusReadyCheck:
		isReady = g.ReadyToReveal[playerID]
	case models.StatusRoleReveal:
		isReady = g.ReadyAfterReveal[playerID]
	case models.StatusPlaying:
		isReady = g.ReadyToVote[playerID]
	}

	data := struct {
		RoomCode        string
		PlayerID        string
		Status          models.GameStatus
		Players         []*models.Player
		TotalPlayers    int
		Location        *models.Location
		Challenge       string
		IsSpy           bool
		IsReady         bool
		HasVoted        bool
		VoteRound       int
		FirstQuestioner string
		PlayStartedAt   int64 // Unix timestamp for client-side timer sync
	}{
		RoomCode:        roomCode,
		PlayerID:        playerID,
		Status:          g.Status,
		Players:         render.GetPlayerList(lobby.Players),
		TotalPlayers:    len(lobby.Players),
		Location:        g.Location,
		Challenge:       playerInfo.Challenge,
		IsSpy:           playerInfo.IsSpy,
		IsReady:         isReady,
		HasVoted:        g.Votes[playerID] != "",
		VoteRound:       g.VoteRound,
		FirstQuestioner: g.FirstQuestioner,
		PlayStartedAt:   g.PlayStartedAt.Unix(),
	}
	lobby.RUnlock()

	// Select template by phase
	tmpl := ""
	switch g.Status {
	case models.StatusReadyCheck:
		tmpl = "game_confirm_reveal.html"
	case models.StatusRoleReveal:
		tmpl = "game_roles.html"
	case models.StatusPlaying:
		tmpl = "game_play.html"
	case models.StatusVoting:
		tmpl = "game_voting.html"
	default:
		// Should not happen due to guard; send to lobby
		w.Header().Set("HX-Redirect", "/lobby/"+roomCode)
		w.WriteHeader(http.StatusOK)
		return
	}
	ctx.Templates.ExecuteTemplate(w, tmpl, data)
}

// gameHandleReadyCookie updates readiness using cookie-based player ID
func (ctx *Context) gameHandleReadyCookie(w http.ResponseWriter, r *http.Request, roomCode string) {
	lobby, exists := ctx.LobbyStore.Get(roomCode)
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

	lobby.Lock()
	g := lobby.CurrentGame
	if g == nil {
		lobby.Unlock()
		http.Error(w, "No game in progress", http.StatusBadRequest)
		return
	}

	statusBefore := g.Status

	// Update readiness per phase rules (toggle in all phases to surface issues)
	var isReady bool
	var prev bool
	var readyStateMap map[string]bool
	switch statusBefore {
	case models.StatusReadyCheck:
		readyStateMap = g.ReadyToReveal
		prev = g.ReadyToReveal[playerID]
		g.ReadyToReveal[playerID] = !g.ReadyToReveal[playerID]
		isReady = g.ReadyToReveal[playerID]
	case models.StatusRoleReveal:
		readyStateMap = g.ReadyAfterReveal
		prev = g.ReadyAfterReveal[playerID]
		g.ReadyAfterReveal[playerID] = !g.ReadyAfterReveal[playerID]
		isReady = g.ReadyAfterReveal[playerID]
	case models.StatusPlaying:
		readyStateMap = g.ReadyToVote
		prev = g.ReadyToVote[playerID]
		g.ReadyToVote[playerID] = !g.ReadyToVote[playerID]
		isReady = g.ReadyToVote[playerID]
	default:
		lobby.Unlock()
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
				confirmedNames = append(confirmedNames, "unknown("+id+")")
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
	case models.StatusReadyCheck, models.StatusRoleReveal:
		shouldAdvance = readyCount == totalPlayers
	case models.StatusPlaying:
		shouldAdvance = readyCount > totalPlayers/2
	}

	// Prepare outgoing UI for the CURRENT (pre-advance) phase
	switch statusBefore {
	case models.StatusReadyCheck:
		readyCountMsg = ctx.ReadyCount(readyCount, len(lobby.Players), "players ready")
		readyCountEventName = "ready-count-check"
	case models.StatusRoleReveal:
		readyCountMsg = ctx.ReadyCount(readyCount, len(lobby.Players), "players ready")
		readyCountEventName = "ready-count-reveal"
	case models.StatusPlaying:
		readyCountMsg = ctx.ReadyCount(readyCount, len(lobby.Players), "players ready to vote")
		readyCountEventName = "ready-count-playing"
	}

	buttonID := "ready-button-check"
	buttonText := "I'm Ready to See My Role"
	buttonClass := "btn btn-primary"
	switch statusBefore {
	case models.StatusReadyCheck:
		buttonID = "ready-button-check"
		if isReady {
			buttonText = "✓ Ready - Waiting for others..."
			buttonClass = "btn btn-success"
		} else {
			buttonText = "I'm Ready to See My Role"
			buttonClass = "btn btn-primary"
		}
	case models.StatusRoleReveal:
		buttonID = "ready-button-role"
		if isReady {
			buttonText = "✓ Waiting for others..."
			buttonClass = "btn btn-success"
		} else {
			buttonText = "I've Seen My Role ✓"
			buttonClass = "btn btn-primary"
		}
	case models.StatusPlaying:
		buttonID = "ready-button-playing"
		if isReady {
			buttonText = "✓ Ready to Vote"
			buttonClass = "btn btn-success"
		} else {
			buttonText = "Ready to Vote?"
			buttonClass = "btn btn-secondary"
		}
	}
	var bb strings.Builder
	bb.WriteString(`<button id="`)
	bb.WriteString(buttonID)
	bb.WriteString(`" type="submit" class="`)
	bb.WriteString(buttonClass)
	bb.WriteString(`">`)
	bb.WriteString(buttonText)
	bb.WriteString(`</button>`)
	buttonHTML = bb.String()

	// Detailed logging for readiness change
	if debug {
		log.Printf("ready: room=%s phase=%s actor=%s(%s) prev=%v now=%v confirmed=[%s] count=%d/%d", roomCode, statusBefore, actorName, playerID, prev, isReady, strings.Join(confirmedNames, ", "), readyCount, totalPlayers)
	}

	// Advance AFTER preparing current-phase outputs
	nextPath := ""
	if shouldAdvance {
		switch statusBefore {
		case models.StatusReadyCheck:
			g.Status = models.StatusRoleReveal
			// Pre-seed next phase readiness map
			for id := range lobby.Players {
				if _, ok := g.ReadyAfterReveal[id]; !ok {
					g.ReadyAfterReveal[id] = false
				}
			}
			nextPath = game.PhasePathFor(roomCode, g.Status)
			shouldBroadcastPhase = true
		case models.StatusRoleReveal:
			g.Status = models.StatusPlaying
			// Record when playing phase started (for timer sync)
			g.PlayStartedAt = time.Now()
			// Pre-seed next phase readiness map
			for id := range lobby.Players {
				if _, ok := g.ReadyToVote[id]; !ok {
					g.ReadyToVote[id] = false
				}
			}
			// Choose random first questioner
			playerIDs := make([]string, 0, len(lobby.Players))
			for id := range lobby.Players {
				playerIDs = append(playerIDs, id)
			}
			g.FirstQuestioner = playerIDs[rand.Intn(len(playerIDs))]
			nextPath = game.PhasePathFor(roomCode, g.Status)
			shouldBroadcastPhase = true
		case models.StatusPlaying:
			g.Status = models.StatusVoting
			nextPath = game.PhasePathFor(roomCode, g.Status)
			shouldBroadcastPhase = true
		}
	}
	lobby.Unlock()

	// Broadcast the server-derived current-phase count
	sse.Broadcast(lobby, readyCountEventName, readyCountMsg)

	// If phase advanced, instruct clients to navigate; no client-side math
	if shouldBroadcastPhase {
		sse.Broadcast(lobby, sse.EventNavRedirect, ctx.RedirectSnippet(roomCode, nextPath))
		// Also ensure the initiating client navigates via HX-Redirect
		w.Header().Set("HX-Redirect", nextPath)
		w.WriteHeader(http.StatusOK)
		return
	}

	w.Header().Set("Content-Type", "text/html")
	w.Write([]byte(buttonHTML))
}

// gameHandleVoteCookie records a vote using cookie-based player ID
func (ctx *Context) gameHandleVoteCookie(w http.ResponseWriter, r *http.Request, roomCode string) {
	lobby, exists := ctx.LobbyStore.Get(roomCode)
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

	lobby.Lock()
	g := lobby.CurrentGame
	if g == nil || g.Status != models.StatusVoting {
		lobby.Unlock()
		http.Error(w, "Not in voting phase", http.StatusBadRequest)
		return
	}

	g.Votes[playerID] = suspectID

	if len(g.Votes) == len(lobby.Players) {
		// Count votes
		voteCount := make(map[string]int)
		for _, votedFor := range g.Votes {
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

		if len(playersWithMaxVotes) > 1 && g.VoteRound < game.MaxVoteRounds {
			// tie -> revote
			g.Votes = make(map[string]string)
			g.VoteRound++
			shouldRevote = true
		} else {
			// finish game
			g.Status = models.StatusFinished
			innocentWon := len(playersWithMaxVotes) == 1 && playersWithMaxVotes[0] == g.SpyID
			for id := range lobby.Players {
				if id == g.SpyID {
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

	voteCountMsg = ctx.VoteCount(len(g.Votes), len(lobby.Players))
	lobby.Unlock()

	sse.Broadcast(lobby, sse.EventVoteCount, voteCountMsg)
	if shouldRevote {
		sse.Broadcast(lobby, sse.EventNavRedirect, ctx.RedirectSnippet(roomCode, game.PhasePathFor(roomCode, models.StatusVoting)))
	} else if shouldFinish {
		sse.Broadcast(lobby, sse.EventNavRedirect, ctx.RedirectSnippet(roomCode, game.PhasePathFor(roomCode, models.StatusFinished)))
	}

	w.Header().Set("Content-Type", "text/html")
	w.Write([]byte(ctx.VotedConfirmation()))
}
