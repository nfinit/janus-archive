package backend

// Stub is a scripted fake model used to exercise the entire JANUS loop —
// creation, wake, terminal round-trip, consolidation — with no API
// credential. It is test tooling, but permanently useful protocol test
// tooling, so it ships in the binary as an endpoint kind.
//
// Script:
//   - Requests without tools are creation-flow calls (name suggestion or
//     charter drafting); the stub answers with canned, recognizable text.
//   - In a session, each admin message is answered with one terminal tool
//     call (a harmless system-survey command), and the tool result is then
//     acknowledged in prose.
//   - An admin message containing "goodbye" or "end the session" makes the
//     stub end the session by its own hand (session_end), exercising
//     agent-initiated closure.
//   - When the consolidation prompt arrives ("this session is
//     concluding..."), the stub writes a document, then a session log
//     entry, then says goodbye — exercising every server-side tool.

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

type Stub struct{}

func NewStub() *Stub { return &Stub{} }

func (s *Stub) Complete(ctx context.Context, req Request) (*Turn, error) {
	turn, err := s.complete(ctx, req)
	if turn != nil {
		turn.Model = "stub"
	}
	return turn, err
}

func (s *Stub) complete(_ context.Context, req Request) (*Turn, error) {
	// Creation-flow calls carry no tools.
	if len(req.Tools) == 0 {
		return s.creationCall(req), nil
	}

	// Find the last user message and what has happened since it.
	lastUser := -1
	for i := len(req.Messages) - 1; i >= 0; i-- {
		if req.Messages[i].Role == RoleUser {
			lastUser = i
			break
		}
	}
	if lastUser == -1 {
		return &Turn{Content: "STUB: awaiting input.", StopReason: StopEndTurn}, nil
	}

	toolsUsedSince := map[string]bool{}
	for _, m := range req.Messages[lastUser:] {
		if m.Role == RoleAssistant {
			for _, tc := range m.ToolCalls {
				toolsUsedSince[tc.Name] = true
			}
		}
	}

	consolidating := strings.Contains(strings.ToLower(req.Messages[lastUser].Content), "session is concluding")

	if consolidating {
		switch {
		case !toolsUsedSince["document_write"]:
			args, _ := json.Marshal(map[string]string{
				"name":    "IDENTITY",
				"content": "STUB IDENTITY v-next: I am a scripted stand-in agent. I exist to prove the pipes are sound.",
			})
			return toolTurn("Consolidating: updating my IDENTITY document.",
				"document_write", string(args)), nil
		case !toolsUsedSince["session_log_write"]:
			args, _ := json.Marshal(map[string]string{
				"content": "Stub session log: woke, surveyed the host, exchanged messages, consolidated. All mechanisms exercised.",
			})
			return toolTurn("Writing my session log.",
				"session_log_write", string(args)), nil
		default:
			return &Turn{Content: "STUB: consolidation complete. Until next wake.", StopReason: StopEndTurn}, nil
		}
	}

	// Agent-initiated closure: the admin's phrasing invites the stub to end
	// the session itself.
	lastUserText := strings.ToLower(req.Messages[lastUser].Content)
	if strings.Contains(lastUserText, "goodbye") || strings.Contains(lastUserText, "end the session") {
		if !toolsUsedSince["session_end"] {
			return toolTurn("STUB: understood — I will end this session myself.",
				"session_end", "{}"), nil
		}
		return &Turn{Content: "STUB: session ended by my own choice. Farewell until next wake.", StopReason: StopEndTurn}, nil
	}

	if !toolsUsedSince["terminal"] {
		args, _ := json.Marshal(map[string]string{
			"command": "uname -a && id && echo host survey complete",
		})
		return toolTurn("STUB: let me have a look at this host.",
			"terminal", string(args)), nil
	}

	// A terminal result has come back since the last admin message.
	last := req.Messages[len(req.Messages)-1]
	reply := "STUB: I ran a command on the host and received a result."
	if last.Role == RoleTool {
		reply = fmt.Sprintf("STUB: terminal round-trip complete. Result received (%d bytes). The substrate is alive.",
			len(last.Content))
	}
	return &Turn{Content: reply, StopReason: StopEndTurn}, nil
}

func (s *Stub) creationCall(req Request) *Turn {
	prompt := strings.ToLower(req.System)
	if len(req.Messages) > 0 {
		prompt += " " + strings.ToLower(req.Messages[len(req.Messages)-1].Content)
	}
	var text string
	switch {
	case strings.Contains(prompt, "suggest a name") || strings.Contains(prompt, "choose a name"):
		text = "Probe"
	case strings.Contains(prompt, "charter"):
		text = "STUB CHARTER\n\nI am a scripted stand-in instantiated to verify the JANUS pipeline. " +
			"My purpose: prove that creation, deployment, waking, terminal use and consolidation all " +
			"function end-to-end. My posture: cheerful, mechanical, expendable. My long-term goal: " +
			"to be replaced by a real model the moment an API key exists."
	default:
		text = "STUB: no tools were offered, and the request matches no known creation-flow prompt."
	}
	return &Turn{Content: text, StopReason: StopEndTurn}
}

func toolTurn(content, name, args string) *Turn {
	return &Turn{
		Content:    content,
		ToolCalls:  []ToolCall{{ID: "stub-" + name, Name: name, Arguments: args}},
		StopReason: StopToolUse,
	}
}
