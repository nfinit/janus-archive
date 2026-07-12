-- JANUS Phase 0 schema.
--
-- Design rules carried in from the Phase 0 decisions:
--   * bindings are (agent, client-binding) pairs: the schema permits many
--     bindings per agent even though Phase 0 logic enforces exactly one.
--   * deleting an open session is a full expunge: DELETE with cascading
--     messages. Locked/closed sessions are never deleted (their substance
--     has been consolidated into agent documents).
--   * agent documents are versioned; agents write them only through
--     server-side tools, never directly.

PRAGMA journal_mode = WAL;
PRAGMA foreign_keys = ON;

CREATE TABLE IF NOT EXISTS settings (
    key   TEXT PRIMARY KEY,
    value TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS endpoints (
    id         INTEGER PRIMARY KEY,
    name       TEXT    NOT NULL UNIQUE,
    kind       TEXT    NOT NULL DEFAULT 'openai',  -- 'openai' | 'stub'
    base_url   TEXT    NOT NULL DEFAULT '',
    api_key    TEXT    NOT NULL DEFAULT '',
    created_at INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS agents (
    id             INTEGER PRIMARY KEY,
    identifier     TEXT    NOT NULL UNIQUE, -- server-known unique handle
    name           TEXT    NOT NULL,        -- formal name the agent goes by
    endpoint_id    INTEGER NOT NULL REFERENCES endpoints(id),
    model          TEXT    NOT NULL,
    primer         TEXT    NOT NULL,
    charter        TEXT    NOT NULL DEFAULT '',
    charter_status TEXT    NOT NULL DEFAULT 'draft', -- 'draft' | 'approved'
    model_aware    INTEGER NOT NULL DEFAULT 0,
    created_at     INTEGER NOT NULL,
    updated_at     INTEGER NOT NULL
);

-- A binding is an agent's deployment onto one client instance.
CREATE TABLE IF NOT EXISTS bindings (
    id            INTEGER PRIMARY KEY,
    agent_id      INTEGER NOT NULL REFERENCES agents(id) ON DELETE CASCADE,
    token         TEXT    NOT NULL UNIQUE,
    label         TEXT    NOT NULL DEFAULT '',
    last_seen     INTEGER,                  -- unix seconds of last poll
    last_hostname TEXT    NOT NULL DEFAULT '',
    last_version  TEXT    NOT NULL DEFAULT '',
    created_at    INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS sessions (
    id            INTEGER PRIMARY KEY,
    agent_id      INTEGER NOT NULL REFERENCES agents(id) ON DELETE CASCADE,
    status        TEXT    NOT NULL DEFAULT 'open', -- 'open'|'locked'|'closed'
    opened_at     INTEGER NOT NULL,
    last_activity INTEGER NOT NULL,
    closed_at     INTEGER
);

-- Transcript: the admin-visible verbatim record of a session.
CREATE TABLE IF NOT EXISTS messages (
    id         INTEGER PRIMARY KEY,
    session_id INTEGER NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
    role       TEXT    NOT NULL, -- 'admin'|'agent'|'tool_call'|'tool_result'|'system'
    content    TEXT    NOT NULL,
    model      TEXT    NOT NULL DEFAULT '', -- model that served an agent turn, if known
    created_at INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_messages_session ON messages(session_id);

-- The normalized model conversation for a session, persisted so an
-- in-flight session survives a server restart. Distinct from `messages`:
-- that is the admin-visible transcript, this is what the model sees.
CREATE TABLE IF NOT EXISTS convo (
    id         INTEGER PRIMARY KEY,
    session_id INTEGER NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
    payload    TEXT    NOT NULL -- JSON-encoded backend.Message
);
CREATE INDEX IF NOT EXISTS idx_convo_session ON convo(session_id);

-- Versioned agent documents (IDENTITY, PRACTICES, ...).
CREATE TABLE IF NOT EXISTS documents (
    id              INTEGER PRIMARY KEY,
    agent_id        INTEGER NOT NULL REFERENCES agents(id) ON DELETE CASCADE,
    name            TEXT    NOT NULL,
    current_version INTEGER NOT NULL DEFAULT 0,
    UNIQUE(agent_id, name)
);

CREATE TABLE IF NOT EXISTS document_versions (
    id          INTEGER PRIMARY KEY,
    document_id INTEGER NOT NULL REFERENCES documents(id) ON DELETE CASCADE,
    version     INTEGER NOT NULL,
    content     TEXT    NOT NULL,
    author      TEXT    NOT NULL, -- 'agent' | 'admin'
    created_at  INTEGER NOT NULL,
    UNIQUE(document_id, version)
);

-- Session logs: the agent's own append-only account, written during
-- consolidation. Distinct from the transcript.
CREATE TABLE IF NOT EXISTS session_logs (
    id         INTEGER PRIMARY KEY,
    agent_id   INTEGER NOT NULL REFERENCES agents(id) ON DELETE CASCADE,
    session_id INTEGER,
    content    TEXT    NOT NULL,
    created_at INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_session_logs_agent ON session_logs(agent_id);
