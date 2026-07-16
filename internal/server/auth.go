package server

// Single-admin console authentication: a first-run setup password, hashed
// with PBKDF2-HMAC-SHA256 in the settings table, and an in-memory cookie
// session after login. Phase 0 has one admin identity.

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/pbkdf2"
)

const (
	pbkdfIter     = 200_000
	sessionCookie = "janus_admin"
	settingPWHash = "admin_pw"
)

type authManager struct {
	mu       sync.Mutex
	sessions map[string]time.Time // token -> expiry
}

func newAuthManager() *authManager {
	return &authManager{sessions: make(map[string]time.Time)}
}

func hashPassword(pw string) string {
	salt := make([]byte, 16)
	rand.Read(salt)
	dk := pbkdf2.Key([]byte(pw), salt, pbkdfIter, 32, sha256.New)
	return strconv.Itoa(pbkdfIter) + "$" +
		base64.RawStdEncoding.EncodeToString(salt) + "$" +
		base64.RawStdEncoding.EncodeToString(dk)
}

func verifyPassword(pw, stored string) bool {
	parts := strings.Split(stored, "$")
	if len(parts) != 3 {
		return false
	}
	iter, err := strconv.Atoi(parts[0])
	if err != nil {
		return false
	}
	salt, err := base64.RawStdEncoding.DecodeString(parts[1])
	if err != nil {
		return false
	}
	want, err := base64.RawStdEncoding.DecodeString(parts[2])
	if err != nil {
		return false
	}
	got := pbkdf2.Key([]byte(pw), salt, iter, len(want), sha256.New)
	return subtle.ConstantTimeCompare(got, want) == 1
}

func (m *authManager) newSession() string {
	tok := NewToken() + NewToken()
	m.mu.Lock()
	m.sessions[tok] = time.Now().Add(12 * time.Hour)
	m.mu.Unlock()
	return tok
}

func (m *authManager) valid(tok string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	exp, ok := m.sessions[tok]
	if !ok {
		return false
	}
	if time.Now().After(exp) {
		delete(m.sessions, tok)
		return false
	}
	return true
}

func (m *authManager) destroy(tok string) {
	m.mu.Lock()
	delete(m.sessions, tok)
	m.mu.Unlock()
}

func (s *Server) isConfigured() bool {
	h, _ := s.store.Setting(settingPWHash)
	return h != ""
}

func (s *Server) authed(r *http.Request) bool {
	c, err := r.Cookie(sessionCookie)
	if err != nil {
		return false
	}
	return s.auth.valid(c.Value)
}
