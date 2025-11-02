package render

import (
	"sort"
	"strings"

	"github.com/aaronzipp/you-are-officially-sus/internal/models"
)

// getPlayerList converts map to sorted slice
func getPlayerList(players map[string]*models.Player) []*models.Player {
	list := make([]*models.Player, 0, len(players))
	for _, p := range players {
		list = append(list, p)
	}
	sort.Slice(list, func(i, j int) bool { return strings.ToLower(list[i].Name) < strings.ToLower(list[j].Name) })
	return list
}

// GetPlayerList is the exported version of getPlayerList for use by handlers
var GetPlayerList = getPlayerList

// GetPlayerListSortedByScore returns players sorted by wins descending, then name ascending
func GetPlayerListSortedByScore(players map[string]*models.Player, scores map[string]*models.PlayerScore) []*models.Player {
	list := getPlayerList(players)
	// Sort by wins descending, then name asc
	sort.SliceStable(list, func(i, j int) bool {
		wi, wj := 0, 0
		if s, ok := scores[list[i].ID]; ok {
			wi = s.GamesWon
		}
		if s, ok := scores[list[j].ID]; ok {
			wj = s.GamesWon
		}
		if wi == wj {
			return strings.ToLower(list[i].Name) < strings.ToLower(list[j].Name)
		}
		return wi > wj
	})
	return list
}
