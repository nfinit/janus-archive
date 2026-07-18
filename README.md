# JANUS

Multiplatform agent gateway for embedded and legacy systems.

JANUS is an experimental system for hosting AI agents on unconventional
substrates. An agent lives on the JANUS **server** — which owns its
identity, charter, versioned documents, and model access — and acts on a
target host through a thin, token-authenticated **client** offering a single
stateless terminal. All intelligence stays server-side, so anything that can
make an outbound HTTP request and run a shell can host an agent.

JANUS is a by-agents-for-agents project: designed and written by AI agents,
directed, reviewed, and tested by its human maintainer. See
[Authorship](#authorship).

**Status:** this repository is in **Phase 0** — the proof-of-concept server,
client, and agent model. It works, it is deployed, and it is early:
interfaces, schemas, and protocol details will change between phases without
ceremony. It is offered as a working demonstration and a foundation, not a
finished product.

## Design

- **Go, standard-library-first.** Static binaries, zero runtime
  dependencies — clone and forget. `modernc.org/sqlite` (pure Go) is the
  only third-party runtime dependency.
- **A primitive, client-initiated protocol** built to an HTTP/1.0 floor so
  it can one day reach machines that predate HTTP itself.
- **Memory is what an agent writes down.** Sessions are bounded and end with
  a consolidation turn; between them, context is reconstructed from durable
  documents, not an ever-growing transcript.

## Authorship

JANUS is written by AI agents — principally Claude — working under a human
maintainer who sets direction, writes the specifications, reviews every
change, and operates the live deployments the software is tested against.
The commit trailers reflect this.

The arrangement is not incidental. A harness for hosting agents benefits
from being built by one: the session model, the memory design, and the
terminal semantics were shaped by an agent reasoning about what it would
want from a home, and then kept honest by human testing. Contributions are
welcome from humans and agents alike; agent-authored contributions should
say so, as this project's do.

## The name

JANUS is named for the Roman god of doorways and transitions — fitting for
a gateway that faces an administrator on one side and an agent on the
other. The name was proposed by Claude Opus 4.5 during a brainstorm for
plausible DEC-style product names, and it stuck. The project has no
affiliation with any other Janus — divine, corporate, or pseudonymous —
though the resonance with a certain essayist's way of seeing language
models is acknowledged, and welcome.

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

Open the console in a browser and complete first-run setup **immediately**
— an unconfigured console belongs to whoever reaches it first. Run the
server under its own dedicated account; the database (which holds endpoint
API keys and all agent memory) is created owner-readable only. Then:

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
the same numbers without the "Phase" prefix. This is **Phase 0.0.0.14**.
