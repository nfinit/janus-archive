// Package protocol defines the JANUS client↔server wire protocol.
//
// The protocol is deliberately primitive: it must remain implementable by
// a small C program on systems that predate HTTP itself, reached through
// nothing more than socket(); connect(); write(); read-until-close.
//
// Binding rules (see docs: "the HTTP/1.0 floor"):
//
//   - The client always initiates; the server never dials a client.
//   - Bare HTTP/1.0 requests must be accepted by the server.
//   - Responses always carry Content-Length; chunked transfer encoding is
//     never used on API paths.
//   - No redirects, no cookies, no required headers beyond Content-Type
//     and Content-Length on API paths.
//   - TLS is never required by the protocol; transport security is a
//     deployment concern (reverse proxy, isolated network segment).
//   - Connection close after each exchange is always acceptable.
//
// Exchanges:
//
//	POST /api/poll    — the client's work loop. The request carries a
//	                    "wait" parameter: the maximum seconds the server
//	                    may hold the request open before answering. Modern
//	                    clients long-poll (wait≈25); constrained stacks
//	                    degrade to plain polling with wait=0. The same
//	                    protocol at different parameter values.
//	POST /api/result  — the client reports the outcome of a work item,
//	                    then re-enters the poll loop.
//
// All request and response bodies are JSON (UTF-8). A future phase may add
// a line-oriented alternate encoding for very old clients.
package protocol

// WorkTypeTerminal is the only work type in Phase 0: run one command in a
// fresh stateless shell and report the outcome.
const WorkTypeTerminal = "terminal"

// PollRequest is the body of POST /api/poll.
type PollRequest struct {
	// Token identifies the client instance (and thus the agent binding).
	Token string `json:"token"`
	// Wait is the maximum number of seconds the server may hold this
	// request open waiting for work. 0 means answer immediately.
	Wait int `json:"wait"`
	// Version is the client's version string, e.g. "JANUS Client 0.0.0.1".
	// Informational; shown in the console.
	Version string `json:"version,omitempty"`
	// Hostname is the client host's self-reported name. Informational.
	Hostname string `json:"hostname,omitempty"`
}

// PollResponse is the body of the POST /api/poll response. Work is null
// when the wait elapsed with nothing to do; the client simply polls again.
type PollResponse struct {
	Work *Work `json:"work"`
}

// Work is a single item the server asks the client to perform.
type Work struct {
	// ID is unique per work item; echoed back in ResultRequest.
	ID string `json:"id"`
	// Type is the work type; Phase 0 defines only "terminal".
	Type string `json:"type"`
	// Command is the shell command to execute (Type "terminal").
	Command string `json:"command,omitempty"`
	// TimeoutSeconds is the requested execution timeout. The client caps
	// this at its configured maximum. 0 means the client's default.
	TimeoutSeconds int `json:"timeout_seconds,omitempty"`
}

// ResultRequest is the body of POST /api/result.
type ResultRequest struct {
	Token  string         `json:"token"`
	WorkID string         `json:"work_id"`
	Result TerminalResult `json:"result"`
}

// TerminalResult reports everything that happened when a terminal work
// item was executed. Failure honesty is a protocol requirement: every
// abnormal path (timeout, truncation, spawn failure) is reported
// explicitly and structurally; the agent must never be lied to about what
// happened on its host.
type TerminalResult struct {
	Stdout   string `json:"stdout"`
	Stderr   string `json:"stderr"`
	ExitCode int    `json:"exit_code"`
	// DurationMS is wall-clock execution time in milliseconds.
	DurationMS int64 `json:"duration_ms"`
	// TimedOut is true when the command was killed at the timeout; any
	// partial output captured is still returned.
	TimedOut bool `json:"timed_out"`
	// StdoutTruncated / StderrTruncated are true when the respective
	// stream exceeded the client's output cap. Truncated streams keep
	// head and tail with an elision marker in between.
	StdoutTruncated bool `json:"stdout_truncated,omitempty"`
	StderrTruncated bool `json:"stderr_truncated,omitempty"`
	// SpawnError is set when the command could not be started at all
	// (e.g. fork failure). Exit code is meaningless in that case.
	SpawnError string `json:"spawn_error,omitempty"`
}

// ResultResponse is the body of the POST /api/result response.
type ResultResponse struct {
	OK bool `json:"ok"`
}

// ErrorResponse is the body of any API error response (4xx/5xx).
type ErrorResponse struct {
	Error string `json:"error"`
}
