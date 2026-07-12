package client

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/nfinit/janus/internal/protocol"
)

// TestTerminalEnvIsolation is the regression test for the environment-leak
// incident: a secret in the client process environment must never reach the
// agent's shell. The terminal hands over a controlled environment only.
func TestTerminalEnvIsolation(t *testing.T) {
	os.Setenv("JANUS_LEAK_CANARY", "SECRET-SHOULD-NOT-APPEAR")
	defer os.Unsetenv("JANUS_LEAK_CANARY")

	res := RunTerminal(context.Background(), DefaultTerminalConfig(),
		protocol.Work{Command: "env"})
	if strings.Contains(res.Stdout, "SECRET-SHOULD-NOT-APPEAR") || strings.Contains(res.Stdout, "JANUS_LEAK_CANARY") {
		t.Errorf("terminal leaked a parent-process environment variable:\n%s", res.Stdout)
	}
	if !strings.Contains(res.Stdout, "HOME=") {
		t.Error("controlled environment should still provide HOME")
	}

	// ExtraEnv is the sanctioned way to add variables.
	cfg := DefaultTerminalConfig()
	cfg.ExtraEnv = []string{"JANUS_OPT_IN=yes"}
	res = RunTerminal(context.Background(), cfg, protocol.Work{Command: "echo $JANUS_OPT_IN"})
	if strings.TrimSpace(res.Stdout) != "yes" {
		t.Errorf("ExtraEnv not applied: %q", res.Stdout)
	}
}

func TestTerminalBasic(t *testing.T) {
	res := RunTerminal(context.Background(), DefaultTerminalConfig(),
		protocol.Work{Command: "echo hello; echo oops 1>&2; exit 3"})
	if res.ExitCode != 3 {
		t.Errorf("exit code = %d, want 3", res.ExitCode)
	}
	if strings.TrimSpace(res.Stdout) != "hello" {
		t.Errorf("stdout = %q, want hello", res.Stdout)
	}
	if strings.TrimSpace(res.Stderr) != "oops" {
		t.Errorf("stderr = %q, want oops", res.Stderr)
	}
	if res.SpawnError != "" {
		t.Errorf("unexpected spawn error: %s", res.SpawnError)
	}
}

func TestTerminalTimeout(t *testing.T) {
	cfg := DefaultTerminalConfig()
	res := RunTerminal(context.Background(), cfg,
		protocol.Work{Command: "echo before; sleep 30; echo after", TimeoutSeconds: 1})
	if !res.TimedOut {
		t.Error("expected TimedOut=true")
	}
	// Partial output before the kill must survive.
	if !strings.Contains(res.Stdout, "before") {
		t.Errorf("expected partial stdout to contain 'before', got %q", res.Stdout)
	}
	if strings.Contains(res.Stdout, "after") {
		t.Error("command should have been killed before printing 'after'")
	}
}

func TestTerminalTruncation(t *testing.T) {
	cfg := DefaultTerminalConfig()
	cfg.OutputCap = 1000
	// Emit ~5000 bytes; distinctive head and tail markers.
	res := RunTerminal(context.Background(), cfg,
		protocol.Work{Command: "printf 'HEADMARK'; for i in $(seq 1 500); do printf '0123456789'; done; printf 'TAILMARK'"})
	if !res.StdoutTruncated {
		t.Fatal("expected StdoutTruncated=true")
	}
	if len(res.Stdout) > cfg.OutputCap+200 {
		t.Errorf("truncated output too long: %d bytes", len(res.Stdout))
	}
	if !strings.Contains(res.Stdout, "HEADMARK") {
		t.Error("head of output should be preserved")
	}
	if !strings.Contains(res.Stdout, "TAILMARK") {
		t.Error("tail of output should be preserved")
	}
	if !strings.Contains(res.Stdout, "elided") {
		t.Error("truncation should be announced with an elision marker")
	}
}

func TestTerminalMaxTimeoutCap(t *testing.T) {
	cfg := DefaultTerminalConfig()
	cfg.MaxTimeout = 2 * time.Second
	start := time.Now()
	// Request 60s but the client caps at 2s.
	res := RunTerminal(context.Background(), cfg,
		protocol.Work{Command: "sleep 60", TimeoutSeconds: 60})
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Errorf("command ran %s; max timeout cap not enforced", elapsed)
	}
	if !res.TimedOut {
		t.Error("expected TimedOut=true from capped timeout")
	}
}
