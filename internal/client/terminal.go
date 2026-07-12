// Package client implements the JANUS Phase 0 client: a token-authenticated
// poll loop and a stateless terminal tool.
//
// The client contains ZERO privilege-switching code — no setuid, no sudo,
// no su. It runs as whatever local user the deployer launched it as, and
// commands inherit exactly that identity and its permissions. Onboarding an
// agent is a matter of `useradd` and running the client under that account,
// exactly as a human user would have been added to the system.
package client

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"syscall"
	"time"

	"github.com/nfinit/janus/internal/protocol"
)

// TerminalConfig bounds command execution.
type TerminalConfig struct {
	DefaultTimeout time.Duration // when the work item requests none
	MaxTimeout     time.Duration // ceiling on any requested timeout
	OutputCap      int           // max bytes per stream before head+tail truncation
	// ExtraEnv holds additional VAR=value entries a deployer opts into for
	// the agent's shell. The terminal does NOT inherit the client process
	// environment (see baseEnv); this is the sanctioned way to add to it.
	ExtraEnv []string
}

func DefaultTerminalConfig() TerminalConfig {
	return TerminalConfig{
		DefaultTimeout: 60 * time.Second,
		MaxTimeout:     300 * time.Second,
		OutputCap:      64 * 1024,
	}
}

// baseEnv is the controlled environment handed to every agent command.
//
// The terminal deliberately does NOT pass through the client process
// environment. Inheriting it leaks whatever launched the client — harness
// variables, unrelated secrets — into the agent's view, and makes the
// agent's environment non-deterministic (it depends on how the client was
// started). A defined minimal environment is both a security boundary and a
// reproducibility guarantee: an agent sees the same environment on every
// host, regardless of how its client was launched. Deployers add to it
// explicitly via TerminalConfig.ExtraEnv.
func baseEnv() []string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		home = "/"
	}
	username := "janus"
	if u, err := user.Current(); err == nil && u.Username != "" {
		username = u.Username
	}
	return []string{
		"HOME=" + home,
		"PWD=" + home,
		"USER=" + username,
		"LOGNAME=" + username,
		"SHELL=/bin/sh",
		"PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin",
		"TERM=dumb",
		"LANG=C.UTF-8",
	}
}

// RunTerminal executes one command in a fresh stateless shell and reports
// everything that happened. Statelessness: every call starts in $HOME with
// no inherited working directory, and no state survives between calls.
func RunTerminal(parent context.Context, cfg TerminalConfig, work protocol.Work) protocol.TerminalResult {
	timeout := cfg.DefaultTimeout
	if work.TimeoutSeconds > 0 {
		timeout = time.Duration(work.TimeoutSeconds) * time.Second
	}
	if timeout > cfg.MaxTimeout {
		timeout = cfg.MaxTimeout
	}

	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()

	// Fresh POSIX shell. Non-login, with a controlled environment — the
	// client process environment is deliberately NOT inherited (see baseEnv).
	cmd := exec.CommandContext(ctx, "/bin/sh", "-c", work.Command)
	cmd.Env = append(baseEnv(), cfg.ExtraEnv...)
	if home, err := os.UserHomeDir(); err == nil {
		cmd.Dir = home
	}
	// On timeout, kill the whole process group so children die too.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		if cmd.Process != nil {
			// Negative pid → signal the process group.
			syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM)
		}
		return nil
	}
	cmd.WaitDelay = 5 * time.Second // grace before SIGKILL

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	start := time.Now()
	err := cmd.Run()
	elapsed := time.Since(start)

	res := protocol.TerminalResult{DurationMS: elapsed.Milliseconds()}
	res.Stdout, res.StdoutTruncated = capOutput(stdout.Bytes(), cfg.OutputCap)
	res.Stderr, res.StderrTruncated = capOutput(stderr.Bytes(), cfg.OutputCap)

	if ctx.Err() == context.DeadlineExceeded {
		res.TimedOut = true
	}

	switch e := err.(type) {
	case nil:
		res.ExitCode = 0
	case *exec.ExitError:
		res.ExitCode = e.ExitCode()
		// ExitCode is -1 when killed by signal (e.g. our timeout).
	default:
		// Could not start the command at all.
		if !res.TimedOut {
			res.SpawnError = fmt.Sprintf("%v", err)
			res.ExitCode = -1
		}
	}
	return res
}

// capOutput truncates an output stream to fit the cap, keeping head and
// tail with an elision marker. Head+tail matters: a build's tail holds the
// error summary, its head holds the invocation context. Truncation is
// always visible — never silent.
func capOutput(b []byte, cap int) (string, bool) {
	if cap <= 0 || len(b) <= cap {
		return string(b), false
	}
	dropped := len(b) - cap
	marker := fmt.Sprintf("\n\n[... %d bytes elided; showing head and tail ...]\n\n", dropped)
	half := (cap - len(marker)) / 2
	if half < 0 {
		half = cap / 2
	}
	head := b[:half]
	tail := b[len(b)-half:]
	return string(head) + marker + string(tail), true
}
