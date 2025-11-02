package handlers

import (
	"fmt"
	"net/http"

	"github.com/aaronzipp/you-are-officially-sus/internal/models"
)

// getLobbyAndPlayer validates membership using session cookie
func (ctx *Context) getLobbyAndPlayer(r *http.Request, roomCode string) (*models.Lobby, string, error) {
	lobby, exists := ctx.LobbyStore.Get(roomCode)
	if !exists {
		return nil, "", fmt.Errorf("lobby not found")
	}
	cookie, err := r.Cookie("player_id")
	if err != nil {
		return nil, "", fmt.Errorf("no session")
	}
	playerID := cookie.Value
	lobby.RLock()
	_, member := lobby.Players[playerID]
	lobby.RUnlock()
	if !member {
		return nil, "", fmt.Errorf("not a member")
	}
	return lobby, playerID, nil
}
