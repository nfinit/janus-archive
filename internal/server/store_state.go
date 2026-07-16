package server

// Bindings, sessions, transcripts, documents, session logs.

import (
	"database/sql"
	"errors"
	"fmt"
)

// ---- bindings ----

type Binding struct {
	ID           int64
	AgentID      int64
	Token        string
	Label        string
	LastSeen     int64 // 0 = never
	LastHostname string
	LastVersion  string
	CreatedAt    int64
}

func (s *Store) CreateBinding(agentID int64, label string) (*Binding, error) {
	// Phase 0 rule: one binding per agent, enforced in logic (the schema
	// deliberately permits more; see Phase 0 decision Q6).
	var n int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM bindings WHERE agent_id=?`, agentID).Scan(&n); err != nil {
		return nil, err
	}
	if n > 0 {
		return nil, errors.New("agent already has a client binding (Phase 0 allows one)")
	}
	b := &Binding{AgentID: agentID, Token: NewToken(), Label: label, CreatedAt: now()}
	res, err := s.db.Exec(`INSERT INTO bindings(agent_id,token,label,created_at) VALUES(?,?,?,?)`,
		b.AgentID, b.Token, b.Label, b.CreatedAt)
	if err != nil {
		return nil, err
	}
	b.ID, _ = res.LastInsertId()
	return b, nil
}

func (s *Store) DeleteBinding(id int64) error {
	_, err := s.db.Exec(`DELETE FROM bindings WHERE id=?`, id)
	return err
}

const bindingCols = `id,agent_id,token,label,COALESCE(last_seen,0),last_hostname,last_version,created_at`

func scanBinding(row interface{ Scan(...any) error }) (*Binding, error) {
	b := &Binding{}
	err := row.Scan(&b.ID, &b.AgentID, &b.Token, &b.Label, &b.LastSeen,
		&b.LastHostname, &b.LastVersion, &b.CreatedAt)
	return b, err
}

func (s *Store) BindingByToken(token string) (*Binding, error) {
	b, err := scanBinding(s.db.QueryRow(`SELECT `+bindingCols+` FROM bindings WHERE token=?`, token))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, errors.New("unknown token")
	}
	return b, err
}

func (s *Store) AgentBinding(agentID int64) (*Binding, error) {
	b, err := scanBinding(s.db.QueryRow(`SELECT `+bindingCols+` FROM bindings WHERE agent_id=? LIMIT 1`, agentID))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil // unbound agent: not an error
	}
	return b, err
}

func (s *Store) TouchBinding(id int64, hostname, version string) error {
	_, err := s.db.Exec(`UPDATE bindings SET last_seen=?,last_hostname=?,last_version=? WHERE id=?`,
		now(), hostname, version, id)
	return err
}

// ---- sessions ----

const (
	SessionOpen   = "open"
	SessionLocked = "locked" // consolidation in progress
	SessionClosed = "closed"
)

type Session struct {
	ID           int64
	AgentID      int64
	Status       string
	OpenedAt     int64
	LastActivity int64
	ClosedAt     int64 // 0 = still open
}

func (s *Store) CreateSession(agentID int64) (*Session, error) {
	// One open session per agent at a time.
	var n int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM sessions WHERE agent_id=? AND status IN ('open','locked')`,
		agentID).Scan(&n); err != nil {
		return nil, err
	}
	if n > 0 {
		return nil, errors.New("agent already has an active session")
	}
	t := now()
	sess := &Session{AgentID: agentID, Status: SessionOpen, OpenedAt: t, LastActivity: t}
	res, err := s.db.Exec(`INSERT INTO sessions(agent_id,status,opened_at,last_activity) VALUES(?,?,?,?)`,
		agentID, sess.Status, t, t)
	if err != nil {
		return nil, err
	}
	sess.ID, _ = res.LastInsertId()
	return sess, nil
}

const sessionCols = `id,agent_id,status,opened_at,last_activity,COALESCE(closed_at,0)`

func scanSession(row interface{ Scan(...any) error }) (*Session, error) {
	sess := &Session{}
	err := row.Scan(&sess.ID, &sess.AgentID, &sess.Status, &sess.OpenedAt,
		&sess.LastActivity, &sess.ClosedAt)
	return sess, err
}

