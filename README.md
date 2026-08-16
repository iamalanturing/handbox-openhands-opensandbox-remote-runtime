# handbox

> **Status: early / unreleased.** This is a from-scratch implementation, not a
> fork of [`zparnold/openhands-kubernetes-remote-runtime`](https://github.com/zparnold/openhands-kubernetes-remote-runtime).
> That project is credited as the source of the verified OpenHands Remote
> Runtime API contract (Section 4 below) — it's a real, working alternative
> backend implementing the same contract against Kubernetes instead of
> Docker/OpenSandbox. Nothing here has been run against a live OpenHands +
> OpenSandbox deployment yet; treat all of it as unverified until the
> acceptance criteria in the implementation spec pass end-to-end.

## What it is

`handbox` is a translation layer between two systems that don't know about
each other. [OpenHands](https://github.com/OpenHands/OpenHands) (an
open-source AI coding agent with a browser UI) supports delegating sandbox
creation to an external **Remote Runtime** — any HTTP service implementing a
specific API contract — instead of spawning sandboxes itself via the local
Docker socket. [OpenSandbox](https://github.com/opensandbox-group/OpenSandbox)
(Alibaba's open-source sandbox platform for AI agents) can create sandboxes
with **Kata Containers** isolation and **FQDN-based egress filtering** —
capabilities OpenHands' own built-in sandbox spawner does not have. `handbox`
sits in the middle: it receives OpenHands' Remote Runtime calls, translates
each into the equivalent OpenSandbox API call, and returns OpenSandbox's
response in the shape OpenHands expects. It also owns one piece of policy
logic neither system has: reading a local network-level state file and
translating the current level (offline / GitHub-only / research / full) into
the correct OpenSandbox `networkPolicy` payload on every sandbox creation.

`handbox` itself runs as a plain, trusted, long-lived service (not
per-conversation, not sandboxed) — it executes no agent-generated code and
holds no Docker socket access itself.

## Architecture

```
openhands-app  →(Remote Runtime API)→  handbox  →(OpenSandbox API)→  OpenSandbox server  →  Kata sandbox
                                            ↑
                                   reads network-level
                                      state file
```

## Prerequisites

`handbox` does not install or manage any of the following — they're assumed
to already be set up:

- Docker Engine running on the host
- Kata Containers installed and registered as a Docker runtime
- OpenSandbox server installed and runnable, configured with
  `secure_runtime.docker_runtime = "kata"` in its `config.toml` (this makes
  Kata the runtime for **every** sandbox OpenSandbox creates — a one-time
  server-wide setting, not something `handbox` sets per-request)
- OpenSandbox server reachable at a known URL, with an API key
- The `ghcr.io/openhands/agent-server` image pullable
- Go 1.22+ toolchain for building `handbox`

## Configuration

`handbox` is configured via a file (`handbox.toml`) or environment
variables, covering:

- listen address/port
- OpenSandbox API URL and API key
- network-level state file path
- the research-level egress allowlist (configurable, not hardcoded)
- `single_use_level` (default `true` — see below)

`handbox` needs **no Docker socket access** — only outbound HTTP reachability
to OpenSandbox's API. This is a meaningful improvement over OpenHands'
default architecture, which requires `openhands-app` to hold
`/var/run/docker.sock` directly. `handbox` runs as a plain, long-lived
process/container (not Kata, not gVisor for v1 — this is trusted code with a
narrow attack surface, not an untrusted-code execution boundary), and
validates the `SANDBOX_API_KEY` OpenHands sends on every request against its
own configured key before processing anything.

## Network levels

On every `POST /start` from OpenHands, `handbox` reads the current level
from the state file and builds the corresponding OpenSandbox `networkPolicy`:

| Level | State file value | `networkPolicy` sent to OpenSandbox |
|---|---|---|
| ⚪ Ask (default / unset) | `ask`, missing, or any unrecognized value | **No sandbox is created.** `/start` returns an error rather than guessing or falling back to a default level. |
| 🔴 Offline | `offline` | Omit the field entirely (unrestricted default networking) — verify Ollama reachability under this before relying on it. |
| 🟡 GitHub-only | `github` | `defaultAction: deny`, egress allowed to `github.com`, `api.github.com`, `*.githubusercontent.com`, `codeload.github.com`. |
| 🟢 Research | `research` | Same as GitHub-only, plus a configurable allowlist for documentation/search (e.g. `*.python.org`, `pypi.org`, `*.npmjs.org`, `stackoverflow.com`, `developer.mozilla.org`, `en.wikipedia.org`). |
| 🟣 Full | `full` | Omit the field entirely (unrestricted networking) — rare, deliberate, no filtering. |

Every level value is **single-use**: it's consumed and reset back to `ask`
by the `/start` call that reads it, not by anything happening later (see the
implementation spec for the full reasoning — this is a deliberate response
to conversation start/stop ordering not being guaranteed). Set a level
before starting a conversation with the companion CLI:

```
ai-level <offline|github|research|full>
```

## Development

```
go build -o handbox ./cmd/handbox
go build -o ai-level ./cmd/ai-level
go test ./...
```

## License

MIT — see [LICENSE](./LICENSE).
