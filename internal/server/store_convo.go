package server

// Persistence for the normalized model conversation. This is what the
// model sees; `messages` is what the admin reads. Keeping them separate
// means an in-flight session survives a server restart with full fidelity
// (tool call IDs and all), while the transcript stays a clean human record.

import (
	"encoding/json"

	"github.com/nfinit/janus/internal/backend"
)

func (s *Store) AppendConvo(sessionID int64, m backend.Message) error {
	payload, err := json.Marshal(m)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(`INSERT INTO convo(session_id,payload) VALUES(?,?)`, sessionID, string(payload))
	return err
}

func (s *Store) Convo(sessionID int64) ([]backend.Message, error) {
	rows, err := s.db.Query(`SELECT payload FROM convo WHERE session_id=? ORDER BY id`, sessionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []backend.Message
	for rows.Next() {
		var payload string
		if err := rows.Scan(&payload); err != nil {
			return nil, err
		}
		var m backend.Message
		if err := json.Unmarshal([]byte(payload), &m); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}
