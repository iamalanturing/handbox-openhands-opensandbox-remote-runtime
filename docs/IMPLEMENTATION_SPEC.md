# handbox — Implementation Specification

**For:** Claude Code, implementing this repository from scratch
**Repo:** `handbox-openhands-opensandbox-remote-runtime` (public GitHub repo)
**Command name:** `handbox`
**Language:** Go
**License:** MIT
**Spec status:** API contracts below are pulled directly from primary sources (see citations) — not inferred or approximated. Where something is genuinely unverified, it's flagged explicitly in "Known Unknowns" rather than assumed.

---

## 1. What This Is

`handbox` is a translation layer between two systems that don't know about each other:

- **OpenHands** (an open-source AI coding agent with a browser UI) supports delegating sandbox creation to an external **Remote Runtime** — any HTTP service implementing a specific API contract — instead of spawning sandboxes itself via the local Docker socket.
- **OpenSandbox** (Alibaba's open-source sandbox platform for AI agents) can create sandboxes with **Kata Containers** isolation and **FQDN-based egress filtering** — capabilities OpenHands' own built-in sandbox spawner does not have.

`handbox` sits in the middle: it receives OpenHands' Remote Runtime calls, translates each into the equivalent OpenSandbox API call, and returns OpenSandbox's response in the shape OpenHands expects. It also owns one piece of policy logic neither system has: reading a local **network-level state file** and translating the current level (offline / GitHub-only / research / full) into the correct OpenSandbox `networkPolicy` payload on every sandbox creation.

```
openhands-app  →(Remote Runtime API)→  handbox  →(OpenSandbox API)→  OpenSandbox server  →  Kata sandbox
                                            ↑
                                   reads network-level
                                      state file
```

`handbox` itself runs as a plain, trusted, long-lived service (not per-conversation, not sandboxed) — it executes no agent-generated code and holds no Docker socket access itself. See Section 7 for its own security posture.

> **Status note (added 2026-08-20, after initial implementation):** this spec's premise — OpenHands' "Remote Runtime" delegation API — has since been found to be deprecated/removed from current OpenHands (deprecated since OpenHands v1.0.0, scheduled removal April 1 2026) and conceptually superseded by OpenHands' June 2026 "Agent Canvas" rewrite, which abandoned dynamic per-conversation sandbox creation as a product direction. See the repository's issue tracker and README for current status. This document is preserved as-written for historical/reference accuracy about what was originally built and why.

---

## 2. Prerequisites (assume these are already set up; not `handbox`'s job to install them)

- Docker Engine running on the host
- Kata Containers installed and registered as a Docker runtime
- OpenSandbox server installed and runnable, configured with `secure_runtime.docker_runtime = "kata"` in its `config.toml` (this makes Kata the runtime for **every** sandbox OpenSandbox creates — a one-time server-wide setting, not something `handbox` sets per-request)
- OpenSandbox server reachable at a known URL, with an API key
- The `ghcr.io/openhands/agent-server` image pullable
- Go 1.22+ toolchain for building `handbox`

---

## 3. Repository Structure

```
handbox-openhands-opensandbox-remote-runtime/
├── cmd/
│   └── handbox/
│       └── main.go              # entrypoint; binary builds as "handbox"
├── internal/
│   ├── server/                  # HTTP server implementing the OpenHands Remote Runtime contract
│   │   ├── server.go
│   │   ├── handlers.go
│   │   └── handlers_test.go
│   ├── opensandbox/              # OpenSandbox API client
│   │   ├── client.go
│   │   ├── types.go
│   │   └── client_test.go
│   ├── levels/                   # network-level state file reading + policy mapping
│   │   ├── state.go
│   │   ├── policy.go
│   │   └── policy_test.go
│   └── config/
│       └── config.go             # handbox's own config (its listen port, OpenSandbox URL/key, state file path)
├── cmd/ai-level/                 # small companion CLI: reads/writes the state file directly, and clears the "ask" hard-stop
│   └── main.go
├── .gitignore                    # create with the exact contents in Appendix A — do not improvise a different version
├── LICENSE                       # MIT — see Section 9
├── README.md                     # see Section 10
├── go.mod
└── go.sum
```

---

## 4. The OpenHands Remote Runtime API — what `handbox` must implement (server side)

Source: OpenHands' self-hosted Remote Runtime contract, confirmed via `zparnold/openhands-kubernetes-remote-runtime` (a real, working alternative backend implementing this exact contract against Kubernetes instead of Docker). OpenHands is configured to talk to `handbox` via two environment variables on the `openhands-app` side: `SANDBOX_REMOTE_RUNTIME_API_URL` (points at `handbox`) and `SANDBOX_API_KEY` (auth token `handbox` must check on every request).

| Method + Path | Purpose |
|---|---|
| `POST /start` | Create a sandbox |
| `POST /stop` | Terminate a sandbox |
| `POST /pause` | Pause (tear down compute, keep state) |
| `POST /resume` | Resume a paused sandbox |
| `GET /list` | List all runtimes |
| `GET /runtime/{runtime_id}` | Get one runtime's details |
| `GET /sessions/{session_id}` | Get runtime by session ID |
| `GET /sessions/batch?ids=...` | Batch session query |
| `GET /registry_prefix` | Container registry prefix |
| `GET /image_exists?image=...` | Check image availability |
| `GET /health` | Liveness check |

### `POST /start` — the important one

Request body (fields `handbox` cares about):
```json
{
  "image": "ghcr.io/openhands/agent-server:1.26.0-python",
  "command": ["<agent-server startup command>"],
  "working_dir": "/workspace",
  "environment": { "LLM_MODEL": "ollama/devstral", "...": "..." },
  "session_id": "abc123",
  "resource_factor": 1
}
```
Note: this protocol also defines a `runtime_class` field (used in the K8s reference implementation to select things like `gvisor`/`sysbox-runc`). **`handbox` does not need to read or act on this field** — secure runtime (Kata) is a one-time OpenSandbox server-side config, not per-request (see Section 2). Accept the field if present, ignore its value.

Response body `handbox` must return:
```json
{
  "runtime_id": "<opensandbox sandbox id>",
  "session_id": "<echoed from request>",
  "url": "<reachable URL for the agent-server, from OpenSandbox's endpoint API>",
  "session_api_key": "<a key OpenHands will use to authenticate to the agent-server directly>",
  "status": "running",
  "pod_status": "running",
  "work_hosts": {}
}
```

### `POST /stop`, `/pause`, `/resume`
Request: `{"runtime_id": "..."}`. No response body content is critical beyond a success status — mirror the K8s reference implementation's response shapes for these three if more detail is needed.

### `GET /list`, `/runtime/{runtime_id}`, `/sessions/{session_id}`, `/sessions/batch`
These require `handbox` to keep its own lightweight mapping of `session_id ↔ opensandbox_sandbox_id`, since OpenSandbox has no concept of "OpenHands session." A simple in-memory map is sufficient for v1 (see Section 8, out of scope: persistence across `handbox` restarts).

### `GET /registry_prefix`, `GET /image_exists`
Since this is a single-image (`ghcr.io/openhands/agent-server`), single-host local deployment, these can be implemented simply — `/image_exists` can shell out to `docker image inspect` on the host, or just return `true` unconditionally for v1 given the image is expected to already be pulled as a prerequisite.

### `GET /health`
Return healthy only if `handbox` can also reach OpenSandbox's own `/health`-equivalent (don't just check that `handbox` itself is up — a `handbox` that can't reach its backend isn't actually healthy).

