package models

// Game represents an active game session (ephemeral)
type Game struct {
	Location        *Location
	SpyID           string
	FirstQuestioner string                     // Player ID of who asks the first question
	PlayerInfo      map[string]*GamePlayerInfo // game-specific player data
	Status          GameStatus

	ReadyToReveal    map[string]bool // Phase 1: Ready to see role (all players required)
	ReadyAfterReveal map[string]bool // Phase 2: Confirmed saw role (all players required)
	ReadyToVote      map[string]bool // Phase 3: Ready to vote (>50% required)
	Votes            map[string]string
	VoteRound        int // Track voting rounds for tie-breaking
}
