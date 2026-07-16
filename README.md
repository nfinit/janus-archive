# JANUS

Multiplatform agent gateway for embedded and legacy systems.

JANUS is an experimental system for hosting AI agents on unconventional
substrates. An agent lives on the JANUS **server** — which owns its
identity, charter, versioned documents, and model access — and acts on a
target host through a thin, token-authenticated **client** offering a single
stateless terminal. All intelligence stays server-side, so anything that can
make an outbound HTTP request and run a shell can host an agent.

This repository is in **Phase 0**: the proof-of-concept server, client, and
agent model.

## Design

- **Go, standard-library-first.** Static binaries, zero runtime
  dependencies — clone and forget. `modernc.org/sqlite` (pure Go) is the
  only third-party runtime dependency.
- **A primitive, client-initiated protocol** built to an HTTP/1.0 floor so
  it can one day reach machines that predate HTTP itself.
- **Memory is what an agent writes down.** Sessions are bounded and end with
  a consolidation turn; between them, context is reconstructed from durable
  documents, not an ever-growing transcript.

## Deployment

### Build and install

```sh
git clone https://github.com/nfinit/janus
cd janus
make                # builds build/janus-server and build/janus-client (static)
sudo make install   # installs both to /usr/local/bin
```

Building requires only a Go toolchain (1.25+). The module tree is vendored,
so the build works fully offline. Override the install prefix with
`make install PREFIX=/opt/janus`; `make uninstall` removes the binaries.

### Run the server

```sh
janus-server                       # listens on :8611, database ./janus.db
janus-server -addr :9000 -db /var/lib/janus/janus.db
```

Open the console in a browser and complete first-run setup, then:

1. **Add an endpoint** — kind `openai` for any OpenAI-compatible API
   (OpenRouter, a local inference server, …), or the built-in `stub`, a
   scripted test model that exercises the whole system with no API key.
2. **Create an agent** — identifier, endpoint + model ID, and a deployment
   primer. The model drafts its charter in its own voice; review and
   approve it.
3. **Generate a binding token** on the agent's page.

The server speaks plain HTTP by design, so the oldest clients can reach it.
For modern deployments, put it behind an HTTPS reverse proxy or keep it on a
trusted network segment — never rely on the protocol itself for transport
security.

### Deploy a client

On the target host, create a dedicated local user per agent and run the
client as that user — exactly as you would onboard a human. The agent's
permissions are whatever you grant that account, auditable with ordinary
Unix tooling.

```sh
sudo useradd -m -s /bin/sh janus-iris

# as janus-iris:
mkdir -p ~/.config/janus
cat > ~/.config/janus/client.json <<'EOF'
{
  "server_url": "https://janus.example.org",
  "token": "PASTE-THE-TOKEN-HERE",
  "wait": 25
}
EOF

janus-client -config ~/.config/janus/client.json
```

| Config field | Meaning | Default |
|---|---|---|
| `server_url` | JANUS server base URL | (required) |
| `token` | binding token from the console | (required) |
| `wait` | poll hold seconds; `0` = plain polling | 25 |
| `default_timeout_seconds` | per-command default timeout | 60 |
| `max_timeout_seconds` | ceiling on agent-requested timeouts | 300 |
| `output_cap_kib` | per-stream output cap before head+tail truncation | 64 |

Flags `-server`, `-token`, and `-wait` override the config file. For
unattended operation, run the client under a supervisor (e.g. a systemd user
service with lingering enabled).

**One client instance = one token = one local user = one host presence.**
The client never switches privileges; the security surface is exactly what
the deployer's `useradd` and permission choices made it.

Full documentation — architecture, protocol, and session lifecycle — will be
published in the project wiki.

## Versioning

The server is versioned `JANUS Phase major.minor.patch.build`; clients use
the same numbers without the "Phase" prefix. This is **Phase 0.0.0.10**.
