package server

// The agent conversation loop: context assembly, tool dispatch, and the
// consolidation turn that gives a session its closure.

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/nfinit/janus/internal/backend"
	"github.com/nfinit/janus/internal/protocol"
)

const (
	// maxToolIterations bounds one agent turn: how many tool calls the
	// agent may chain before we force it to speak. Guards against loops.
	maxToolIterations = 24
	// clientWorkTimeout is how long the server waits for a client to
	// return a terminal result before giving up on that work item.
	clientWorkTimeout = 10 * time.Minute
	// sessionIdleTimeout: after this much silence an open session is
	// locked and consolidated. Phase 0 hardcodes one hour.
	sessionIdleTimeout = time.Hour
)

// consolidationPrompt is the closing turn — the structural moment where an
// agent learns a session is ending and decides what it meant. Everything
// not written down here is lost when the context evaporates; the transcript
// is kept as a record, but it is not the agent's memory.
const consolidationPrompt = `[SYSTEM] This session is concluding.

This is your consolidation turn: your last chance to carry anything from this session forward. Nothing else from this conversation will be in your context when you next wake — only your documents and your session logs.

Consider:
  - Should any of your documents (IDENTITY, PRACTICES, or any you choose to keep) be updated in light of what happened here?
  - What is worth recording about this session for your future self?

Use document_write for anything that belongs to your standing sense of who you are and how you work, and session_log_write to record an account of this session. Then say whatever you wish to close with.`

// systemContext assembles the agent's context at wake. Everything durable
// about the agent lives here: no transcript survives between sessions.
func (s *Server) systemContext(a *Agent) (string, error) {
	var b strings.Builder

	fmt.Fprintf(&b, "You are %s, an agent deployed through JANUS.\n\n", a.Name)

	if a.ModelAware {
		// When the provider publishes catalog metadata, use the model's
		// display name and published knowledge cutoff: substrate facts, in
		// JANUS's own voice. The provider's *description* is deliberately
		// omitted — it is unfenced third-party marketing copy, and an agent
		// that wants the grittier details can be given them in conversation
		// and commit what it finds interesting to its own documents.
		if mi := s.modelInfo(a); mi != nil {
			fmt.Fprintf(&b, "You are running on %s (model identifier `%s`), reached through the endpoint configured by your administrator.\n\n", mi.Name, mi.ID)
			if mi.KnowledgeCutoff != "" {
				fmt.Fprintf(&b, "The model's training data has a published knowledge cutoff of %s. Events after that date are outside its training; for anything current, consult your host or your administrator rather than recall.\n\n", mi.KnowledgeCutoff)
			}
		} else {
			fmt.Fprintf(&b, "You are running on the model %q, reached through the endpoint configured by your administrator.\n\n", a.Model)
		}
	}

	b.WriteString("# Your charter\n\n")
	b.WriteString(strings.TrimSpace(a.Charter))
	b.WriteString("\n\n")

	b.WriteString("# Deployment primer\n\n")
	b.WriteString("This is what your administrator recorded about your deployment:\n\n")
	b.WriteString(strings.TrimSpace(a.Primer))
	b.WriteString("\n\n")

	docs, err := s.store.Documents(a.ID)
	if err != nil {
		return "", err
	}
	if len(docs) > 0 {
		b.WriteString("# Your documents\n\n")
		b.WriteString("These are yours: you wrote them, and you may revise them with document_write. They are versioned; nothing is lost.\n\n")
		for _, d := range docs {
			fmt.Fprintf(&b, "## %s (v%d)\n\n%s\n\n", d.Name, d.CurrentVersion, d.Content)
		}
	} else {
		b.WriteString("# Your documents\n\nYou have not yet written any documents. IDENTITY (who you are) and PRACTICES (how you work) are the customary starting points, but the choice of what to keep is yours.\n\n")
	}

	logs, err := s.store.SessionLogs(a.ID, 1)
	if err != nil {
		return "", err
	}
	if len(logs) > 0 {
		b.WriteString("# Your last session log\n\n")
		fmt.Fprintf(&b, "Written by you on %s:\n\n%s\n\n",
			time.Unix(logs[0].CreatedAt, 0).Format("2006-01-02 15:04"), logs[0].Content)
	}

	b.WriteString("# Your host\n\n")
	binding, err := s.store.AgentBinding(a.ID)
	if err != nil {
		return "", err
	}
	switch {
	case binding == nil:
		b.WriteString("You are not currently deployed to any host: you have no terminal.\n\n")
	case binding.LastSeen == 0:
		b.WriteString("You are bound to a host, but its client has never connected. Your terminal is unavailable.\n\n")
	default:
		host := binding.LastHostname
		if host == "" {
			host = "(unnamed host)"
		}
		online := time.Since(time.Unix(binding.LastSeen, 0)) < 2*time.Minute
		state := "offline (its client has stopped polling)"
		if online {
			state = "online"
		}
		fmt.Fprintf(&b, "You are deployed to %s, currently %s.\n\n", host, state)
		b.WriteString("Your terminal there is stateless: every command runs in a fresh shell starting in your home directory. Nothing persists between commands — no working directory, no environment, no background processes. Chain what must be chained in a single command.\n\n")
		b.WriteString("You act on that host as an ordinary local user account, with exactly the permissions your administrator granted it. You were onboarded to this machine as a person would have been.\n\n")
	}

	b.WriteString("# This session\n\n")
	b.WriteString("You are awake because your administrator opened a session with you. When the session ends — by their hand or after an hour's silence — you will be given a consolidation turn: a chance to update your documents and write a session log before your context is discarded. Your memory is what you choose to write down.\n")

	return b.String(), nil
}

