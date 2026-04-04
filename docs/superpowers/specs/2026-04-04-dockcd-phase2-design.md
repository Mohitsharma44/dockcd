# dockcd Phase 2 Design Spec

**Date:** 2026-04-04
**Status:** Draft
**Scope:** Hooks, CLI, notifications, multi-branch, trace spans, Dockerfile CI

---

## 1. Priority Tiers

| Tier | Feature |
|------|---------|
| Must have | Pre/post deploy hooks (SOPS via hooks), CLI + API surface, force-deploy |
| Should have | Slack + webhook notifications, multi-branch support |
| Nice to have | Dockerfile CI testing, OpenTelemetry trace spans |

---

## 2. Config Changes

The `gitops.yaml` config expands with hooks, notifications, per-stack branch overrides, and env var substitution for sensitive values.

```yaml
repo: git@github.com:user/homeops.git
base_path: docker/stacks
branch: main

notifications:
  - name: slack-alerts
    type: slack
    url: "${SLACK_WEBHOOK_URL}"
    events:
      - deploy_success
      - deploy_failure
      - rollback_success
      - rollback_failure
      - reconcile_start
      - reconcile_complete
      - force_deploy
      - stack_suspended
      - stack_resumed
      - hook_failure
  - name: generic-webhook
    type: webhook
    url: "${WEBHOOK_URL}"
    events:
      - deploy_failure
      - rollback_failure

hosts:
  server04:
    stacks:
      - name: traefik
        path: server04/traefik
        branch: canary
        depends_on: []
        auto_rollback: true
        health_check_timeout: 60s
        post_deploy_delay: 30s
        hooks:
          pre_deploy:
            command: "sops-decrypt.sh"
            timeout: 30s
          post_deploy:
            command: "verify-routes.sh"
            timeout: 30s
```

### Env var substitution

All string values in config support `${VAR}` expansion via `os.ExpandEnv` at load time. Sensitive values (webhook URLs) are stored in environment variables, sourced from systemd `EnvironmentFile=`, container env, or a SOPS-decrypted `.env` on the host.

### Validation additions

- `notifications[].type` must be `slack` or `webhook`
- `notifications[].events` must be valid event type enums
- `notifications[].url` must be non-empty after env var expansion
- `hooks.pre_deploy.command` and `hooks.post_deploy.command` must be non-empty strings if the hook block is present
- `hooks.*.timeout` defaults to 30s, must be positive duration
- `branch` per-stack defaults to top-level `branch`, which defaults to repo's default branch

---

## 3. Pre/Post Deploy Hooks

### Lifecycle

```
pre_deploy hook -> docker compose pull -> docker compose up -d -> post_deploy hook -> health check
```

### Execution model

- Hooks run via `sh -c <command>` with working directory set to the stack's path in the repo clone
- Environment variables injected: `DOCKCD_STACK_NAME`, `DOCKCD_COMMIT`, `DOCKCD_REPO_DIR`, `DOCKCD_BRANCH`
- Hooks inherit the dockcd process environment (e.g., `SOPS_AGE_KEY_FILE`)
- Stdout/stderr captured and logged (info for stdout, warn for stderr)
- Configurable timeout per hook, default 30s

### Failure behavior

- **Pre-deploy failure** (non-zero exit): skip deploy, mark stack failed, emit `hook_failure` event, notify
- **Post-deploy failure**: log warning, emit `hook_failure` event, notify, but do NOT rollback (compose is already running)

### Rollback interaction

