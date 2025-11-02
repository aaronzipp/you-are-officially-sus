package game

import (
	crand "crypto/rand"
	"math/big"
	"math/rand"

	"github.com/aaronzipp/you-are-officially-sus/internal/models"
	"github.com/aaronzipp/you-are-officially-sus/internal/store"
)

// GenerateRoomCode creates a random room code
func GenerateRoomCode() string {
	code := make([]byte, RoomCodeLength)
	for i := range RoomCodeLength {
		n, err := crand.Int(crand.Reader, big.NewInt(int64(len(RoomCodeChars))))
		if err != nil {
			// fallback to math/rand if crypto fails
			code[i] = RoomCodeChars[rand.Intn(len(RoomCodeChars))]
			continue
		}
		code[i] = RoomCodeChars[n.Int64()]
	}
	return string(code)
}

// GetUniqueRoomCode generates a unique room code
func GetUniqueRoomCode(lobbyStore *store.LobbyStore) string {
	for {
		code := GenerateRoomCode()
		if !lobbyStore.Exists(code) {
			return code
		}
	}
}

// PhasePathFor returns the URL path for a given game phase
func PhasePathFor(roomCode string, status models.GameStatus) string {
	switch status {
	case models.StatusReadyCheck:
		return "/game/" + roomCode + "/confirm-reveal"
	case models.StatusRoleReveal:
		return "/game/" + roomCode + "/roles"
	case models.StatusPlaying:
		return "/game/" + roomCode + "/play"
	case models.StatusVoting:
		return "/game/" + roomCode + "/voting"
	case models.StatusFinished:
		return "/results/" + roomCode
	default:
		return "/lobby/" + roomCode
	}
}
