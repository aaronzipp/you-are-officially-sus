package game

const (
	// MinPlayers is the minimum number of players required to start a game
	MinPlayers = 3

	// MaxVoteRounds is the maximum number of voting rounds before forcing a result
	MaxVoteRounds = 3

	// ReadyThresholdAll requires 100% of players to be ready (phases 1 & 2)
	ReadyThresholdAll = 1.0

	// ReadyThresholdMajority requires >50% of players to be ready (phase 3)
	ReadyThresholdMajority = 0.5

	// SSEBufferSize is the buffer size for SSE message channels
	SSEBufferSize = 10

	// SSETimeout is the timeout for sending messages to SSE clients
	SSETimeoutSeconds = 1

	// RoomCodeLength is the length of generated room codes
	RoomCodeLength = 6

	// RoomCodeChars are the characters used for generating room codes (excluding ambiguous chars)
	RoomCodeChars = "ABCDEFGHJKLMNPQRSTUVWXYZ23456789"
)