---

## 5. The OpenSandbox API — what `handbox` must call (client side)

Source: `specs/sandbox-lifecycle.yml` in the OpenSandbox repo (`opensandbox-group/OpenSandbox`) — pulled directly from the actual OpenAPI spec, not secondary documentation. Clone the repo and read `server/specs/sandbox-lifecycle.yml` directly if anything below needs double-checking during implementation; do not rely on OpenSandbox's docs pages, which have shown looser/less precise phrasing than the spec itself in prior research for this project.

**Auth:** every request needs header `OPEN-SANDBOX-API-KEY: <api-key>`.

| Method + Path | Purpose |
|---|---|
| `POST /sandboxes` | Create a sandbox |
| `GET /sandboxes` | List sandboxes |
| `GET /sandboxes/{sandboxId}` | Fetch a sandbox |
| `DELETE /sandboxes/{sandboxId}` | Delete a sandbox |
| `POST /sandboxes/{sandboxId}/pause` | Pause |
| `POST /sandboxes/{sandboxId}/resume` | Resume |
| `GET /sandboxes/{sandboxId}/endpoints/{port}` | Get the public URL for a port inside the sandbox — **this is how `handbox` gets the `url` field it returns to OpenHands** |
| `PATCH /sandboxes/{sandboxId}/metadata` | Patch metadata (not needed for v1) |

