package handlers

import (
	"net/http"
	"strings"

	"github.com/aaronzipp/you-are-officially-sus/internal/game"
	"github.com/aaronzipp/you-are-officially-sus/internal/models"
)

// HandleResults displays the game results
func (ctx *Context) HandleResults(w http.ResponseWriter, r *http.Request) {
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
		_, pid, err := ctx.getLobbyAndPlayer(r, roomCode)
		if err != nil {
			http.Redirect(w, r, "/", http.StatusSeeOther)
			return
		}
		playerID = pid
	}

	lobby, exists := ctx.LobbyStore.Get(roomCode)
	if !exists {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}

	lobby.RLock()
	defer lobby.RUnlock()

	if lobby.CurrentGame == nil {
		http.Redirect(w, r, "/lobby/"+roomCode, http.StatusSeeOther)
		return
	}

	currentGame := lobby.CurrentGame
	if currentGame.Status != models.StatusFinished {
		http.Redirect(w, r, game.PhasePathFor(roomCode, currentGame.Status), http.StatusSeeOther)
		return
	}

	// Calculate vote counts
	voteCount := make(map[string]int)
	for _, suspectID := range currentGame.Votes {
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

	innocentWon := !isTie && mostVoted == currentGame.SpyID

	// Build challenges map
	challengesMap := make(map[string]string)
	for pid, info := range currentGame.PlayerInfo {
		challengesMap[pid] = info.Challenge
	}

	// Build voted correctly map
	votedCorrectly := make(map[string]bool)
	for voterID, suspectID := range currentGame.Votes {
		votedCorrectly[voterID] = suspectID == currentGame.SpyID
	}

	spy := lobby.Players[currentGame.SpyID]

	data := struct {
		RoomCode       string
		PlayerID       string
		IsHost         bool
		Players        []*models.Player
		Spy            *models.Player
		Location       *models.Location
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
		Location:       currentGame.Location,
		Challenges:     challengesMap,
		Votes:          currentGame.Votes,
		VoteCount:      voteCount,
		VotedCorrectly: votedCorrectly,
		VoteRounds:     currentGame.VoteRound,
		MostVoted:      mostVoted,
		IsTie:          isTie,
		InnocentWon:    innocentWon,
	}

	ctx.Templates.ExecuteTemplate(w, "results.html", data)
}