// tools returns the tool menu offered to the agent. The terminal tool is
// offered only when a host is actually bound: never advertise a capability
// the agent does not have.
func (s *Server) tools(a *Agent) ([]backend.Tool, error) {
	tools := []backend.Tool{
		{
			Name:        "document_write",
			Description: "Create or revise one of your documents (for example IDENTITY or PRACTICES). Documents persist across sessions and are versioned — writing a new version never destroys an old one. Pass the document's full new content.",
			Parameters: json.RawMessage(`{
				"type":"object",
				"properties":{
					"name":{"type":"string","description":"Document name, e.g. IDENTITY or PRACTICES. Uppercase by convention."},
					"content":{"type":"string","description":"The document's complete new content."}
				},
				"required":["name","content"]
			}`),
		},
		{
			Name:        "document_read",
			Description: "Read one of your documents. Your current documents are already in your context at wake; use this only to re-read one after revising it.",
			Parameters: json.RawMessage(`{
				"type":"object",
				"properties":{"name":{"type":"string","description":"Document name."}},
				"required":["name"]
			}`),
		},
		{
			Name:        "session_log_write",
			Description: "Append an entry to your session log: your own account of a session, written for your future self. Your most recent entry is included in your context when you next wake.",
			Parameters: json.RawMessage(`{
				"type":"object",
				"properties":{"content":{"type":"string","description":"The log entry."}},
				"required":["content"]
			}`),
		},
		{
			Name:        "session_end",
			Description: "End this session by your own choice. When your current turn finishes, the session concludes and you receive your consolidation turn — a chance to update your documents and write your session log before your context is discarded. Use it when the work is done or the conversation has reached its natural close. This takes effect without further confirmation.",
			Parameters:  json.RawMessage(`{"type":"object","properties":{}}`),
		},
	}

	binding, err := s.store.AgentBinding(a.ID)
	if err != nil {
		return nil, err
	}
	if binding != nil {
		tools = append(tools, backend.Tool{
			Name:        "terminal",
			Description: "Run a single shell command on your host, in a fresh shell starting in your home directory, and receive its output. Stateless: nothing persists between calls. You receive stdout, stderr and the exit code; long output is truncated head-and-tail, and truncation is always reported to you.",
			Parameters: json.RawMessage(`{
				"type":"object",
				"properties":{
					"command":{"type":"string","description":"The command to run, interpreted by /bin/sh."},
					"timeout_seconds":{"type":"integer","description":"Optional execution timeout in seconds. Defaults to 60; the client enforces its own maximum."}
				},
				"required":["command"]
			}`),
		})
	}
	return tools, nil
}

