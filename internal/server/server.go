// Package server implements the JANUS Phase 0 server: agent creation,
// deployment, session management, the client API, and the admin web
// console.
package server

import (
	"context"
	"errors"
	"fmt"

	"github.com/nfinit/janus/internal/backend"
)

var (
	errNotApproved    = errors.New("agent charter is not approved")
	errSessionNotOpen = errors.New("session is not open")
)

// Server ties together the store, the client hub, and the web console.
type Server struct {
	store *Store
	hub   *Hub
	auth  *authManager
	tmpl  *templates
	meta  *modelMeta
}

func New(store *Store) (*Server, error) {
	tmpl, err := loadTemplates()
	if err != nil {
		return nil, err
	}
	return &Server{store: store, hub: NewHub(), auth: newAuthManager(), tmpl: tmpl, meta: newModelMeta()}, nil
}

// modelInfo looks up catalog metadata for an agent's configured model.
// Returns nil whenever the provider publishes no enrichment (bare
// OpenAI-compatible servers list ids without names); callers fall back to
// the raw slug.
func (s *Server) modelInfo(a *Agent) *ModelInfo {
	e, err := s.store.Endpoint(a.EndpointID)
	if err != nil {
		return nil
	}
	mi := s.meta.Lookup(e, a.Model)
	if mi == nil || mi.Name == "" {
		return nil
	}
	return mi
}

func (s *Server) Store() *Store { return s.store }

// Start launches background workers (the idle-session reaper).
func (s *Server) Start(ctx context.Context) {
	go s.reaper(ctx)
}

// backendFor builds the model backend for an agent from its endpoint.
func (s *Server) backendFor(a *Agent) (backend.Backend, error) {
	return s.backendForEndpoint(a.EndpointID)
}

func (s *Server) backendForEndpoint(endpointID int64) (backend.Backend, error) {
	e, err := s.store.Endpoint(endpointID)
	if err != nil {
		return nil, err
	}
	switch e.Kind {
	case "stub":
		return backend.NewStub(), nil
	case "openai", "":
		if e.BaseURL == "" {
			return nil, fmt.Errorf("endpoint %q has no base URL", e.Name)
		}
		return backend.NewOpenAI(e.BaseURL, e.APIKey), nil
	default:
		return nil, fmt.Errorf("unknown endpoint kind %q", e.Kind)
	}
}
