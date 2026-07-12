package server

// The hub is the rendezvous between the agent's conversation loop (which
// wants a command run on a host) and the client's poll loop (which is
// waiting to be told what to do).
//
// The client always dials us; we never dial the client. A held-open poll
// is just a goroutine blocked on a channel — the Go dividend that makes
// long-polling free.

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/nfinit/janus/internal/protocol"
)

var ErrClientOffline = errors.New("no client is polling for this agent")

type pendingWork struct {
	work   protocol.Work
	result chan protocol.TerminalResult
}

type Hub struct {
	mu sync.Mutex
	// queued work per binding token, awaiting a poll to collect it
	queued map[string][]*pendingWork
	// results awaited, keyed by work ID
	awaiting map[string]*pendingWork
	// a waiting poller per token, signalled when work is queued
	waiters map[string][]chan struct{}
}

func NewHub() *Hub {
	return &Hub{
		queued:   make(map[string][]*pendingWork),
		awaiting: make(map[string]*pendingWork),
		waiters:  make(map[string][]chan struct{}),
	}
}

// Dispatch queues a work item for the client holding token and blocks until
// the client reports a result, the context is cancelled, or timeout elapses.
func (h *Hub) Dispatch(ctx context.Context, token string, work protocol.Work, timeout time.Duration) (*protocol.TerminalResult, error) {
	pw := &pendingWork{work: work, result: make(chan protocol.TerminalResult, 1)}

	h.mu.Lock()
	h.queued[token] = append(h.queued[token], pw)
	h.awaiting[work.ID] = pw
	// Wake any parked poller for this token.
	for _, ch := range h.waiters[token] {
		close(ch)
	}
	h.waiters[token] = nil
	h.mu.Unlock()

	defer func() {
		h.mu.Lock()
		delete(h.awaiting, work.ID)
		h.mu.Unlock()
	}()

	select {
	case res := <-pw.result:
		return &res, nil
	case <-time.After(timeout):
		return nil, errors.New("client did not return a result in time")
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// Collect is called by the poll handler. It returns a work item for the
// token, waiting up to `wait` for one to appear. A nil return with no error
// means "nothing to do" — the client simply polls again.
//
// wait=0 answers immediately: the same protocol, degraded to plain polling
// for clients whose stacks dislike long-held connections.
func (h *Hub) Collect(ctx context.Context, token string, wait time.Duration) *protocol.Work {
	if w := h.take(token); w != nil {
		return w
	}
	if wait <= 0 {
		return nil
	}

	ch := make(chan struct{})
	h.mu.Lock()
	h.waiters[token] = append(h.waiters[token], ch)
	h.mu.Unlock()

	timer := time.NewTimer(wait)
	defer timer.Stop()

	select {
	case <-ch:
		return h.take(token)
	case <-timer.C:
		h.removeWaiter(token, ch)
		return h.take(token) // race guard: work may have arrived as we timed out
	case <-ctx.Done():
		h.removeWaiter(token, ch)
		return nil
	}
}

func (h *Hub) take(token string) *protocol.Work {
	h.mu.Lock()
	defer h.mu.Unlock()
	q := h.queued[token]
	if len(q) == 0 {
		return nil
	}
	pw := q[0]
	h.queued[token] = q[1:]
	w := pw.work
	return &w
}

func (h *Hub) removeWaiter(token string, ch chan struct{}) {
	h.mu.Lock()
	defer h.mu.Unlock()
	ws := h.waiters[token]
	for i, c := range ws {
		if c == ch {
			h.waiters[token] = append(ws[:i], ws[i+1:]...)
			return
		}
	}
}

// Deliver hands a client-reported result to whoever is awaiting it.
func (h *Hub) Deliver(workID string, res protocol.TerminalResult) error {
	h.mu.Lock()
	pw, ok := h.awaiting[workID]
	h.mu.Unlock()
	if !ok {
		return errors.New("unknown or expired work id")
	}
	select {
	case pw.result <- res:
	default: // already answered; ignore duplicate
	}
	return nil
}
