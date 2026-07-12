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

## Quick start

```sh
make            # build static janus-server and janus-client
sudo make install

janus-server    # open http://localhost:8611 and complete setup
```

Add an endpoint (an OpenAI-compatible API such as OpenRouter — or the
built-in `stub` for a keyless trial), create an agent, approve its charter,
generate a token, and run `janus-client` on a target host with that token.

## Design

- **Go, standard-library-first.** Static binaries, zero runtime
  dependencies — clone and forget. `modernc.org/sqlite` (pure Go) is the
  only third-party runtime dependency.
- **A primitive, client-initiated protocol** built to an HTTP/1.0 floor so
  it can one day reach machines that predate HTTP itself.
- **Memory is what an agent writes down.** Sessions are bounded and end with
  a consolidation turn; between them, context is reconstructed from durable
  documents, not an ever-growing transcript.

See [`docs/`](docs/) for the architecture, protocol, session lifecycle, and
deployment guide.

## Versioning

The server is versioned `JANUS Phase major.minor.patch.build`; clients use
the same numbers without the "Phase" prefix. This is **Phase 0.0.0.1**.