### `POST /sandboxes` — exact request shape

```json
{
  "image": { "uri": "ghcr.io/openhands/agent-server:1.26.0-python" },
  "entrypoint": ["<command from OpenHands' /start request>"],
  "timeout": 3600,
  "resourceLimits": { "cpu": "1000m", "memory": "2Gi" },
  "networkPolicy": {
    "defaultAction": "deny",
    "egress": [
      { "action": "allow", "target": "github.com" },
      { "action": "allow", "target": "api.github.com" }
    ]
  }
}
```

Key details confirmed from the spec, not assumed:
- `entrypoint` is **required** when creating from `image` (not required when restoring from a snapshot — irrelevant for v1, we always create from image).
- `networkPolicy` is entirely **optional**. If omitted, no egress sidecar is created at all and the sandbox gets ordinary, unrestricted Docker networking. If present but `defaultAction` is omitted, the sidecar defaults to `"deny"`. **Passing an empty object or `null` results in allow-all** — this distinction matters for how Level 3 is implemented (see Section 6).
- `NetworkRule.target` accepts an FQDN or wildcard domain (`"example.com"`, `"*.example.com"`) — **IP/CIDR is explicitly not supported** in the current egress MVP. Don't attempt IP-based rules.

### `POST /sandboxes` — response (HTTP 202)

