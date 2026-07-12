package server

// View-model assembly for the console's data-heavy pages.

import (
	"net/http"
	"strconv"
	"time"
)

const onlineWindow = 2 * time.Minute

func isOnline(lastSeen int64) bool {
	return lastSeen != 0 && time.Since(time.Unix(lastSeen, 0)) < onlineWindow
}

// ---- dashboard ----

type agentRow struct {
	Agent  *Agent
	Bound  bool
	Online bool
	Host   string
}

type dashboardData struct {
	pageData
	Agents    []agentRow
	Endpoints []*Endpoint
}

func (s *Server) dashboardVM(r *http.Request) (*dashboardData, error) {
	agents, err := s.store.Agents()
	if err != nil {
		return nil, err
	}
	eps, err := s.store.Endpoints()
	if err != nil {
		return nil, err
	}
	d := &dashboardData{pageData: s.base("Dashboard", r), Endpoints: eps}
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
		d.Agents = append(d.Agents, row)
	}
	return d, nil
}

// ---- agent detail ----

type agentData struct {
	pageData
	Agent         *Agent
	EndpointName  string
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
}

func (s *Server) renderSession(w http.ResponseWriter, r *http.Request, sess *Session) {
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
	s.render(w, "session", d)
}
