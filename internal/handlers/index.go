package handlers

import (
	"bytes"
	"html/template"
	"log"
	"net/http"

	"github.com/aaronzipp/you-are-officially-sus/internal/models"
	"github.com/aaronzipp/you-are-officially-sus/internal/render"
	"github.com/aaronzipp/you-are-officially-sus/internal/store"
)

// Context holds shared application dependencies
type Context struct {
	LobbyStore *store.LobbyStore
	Templates  *template.Template
	Locations  []models.Location
	Challenges []string
	BaseURL    string
}

// ExecutePartial executes a template partial and returns the HTML string
func (ctx *Context) ExecutePartial(name string, data interface{}) string {
	var buf bytes.Buffer
	if err := ctx.Templates.ExecuteTemplate(&buf, name, data); err != nil {
		// Log error to help debug template issues
		log.Printf("ERROR: ExecutePartial failed for %s: %v (data type: %T)", name, err, data)
		return ""
	}
	return buf.String()
}

// PlayerList generates HTML for the player list using template partials
func (ctx *Context) PlayerList(players map[string]*models.Player) string {
	return ctx.ExecutePartial("player_list.html", struct {
		Players []*models.Player
	}{
		Players: render.GetPlayerList(players),
	})
}

// HostControls generates HTML for host controls using template partials
func (ctx *Context) HostControls(lobby *models.Lobby, playerID string) string {
	return ctx.ExecutePartial("host_controls.html", struct {
		IsHost      bool
		PlayerCount int
		InGame      bool
		RoomCode    string
	}{
		IsHost:      lobby.Host == playerID,
		PlayerCount: len(lobby.Players),
		InGame:      lobby.CurrentGame != nil,
		RoomCode:    lobby.Code,
	})
}

// ScoreTable generates HTML for the score table using template partials
func (ctx *Context) ScoreTable(lobby *models.Lobby) string {
	return ctx.ExecutePartial("score_table.html", struct {
		Players []*models.Player
		Scores  map[string]*models.PlayerScore
	}{
		Players: render.GetPlayerListSortedByScore(lobby.Players, lobby.Scores),
		Scores:  lobby.Scores,
	})
}

// ReadyCount generates HTML for ready count display
func (ctx *Context) ReadyCount(ready, total int, label string) string {
	return ctx.ExecutePartial("ready_count.html", struct {
		ReadyCount int
		TotalCount int
		Label      string
	}{
		ReadyCount: ready,
		TotalCount: total,
		Label:      label,
	})
}

// VoteCount generates HTML for vote count display
func (ctx *Context) VoteCount(count, total int) string {
	return ctx.ExecutePartial("vote_count.html", struct {
		VoteCount  int
		TotalCount int
	}{
		VoteCount:  count,
		TotalCount: total,
	})
}

// VotedConfirmation generates HTML for "you voted" confirmation
func (ctx *Context) VotedConfirmation() string {
	return ctx.ExecutePartial("voted_confirmation.html", nil)
}

// ErrorMessage generates HTML for error messages
func (ctx *Context) ErrorMessage(message string) string {
	return ctx.ExecutePartial("error_message.html", struct {
		Message string
	}{
		Message: message,
	})
}

// RedirectSnippet returns an HTMX snippet that triggers a client-side redirect
func (ctx *Context) RedirectSnippet(roomCode, to string) string {
	return ctx.ExecutePartial("redirect_snippet.html", struct {
		RoomCode string
		To       string
	}{
		RoomCode: roomCode,
		To:       to,
	})
}

// GameAbortedMessage generates HTML for game aborted warning
func (ctx *Context) GameAbortedMessage(reason string) string {
	return ctx.ExecutePartial("game_aborted_message.html", struct {
		Reason string
	}{
		Reason: reason,
	})
}

// HostNotification generates HTML for new host notification
func (ctx *Context) HostNotification() string {
	return ctx.ExecutePartial("host_notification.html", nil)
}

// HandleIndex serves the landing page
func (ctx *Context) HandleIndex(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	ctx.Templates.ExecuteTemplate(w, "index.html", nil)
}
