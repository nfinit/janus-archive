package server

// Session lifecycle: open → locked → closed, or open → deleted.
//
// A wake begins a session. A session ends in one of four ways:
//
//   - "End Chat and Commit": the admin closes it deliberately.
//   - session_end: the agent closes it deliberately — the same door from
//     the other side of the table.
//   - Idle timeout (1h): it locks and consolidates anyway — closure happens
//     even when the human wandered off.
//   - Delete: full expunge, available only while the session is open.
//     Denies the agent closure; the deliberate price of keeping some
//     things out of an agent's continuity.
//
// All but Delete run the consolidation turn: the structural moment where
// the agent learns the session is ending and decides what it meant.

import (
	"context"
	"encoding/json"
	"log"
	"sync"
	"time"

	"github.com/nfinit/janus/internal/backend"
)

// turnBudget bounds a background turn. Generous: a turn may chain many tool
// calls, each with its own terminal timeout. It exists so a hung backend can
// never pin a session busy forever.
const turnBudget = 10 * time.Minute

// turnGate tracks which sessions have a turn in flight. Turns run in
// background goroutines (the console must never block a browser request on a
// model call), so the gate is what serializes them: one turn per session,
// and admin actions that would race a live turn are refused while it runs.
// In-memory by design — a restart clears it, and the persisted convo means a
// killed turn loses nothing but its answer.
type turnGate struct {
	mu     sync.Mutex
	active map[int64]bool
}

func newTurnGate() *turnGate { return &turnGate{active: make(map[int64]bool)} }

func (g *turnGate) tryStart(id int64) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.active[id] {
		return false
	}
	g.active[id] = true
	return true
}

func (g *turnGate) finish(id int64) {
	g.mu.Lock()
	defer g.mu.Unlock()
	delete(g.active, id)
}

func (g *turnGate) running(id int64) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.active[id]
}

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

// SendMessage delivers an admin message into an open session and launches
// the agent's turn in the background, returning as soon as the message is
// persisted. The console polls the transcript while the turn runs; errors
// from the turn itself land in the transcript, not here.
func (s *Server) SendMessage(sessionID int64, text string) error {
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
	if !s.gate.tryStart(sess.ID) {
		return errAgentBusy
	}

	if _, err := s.store.AppendMessage(sess.ID, RoleAdmin, text); err != nil {
		s.gate.finish(sess.ID)
		return err
	}
	if err := s.store.AppendConvo(sess.ID, backend.Message{
		Role: backend.RoleUser, Content: text,
	}); err != nil {
		s.gate.finish(sess.ID)
		return err
	}
	s.store.TouchSession(sess.ID)

	go func() {
		defer s.gate.finish(sess.ID)
		ctx, cancel := context.WithTimeout(context.Background(), turnBudget)
		defer cancel()
		endRequested, err := s.runTurn(ctx, a, sess)
		if err != nil {
			log.Printf("session %d: turn failed: %v", sess.ID, err)
			return
		}
		if endRequested {
			// The agent chose to end the session; give it its consolidation
			// turn now, exactly as if the admin had committed the chat.
			if err := s.Consolidate(ctx, sess.ID); err != nil {
				log.Printf("session %d: agent-requested consolidation: %v", sess.ID, err)
			}
		}
	}()
	return nil
}

// ConsolidateAsync is the console's path to "End Chat and Commit": it locks
// out overlapping turns via the gate and runs the consolidation in the
// background, so the admin's browser is never held through the closing turn.
func (s *Server) ConsolidateAsync(sessionID int64) error {
	sess, err := s.store.Session(sessionID)
	if err != nil {
		return err
	}
	if sess.Status != SessionOpen {
		return errSessionNotOpen
	}
	if !s.gate.tryStart(sess.ID) {
		return errAgentBusy
	}
	go func() {
		defer s.gate.finish(sess.ID)
		ctx, cancel := context.WithTimeout(context.Background(), turnBudget)
		defer cancel()
		if err := s.Consolidate(ctx, sess.ID); err != nil {
			log.Printf("session %d: consolidation: %v", sess.ID, err)
		}
	}()
	return nil
}

// TurnRunning reports whether a session has a turn in flight.
func (s *Server) TurnRunning(sessionID int64) bool { return s.gate.running(sessionID) }

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

	_, turnErr := s.runTurn(ctx, a, sess)
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

// DeleteSession expunges an open session entirely. Refused while a turn is
// in flight: expunging rows out from under a running turn would corrupt it.
func (s *Server) DeleteSession(sessionID int64) error {
	if s.gate.running(sessionID) {
		return errAgentBusy
	}
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
				// A session with a turn in flight is not idle, whatever its
				// last_activity says; leave it to finish.
				if !s.gate.tryStart(sess.ID) {
					continue
				}
				log.Printf("session %d: idle for %s, consolidating", sess.ID, sessionIdleTimeout)
				if err := s.Consolidate(ctx, sess.ID); err != nil {
					log.Printf("session %d: idle consolidation: %v", sess.ID, err)
				}
				s.gate.finish(sess.ID)
			}
		}
	}
}
