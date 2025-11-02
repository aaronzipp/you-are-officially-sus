package handlers

import (
	"fmt"
	"net/http"
	"sort"
	"strings"

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

// getPlayerList converts map to sorted slice for templates
func getPlayerList(players map[string]*models.Player) []*models.Player {
	list := make([]*models.Player, 0, len(players))
	for _, p := range players {
		list = append(list, p)
	}
	sort.Slice(list, func(i, j int) bool { return strings.ToLower(list[i].Name) < strings.ToLower(list[j].Name) })
	return list
}
