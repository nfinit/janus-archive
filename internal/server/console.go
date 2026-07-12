package server

// The admin web console: server-rendered HTML over html/template, no
// client-side framework. Handlers are grouped here; view-model assembly
// for the heavier pages lives in console_views.go.

import (
	"context"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/nfinit/janus/internal/version"
)

// Handler returns the console + client-API mux.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	// Client-facing API (token-authenticated, HTTP/1.0 floor).
	mux.HandleFunc("/api/poll", s.handlePoll)
	mux.HandleFunc("/api/result", s.handleResult)

	// Console.
	mux.HandleFunc("/setup", s.hSetup)
	mux.HandleFunc("/login", s.hLogin)
	mux.HandleFunc("/logout", s.hLogout)
	mux.HandleFunc("/", s.guard(s.hDashboard))
	mux.HandleFunc("/endpoints", s.guard(s.hEndpoints))
	mux.HandleFunc("/endpoints/", s.guard(s.hEndpointAction))
	mux.HandleFunc("/agents", s.guard(s.hAgents))
	mux.HandleFunc("/agents/new", s.guard(s.hAgentNew))
	mux.HandleFunc("/agents/", s.guard(s.hAgentRoutes))
	mux.HandleFunc("/sessions/", s.guard(s.hSessionRoutes))

	return mux
}

func (s *Server) base(title string, r *http.Request) pageData {
	return pageData{Title: title, Version: version.Server(), Authed: s.authed(r)}
}

func (s *Server) render(w http.ResponseWriter, page string, data any) {
	if err := s.tmpl.render(w, page, data); err != nil {
		log.Printf("render %s: %v", page, err)
		http.Error(w, "template error", http.StatusInternalServerError)
	}
}

// guard requires an authenticated admin, redirecting to setup or login.
func (s *Server) guard(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !s.isConfigured() {
			http.Redirect(w, r, "/setup", http.StatusSeeOther)
			return
		}
		if !s.authed(r) {
			http.Redirect(w, r, "/login", http.StatusSeeOther)
			return
		}
		h(w, r)
	}
}

func redirect(w http.ResponseWriter, r *http.Request, path string) {
	http.Redirect(w, r, path, http.StatusSeeOther)
}

// ---- setup / auth ----

func (s *Server) hSetup(w http.ResponseWriter, r *http.Request) {
	if s.isConfigured() {
		redirect(w, r, "/login")
		return
	}
	if r.Method == http.MethodPost {
		pw := r.FormValue("password")
		if len(pw) < 6 || pw != r.FormValue("confirm") {
			d := s.base("Setup", r)
			d.Flash, d.FlashKind = "Passwords must match and be at least 6 characters.", "err"
			s.render(w, "setup", d)
			return
		}
		if err := s.store.SetSetting(settingPWHash, hashPassword(pw)); err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		s.setSessionCookie(w)
		redirect(w, r, "/")
		return
	}
	s.render(w, "setup", s.base("Setup", r))
}

func (s *Server) hLogin(w http.ResponseWriter, r *http.Request) {
	if !s.isConfigured() {
		redirect(w, r, "/setup")
		return
	}
	if r.Method == http.MethodPost {
		stored, _ := s.store.Setting(settingPWHash)
		if verifyPassword(r.FormValue("password"), stored) {
			s.setSessionCookie(w)
			redirect(w, r, "/")
			return
		}
		d := s.base("Sign in", r)
		d.Flash, d.FlashKind = "Incorrect password.", "err"
		s.render(w, "login", d)
		return
	}
	if s.authed(r) {
		redirect(w, r, "/")
		return
	}
	s.render(w, "login", s.base("Sign in", r))
}

func (s *Server) setSessionCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name: sessionCookie, Value: s.auth.newSession(),
		Path: "/", HttpOnly: true, SameSite: http.SameSiteLaxMode,
	})
}

func (s *Server) hLogout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(sessionCookie); err == nil {
		s.auth.destroy(c.Value)
	}
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: "", Path: "/", MaxAge: -1})
	redirect(w, r, "/login")
}

