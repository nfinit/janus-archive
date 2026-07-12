// Package backend defines JANUS's thin, owned model-backend abstraction.
//
// One operation — Complete — takes a normalized conversation plus tool
// definitions and returns the model's turn with a stop reason. Phase 0
// ships a single concrete implementation (the OpenAI-compatible
// chat-completions dialect) plus a scripted stub for end-to-end testing
// without any API credential.
//
// Robustness rule: all model output is untrusted input. Malformed tool
// calls or unexpected shapes must surface as ordinary values or errors the
// conversation loop can respond to — never a crash or a hung session.
package backend

import (
	"context"
	"encoding/json"
)

type Role string

const (
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
	RoleTool      Role = "tool"
)

// ToolCall is a model's request to invoke a tool. Arguments is the raw
// JSON string exactly as the model produced it — it may be malformed, and
// the caller must treat it accordingly.
type ToolCall struct {
	ID        string
	Name      string
	Arguments string
}

// Message is one entry in the normalized conversation.
type Message struct {
	Role       Role
	Content    string
	ToolCalls  []ToolCall // assistant messages only
	ToolCallID string     // tool-result messages only: which call this answers
}

// Tool is a normalized tool definition.
type Tool struct {
	Name        string
	Description string
	Parameters  json.RawMessage // JSON Schema object
}

// Request is the input to Complete.
type Request struct {
	Model     string
	System    string
	Messages  []Message
	Tools     []Tool
	MaxTokens int // 0 = backend default
}

type StopReason string

const (
	StopEndTurn   StopReason = "end_turn"
	StopToolUse   StopReason = "tool_use"
	StopMaxTokens StopReason = "max_tokens"
)

// Turn is the model's reply.
type Turn struct {
	Content    string
	ToolCalls  []ToolCall
	StopReason StopReason
	// Model is the model that actually served this turn, as reported by the
	// backend. For a router endpoint (e.g. openrouter/free) this is the
	// concrete model chosen for the call, which may differ turn to turn.
	Model string
}

// Backend produces model turns. Implementations must be safe for
// concurrent use.
type Backend interface {
	Complete(ctx context.Context, req Request) (*Turn, error)
}