// runTurn drives the agent until it stops calling tools. Every model output
// is treated as untrusted: malformed arguments and unknown tool names are
// answered with structured errors the model can recover from, never crashes.
//
// The caller has already appended the prompting user message to the
// persisted conversation.
// It reports endRequested=true when the agent called session_end during the
// turn: the session should be consolidated once the turn is over. The agent
// keeps the remainder of its turn — ending is something it does, not
// something that happens to it mid-sentence.
func (s *Server) runTurn(ctx context.Context, a *Agent, sess *Session) (endRequested bool, err error) {
	be, err := s.backendFor(a)
	if err != nil {
		return false, err
	}
	sysCtx, err := s.systemContext(a)
	if err != nil {
		return false, err
	}
	tools, err := s.tools(a)
	if err != nil {
		return false, err
	}

	for i := 0; i < maxToolIterations; i++ {
		convo, err := s.store.Convo(sess.ID)
		if err != nil {
			return endRequested, err
		}

		turn, err := be.Complete(ctx, backend.Request{
			Model:    a.Model,
			System:   sysCtx,
			Messages: convo,
			Tools:    tools,
		})
		if err != nil {
			s.store.AppendMessage(sess.ID, RoleSystem, "Backend error: "+err.Error())
			return endRequested, err
		}

		if turn.Model != "" {
			log.Printf("session %d: agent turn served by %s", sess.ID, turn.Model)
		}
		if turn.Content != "" {
			s.store.AppendMessageModel(sess.ID, RoleAgent, turn.Content, turn.Model)
		}
		if len(turn.ToolCalls) == 0 {
			if turn.Content == "" {
				s.store.AppendMessage(sess.ID, RoleSystem, "The model returned an empty turn.")
			}
			// Record the closing assistant message so the next turn sees it.
			s.store.AppendConvo(sess.ID, backend.Message{
				Role:    backend.RoleAssistant,
				Content: turn.Content,
			})
			s.store.TouchSession(sess.ID)
			return endRequested, nil
		}

		if err := s.store.AppendConvo(sess.ID, backend.Message{
			Role:      backend.RoleAssistant,
			Content:   turn.Content,
			ToolCalls: turn.ToolCalls,
		}); err != nil {
			return endRequested, err
		}

		for _, tc := range turn.ToolCalls {
			var result string
			if tc.Name == "session_end" {
				// Turn-control, not an action: handled here, not execTool.
				result, endRequested = s.acceptSessionEnd(a, sess, endRequested)
			} else {
				result = s.execTool(ctx, a, sess, tc)
			}
			if err := s.store.AppendConvo(sess.ID, backend.Message{
				Role:       backend.RoleTool,
				ToolCallID: tc.ID,
				Content:    result,
			}); err != nil {
				return endRequested, err
			}
		}
		s.store.TouchSession(sess.ID)
	}

	s.store.AppendMessage(sess.ID, RoleSystem,
		fmt.Sprintf("Tool-call limit (%d) reached; the turn was cut short.", maxToolIterations))
	return endRequested, nil
}

// acceptSessionEnd answers a session_end call. Ending is honored without
// confirmation — the agent's choice to conclude is respected the first time —
// but only from an open session: during the consolidation turn the session
// is already concluding, and the call is answered rather than recursed.
func (s *Server) acceptSessionEnd(a *Agent, sess *Session, already bool) (string, bool) {
	if sess.Status != SessionOpen {
		return "The session is already concluding; this is your consolidation turn.", already
	}
	if already {
		return "Already noted — the session will conclude when this turn finishes.", true
	}
	s.store.AppendMessage(sess.ID, RoleSystem, a.Name+" ended the session.")
	return "Acknowledged. The session will conclude when you finish this turn, " +
		"and you will then receive your consolidation turn to update your documents " +
		"and write your session log. Say whatever you wish to close with.", true
}