```json
{
  "id": "sbx_abc123",
  "status": { "state": "Running", "reason": "...", "message": "..." },
  "metadata": {},
  "createdAt": "2026-08-15T...",
  "entrypoint": ["..."]
}
```
Note: the create response deliberately omits some fields (per the spec's own description) — if `handbox` needs anything not present here, follow up with `GET /sandboxes/{sandboxId}`.

### `GET /sandboxes/{sandboxId}/endpoints/{port}`

This is how `handbox` resolves the `url` field it needs to hand back to OpenHands. The agent-server image listens on port `8000` (confirmed from the running container's port mappings observed in this project's earlier local testing — `8002/tcp`, and `0.0.0.0:X->8000/tcp` etc. were the ports seen on `oh-agent-server-*` containers). Query param `use_server_proxy` (bool) controls whether you get a server-proxied URL or a direct one — **verify which is actually reachable from `handbox`'s own network position during implementation** (see Known Unknowns, issue #252 below — this exact kind of reachability problem has bitten a real user in a Docker Compose bridge-network setup).

---

## 6. Network Level → `networkPolicy` Mapping

This is `handbox`'s core policy logic. On every `POST /start` from OpenHands, `handbox` reads the current level from the state file (Section 7) and builds the corresponding OpenSandbox `networkPolicy`:

| Level | State file value | `networkPolicy` sent to OpenSandbox |
|---|---|---|
| ⚪ **Ask (default / unset)** | `ask`, missing, or any unrecognized value | **No sandbox is created.** `/start` returns an error (see below) rather than guessing or falling back to a default level. Every level value is single-use — consumed and cleared back to `ask` by the `/start` call that reads it (see below), not by anything happening later. |
| 🔴 Offline | `offline` | **Omit the field entirely.** Per Section 5, no `networkPolicy` = no egress sidecar at all. Since the sandbox still needs to reach Ollama, this relies on Ollama being reachable via whatever default networking OpenSandbox gives the sandbox — **verify this during implementation** (see Known Unknowns). If it isn't reachable by default, Level 0 will need a minimal `networkPolicy` allowing only the Ollama host's address, not a fully bare sandbox. |
| 🟡 GitHub-only | `github` | `{"defaultAction": "deny", "egress": [{"action":"allow","target":"github.com"}, {"action":"allow","target":"api.github.com"}, {"action":"allow","target":"*.githubusercontent.com"}, {"action":"allow","target":"codeload.github.com"}]}` |
| 🟢 Research | `research` | Same as GitHub-only, plus additional allowed targets for documentation/search — starter list: `*.python.org`, `pypi.org`, `*.npmjs.org`, `stackoverflow.com`, `developer.mozilla.org`, `en.wikipedia.org`. **Treat this list as configurable, not hardcoded** — expose it via `handbox`'s own config file so it can be extended without a rebuild. |
| 🟣 Full | `full` | Omit the field entirely (same as Offline — no sidecar, unrestricted networking) — matches the "rare, deliberate, no filtering" intent from the original design. |

### The "ask" state — explicit confirmation before each conversation

`handbox` cannot interactively prompt mid-request — `/start` is a synchronous HTTP call from `openhands-app` expecting an immediate response, not something that can pause and wait for a person to answer a prompt. So "ask" works as a **hard stop, not a live prompt**, and — critically — the level is a **single-use permission slip, consumed at start time, not reverted at stop time.**

- **The state file's default value is `ask`.**
- If `/start` is called while the state is `ask` (or missing/unrecognized), `handbox` returns a clear HTTP error — e.g. `409 Conflict` with a body like `{"error": "No network level selected. Run: ai-level <offline|github|research|full>"}`. OpenHands' UI will surface this as a visible error on the conversation (matching the kind of "Sandbox entered error state" errors seen during this project's own OpenHands troubleshooting) rather than silently defaulting to something.
- **When `/start` succeeds, `handbox` immediately consumes the level: it reads the current value, uses it to build that sandbox's `networkPolicy`, and atomically resets the state file back to `ask` — all within the same request handler, before returning a response.** This must happen on every successful `/start`, not on `/stop`.

  **Why not reset-on-stop (a design considered and rejected during review):** resetting when a conversation *ends* doesn't guarantee a fresh choice before the *next* conversation *starts* — those two events aren't reliably ordered. Concretely: if conversation 1 starts at `github` and conversation 2 starts before conversation 1 stops (or even after, if nobody runs `ai-level` in between), conversation 2 would silently read whatever value conversation 1 left behind — exactly the implicit carryover the whole design exists to prevent. Consuming the level at `/start` time instead ties the requirement directly to the event that actually matters (a new sandbox being created), independent of how many other conversations are concurrently running or already stopped. It's also simpler and more robust: it doesn't depend on `/stop` firing reliably at all (a sandbox that's killed abruptly rather than cleanly stopped would leave a reset-on-stop design stuck on a stale level forever; consume-on-start has no such failure mode).
  - **Concurrency requirement:** the read-then-clear sequence in the `/start` handler must be atomic (file lock or equivalent) — two `/start` calls arriving close together must not both be able to read the same level before either clears it. Without this, two concurrent conversations could both silently consume a single `ai-level` call meant for just one of them.
  - **Practical effect:** starting two concurrent conversations at the *same* level requires running `ai-level <level>` once before each `/start` — mildly more friction than before, but this is the friction the design is supposed to have; the old reset-on-stop version only *looked* like it enforced this.
  - Make this behavior a config toggle (`single_use_level: true` as the v1 default) rather than hardcoding it, so a user who genuinely wants "persists until manually changed" instead can opt out with one config line — clearly documented as reintroducing the implicit-carryover trade-off as a deliberate choice, not a default.

**Security requirement, non-negotiable:** the state file must live on the host filesystem, **never** bind-mounted into `openhands-app` or any container the agent can reach. `handbox` reads it fresh on every `/start` call (no caching across requests) and **imposes** the level — it must not accept or trust any network-related hint from the incoming OpenHands request itself, even if one is technically present in the payload.

---

## 7. `handbox`'s Own Config and Security Posture

- Config via a file (`handbox.toml` or environment variables — implementer's choice, but document whichever is chosen in the README) covering: listen address/port, OpenSandbox API URL + key, state file path, the research-level allowlist.
- `handbox` runs as a plain, long-lived process/container — **not** Kata, **not** gVisor for v1 (see project context: cheap to add gVisor later, not urgent, since this is trusted code with a narrow attack surface, not an untrusted-code execution boundary).
- `handbox` needs **no Docker socket access** — it only needs outbound HTTP reachability to OpenSandbox's API. This is a real, meaningful improvement over OpenHands' default architecture, which requires `openhands-app` to hold `/var/run/docker.sock` directly.
- Validate the `SANDBOX_API_KEY` OpenHands sends on every request against `handbox`'s own configured key before processing anything.

---

## 8. Out of Scope for v1

Be explicit about NOT building these, to keep the first version scoped:
- Kubernetes backend (OpenSandbox supports it; not needed for this single-desktop use case)
- Snapshot/restore support (`POST /sandboxes/{id}/snapshots`, etc.)
- Credential proxy / MITM injection (`CredentialProxyConfig` in the OpenSandbox spec — a real feature, not needed yet)
- Persistence of `handbox`'s session-id mapping across its own restarts (in-memory is fine for v1)
- Multi-tenant auth beyond a single static API key
- Any GUI — `handbox` is a headless service plus the small `ai-level` CLI companion

---

## 9. License and Public Repo Hygiene

- **LICENSE file: MIT.** Matches the K8s reference implementation this project is patterned after; compatible with both OpenSandbox (Apache 2.0) and OpenHands (MIT) as integration targets. If OpenSandbox's Go SDK ends up vendored directly rather than hand-rolling the HTTP client, preserve its Apache 2.0 notice per standard Apache-in-MIT-project practice — this does not obligate relicensing the whole project.
- **`.gitignore`:** create it with the exact contents given in Appendix A — already designed to cover secrets, the state file, Go build artifacts, and editor/OS cruft correctly; no need to redesign it.
- **Before first public push, scrub:** real machine hostname/username if they appear anywhere in example configs or comments; any real API keys/tokens even as copy-pasted "example" values; real repo/org names in test fixtures — use clearly-fake placeholders (`your-hostname`, `youruser/your-repo`) throughout.

---

## 10. README.md — required sections

Keep it honest about project maturity — mark it clearly as an early/unreleased project. Required sections: what it is (one paragraph, similar framing to Section 1 above), architecture diagram (ASCII is fine, matches Section 1), prerequisites (Section 2), configuration (Section 7), the network-level table (Section 6), and a "Status" callout noting this is a from-scratch implementation, not a fork of the Kubernetes reference project (credit that project as the source of the verified API contract, per good open-source practice).

---

## 11. Known Unknowns — verify these during implementation, do not assume

These are flagged because they were identified as open questions during design, not because they're expected problems — resolve by testing directly against a real local OpenSandbox instance, not by guessing:

1. **Sandbox-to-Ollama reachability under Level 0's "no networkPolicy" setting.** Confirm what network a sandbox created with no `networkPolicy` field actually lands on by default in OpenSandbox's Docker mode, and whether that network can reach the host's Ollama container. If not, Level 0 needs a minimal policy allowing just Ollama's address, not a fully bare sandbox.
2. **Docker Compose bridge-network reachability gotcha** — a real, previously-reported OpenSandbox issue: connecting to a sandbox's mapped port from another container on the same Compose network can fail (connection refused) despite the port mapping showing correctly via `docker ps`. Test `handbox`'s actual connection to a sandbox's `endpoints/{port}` URL early, since this exact scenario (one container reaching another's sandbox-mapped port within a Compose-managed bridge network) is what `handbox` will be doing constantly.
3. **`use_server_proxy` query param behavior** on the endpoints API — confirm empirically which setting actually produces a URL reachable from `handbox`'s network position.
4. **Whether `ghcr.io/openhands/agent-server`'s expected startup command/entrypoint maps cleanly onto OpenSandbox's generic image-based sandbox creation** — OpenSandbox wasn't purpose-built around this specific image, so confirm the agent-server actually starts and becomes reachable on its expected port when launched this way, rather than assuming compatibility.

---

## 12. Acceptance Criteria for v1

- `handbox` builds as a single static binary named `handbox` via `go build -o handbox ./cmd/handbox`.
- With OpenSandbox running locally (Kata-configured) and `handbox` pointed at it, setting `SANDBOX_REMOTE_RUNTIME_API_URL`/`SANDBOX_API_KEY` on a real `openhands-app` instance and starting a new conversation successfully creates a working sandbox (verify: OpenHands' UI reaches "ready," not stuck on "Waiting for runtime to start...").
- Switching the state file between `offline`/`github`/`research`/`full` and starting a new conversation for each demonstrably changes what the sandbox can reach (verify by asking the agent to fetch a disallowed URL and confirming it fails for restricted levels, succeeds for `full`).
- Calling `/start` while the state file is `ask` (including a fresh install's default) returns a clear error, not a silently-created sandbox at some default level.
- With `single_use_level` enabled (the v1 default): after one `ai-level <level>` call and one successful `/start`, a second `/start` — issued without an intervening `ai-level` call, and **without stopping the first conversation** — reproduces the `ask`-state error. This is the specific concurrent-conversation scenario that motivated consuming the level at start time rather than resetting it at stop time; test it explicitly, not just the sequential case.
- Two concurrent conversations can still be started at different levels by calling `ai-level <level>` once before each `/start` — confirms the atomic read-then-clear doesn't block legitimate concurrent use, only silent reuse.
- `/pause` and `/resume` work end-to-end, not just `/start`/`/stop`.
- Unit tests cover the level→policy mapping (Section 6) and the OpenHands↔OpenSandbox request/response translation (Section 4/5) independent of a live OpenSandbox instance (mock the HTTP client).

---

## Appendix A: `.gitignore` (exact contents — create this file verbatim)

```
# --- Secrets / credentials ---
*.env
.env
.env.*
**/session_api_key*
**/api_key*
**/*_key.json
**/*.pem
**/*.key
config.local.*
config.local.toml
config.local.yaml
secrets/

# --- Runtime state (network-level state file, never commit) ---
/state/
level.state
*.state

# --- Build artifacts ---
bin/
dist/
handbox
handbox-*
*.exe
*.test
*.out

# --- Go ---
vendor/
go.work
go.work.sum

# --- Logs / local dev ---
*.log
logs/
tmp/
*.tmp

# --- Editor / IDE ---
.vscode/
.idea/
*.swp
*.swo
*~

# --- OS cruft ---
.DS_Store
Thumbs.db

# --- Local testing artifacts ---
*.local.json
*.local.yaml
test-output/
coverage.out
coverage.html

# --- Container/OpenSandbox local artifacts ---
.opensandbox/
*.sandbox-state
```
