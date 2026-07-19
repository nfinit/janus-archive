package server

// Agent creation: admin inputs → (optional name suggestion) → charter
// drafting → admin approval. Distinct from deployment (binding to a
// client). "Creation" and "deployment" are the two faces the overloaded
// word "instantiation" was split into.

import (
	"context"
	"strings"

	"github.com/nfinit/janus/internal/backend"
)

// SuggestName asks the agent's model to choose a name for itself given the
// primer. Optional: the admin may name the agent directly instead.
//
// guidance is namer-only context: it reaches this one call and is never
// stored, so it can carry facts the primer deliberately withholds (e.g. the
// hardware the agent is meant to discover on its own) without those facts
// ever entering the agent's context. Names always arrive from outside;
// what matters for discovery is what enters the agent's wakes.
func (s *Server) SuggestName(ctx context.Context, endpointID int64, model, primer string, modelAware bool, guidance string) (string, error) {
	be, err := s.backendForEndpoint(endpointID)
	if err != nil {
		return "", err
	}
	var sys strings.Builder
	sys.WriteString("You are being deployed as an agent through JANUS. Given the deployment primer below, suggest a name for yourself — the name you will go by. Reply with only the name, nothing else.\n\n")
	if modelAware {
		sys.WriteString("(You are running on the model \"" + model + "\".)\n\n")
	}
	sys.WriteString("# Deployment primer\n\n")
	sys.WriteString(strings.TrimSpace(primer))
	if g := strings.TrimSpace(guidance); g != "" {
		sys.WriteString("\n\n# Additional context from the administrator\n\nThis is offered to inform the choice of name only; the agent will not see it:\n\n" + g)
	}

	// Generous despite the one-word answer: reasoning models spend
	// completion tokens thinking before any content appears, and a tight
	// cap starves them into an empty reply (HTTP 200, no name). firstLine
	// keeps the result honest regardless of how much arrives.
	turn, err := be.Complete(ctx, backend.Request{
		Model:     model,
		System:    sys.String(),
		Messages:  []backend.Message{{Role: backend.RoleUser, Content: "Suggest a name for yourself."}},
		MaxTokens: 2048,
	})
	if err != nil {
		return "", err
	}
	return firstLine(turn.Content), nil
}

// DraftCharter synthesizes the admin's inputs into a charter proposal. The
// admin reviews the draft and either approves it, requests changes, or
// rejects it and revises the inputs.
func (s *Server) DraftCharter(ctx context.Context, a *Agent, guidance string) (string, error) {
	be, err := s.backendFor(a)
	if err != nil {
		return "", err
	}

	var sys strings.Builder
	sys.WriteString("You are helping instantiate an agent deployed through JANUS. ")
	sys.WriteString("From the inputs below, draft this agent's CHARTER: a few paragraphs defining its starting identity, purpose, posture, and any long-term interests or goals for its time on its host system(s). ")
	sys.WriteString("The charter is written in the agent's own voice — it is who this agent begins as. Write only the charter text.\n\n")
	if a.ModelAware {
		sys.WriteString("The agent is running on the model \"" + a.Model + "\".\n\n")
	}
	sys.WriteString("# Agent name\n\n" + a.Name + "\n\n")
	sys.WriteString("# Deployment primer\n\n" + strings.TrimSpace(a.Primer) + "\n\n")

	userMsg := "Draft the charter."
	if strings.TrimSpace(guidance) != "" {
		userMsg = "Draft the charter. The administrator adds this steering: " + strings.TrimSpace(guidance)
	}

	// Budget covers reasoning tokens plus the charter itself; reasoning
	// models deliberate before writing, and that deliberation bills against
	// max_tokens.
	turn, err := be.Complete(ctx, backend.Request{
		Model:     a.Model,
		System:    sys.String(),
		Messages:  []backend.Message{{Role: backend.RoleUser, Content: userMsg}},
		MaxTokens: 8192,
	})
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(turn.Content), nil
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	return strings.Trim(strings.TrimSpace(s), `"'.`)
}
