package game

import (
	"github.com/aaronzipp/you-are-officially-sus/internal/models"
)

// VoteResult represents the outcome of vote counting
type VoteResult struct {
	MostVoted      string
	IsTie          bool
	InnocentWon    bool
	VoteCount      map[string]int
	VotedCorrectly map[string]bool
}

// CountVotes analyzes votes and determines the result
func CountVotes(game *models.Game, players map[string]*models.Player) *VoteResult {
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

	result := &VoteResult{
		VoteCount: voteCount,
		IsTie:     len(playersWithMaxVotes) > 1,
	}

	if !result.IsTie {
		result.MostVoted = playersWithMaxVotes[0]
		result.InnocentWon = result.MostVoted == game.SpyID
	}

	// Build voted correctly map
	result.VotedCorrectly = make(map[string]bool)
	for voterID, suspectID := range game.Votes {
		result.VotedCorrectly[voterID] = suspectID == game.SpyID
	}

	return result
}

// ShouldAdvancePhase determines if a phase should advance based on ready counts
func ShouldAdvancePhase(readyCount, totalPlayers int, status models.GameStatus) bool {
	switch status {
	case models.StatusReadyCheck, models.StatusRoleReveal:
		return readyCount == totalPlayers
	case models.StatusPlaying:
		return readyCount > totalPlayers/2
	default:
		return false
	}
}

// GetReadyStateMap returns the appropriate ready state map for the current phase
func GetReadyStateMap(game *models.Game) map[string]bool {
	switch game.Status {
	case models.StatusReadyCheck:
		return game.ReadyToReveal
	case models.StatusRoleReveal:
		return game.ReadyAfterReveal
	case models.StatusPlaying:
		return game.ReadyToVote
	default:
		return nil
	}
}

// CountReadyPlayers counts how many players are ready in the given map
func CountReadyPlayers(readyMap map[string]bool, players map[string]*models.Player) int {
	count := 0
	for id := range players {
		if readyMap[id] {
			count++
		}
	}
	return count
}

// GetReadyPlayerNames returns the names of all ready players
func GetReadyPlayerNames(readyMap map[string]bool, players map[string]*models.Player) []string {
	names := make([]string, 0)
	for id := range players {
		if readyMap[id] {
			if p, ok := players[id]; ok {
				names = append(names, p.Name)
			} else {
				names = append(names, "unknown("+id+")")
			}
		}
	}
	return names
}
