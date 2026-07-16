package server

// View-model assembly for the console's data-heavy pages.

import (
	"bytes"
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	"github.com/nfinit/janus/internal/backend"
)

const onlineWindow = 2 * time.Minute

func isOnline(lastSeen int64) bool {
	return lastSeen != 0 && time.Since(time.Unix(lastSeen, 0)) < onlineWindow
}

// ---- agents list ----

type agentRow struct {
	Agent  *Agent
	Bound  bool
	Online bool
	Host   string
}

type agentsData struct {
	pageData
	Agents []agentRow
}

func (s *Server) agentRows() ([]agentRow, error) {
	agents, err := s.store.Agents()
	if err != nil {
		return nil, err
	}
	rows := make([]agentRow, 0, len(agents))
	for _, a := range agents {
		row := agentRow{Agent: a}
		if b, _ := s.store.AgentBinding(a.ID); b != nil {
			row.Bound = true
			row.Online = isOnline(b.LastSeen)
			row.Host = b.LastHostname
			if row.Host == "" {
				row.Host = "bound"
			}
		}
		rows = append(rows, row)
	}
	return rows, nil
}

func (s *Server) agentsVM(r *http.Request) (*agentsData, error) {
	rows, err := s.agentRows()
	if err != nil {
		return nil, err
	}
	return &agentsData{pageData: s.base("Agents", r), Agents: rows}, nil
}

// ---- dashboard ----

// activeSessionRow is one live session on the overview.
type activeSessionRow struct {
	Session   *Session
	AgentName string
	Busy      bool
}

// attentionItem is one thing the administrator should probably look at.
type attentionItem struct {
	Text string
	Href string
}

type dashboardData struct {
	pageData
	AgentCount    int
	EndpointCount int
	Active        []activeSessionRow
	Attention     []attentionItem
}

func (s *Server) dashboardVM(r *http.Request) (*dashboardData, error) {
	rows, err := s.agentRows()
	if err != nil {
		return nil, err
	}
	eps, err := s.store.Endpoints()
	if err != nil {
		return nil, err
	}
	d := &dashboardData{
		pageData:      s.base("Dashboard", r),
		AgentCount:    len(rows),
		EndpointCount: len(eps),
	}

	// Live sessions, with turn state.
	active, _ := s.store.ActiveSessions()
	for _, sess := range active {
		row := activeSessionRow{Session: sess, Busy: s.gate.running(sess.ID)}
		if a, err := s.store.Agent(sess.AgentID); err == nil {
			row.AgentName = a.Name
		}
		d.Active = append(d.Active, row)
	}

	// Things needing an administrator's eye. Ordered by how actionable
	// they are: charters block wakes entirely, dead clients block hands,
	// retirements are advance warning.
	for _, row := range rows {
		href := "/agents/" + strconv.FormatInt(row.Agent.ID, 10)
		if row.Agent.CharterStatus != "approved" {
			d.Attention = append(d.Attention, attentionItem{
				Text: row.Agent.Name + " — charter awaiting review and approval", Href: href})
		}
	}
	for _, row := range rows {
		href := "/agents/" + strconv.FormatInt(row.Agent.ID, 10)
		if row.Bound && !row.Online {
			d.Attention = append(d.Attention, attentionItem{
				Text: row.Agent.Name + " — client offline (" + row.Host + ")", Href: href})
		}
	}
	for _, row := range rows {
		href := "/agents/" + strconv.FormatInt(row.Agent.ID, 10)
		if mi := s.modelInfo(row.Agent); mi != nil && mi.ExpirationDate != "" {
			d.Attention = append(d.Attention, attentionItem{
				Text: row.Agent.Name + " — provider retires " + mi.Name + " on " + mi.ExpirationDate, Href: href})
		}
	}
	return d, nil
}

// ---- agent detail ----

type agentData struct {
	pageData
	Agent         *Agent
	EndpointName  string
	Endpoints     []*Endpoint // for the change-model form
	ModelInfo     *ModelInfo  // provider catalog metadata; nil = slug only
	Binding       *Binding
	Online        bool
	LastSeenHuman string
	ActiveSession *Session
	Sessions      []*Session
	Documents     []*Document
	SessionLogs   []*SessionLog
}

