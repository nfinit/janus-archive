package server

// Session lifecycle: open → locked → closed, or open → deleted.
//
// A wake begins a session. A session ends in one of three ways:
//
//   - "End Chat and Commit": the admin closes it deliberately.
//   - Idle timeout (1h): it locks and consolidates anyway — closure happens
//     even when the human wandered off.
//   - Delete: full expunge, available only while the session is open.
//     Denies the agent closure; the deliberate price of keeping some
//     things out of an agent's continuity.
//
// Paths one and two both run the consolidation turn: the structural moment
// where the agent learns the session is ending and decides what it meant.

import (
	"context"
	"encoding/json"
	"log"
	"time"

	"github.com/nfinit/janus/internal/backend"
)

// Wake opens a session with an agent.
func (s *Server) Wake(agentID int64) (*Session, error) {
	a, err := s.store.Agent(agentID)
	if err != nil {
		return nil, err
	}
	if a.CharterStatus != "approved" {
		return nil, errNotApproved
	}
	sess, err := s.store.CreateSession(a.ID)
	if err != nil {
		return nil, err
	}

	// Snapshot what the agent will see, exactly as assembled at this moment.
	// Diagnostics only — turns recompute context live — so failure to
	// snapshot is logged, not fatal. (Documents written mid-session enter
	// later turns; the snapshot is the wake-time truth, and is labeled so.)
	if sysCtx, err := s.systemContext(a); err == nil {
		toolsJSON := ""
		if tools, err := s.tools(a); err == nil {
			if buf, err := json.Marshal(tools); err == nil {
				toolsJSON = string(buf)
			}
		}
		if err := s.store.SetSessionContext(sess.ID, sysCtx, toolsJSON); err != nil {
			log.Printf("session %d: context snapshot not saved: %v", sess.ID, err)
		}
	} else {
		log.Printf("session %d: context snapshot not assembled: %v", sess.ID, err)
	}

	return sess, nil
}

// SendMessage delivers an admin message into an open session and runs the
// agent's turn synchronously.
func (s *Server) SendMessage(ctx context.Context, sessionID int64, text string) error {
	sess, err := s.store.Session(sessionID)
	if err != nil {
		return err
	}
	if sess.Status != SessionOpen {
		return errSessionNotOpen
	}
	a, err := s.store.Agent(sess.AgentID)
	if err != nil {
		return err
	}

	if _, err := s.store.AppendMessage(sess.ID, RoleAdmin, text); err != nil {
		return err
	}
	if err := s.store.AppendConvo(sess.ID, backend.Message{
		Role: backend.RoleUser, Content: text,
	}); err != nil {
		return err
	}
	s.store.TouchSession(sess.ID)

	return s.runTurn(ctx, a, sess)
}

// Consolidate locks a session and runs the closing turn, then closes it.
// Used by both the "End Chat and Commit" control and the idle timeout.
func (s *Server) Consolidate(ctx context.Context, sessionID int64) error {
	sess, err := s.store.Session(sessionID)
	if err != nil {
		return err
	}
	if sess.Status != SessionOpen {
		return errSessionNotOpen
	}
	a, err := s.store.Agent(sess.AgentID)
	if err != nil {
		return err
	}

	// Lock first: the delete window closes here, and no further admin
	// messages may enter.
	if err := s.store.SetSessionStatus(sess.ID, SessionLocked); err != nil {
		return err
	}
	sess.Status = SessionLocked

	if _, err := s.store.AppendMessage(sess.ID, RoleSystem, "Session concluding — consolidation turn."); err != nil {
		return err
	}
	if err := s.store.AppendConvo(sess.ID, backend.Message{
		Role: backend.RoleUser, Content: consolidationPrompt,
	}); err != nil {
		return err
	}

	turnErr := s.runTurn(ctx, a, sess)
	if turnErr != nil {
		// The session still closes: a failed consolidation must not leave
		// a session wedged open forever. The failure is recorded in the
		// transcript by runTurn.
		log.Printf("session %d: consolidation turn failed: %v", sess.ID, turnErr)
	}

	if err := s.store.SetSessionStatus(sess.ID, SessionClosed); err != nil {
		return err
	}
	return turnErr
}

// DeleteSession expunges an open session entirely.
func (s *Server) DeleteSession(sessionID int64) error {
	return s.store.ExpungeSession(sessionID)
}

// reaper locks and consolidates sessions that have gone quiet. This is what
// gives an abandoned session its closure: the agent is not left hanging
// mid-thought forever merely because the administrator walked away.
func (s *Server) reaper(ctx context.Context) {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			cutoff := time.Now().Add(-sessionIdleTimeout).Unix()
			idle, err := s.store.IdleOpenSessions(cutoff)
			if err != nil {
				log.Printf("reaper: %v", err)
				continue
			}
			for _, sess := range idle {
				log.Printf("session %d: idle for %s, consolidating", sess.ID, sessionIdleTimeout)
				if err := s.Consolidate(ctx, sess.ID); err != nil {
					log.Printf("session %d: idle consolidation: %v", sess.ID, err)
				}
			}
		}
	}
}
