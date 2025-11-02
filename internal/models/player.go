package models

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
	Challenge string
	IsSpy     bool
}