// ---- dashboard ----

func (s *Server) hDashboard(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	vm, err := s.dashboardVM(r)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	s.render(w, "dashboard", vm)
}

// ---- endpoints ----

func (s *Server) hEndpoints(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodPost {
		e := &Endpoint{
			Name:    strings.TrimSpace(r.FormValue("name")),
			Kind:    r.FormValue("kind"),
			BaseURL: strings.TrimSpace(r.FormValue("base_url")),
			APIKey:  strings.TrimSpace(r.FormValue("api_key")),
		}
		if e.Kind == "stub" {
			e.BaseURL, e.APIKey = "", ""
		}
		if err := s.store.CreateEndpoint(e); err != nil {
			s.renderEndpoints(w, r, "Could not add endpoint: "+err.Error(), "err")
			return
		}
		redirect(w, r, "/endpoints")
		return
	}
	s.renderEndpoints(w, r, "", "")
}

func (s *Server) renderEndpoints(w http.ResponseWriter, r *http.Request, flash, kind string) {
	eps, _ := s.store.Endpoints()
	d := struct {
		pageData
		Endpoints []*Endpoint
	}{s.base("Endpoints", r), eps}
	d.Flash, d.FlashKind = flash, kind
	s.render(w, "endpoints", d)
}

func (s *Server) hEndpointAction(w http.ResponseWriter, r *http.Request) {
	// /endpoints/{id}/delete
	parts := splitPath(r.URL.Path)
	if len(parts) == 3 && parts[2] == "delete" && r.Method == http.MethodPost {
		id, _ := strconv.ParseInt(parts[1], 10, 64)
		if err := s.store.DeleteEndpoint(id); err != nil {
			s.renderEndpoints(w, r, "Could not delete: "+err.Error(), "err")
			return
		}
	}
	redirect(w, r, "/endpoints")
}

// ---- agents ----

func (s *Server) hAgents(w http.ResponseWriter, r *http.Request) {
	redirect(w, r, "/") // dashboard is the agent list
}

func (s *Server) hAgentNew(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodPost {
		s.createAgent(w, r)
		return
	}
	eps, _ := s.store.Endpoints()
	d := struct {
		pageData
		Endpoints []*Endpoint
	}{s.base("New agent", r), eps}
	s.render(w, "agent_new", d)
}

func (s *Server) createAgent(w http.ResponseWriter, r *http.Request) {
	endpointID, _ := strconv.ParseInt(r.FormValue("endpoint_id"), 10, 64)
	a := &Agent{
		Identifier:    strings.TrimSpace(r.FormValue("identifier")),
		Name:          strings.TrimSpace(r.FormValue("name")),
		EndpointID:    endpointID,
		Model:         strings.TrimSpace(r.FormValue("model")),
		Primer:        strings.TrimSpace(r.FormValue("primer")),
		ModelAware:    r.FormValue("model_aware") != "",
		CharterStatus: "draft",
	}
	if a.Identifier == "" || a.Model == "" || a.Primer == "" {
		http.Error(w, "identifier, model and primer are required", http.StatusBadRequest)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 90*time.Second)
	defer cancel()

	// Optionally let the model name itself.
	if a.Name == "" {
		name, err := s.SuggestName(ctx, endpointID, a.Model, a.Primer, a.ModelAware)
		if err != nil || name == "" {
			a.Name = a.Identifier // fall back to the handle
		} else {
			a.Name = name
		}
	}

	if err := s.store.CreateAgent(a); err != nil {
		http.Error(w, "could not create agent: "+err.Error(), 500)
		return
	}

	// Draft the initial charter now so the admin lands on a reviewable draft.
	if charter, err := s.DraftCharter(ctx, a, ""); err == nil {
		a.Charter = charter
		s.store.UpdateAgent(a)
	} else {
		log.Printf("agent %d: initial charter draft failed: %v", a.ID, err)
	}

	redirect(w, r, "/agents/"+strconv.FormatInt(a.ID, 10))
}

func splitPath(p string) []string {
	return strings.Split(strings.Trim(p, "/"), "/")
}
