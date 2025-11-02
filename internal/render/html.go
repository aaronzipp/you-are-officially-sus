package render

import (
	htmlpkg "html"
	"sort"
	"strconv"
	"strings"

	"github.com/aaronzipp/you-are-officially-sus/internal/models"
)

// PlayerList generates HTML for the player list
func PlayerList(players map[string]*models.Player) string {
	list := getPlayerList(players)
	var b strings.Builder
	b.WriteString(`<h2>Players (`)
	b.WriteString(strconv.Itoa(len(list)))
	b.WriteString(`)</h2><ul class="player-list">`)
	for _, p := range list {
		name := htmlpkg.EscapeString(p.Name)
		b.WriteString(`<li class="player-item"><span class="player-name">`)
		b.WriteString(name)
		b.WriteString(`</span></li>`)
	}
	b.WriteString(`</ul>`)
	return b.String()
}

// HostControls generates HTML for host controls
func HostControls(lobby *models.Lobby, playerID string) string {
	isHost := lobby.Host == playerID
	playerCount := len(lobby.Players)
	inGame := lobby.CurrentGame != nil

	if inGame {
		return "" // No controls during game
	}

	if isHost {
		if playerCount >= 3 {
			var b strings.Builder
			b.WriteString(`<div class="button-stack"><form hx-post="/start-game/`)
			b.WriteString(lobby.Code)
			b.WriteString(`"><button type="submit" class="btn btn-primary">Start Game</button></form><form hx-post="/close-lobby/`)
			b.WriteString(lobby.Code)
			b.WriteString(`"><button type="submit" class="btn btn-secondary">Close Lobby</button></form></div>`)
			return b.String()
		} else {
			var b strings.Builder
			b.WriteString(`<p>Waiting for players to join...</p><p class="text-muted">Need at least 3 players to start</p><div class="button-stack"><form hx-post="/close-lobby/`)
			b.WriteString(lobby.Code)
			b.WriteString(`"><button type="submit" class="btn btn-secondary">Close Lobby</button></form></div>`)
			return b.String()
		}
	}
	return `<p>Waiting for host to start the game...</p>`
}

// ScoreTable generates HTML for the score table
func ScoreTable(lobby *models.Lobby) string {
	if len(lobby.Scores) == 0 {
		return ""
	}

	players := getPlayerList(lobby.Players)
	// Sort by wins descending, then name asc
	sort.SliceStable(players, func(i, j int) bool {
		wi, wj := 0, 0
		if s, ok := lobby.Scores[players[i].ID]; ok {
			wi = s.GamesWon
		}
		if s, ok := lobby.Scores[players[j].ID]; ok {
			wj = s.GamesWon
		}
		if wi == wj {
			return strings.ToLower(players[i].Name) < strings.ToLower(players[j].Name)
		}
		return wi > wj
	})

	var b strings.Builder
	b.WriteString(`<h2>Scores</h2><table class="score-table" aria-label="Scoreboard sorted by wins"><thead><tr><th>Player</th><th aria-sort="descending" title="Sorted by wins (desc)">Wins ↓</th><th>Losses</th></tr></thead><tbody>`)
	for _, p := range players {
		score := lobby.Scores[p.ID]
		wins, losses := 0, 0
		if score != nil {
			wins = score.GamesWon
			losses = score.GamesLost
		}
		name := htmlpkg.EscapeString(p.Name)
		b.WriteString(`<tr><td class="score-player">`)
		b.WriteString(name)
		b.WriteString(`</td><td><span class="badge-pill badge-win">`)
		b.WriteString(strconv.Itoa(wins))
		b.WriteString(`</span></td><td><span class="badge-pill badge-loss">`)
		b.WriteString(strconv.Itoa(losses))
		b.WriteString(`</span></td></tr>`)
	}
	b.WriteString(`</tbody></table>`)
	return b.String()
}

// ReadyCount generates HTML for ready count display (unified for all phases)
func ReadyCount(ready, total int, label string) string {
	var b strings.Builder
	b.WriteString(`<p class="ready-count">`)
	b.WriteString(strconv.Itoa(ready))
	b.WriteString(`/`)
	b.WriteString(strconv.Itoa(total))
	b.WriteString(` `)
	b.WriteString(label)
	b.WriteString(`</p>`)
	return b.String()
}

// VoteCount generates HTML for vote count display
func VoteCount(count, total int) string {
	var b strings.Builder
	b.WriteString(`<p class="ready-count">`)
	b.WriteString(strconv.Itoa(count))
	b.WriteString(`/`)
	b.WriteString(strconv.Itoa(total))
	b.WriteString(` players have voted</p>`)
	return b.String()
}

// VotedConfirmation generates HTML for "you voted" confirmation
func VotedConfirmation() string {
	return `<div class="card">
		<p class="vote-status">✓ You voted</p>
		<p class="text-muted">Waiting for other players to vote...</p>
	</div>`
}

// RedirectSnippet returns an HTMX snippet that triggers a client-side redirect
func RedirectSnippet(roomCode, to string) string {
	var b strings.Builder
	b.WriteString(`<div hx-get="/game/`)
	b.WriteString(roomCode)
	b.WriteString(`/redirect?to=`)
	b.WriteString(to)
	b.WriteString(`" hx-trigger="load" hx-swap="none"></div>`)
	return b.String()
}

// getPlayerList converts map to sorted slice
func getPlayerList(players map[string]*models.Player) []*models.Player {
	list := make([]*models.Player, 0, len(players))
	for _, p := range players {
		list = append(list, p)
	}
	sort.Slice(list, func(i, j int) bool { return strings.ToLower(list[i].Name) < strings.ToLower(list[j].Name) })
	return list
}
