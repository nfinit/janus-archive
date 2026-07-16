package server

import (
	"embed"
	"fmt"
	"html/template"
	"io"
	"time"
)

//go:embed templates/*.html
var templateFS embed.FS

// Each page template is parsed together with the shared layout. Because
// every page defines "content" and the layout defines "layout", we parse
// one clone per page to avoid "content" collisions.
type templates struct {
	pages map[string]*template.Template
}

var pageFiles = map[string]string{
	"setup":     "templates/setup.html",
	"login":     "templates/login.html",
	"dashboard": "templates/dashboard.html",
	"endpoints": "templates/endpoints.html",
	"agent_new": "templates/agent_new.html",
	"agent":     "templates/agent.html",
	"session":   "templates/session.html",
	"history":   "templates/history.html",
	"primer":    "templates/primer.html",
}

func loadTemplates() (*templates, error) {
	t := &templates{pages: make(map[string]*template.Template)}
	for name, file := range pageFiles {
		tmpl, err := template.New("layout").ParseFS(templateFS, "templates/layout.html", file)
		if err != nil {
			return nil, fmt.Errorf("parse template %s: %w", name, err)
		}
		t.pages[name] = tmpl
	}
	return t, nil
}

func (t *templates) render(w io.Writer, page string, data any) error {
	tmpl, ok := t.pages[page]
	if !ok {
		return fmt.Errorf("unknown page %q", page)
	}
	return tmpl.ExecuteTemplate(w, "layout", data)
}

// pageData is the base data every page embeds via the layout.
type pageData struct {
	Title     string
	Version   string
	Authed    bool
	Flash     string
	FlashKind string // "ok" | "err"

	// Refresh, when nonzero, makes the page reload itself every N seconds
	// via a meta refresh tag — the console's zero-JavaScript way to follow
	// work happening in the background.
	Refresh int
}

func (pageData) FmtTime(unix int64) string {
	if unix == 0 {
		return "—"
	}
	return time.Unix(unix, 0).Format("2006-01-02 15:04")
}

func (pageData) RoleLabel(role string) string {
	switch role {
	case RoleAdmin:
		return "administrator"
	case RoleAgent:
		return "agent"
	case RoleToolCall:
		return "→ tool call"
	case RoleToolResult:
		return "← tool result"
	default:
		return "system"
	}
}