func (s *Server) renderAgent(w http.ResponseWriter, r *http.Request, a *Agent, flash, kind string) {
	d := &agentData{pageData: s.base(a.Name, r), Agent: a}
	d.Flash, d.FlashKind = flash, kind

	if e, err := s.store.Endpoint(a.EndpointID); err == nil {
		d.EndpointName = e.Name
	}
	d.Endpoints, _ = s.store.Endpoints()
	d.ModelInfo = s.modelInfo(a)
	if b, _ := s.store.AgentBinding(a.ID); b != nil {
		d.Binding = b
		d.Online = isOnline(b.LastSeen)
		if b.LastSeen != 0 {
			d.LastSeenHuman = time.Unix(b.LastSeen, 0).Format("2006-01-02 15:04:05")
		}
	}
	sessions, _ := s.store.AgentSessions(a.ID)
	d.Sessions = sessions
	for _, sess := range sessions {
		if sess.Status == SessionOpen || sess.Status == SessionLocked {
			d.ActiveSession = sess
			break
		}
	}
	d.Documents, _ = s.store.Documents(a.ID)
	d.SessionLogs, _ = s.store.SessionLogs(a.ID, 3)

	s.render(w, "agent", d)
}

// ---- session detail ----

type sessionData struct {
	pageData
	Agent    *Agent
	Session  *Session
	Messages []*Message

	// Wake-time context snapshot: what the agent actually saw when this
	// session opened. Empty SystemContext means the session predates
	// snapshot capture (0.0.0.4).
	SystemContext string
	Tools         []backend.Tool

	// Busy is true while a turn is in flight; the page then self-refreshes
	// and withholds the input controls.
	Busy bool

	// modelNames maps served-model slugs to provider display names, for
	// transcript "via" labels. Missing entries render as the slug.
	modelNames map[string]string
}

// ModelLabel returns the display name for a served-model slug, or the slug
// itself when the provider publishes no catalog metadata for it.
func (d *sessionData) ModelLabel(slug string) string {
	if name, ok := d.modelNames[slug]; ok && name != "" {
		return name
	}
	return slug
}

// ToolParams pretty-prints a tool's parameter schema for the context panel.
func (sessionData) ToolParams(t backend.Tool) string {
	var buf bytes.Buffer
	if err := json.Indent(&buf, t.Parameters, "", "  "); err != nil {
		return string(t.Parameters)
	}
	return buf.String()
}

// SpeakerLabel names a message's speaker. The agent's own turns carry its
// chosen name rather than the generic "agent" role label; every other role
// falls through to RoleLabel.
func (d *sessionData) SpeakerLabel(role string) string {
	if role == RoleAgent && d.Agent != nil && d.Agent.Name != "" {
		return d.Agent.Name
	}
	return d.RoleLabel(role)
}

func (s *Server) renderSession(w http.ResponseWriter, r *http.Request, sess *Session, flash, kind string) {
	a, err := s.store.Agent(sess.AgentID)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	msgs, _ := s.store.SessionMessages(sess.ID, 0)
	d := &sessionData{
		pageData: s.base("Session "+strconv.FormatInt(sess.ID, 10), r),
		Agent:    a, Session: sess, Messages: msgs,
	}
	d.Flash, d.FlashKind = flash, kind
	d.Busy = s.gate.running(sess.ID)
	if d.Busy {
		// Self-refresh while the turn runs: no JavaScript, just a meta
		// refresh that disappears once the agent has answered.
		d.Refresh = 3
	}
	if sysCtx, toolsJSON, err := s.store.SessionContext(sess.ID); err == nil {
		d.SystemContext = sysCtx
		if toolsJSON != "" {
			json.Unmarshal([]byte(toolsJSON), &d.Tools) // best-effort; empty on failure
		}
	}
	// Prettify per-turn "via" labels from the agent's endpoint catalog.
	// One Lookup per distinct slug; the catalog itself is cached.
	if e, err := s.store.Endpoint(a.EndpointID); err == nil {
		d.modelNames = make(map[string]string)
		for _, m := range msgs {
			if m.Model == "" {
				continue
			}
			if _, seen := d.modelNames[m.Model]; seen {
				continue
			}
			name := ""
			if mi := s.meta.Lookup(e, m.Model); mi != nil {
				name = mi.Name
			}
			d.modelNames[m.Model] = name
		}
	}
	s.render(w, "session", d)
}