- Pre-deploy hooks run again during rollback (the old commit's stack dir may also need SOPS decryption)
- Hooks must be idempotent

### SOPS integration

dockcd does not know about SOPS. The `sops-decrypt.sh` hook (user-provided) handles decryption:

```bash
#!/bin/bash
export SOPS_AGE_KEY_FILE="${SOPS_AGE_KEY_FILE:-/etc/sops/age/keys.txt}"
for f in *.sops.env; do
    [ -f "$f" ] && sops -d "$f" > "${f/.sops.env/.env}"
done
for f in *.sops.json; do
    [ -f "$f" ] && sops -d "$f" > "${f/.sops.json/.json}"
done
```

Decrypted files are left in place (option A). The repo clone directory is permission-restricted (0700, dockcd user). This mirrors Flux's approach where decrypted secrets exist in-cluster guarded by RBAC.

Requirements: `sops` and `age` must be installed on the host (or in the dockcd container image).

---

## 4. CLI + API Surface

### Single binary, subcommand model

`dockcd` is one binary. `dockcd serve` (or no subcommand for backward compat) runs the daemon. All other subcommands are CLI clients that talk to the server over HTTP.

### Command structure

Follows Flux's `<verb> <resource> [name]` pattern:

| Command | API Endpoint | Method | Description |
|---------|-------------|--------|-------------|
| `dockcd serve` | -- | -- | Start daemon |
| `dockcd get stacks` | `GET /api/v1/stacks` | GET | List all stacks with status |
| `dockcd get stack <name>` | `GET /api/v1/stacks/{name}` | GET | Single stack detail |
| `dockcd reconcile stacks` | `POST /api/v1/reconcile` | POST | Force reconcile all (async) |
| `dockcd reconcile stack <name>` | `POST /api/v1/reconcile/{name}` | POST | Force reconcile one (async) |
| `dockcd suspend stack <name>` | `POST /api/v1/stacks/{name}/suspend` | POST | Pause reconciliation |
| `dockcd resume stack <name>` | `POST /api/v1/stacks/{name}/resume` | POST | Resume reconciliation |
| `dockcd logs` | `GET /api/v1/logs` | GET (SSE) | Stream server logs |
| `dockcd stats` | `GET /metrics` | GET | Formatted metrics summary |
| `dockcd version` | `GET /api/v1/version` | GET | Client + server version |

### CLI connection

- `--server` flag, defaults to `http://localhost:9092`
- `DOCKCD_SERVER` env var as fallback
- No auth

### Reconcile UX (Flux-style streaming)

Reconcile is async on the server. The CLI opens an SSE connection to stream progress:

```
$ dockcd reconcile stack traefik
> triggering reconciliation for stack traefik
OK pull complete (abc123f -> def456a)
*  running pre-deploy hook: sops-decrypt.sh
OK pre-deploy hook completed
*  deploying traefik
OK health check passed
OK reconciliation finished in 34s
```

Error case:
```
$ dockcd reconcile stack traefik
> triggering reconciliation for stack traefik
OK pull complete (abc123f -> def456a)
*  deploying traefik
X  health check failed: container traefik-proxy unhealthy after 60s
*  rolling back to abc123f
OK rollback successful
X  reconciliation failed (rolled back)
```

Flags:
- `--no-wait` — return 202 immediately without streaming
- `--timeout` — max wait time (default 5m)

### Get stacks output

```
NAME         BRANCH   STATUS       HEALTH    LAST DEPLOY           COMMIT
traefik      main     Ready        Healthy   2026-04-04 12:00:00   abc123f
vaultwarden  canary   Ready        Healthy   2026-04-04 11:45:00   def456a
frigate      main     Suspended    --        2026-04-04 10:30:00   789beef
alloy        main     Failed       Degraded  2026-04-04 09:15:00   ccc0000
```

- Unicode status indicators in color: `✓` green (Ready), `✗` red (Failed), `⏸` yellow (Suspended), `↻` blue (Reconciling)
- `--output json` for machine-readable output
- `--watch` polls and refreshes the table
- `--no-color` disables color (respects `NO_COLOR` env var)
- `--host` filters stacks by host

### Exit codes

- 0: success
- 1: failure (deploy failed, stack not found, etc.)
- 2: usage error (bad flags, missing args)

### Backward compatibility

Running `dockcd` with no args starts the daemon (current behavior). Existing systemd units and Dockerfiles continue to work unchanged.

---

## 5. Server API Details

### Async reconcile

```
POST /api/v1/reconcile/traefik
-> 202 Accepted
{
  "id": "rec-1712234567",
  "status": "queued",
  "stack": "traefik"
}
```

Progress streamed via SSE:
```
GET /api/v1/reconcile/rec-1712234567/events
-> Content-Type: text/event-stream

data: {"type": "pull_complete", "old_commit": "abc123", "new_commit": "def456"}
data: {"type": "hook_start", "hook": "pre_deploy", "command": "sops-decrypt.sh"}
data: {"type": "hook_complete", "hook": "pre_deploy"}
data: {"type": "deploy_start", "stack": "traefik"}
data: {"type": "health_check_passed", "stack": "traefik"}
data: {"type": "complete", "status": "success", "duration_seconds": 34}
```

### Stacks endpoint

```
GET /api/v1/stacks
-> 200 OK
{
  "stacks": [
    {
      "name": "traefik",
      "host": "server04",
      "branch": "main",
      "status": "Ready",
      "health": "Healthy",
      "last_deploy": "2026-04-04T12:00:00Z",
      "commit": "abc123f",
      "suspended": false
    }
  ]
}
```

### Suspend / resume

```
POST /api/v1/stacks/traefik/suspend -> 200 OK
POST /api/v1/stacks/traefik/resume  -> 200 OK
```

Both return the updated stack object.

### Version

```
GET /api/v1/version
-> 200 OK
{
  "server": "0.2.0",
  "go": "go1.25"
}
```

CLI prints: `dockcd client: 0.2.0, server: 0.2.0`

### Logs streaming

```
GET /api/v1/logs
-> Content-Type: text/event-stream

data: {"timestamp": "...", "level": "info", "msg": "polling repo", ...}
data: {"timestamp": "...", "level": "info", "msg": "deploying traefik", ...}
```

The server adds an slog handler that tees log records to an internal ring buffer. SSE clients consume from this buffer.

### Existing endpoints unchanged

- `GET /healthz` — health check (unchanged)
- `GET /metrics` — Prometheus metrics (unchanged)

---

## 6. Notification System

### Architecture

```
Deploy pipeline events
        |
  EventBus (channel)
        |
  Dispatcher (goroutine)
        |
  +-------------+
  | Filter by   |
  | event type  |
  +------+------+
         |---> SlackNotifier.Send(event)   -> Slack Block Kit POST
         +---> WebhookNotifier.Send(event) -> Generic JSON POST
```

### Event types (enum)

- `deploy_success`
- `deploy_failure`
- `rollback_success`
- `rollback_failure`
- `reconcile_start`
- `reconcile_complete`
- `force_deploy`
- `stack_suspended`
- `stack_resumed`
- `hook_failure`

### Notifier interface

```go
type Event struct {
    Type      EventType
    Timestamp time.Time
    Stack     string
    Host      string
    Commit    string
    Branch    string
    Message   string
    Metadata  map[string]string
}

type Notifier interface {
    Name() string
    Send(ctx context.Context, event Event) error
}
```

### Generic webhook payload

```json
{
  "event": "deploy_failure",
  "timestamp": "2026-04-04T12:00:00Z",
  "stack": "traefik",
  "host": "server04",
  "commit": "abc123",
  "branch": "main",
  "message": "health check timed out after 60s",
  "metadata": {}
}
```

### Slack formatting

- Color-coded attachments: green (success), red (failure), yellow (rollback/warning)
- Stack name, commit (short hash), branch, host, timestamp
- Error detail in a code block when applicable
- Posted as Slack Block Kit payload to the webhook URL

### Delivery guarantees

- Fire-and-forget goroutine per notification (never blocks deploy pipeline)
- 3 retries with exponential backoff (1s, 2s, 4s) on HTTP failure
- Failed notifications increment `dockcd_notification_failures_total` metric and log a warning
- Notification errors never fail a deploy

---

## 7. Multi-Branch Polling

### Config

Per-stack `branch` field, defaults to top-level `branch`, which defaults to the repo's default branch.

### Poller changes

- Poller discovers all unique branches referenced by stack configs
- `git fetch origin` fetches all remote branches (already does this)
- Diff runs per unique branch: `git diff <last_hash> origin/<branch> --name-only`
- Changed stacks are matched against their configured branch

### File access at deploy time

- Working tree stays on the default branch
- For stacks on the default branch: files read directly from working tree (current behavior)
- For stacks on override branches: `git archive origin/<branch> -- <stack_path>` extracts to a temp directory at deploy time

### State file changes

```json
{
  "branches": {
    "main": "abc123",
    "canary": "def456"
  },
  "last_successful_commits": {
    "traefik": {
      "branch": "main",
      "commit": "abc123"
    },
    "vaultwarden": {
      "branch": "canary",
      "commit": "def456"
    }
  },
  "suspended_stacks": ["frigate"]
}
```

### Backward compatibility

The state file reader detects the old format (single `last_hash` string or flat `last_successful_commits`) and auto-migrates to the new structure on next write.

---

## 8. Suspend / Resume

- Suspension state persisted in the state file (`suspended_stacks` array)
- Suspended stacks are skipped during reconciliation (poll still happens, changes are detected but not deployed)
- When resumed, the stack is marked as "needs reconcile" internally. The next poll cycle deploys it regardless of whether the branch hash has changed (to catch changes that occurred while suspended). After that first reconcile, normal change-detection resumes. Alternatively, the user can `dockcd reconcile stack <name>` to force it immediately.
- Suspending an already-suspended stack is a no-op (200 OK)
- Resuming a non-suspended stack is a no-op (200 OK)

---

## 9. OpenTelemetry Trace Spans

The OTel tracing infrastructure is already wired (gRPC exporter, service name, noop fallback). Add spans to key operations:

- `reconcile` — root span per reconcile cycle
- `git.fetch` — git fetch + diff
- `deploy.stack` — per-stack deploy (child of reconcile)
- `hook.pre_deploy` / `hook.post_deploy` — per-hook execution
- `health_check` — per-stack health check
- `rollback` — rollback operation
- `notification.send` — per-notification delivery

Span attributes: `stack.name`, `stack.branch`, `commit`, `host`, `hook.command`, `notification.type`.

---

## 10. Dockerfile CI

Add a `docker build` step to `.github/workflows/ci.yaml`:

```yaml
- name: Docker build
  run: docker build -t dockcd:ci .
```

Runs after tests pass. Validates the multi-stage build compiles and produces a valid image. No push — just build verification.

The Dockerfile will also need updating to include `sops` and `age` binaries if the container image is the deployment target (matching the Periphery pattern). This is optional — users can also install sops on the host and run dockcd via systemd.

---

## 11. Package / File Layout

### New packages

```
internal/hooks/         # Hook execution (sh -c, timeout, env vars)
internal/notify/        # EventBus, Notifier interface, SlackNotifier, WebhookNotifier
internal/client/        # HTTP client for CLI commands
```

### Modified packages

```
internal/config/        # New fields: hooks, notifications, branch
internal/git/           # Multi-branch tracking, state file changes
internal/deploy/        # Hook integration in deploy lifecycle
internal/reconciler/    # Event emission, suspend/resume, async reconcile
internal/server/        # New API endpoints, SSE streaming, log tee
cmd/dockcd/             # Subcommand router, CLI commands
```

### New files in cmd/dockcd/

```
cmd/dockcd/main.go      # Refactored: subcommand router
cmd/dockcd/serve.go     # Daemon startup (extracted from current main.go)
cmd/dockcd/reconcile.go # CLI: reconcile stack(s)
cmd/dockcd/get.go       # CLI: get stacks / get stack <name>
cmd/dockcd/suspend.go   # CLI: suspend stack
cmd/dockcd/resume.go    # CLI: resume stack
cmd/dockcd/logs.go      # CLI: stream logs
cmd/dockcd/stats.go     # CLI: metrics summary
cmd/dockcd/version.go   # CLI: version
```