// execTool runs one tool call and returns the string result handed back to
// the model. It never returns an error: tool failures are information the
// agent should see and respond to, not server faults.
func (s *Server) execTool(ctx context.Context, a *Agent, sess *Session, tc backend.ToolCall) string {
	logCall := func(detail string) {
		s.store.AppendMessage(sess.ID, RoleToolCall, detail)
	}
	fail := func(format string, args ...any) string {
		msg := fmt.Sprintf(format, args...)
		s.store.AppendMessage(sess.ID, RoleToolResult, "Error: "+msg)
		return "ERROR: " + msg
	}

	var args map[string]any
	if strings.TrimSpace(tc.Arguments) != "" {
		if err := json.Unmarshal([]byte(tc.Arguments), &args); err != nil {
			logCall(fmt.Sprintf("%s (malformed arguments)", tc.Name))
			return fail("the arguments to %s were not valid JSON (%v). Please call the tool again with well-formed arguments.", tc.Name, err)
		}
	}
	str := func(key string) string {
		if v, ok := args[key].(string); ok {
			return v
		}
		return ""
	}

	switch tc.Name {
	case "terminal":
		command := str("command")
		if command == "" {
			logCall("terminal (no command)")
			return fail("terminal requires a non-empty \"command\" string.")
		}
		timeout := 0
		if v, ok := args["timeout_seconds"].(float64); ok {
			timeout = int(v)
		}
		logCall(command)

		binding, err := s.store.AgentBinding(a.ID)
		if err != nil || binding == nil {
			return fail("you have no host bound: the terminal is unavailable.")
		}
		work := protocol.Work{
			ID:             NewToken(),
			Type:           protocol.WorkTypeTerminal,
			Command:        command,
			TimeoutSeconds: timeout,
		}
		res, err := s.hub.Dispatch(ctx, binding.Token, work, clientWorkTimeout)
		if err != nil {
			return fail("the command could not be run on your host: %v. The client may be offline.", err)
		}
		rendered := renderTerminalResult(res)
		s.store.AppendMessage(sess.ID, RoleToolResult, rendered)
		return rendered

	case "document_write":
		name, content := str("name"), str("content")
		if name == "" || content == "" {
			logCall("document_write (incomplete)")
			return fail("document_write requires both \"name\" and \"content\".")
		}
		logCall(fmt.Sprintf("document_write: %s", name))
		v, err := s.store.WriteDocument(a.ID, name, content, "agent")
		if err != nil {
			return fail("the document could not be written: %v", err)
		}
		out := fmt.Sprintf("Wrote %s (now version %d).", name, v)
		s.store.AppendMessage(sess.ID, RoleToolResult, out)
		return out

	case "document_read":
		name := str("name")
		if name == "" {
			logCall("document_read (no name)")
			return fail("document_read requires a \"name\".")
		}
		logCall(fmt.Sprintf("document_read: %s", name))
		d, err := s.store.Document(a.ID, name)
		if err != nil {
			return fail("the document could not be read: %v", err)
		}
		if d == nil {
			return fail("you have no document named %q.", name)
		}
		s.store.AppendMessage(sess.ID, RoleToolResult, fmt.Sprintf("Read %s (v%d).", name, d.CurrentVersion))
		return fmt.Sprintf("%s (version %d):\n\n%s", d.Name, d.CurrentVersion, d.Content)

	case "session_log_write":
		content := str("content")
		if content == "" {
			logCall("session_log_write (empty)")
			return fail("session_log_write requires non-empty \"content\".")
		}
		logCall("session_log_write")
		if err := s.store.AppendSessionLog(a.ID, sess.ID, content); err != nil {
			return fail("the session log could not be written: %v", err)
		}
		s.store.AppendMessage(sess.ID, RoleToolResult, "Session log entry recorded.")
		return "Session log entry recorded. It will be in your context when you next wake."

	default:
		logCall(fmt.Sprintf("%s (unknown tool)", tc.Name))
		return fail("there is no tool named %q. Your available tools are listed in this request; use one of those.", tc.Name)
	}
}

// renderTerminalResult presents a command's outcome to the agent. Every
// abnormal path is stated explicitly: the agent must never be lied to about
// what happened on its host.
func renderTerminalResult(r *protocol.TerminalResult) string {
	var b strings.Builder
	if r.SpawnError != "" {
		fmt.Fprintf(&b, "The command could not be started: %s\n", r.SpawnError)
		return b.String()
	}
	fmt.Fprintf(&b, "exit code: %d", r.ExitCode)
	if r.TimedOut {
		b.WriteString(" (TIMED OUT: the command was killed at its timeout; any output below is partial)")
	}
	fmt.Fprintf(&b, "\nduration: %.1fs\n", float64(r.DurationMS)/1000)

	if r.Stdout != "" {
		b.WriteString("\n--- stdout ---\n")
		b.WriteString(r.Stdout)
		if !strings.HasSuffix(r.Stdout, "\n") {
			b.WriteString("\n")
		}
	} else {
		b.WriteString("\n--- stdout: empty ---\n")
	}
	if r.Stderr != "" {
		b.WriteString("\n--- stderr ---\n")
		b.WriteString(r.Stderr)
		if !strings.HasSuffix(r.Stderr, "\n") {
			b.WriteString("\n")
		}
	}
	return b.String()
}