func (s *Store) Session(id int64) (*Session, error) {
	sess, err := scanSession(s.db.QueryRow(`SELECT `+sessionCols+` FROM sessions WHERE id=?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("session %d not found", id)
	}
	return sess, err
}

func (s *Store) AgentSessions(agentID int64) ([]*Session, error) {
	rows, err := s.db.Query(`SELECT `+sessionCols+` FROM sessions WHERE agent_id=? ORDER BY id DESC`, agentID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Session
	for rows.Next() {
		sess, err := scanSession(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, sess)
	}
	return out, rows.Err()
}

// SetSessionContext stores the wake-time context snapshot: the system prompt
// and tool menu exactly as assembled when the session opened. Kept out of the
// Session struct — only the session detail view ever needs these blobs.
func (s *Store) SetSessionContext(id int64, systemContext, toolsJSON string) error {
	_, err := s.db.Exec(`UPDATE sessions SET system_context=?, tools_json=? WHERE id=?`,
		systemContext, toolsJSON, id)
	return err
}

// SessionContext returns the wake-time snapshot. Both values are empty for
// sessions that predate snapshot capture.
func (s *Store) SessionContext(id int64) (systemContext, toolsJSON string, err error) {
	err = s.db.QueryRow(`SELECT system_context, tools_json FROM sessions WHERE id=?`, id).
		Scan(&systemContext, &toolsJSON)
	return
}

// IdleOpenSessions returns open sessions whose last activity predates cutoff.
func (s *Store) IdleOpenSessions(cutoff int64) ([]*Session, error) {
	rows, err := s.db.Query(`SELECT `+sessionCols+` FROM sessions WHERE status='open' AND last_activity<?`, cutoff)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Session
	for rows.Next() {
		sess, err := scanSession(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, sess)
	}
	return out, rows.Err()
}

func (s *Store) SetSessionStatus(id int64, status string) error {
	if status == SessionClosed {
		_, err := s.db.Exec(`UPDATE sessions SET status=?,closed_at=? WHERE id=?`, status, now(), id)
		return err
	}
	_, err := s.db.Exec(`UPDATE sessions SET status=? WHERE id=?`, status, id)
	return err
}

func (s *Store) TouchSession(id int64) error {
	_, err := s.db.Exec(`UPDATE sessions SET last_activity=? WHERE id=?`, now(), id)
	return err
}

// ExpungeSession implements the delete control: full removal of an OPEN
// session and its transcript. Locked/closed sessions cannot be expunged —
// their substance has been (or is being) consolidated into agent documents,
// and committed notes must never reference a session that "never happened".
func (s *Store) ExpungeSession(id int64) error {
	sess, err := s.Session(id)
	if err != nil {
		return err
	}
	if sess.Status != SessionOpen {
		return errors.New("only open sessions can be deleted; this one has been consolidated")
	}
	_, err = s.db.Exec(`DELETE FROM sessions WHERE id=?`, id) // messages cascade
	return err
}

// ---- transcript messages ----

const (
	RoleAdmin      = "admin"
	RoleAgent      = "agent"
	RoleToolCall   = "tool_call"
	RoleToolResult = "tool_result"
	RoleSystem     = "system"
)

type Message struct {
	ID        int64
	SessionID int64
	Role      string
	Content   string
	Model     string // the model that served an agent turn, if known
	CreatedAt int64
}

func (s *Store) AppendMessage(sessionID int64, role, content string) (*Message, error) {
	return s.AppendMessageModel(sessionID, role, content, "")
}

// AppendMessageModel records a transcript message, stamping the model that
// produced it (used for agent turns; "" for everything else).
func (s *Store) AppendMessageModel(sessionID int64, role, content, model string) (*Message, error) {
	m := &Message{SessionID: sessionID, Role: role, Content: content, Model: model, CreatedAt: now()}
	res, err := s.db.Exec(`INSERT INTO messages(session_id,role,content,model,created_at) VALUES(?,?,?,?,?)`,
		sessionID, role, content, model, m.CreatedAt)
	if err != nil {
		return nil, err
	}
	m.ID, _ = res.LastInsertId()
	return m, nil
}

func (s *Store) SessionMessages(sessionID int64, afterID int64) ([]*Message, error) {
	rows, err := s.db.Query(`SELECT id,session_id,role,content,model,created_at FROM messages
		WHERE session_id=? AND id>? ORDER BY id`, sessionID, afterID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Message
	for rows.Next() {
		m := &Message{}
		if err := rows.Scan(&m.ID, &m.SessionID, &m.Role, &m.Content, &m.Model, &m.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// ---- documents (versioned) ----

type Document struct {
	ID             int64
	AgentID        int64
	Name           string
	CurrentVersion int64
	Content        string // content of current version
}

type DocumentVersion struct {
	Version   int64
	Content   string
	Author    string
	CreatedAt int64
}

// WriteDocument creates a new version of the named document (creating the
// document if needed). Author is "agent" or "admin".
func (s *Store) WriteDocument(agentID int64, name, content, author string) (int64, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()

	if _, err := tx.Exec(`INSERT INTO documents(agent_id,name) VALUES(?,?)
		ON CONFLICT(agent_id,name) DO NOTHING`, agentID, name); err != nil {
		return 0, err
	}
	var docID, cur int64
	if err := tx.QueryRow(`SELECT id,current_version FROM documents WHERE agent_id=? AND name=?`,
		agentID, name).Scan(&docID, &cur); err != nil {
		return 0, err
	}
	next := cur + 1
	if _, err := tx.Exec(`INSERT INTO document_versions(document_id,version,content,author,created_at)
		VALUES(?,?,?,?,?)`, docID, next, content, author, now()); err != nil {
		return 0, err
	}
	if _, err := tx.Exec(`UPDATE documents SET current_version=? WHERE id=?`, next, docID); err != nil {
		return 0, err
	}
	return next, tx.Commit()
}

// RollbackDocument makes an older version current again by re-writing it as
// a new version (history is never destroyed).
func (s *Store) RollbackDocument(agentID int64, name string, version int64) error {
	var docID int64
	if err := s.db.QueryRow(`SELECT id FROM documents WHERE agent_id=? AND name=?`,
		agentID, name).Scan(&docID); err != nil {
		return fmt.Errorf("document %q not found", name)
	}
	var content string
	if err := s.db.QueryRow(`SELECT content FROM document_versions WHERE document_id=? AND version=?`,
		docID, version).Scan(&content); err != nil {
		return fmt.Errorf("version %d of %q not found", version, name)
	}
	_, err := s.WriteDocument(agentID, name, content, "admin")
	return err
}

func (s *Store) Documents(agentID int64) ([]*Document, error) {
	rows, err := s.db.Query(`SELECT d.id,d.agent_id,d.name,d.current_version,COALESCE(v.content,'')
		FROM documents d
		LEFT JOIN document_versions v ON v.document_id=d.id AND v.version=d.current_version
		WHERE d.agent_id=? ORDER BY d.name`, agentID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Document
	for rows.Next() {
		d := &Document{}
		if err := rows.Scan(&d.ID, &d.AgentID, &d.Name, &d.CurrentVersion, &d.Content); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

func (s *Store) Document(agentID int64, name string) (*Document, error) {
	d := &Document{}
	err := s.db.QueryRow(`SELECT d.id,d.agent_id,d.name,d.current_version,COALESCE(v.content,'')
		FROM documents d
		LEFT JOIN document_versions v ON v.document_id=d.id AND v.version=d.current_version
		WHERE d.agent_id=? AND d.name=?`, agentID, name).
		Scan(&d.ID, &d.AgentID, &d.Name, &d.CurrentVersion, &d.Content)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil // absent document: not an error
	}
	return d, err
}

func (s *Store) DocumentVersions(agentID int64, name string) ([]*DocumentVersion, error) {
	rows, err := s.db.Query(`SELECT v.version,v.content,v.author,v.created_at
		FROM document_versions v
		JOIN documents d ON d.id=v.document_id
		WHERE d.agent_id=? AND d.name=? ORDER BY v.version DESC`, agentID, name)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*DocumentVersion
	for rows.Next() {
		v := &DocumentVersion{}
		if err := rows.Scan(&v.Version, &v.Content, &v.Author, &v.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// ---- session logs ----

type SessionLog struct {
	ID        int64
	AgentID   int64
	SessionID int64
	Content   string
	CreatedAt int64
}

func (s *Store) AppendSessionLog(agentID, sessionID int64, content string) error {
	_, err := s.db.Exec(`INSERT INTO session_logs(agent_id,session_id,content,created_at) VALUES(?,?,?,?)`,
		agentID, sessionID, content, now())
	return err
}

func (s *Store) SessionLogs(agentID int64, limit int) ([]*SessionLog, error) {
	rows, err := s.db.Query(`SELECT id,agent_id,COALESCE(session_id,0),content,created_at
		FROM session_logs WHERE agent_id=? ORDER BY id DESC LIMIT ?`, agentID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*SessionLog
	for rows.Next() {
		l := &SessionLog{}
		if err := rows.Scan(&l.ID, &l.AgentID, &l.SessionID, &l.Content, &l.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rows.Err()
}
