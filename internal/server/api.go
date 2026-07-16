package server

// The client-facing API: two endpoints, deliberately primitive.
//
// Everything here honors the HTTP/1.0 floor (see internal/protocol): bare
// HTTP/1.0 accepted, always Content-Length (Go's net/http buffers our
// []byte writes and sets it for us), no chunking, no redirects, no cookies,
// no required headers beyond Content-Type. A client is nothing more than
// socket/connect/write/read-until-close.

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"time"

	"github.com/nfinit/janus/internal/protocol"
)

const (
	maxClientBody  = 4 << 20 // generous cap on client request bodies
	maxPollWait    = 30      // seconds; ceiling on client-requested hold
	pollHardBudget = 35 * time.Second
)

func writeJSON(w http.ResponseWriter, status int, v any) {
	body, err := json.Marshal(v)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	// Explicit Content-Length keeps us off chunked encoding for the
	// benefit of very old clients.
	w.Header().Set("Content-Length", itoa(len(body)))
	w.WriteHeader(status)
	w.Write(body)
}

func apiError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, protocol.ErrorResponse{Error: msg})
}

func decodeClientBody(w http.ResponseWriter, r *http.Request, v any) bool {
	defer r.Body.Close()
	data, err := io.ReadAll(io.LimitReader(r.Body, maxClientBody))
	if err != nil {
		apiError(w, http.StatusBadRequest, "could not read request body")
		return false
	}
	if err := json.Unmarshal(data, v); err != nil {
		apiError(w, http.StatusBadRequest, "request body was not valid JSON")
		return false
	}
	return true
}

// handlePoll implements POST /api/poll — the client's work loop.
func (s *Server) handlePoll(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		apiError(w, http.StatusMethodNotAllowed, "POST required")
		return
	}
	var req protocol.PollRequest
	if !decodeClientBody(w, r, &req) {
		return
	}
	binding, err := s.store.BindingByToken(req.Token)
	if err != nil {
		apiError(w, http.StatusUnauthorized, "unknown token")
		return
	}
	// The poll itself is the liveness signal (Q2: last_seen).
	s.store.TouchBinding(binding.ID, req.Hostname, req.Version)

	wait := req.Wait
	if wait < 0 {
		wait = 0
	}
	if wait > maxPollWait {
		wait = maxPollWait
	}

	ctx, cancel := context.WithTimeout(r.Context(), pollHardBudget)
	defer cancel()

	work := s.hub.Collect(ctx, req.Token, time.Duration(wait)*time.Second)
	writeJSON(w, http.StatusOK, protocol.PollResponse{Work: work})
}

// handleResult implements POST /api/result.
func (s *Server) handleResult(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		apiError(w, http.StatusMethodNotAllowed, "POST required")
		return
	}
	var req protocol.ResultRequest
	if !decodeClientBody(w, r, &req) {
		return
	}
	if _, err := s.store.BindingByToken(req.Token); err != nil {
		apiError(w, http.StatusUnauthorized, "unknown token")
		return
	}
	if err := s.hub.Deliver(req.WorkID, req.Result); err != nil {
		apiError(w, http.StatusNotFound, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, protocol.ResultResponse{OK: true})
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}
