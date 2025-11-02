package handlers

import (
	"html/template"
	"net/http"

	"github.com/aaronzipp/you-are-officially-sus/internal/models"
	"github.com/aaronzipp/you-are-officially-sus/internal/store"
)

// Context holds shared application dependencies
type Context struct {
	LobbyStore *store.LobbyStore
	Templates  *template.Template
	Locations  []models.Location
	Challenges []string
}

// HandleIndex serves the landing page
func (ctx *Context) HandleIndex(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	ctx.Templates.ExecuteTemplate(w, "index.html", nil)
}
