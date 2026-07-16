package server

import (
	"bytes"
	"strings"
	"testing"
)

// TestSessionTemplateBusyStates pins the busy/idle rendering of the session
// page, which cannot be observed end-to-end with the stub (its turns finish
// in microseconds). While a turn is in flight the page must self-refresh and
// withhold the input controls; once idle it must do neither.
func TestSessionTemplateBusyStates(t *testing.T) {
	tmpl, err := loadTemplates()
	if err != nil {
		t.Fatal(err)
	}

	base := func(busy bool, status string) *sessionData {
		d := &sessionData{
			pageData: pageData{Title: "Session 1", Version: "test", Authed: true},
			Agent:    &Agent{ID: 1, Name: "Probe"},
			Session:  &Session{ID: 1, Status: status},
			Messages: []*Message{{Role: RoleAdmin, Content: "hello"}},
			Busy:     busy,
		}
		if busy {
			d.Refresh = 3
		}
		return d
	}

	render := func(d *sessionData) string {
		var buf bytes.Buffer
		if err := tmpl.render(&buf, "session", d); err != nil {
			t.Fatal(err)
		}
		return buf.String()
	}

	busy := render(base(true, SessionOpen))
	if !strings.Contains(busy, `http-equiv="refresh"`) {
		t.Error("busy page should carry a meta refresh")
	}
	if !strings.Contains(busy, "Probe is responding") {
		t.Error("busy page should show the responding indicator")
	}
	if strings.Contains(busy, "/sessions/1/message") {
		t.Error("busy page must withhold the message form")
	}

	idle := render(base(false, SessionOpen))
	if strings.Contains(idle, `http-equiv="refresh"`) {
		t.Error("idle page must not self-refresh")
	}
	if !strings.Contains(idle, "/sessions/1/message") {
		t.Error("idle open page should offer the message form")
	}

	consolidating := render(base(true, SessionLocked))
	if !strings.Contains(consolidating, "Probe is consolidating") {
		t.Error("locked busy page should show the consolidating indicator")
	}

	closed := render(base(false, SessionClosed))
	if !strings.Contains(closed, "This session has ended") {
		t.Error("closed page should show the ended notice")
	}
	if strings.Contains(closed, `http-equiv="refresh"`) {
		t.Error("closed page must not self-refresh")
	}
}
