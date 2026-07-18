package server

import (
	"crypto/rand"
	"database/sql"
	_ "embed"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"os"
	"time"

	_ "modernc.org/sqlite"
)

//go:embed schema.sql
var schemaSQL string

// Store wraps the SQLite database.
type Store struct {
	db *sql.DB
}

func OpenStore(path string) (*Store, error) {
	db, err := sql.Open("sqlite", path+"?_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)&_pragma=journal_mode(WAL)")
	if err != nil {
		return nil, err
	}
	// SQLite handles one writer at a time; serialize access through a
	// single connection to keep things simple and lock-free at this scale.
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(schemaSQL); err != nil {
		db.Close()
		return nil, fmt.Errorf("apply schema: %w", err)
	}
	if err := migrate(db); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrate: %w", err)
	}
	restrictDBFiles(path)
	return &Store{db: db}, nil
}

// restrictDBFiles narrows the database files to owner-only access. The
// database holds endpoint API keys in plaintext and the admin password
// hash; on a multi-user host — which a JANUS host is by design, agents
// being ordinary local users — it must not be world-readable. Best-effort:
// a filesystem that refuses chmod should not stop the server.
func restrictDBFiles(path string) {
	for _, p := range []string{path, path + "-wal", path + "-shm"} {
		if err := os.Chmod(p, 0o600); err != nil && !os.IsNotExist(err) {
			log.Printf("store: could not restrict %s: %v", p, err)
		}
	}
}

// migrate applies additive schema changes to databases created by an
// earlier version. Each step is idempotent so startup is safe to repeat.
func migrate(db *sql.DB) error {
	if !hasColumn(db, "messages", "model") {
		if _, err := db.Exec(`ALTER TABLE messages ADD COLUMN model TEXT NOT NULL DEFAULT ''`); err != nil {
			return err
		}
	}
	// Wake-time context snapshot (0.0.0.4): what the agent actually saw when
	// the session opened. Sessions predating this have empty snapshots, which
	// the console reports honestly rather than reconstructing from current
	// state (documents move on; a reconstruction would be a quiet lie).
	for _, col := range []string{"system_context", "tools_json"} {
		if !hasColumn(db, "sessions", col) {
			if _, err := db.Exec(`ALTER TABLE sessions ADD COLUMN ` + col + ` TEXT NOT NULL DEFAULT ''`); err != nil {
				return err
			}
		}
	}
	return nil
}

func hasColumn(db *sql.DB, table, column string) bool {
	var n int
	err := db.QueryRow(
		`SELECT COUNT(*) FROM pragma_table_info(?) WHERE name=?`, table, column).Scan(&n)
	return err == nil && n > 0
}

func (s *Store) Close() error { return s.db.Close() }

func now() int64 { return time.Now().Unix() }

// NewToken returns a fresh random token (hex, 128 bits).
func NewToken() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		panic(err) // rand.Read does not fail on any supported platform
	}
	return hex.EncodeToString(b)
}

// ---- settings ----

func (s *Store) Setting(key string) (string, error) {
	var v string
	err := s.db.QueryRow(`SELECT value FROM settings WHERE key=?`, key).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return v, err
}

func (s *Store) SetSetting(key, value string) error {
	_, err := s.db.Exec(`INSERT INTO settings(key,value) VALUES(?,?)
		ON CONFLICT(key) DO UPDATE SET value=excluded.value`, key, value)
	return err
}

// ---- endpoints ----

type Endpoint struct {
	ID      int64
	Name    string
	Kind    string // "openai" | "stub"
	BaseURL string
	APIKey  string
}

func (s *Store) CreateEndpoint(e *Endpoint) error {
	res, err := s.db.Exec(`INSERT INTO endpoints(name,kind,base_url,api_key,created_at)
		VALUES(?,?,?,?,?)`, e.Name, e.Kind, e.BaseURL, e.APIKey, now())
	if err != nil {
		return err
	}
	e.ID, _ = res.LastInsertId()
	return nil
}

