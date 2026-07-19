package server

// Agent- and session-scoped console routes. These are dispatched by path
// prefix from the mux in console.go.

import (
	"context"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// hAgentRoutes dispatches /agents/{id} and its sub-actions.
func (s *Server) hAgentRoutes(w http.ResponseWriter, r *http.Request) {
	parts := splitPath(r.URL.Path) // ["agents", id, ...]
	if len(parts) < 2 {
		http.NotFound(w, r)
		return
	}
	id, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	a, err := s.store.Agent(id)
	if err != nil {
		http.NotFound(w, r)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 120*time.Second)
	defer cancel()

	// /agents/{id}
	if len(parts) == 2 {
		s.renderAgent(w, r, a, "", "")
		return
	}

	switch parts[2] {
	case "name":
		s.hName(ctx, w, r, a, parts)
	case "charter":
		s.hCharter(ctx, w, r, a, parts)
	case "binding":
		s.hBinding(w, r, a, parts)
	case "wake":
		s.hWake(w, r, a)
	case "primer":
		s.hPrimer(ctx, w, r, a)
	case "identifier":
		s.hIdentifier(w, r, a)
	case "model":
		s.hModel(w, r, a)
	case "documents":
		s.hDocuments(w, r, a, parts)
	default:
		http.NotFound(w, r)
	}
}

func (s *Server) hCharter(ctx context.Context, w http.ResponseWriter, r *http.Request, a *Agent, parts []string) {
	if r.Method != http.MethodPost || len(parts) < 4 {
		http.NotFound(w, r)
		return
	}
	switch parts[3] {
	case "draft":
		if a.Name == "" {
			s.renderAgent(w, r, a, "Name the agent before drafting its charter — the charter is written in its own voice, under its name.", "err")
			return
		}
		charter, err := s.DraftCharter(ctx, a, r.FormValue("guidance"))
		if err != nil {
			s.renderAgent(w, r, a, "Charter draft failed: "+err.Error(), "err")
			return
		}
		a.Charter = charter
		a.CharterStatus = "draft"
		s.store.UpdateAgent(a)
	case "approve":
		if a.Charter == "" {
			s.renderAgent(w, r, a, "There is no charter to approve.", "err")
			return
		}
		a.CharterStatus = "approved"
		s.store.UpdateAgent(a)
	}
	redirect(w, r, "/agents/"+strconv.FormatInt(a.ID, 10))
}

func (s *Server) hBinding(w http.ResponseWriter, r *http.Request, a *Agent, parts []string) {
	if r.Method != http.MethodPost {
		http.NotFound(w, r)
		return
	}
	// /agents/{id}/binding  or  /agents/{id}/binding/delete
	if len(parts) == 4 && parts[3] == "delete" {
		if b, _ := s.store.AgentBinding(a.ID); b != nil {
			s.store.DeleteBinding(b.ID)
		}
		redirect(w, r, "/agents/"+strconv.FormatInt(a.ID, 10))
		return
	}
	if _, err := s.store.CreateBinding(a.ID, strings.TrimSpace(r.FormValue("label"))); err != nil {
		s.renderAgent(w, r, a, "Could not generate token: "+err.Error(), "err")
		return
	}
	redirect(w, r, "/agents/"+strconv.FormatInt(a.ID, 10))
}

func (s *Server) hWake(w http.ResponseWriter, r *http.Request, a *Agent) {
	if r.Method != http.MethodPost {
		http.NotFound(w, r)
		return
	}
	sess, err := s.Wake(a.ID)
	if err != nil {
		s.renderAgent(w, r, a, "Could not wake agent: "+err.Error(), "err")
		return
	}
	if msg := strings.TrimSpace(r.FormValue("message")); msg != "" {
		// Launches the opening turn in the background; the redirect lands on
		// the live session page, which polls while the agent responds.
		if err := s.SendMessage(sess.ID, msg); err != nil {
			log.Printf("session %d: opening message: %v", sess.ID, err)
		}
	}
	redirect(w, r, "/sessions/"+strconv.FormatInt(sess.ID, 10))
}

func (s *Server) hPrimer(ctx context.Context, w http.ResponseWriter, r *http.Request, a *Agent) {
	if r.Method == http.MethodPost {
		a.Primer = strings.TrimSpace(r.FormValue("primer"))
		// Editing the primer re-drafts the charter and returns it to draft.
		if charter, err := s.DraftCharter(ctx, a, ""); err == nil {
			a.Charter = charter
		}
		a.CharterStatus = "draft"
		s.store.UpdateAgent(a)
		redirect(w, r, "/agents/"+strconv.FormatInt(a.ID, 10))
		return
	}
	d := struct {
		pageData
		Agent *Agent
	}{s.base("Primer", r), a}
	s.render(w, "primer", d)
}

// hName handles the staged-naming controls for an unnamed agent:
// /agents/{id}/name sets the name; /agents/{id}/name/suggest asks the
// agent's model for one, optionally informed by namer-only guidance that is
// used for the single suggestion call and never stored — it can therefore
// carry facts the primer deliberately withholds. Names are set once; a
// named agent has no rename control by design (a name embedded in a
// charter and self-authored documents is identity, not configuration).
func (s *Server) hName(ctx context.Context, w http.ResponseWriter, r *http.Request, a *Agent, parts []string) {
	if r.Method != http.MethodPost {
		http.NotFound(w, r)
		return
	}
	if a.Name != "" {
		s.renderAgent(w, r, a, "This agent is already named; names are set once.", "err")
		return
	}

	if len(parts) == 4 && parts[3] == "suggest" {
		guidance := strings.TrimSpace(r.FormValue("guidance"))
		name, err := s.SuggestName(ctx, a.EndpointID, a.Model, a.Primer, a.ModelAware, guidance)
		if err != nil || name == "" {
			s.renderAgentStage(w, r, a, "Name suggestion failed: "+errText(err), "err", "", guidance)
			return
		}
		s.renderAgentStage(w, r, a, "", "", name, guidance)
		return
	}

	name := strings.TrimSpace(r.FormValue("name"))
	if name == "" {
		s.renderAgent(w, r, a, "Name cannot be empty.", "err")
		return
	}
	a.Name = name
	if err := s.store.UpdateAgent(a); err != nil {
		a.Name = ""
		s.renderAgent(w, r, a, "Could not set name: "+err.Error(), "err")
		return
	}
	redirect(w, r, "/agents/"+strconv.FormatInt(a.ID, 10))
}

func errText(err error) string {
	if err == nil {
		return "the model returned an empty name"
	}
	return err.Error()
}

// hIdentifier renames the agent's server-side handle. The identifier never
// reaches the agent or its client — bindings, sessions and documents all key
// on the agent ID — so a rename is purely an administrative relabeling.
func (s *Server) hIdentifier(w http.ResponseWriter, r *http.Request, a *Agent) {
	if r.Method != http.MethodPost {
		http.NotFound(w, r)
		return
	}
	ident := strings.TrimSpace(r.FormValue("identifier"))
	if ident == "" {
		s.renderAgent(w, r, a, "Identifier cannot be empty.", "err")
		return
	}
	old := a.Identifier
	a.Identifier = ident
	if err := s.store.UpdateAgent(a); err != nil {
		a.Identifier = old // don't render unsaved state
		s.renderAgent(w, r, a, "Could not rename: "+err.Error(), "err")
		return
	}
	redirect(w, r, "/agents/"+strconv.FormatInt(a.ID, 10))
}

// hModel repoints the agent at a different endpoint, model, or both, and
// owns the model_aware flag. Exists for the realities of hosted models:
// outages, provider changes, deprecations, or a slug simply entered wrong at
// creation. Deliberately touches nothing else — charter, documents, session
// logs and binding all persist; the agent's continuity is its memory, not
// its substrate. Takes effect on the next turn (context and backend are
// assembled per turn), which is exactly what an outage rescue needs.
func (s *Server) hModel(w http.ResponseWriter, r *http.Request, a *Agent) {
	if r.Method != http.MethodPost {
		http.NotFound(w, r)
		return
	}
	model := strings.TrimSpace(r.FormValue("model"))
	if model == "" {
		s.renderAgent(w, r, a, "Model cannot be empty.", "err")
		return
	}
	endpointID, err := strconv.ParseInt(r.FormValue("endpoint_id"), 10, 64)
	if err != nil {
		s.renderAgent(w, r, a, "Choose an endpoint.", "err")
		return
	}
	if _, err := s.store.Endpoint(endpointID); err != nil {
		s.renderAgent(w, r, a, "No such endpoint.", "err")
		return
	}

	oldEndpoint, oldModel, oldAware := a.EndpointID, a.Model, a.ModelAware
	a.EndpointID = endpointID
	a.Model = model
	a.ModelAware = r.FormValue("model_aware") != ""
	if err := s.store.UpdateAgent(a); err != nil {
		a.EndpointID, a.Model, a.ModelAware = oldEndpoint, oldModel, oldAware
		s.renderAgent(w, r, a, "Could not update model: "+err.Error(), "err")
		return
	}
	redirect(w, r, "/agents/"+strconv.FormatInt(a.ID, 10))
}

func (s *Server) hDocuments(w http.ResponseWriter, r *http.Request, a *Agent, parts []string) {
	// /agents/{id}/documents/{name}/history|rollback
	if len(parts) < 5 {
		http.NotFound(w, r)
		return
	}
	name := parts[3]
	switch parts[4] {
	case "history":
		versions, _ := s.store.DocumentVersions(a.ID, name)
		doc, _ := s.store.Document(a.ID, name)
		cur := int64(0)
		if doc != nil {
			cur = doc.CurrentVersion
		}
		d := struct {
			pageData
			Agent    *Agent
			DocName  string
			Versions []*DocumentVersion
			Current  int64
		}{s.base(name, r), a, name, versions, cur}
		s.render(w, "history", d)
	case "rollback":
		if r.Method == http.MethodPost {
			v, _ := strconv.ParseInt(r.FormValue("version"), 10, 64)
			s.store.RollbackDocument(a.ID, name, v)
		}
		redirect(w, r, "/agents/"+strconv.FormatInt(a.ID, 10)+"/documents/"+name+"/history")
	default:
		http.NotFound(w, r)
	}
}

// hSessionRoutes dispatches /sessions/{id} and its actions.
func (s *Server) hSessionRoutes(w http.ResponseWriter, r *http.Request) {
	parts := splitPath(r.URL.Path) // ["sessions", id, ...]
	if len(parts) < 2 {
		http.NotFound(w, r)
		return
	}
	id, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	sess, err := s.store.Session(id)
	if err != nil {
		http.NotFound(w, r)
		return
	}

	if len(parts) == 2 {
		s.renderSession(w, r, sess, "", "")
		return
	}

	switch parts[2] {
	case "message":
		if r.Method == http.MethodPost {
			if msg := strings.TrimSpace(r.FormValue("message")); msg != "" {
				if err := s.SendMessage(sess.ID, msg); err != nil {
					// Never drop an admin message silently: show why.
					s.renderSession(w, r, sess, "Message not delivered: "+err.Error(), "err")
					return
				}
			}
		}
		redirect(w, r, "/sessions/"+strconv.FormatInt(sess.ID, 10))
	case "commit":
		if r.Method == http.MethodPost {
			if err := s.ConsolidateAsync(sess.ID); err != nil {
				s.renderSession(w, r, sess, "Could not end the chat: "+err.Error(), "err")
				return
			}
		}
		redirect(w, r, "/sessions/"+strconv.FormatInt(sess.ID, 10))
	case "delete":
		if r.Method == http.MethodPost {
			agentID := sess.AgentID
			if err := s.DeleteSession(sess.ID); err != nil {
				s.renderSession(w, r, sess, "Could not delete: "+err.Error(), "err")
				return
			}
			redirect(w, r, "/agents/"+strconv.FormatInt(agentID, 10))
		}
	default:
		http.NotFound(w, r)
	}
}