func (s *Store) UpdateEndpoint(e *Endpoint) error {
	_, err := s.db.Exec(`UPDATE endpoints SET name=?,kind=?,base_url=?,api_key=? WHERE id=?`,
		e.Name, e.Kind, e.BaseURL, e.APIKey, e.ID)
	return err
}

func (s *Store) DeleteEndpoint(id int64) error {
	var n int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM agents WHERE endpoint_id=?`, id).Scan(&n); err != nil {
		return err
	}
	if n > 0 {
		return fmt.Errorf("endpoint is in use by %d agent(s)", n)
	}
	_, err := s.db.Exec(`DELETE FROM endpoints WHERE id=?`, id)
	return err
}

func (s *Store) Endpoint(id int64) (*Endpoint, error) {
	e := &Endpoint{}
	err := s.db.QueryRow(`SELECT id,name,kind,base_url,api_key FROM endpoints WHERE id=?`, id).
		Scan(&e.ID, &e.Name, &e.Kind, &e.BaseURL, &e.APIKey)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("endpoint %d not found", id)
	}
	return e, err
}

func (s *Store) Endpoints() ([]*Endpoint, error) {
	rows, err := s.db.Query(`SELECT id,name,kind,base_url,api_key FROM endpoints ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Endpoint
	for rows.Next() {
		e := &Endpoint{}
		if err := rows.Scan(&e.ID, &e.Name, &e.Kind, &e.BaseURL, &e.APIKey); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// ---- agents ----

type Agent struct {
	ID            int64
	Identifier    string
	Name          string
	EndpointID    int64
	Model         string
	Primer        string
	Charter       string
	CharterStatus string // "draft" | "approved"
	ModelAware    bool
	CreatedAt     int64
	UpdatedAt     int64
}

func (s *Store) CreateAgent(a *Agent) error {
	t := now()
	res, err := s.db.Exec(`INSERT INTO agents
		(identifier,name,endpoint_id,model,primer,charter,charter_status,model_aware,created_at,updated_at)
		VALUES(?,?,?,?,?,?,?,?,?,?)`,
		a.Identifier, a.Name, a.EndpointID, a.Model, a.Primer, a.Charter,
		a.CharterStatus, boolInt(a.ModelAware), t, t)
	if err != nil {
		return err
	}
	a.ID, _ = res.LastInsertId()
	a.CreatedAt, a.UpdatedAt = t, t
	return nil
}

func (s *Store) UpdateAgent(a *Agent) error {
	a.UpdatedAt = now()
	_, err := s.db.Exec(`UPDATE agents SET identifier=?,name=?,endpoint_id=?,model=?,primer=?,
		charter=?,charter_status=?,model_aware=?,updated_at=? WHERE id=?`,
		a.Identifier, a.Name, a.EndpointID, a.Model, a.Primer,
		a.Charter, a.CharterStatus, boolInt(a.ModelAware), a.UpdatedAt, a.ID)
	return err
}

func (s *Store) DeleteAgent(id int64) error {
	_, err := s.db.Exec(`DELETE FROM agents WHERE id=?`, id)
	return err
}

func scanAgent(row interface{ Scan(...any) error }) (*Agent, error) {
	a := &Agent{}
	var aware int
	err := row.Scan(&a.ID, &a.Identifier, &a.Name, &a.EndpointID, &a.Model,
		&a.Primer, &a.Charter, &a.CharterStatus, &aware, &a.CreatedAt, &a.UpdatedAt)
	if err != nil {
		return nil, err
	}
	a.ModelAware = aware != 0
	return a, nil
}

const agentCols = `id,identifier,name,endpoint_id,model,primer,charter,charter_status,model_aware,created_at,updated_at`

func (s *Store) Agent(id int64) (*Agent, error) {
	a, err := scanAgent(s.db.QueryRow(`SELECT `+agentCols+` FROM agents WHERE id=?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("agent %d not found", id)
	}
	return a, err
}

func (s *Store) Agents() ([]*Agent, error) {
	rows, err := s.db.Query(`SELECT ` + agentCols + ` FROM agents ORDER BY identifier`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Agent
	for rows.Next() {
		a, err := scanAgent(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
