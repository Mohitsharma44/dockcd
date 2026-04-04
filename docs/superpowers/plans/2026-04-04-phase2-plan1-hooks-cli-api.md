# Phase 2 Plan 1: Hooks, CLI & API Surface, Force-Deploy

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add pre/post deploy hooks (enabling SOPS decryption), refactor dockcd into a single-binary CLI+daemon with Flux-style UX, and implement async force-deploy via API.

**Architecture:** The current `main.go` becomes a subcommand router. `dockcd serve` runs the daemon (existing behavior). New CLI subcommands (`get`, `reconcile`, `suspend`, `resume`, `logs`, `stats`, `version`) are thin HTTP clients against new `/api/v1/*` endpoints. Hooks are shell commands executed before/after `docker compose up` in the deploy lifecycle. Force-deploy triggers an async reconcile via POST, with SSE streaming for progress. Suspend/resume state is persisted in the existing JSON state file.

**Tech Stack:** Go stdlib (flag, net/http, encoding/json, os/exec), SSE via chunked HTTP, existing OTel + Prometheus stack. No new dependencies.

**Conventions (read before starting any task):**
- Follow existing patterns: table-driven tests, `writeTemp` helpers, `mockPoller`/`mockRunner` fakes
- All tests use `-race` flag (CI enforces this)
- Integration tests use `//go:build integration` tag and live in `*_integration_test.go`
- Error wrapping: `fmt.Errorf("context: %w", err)`
- Structured logging via `slog`
- Config validation in `validate()` function
- `deploy.Stack` is the deploy-time type, `config.Stack` is the config-time type

---

## File Map

### New files
```
internal/hooks/hooks.go              # Hook execution: Run(ctx, HookConfig, stackDir, env) error
internal/hooks/hooks_test.go         # Unit tests for hook execution
internal/notify/event.go             # Event types and EventBus
internal/notify/event_test.go        # EventBus tests
internal/client/client.go            # HTTP client for CLI commands
internal/client/client_test.go       # Client tests with httptest
cmd/dockcd/serve.go                  # Daemon startup (extracted from main.go)
cmd/dockcd/get.go                    # CLI: get stacks / get stack <name>
cmd/dockcd/reconcile.go              # CLI: reconcile stack(s)
cmd/dockcd/suspend.go                # CLI: suspend/resume stack
cmd/dockcd/logs.go                   # CLI: stream logs
cmd/dockcd/stats.go                  # CLI: formatted metrics summary
cmd/dockcd/version.go                # CLI: client + server version
cmd/dockcd/output.go                 # Shared CLI output formatting (table, color, JSON)
```

### Modified files
```
internal/config/config.go            # Add Hook, Notification, Branch fields to Stack/Config
internal/config/config_test.go       # Tests for new config fields and validation
internal/deploy/graph.go             # Add Hooks field to deploy.Stack
internal/deploy/compose.go           # Integrate hook execution into Deploy lifecycle
internal/deploy/compose_test.go      # Tests for hooks in deploy
internal/git/poller.go               # Add SuspendedStacks to State, add suspend/resume methods
internal/git/poller_test.go          # Tests for suspend state persistence
internal/reconciler/reconciler.go    # Suspend filtering, async reconcile, event emission, hook env vars
internal/reconciler/reconciler_test.go # Tests for suspend, async reconcile
internal/reconciler/reconciler_integration_test.go # Integration tests for hooks + suspend
internal/server/server.go            # New API endpoints, SSE streaming, log ring buffer
internal/server/server_test.go       # Tests for all new endpoints
cmd/dockcd/main.go                   # Refactor to subcommand router
examples/gitops.yaml                 # Update with hook and notification examples
```

---

## Task 1: Hook Execution Package

**Files:**
- Create: `internal/hooks/hooks.go`
- Create: `internal/hooks/hooks_test.go`

### Verification Spec

**Functional:**
- `Run()` executes a shell command via `sh -c` in the specified working directory
- `Run()` injects `DOCKCD_STACK_NAME`, `DOCKCD_COMMIT`, `DOCKCD_REPO_DIR`, `DOCKCD_BRANCH` into the command environment
- `Run()` inherits the parent process environment (so `SOPS_AGE_KEY_FILE` etc. are available)
- `Run()` returns nil on exit code 0
- `Run()` returns a descriptive error on non-zero exit code, including stderr output
- `Run()` respects context cancellation (command is killed)
- `Run()` enforces the configured timeout (returns error after timeout)
- `Run()` with zero/unset timeout uses a 30s default
- Stdout and stderr are captured and returned alongside the error for logging

**Non-functional:**
- No goroutine leaks: cancelled/timed-out commands clean up child processes
- No race conditions: safe to call Run() from multiple goroutines concurrently (stateless function)
- Test coverage: every code path (success, failure, timeout, cancel, env vars)

**Edge cases to test:**
- Command that writes to stdout and stderr
- Command that exits with code 1
- Command that hangs (timeout triggers)
- Context cancelled before command finishes
- Empty command string (error)
- Working directory that doesn't exist (error)

---

- [ ] **Step 1: Define the Hook types**

```go
// internal/hooks/hooks.go
package hooks

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"time"
)

// DefaultTimeout is used when no timeout is configured for a hook.
const DefaultTimeout = 30 * time.Second

// Config describes a single hook to execute.
type Config struct {
	Command string
	Timeout time.Duration
}

// Env holds the environment variables injected into hook commands.
type Env struct {
	StackName string
	Commit    string
	RepoDir   string
	Branch    string
}

// Result holds the output from a hook execution.
type Result struct {
	Stdout string
	Stderr string
}
```

- [ ] **Step 2: Write failing tests for Run()**

```go
// internal/hooks/hooks_test.go
package hooks

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestRunSuccess(t *testing.T) {
	dir := t.TempDir()
	cfg := Config{Command: "echo hello", Timeout: 5 * time.Second}
	env := Env{StackName: "web", Commit: "abc123", RepoDir: "/opt/repo", Branch: "main"}

	result, err := Run(context.Background(), cfg, dir, env)
	if err != nil {
		t.Fatalf("expected success, got: %v", err)
	}
	if !strings.Contains(result.Stdout, "hello") {
		t.Errorf("expected stdout to contain 'hello', got %q", result.Stdout)
	}
}

func TestRunFailure(t *testing.T) {
	dir := t.TempDir()
	cfg := Config{Command: "exit 1", Timeout: 5 * time.Second}
	env := Env{StackName: "web", Commit: "abc123", RepoDir: "/opt/repo", Branch: "main"}

	_, err := Run(context.Background(), cfg, dir, env)
	if err == nil {
		t.Fatal("expected error for non-zero exit")
	}
}

func TestRunCapturesStderr(t *testing.T) {
	dir := t.TempDir()
	cfg := Config{Command: "echo errout >&2; exit 1", Timeout: 5 * time.Second}
	env := Env{StackName: "web", Commit: "abc123", RepoDir: "/opt/repo", Branch: "main"}

	result, err := Run(context.Background(), cfg, dir, env)
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(result.Stderr, "errout") {
		t.Errorf("expected stderr to contain 'errout', got %q", result.Stderr)
	}
}

func TestRunInjectsEnvVars(t *testing.T) {
	dir := t.TempDir()
	cfg := Config{
		Command: "echo $DOCKCD_STACK_NAME $DOCKCD_COMMIT $DOCKCD_REPO_DIR $DOCKCD_BRANCH",
		Timeout: 5 * time.Second,
	}
	env := Env{StackName: "traefik", Commit: "def456", RepoDir: "/opt/repo", Branch: "canary"}

	result, err := Run(context.Background(), cfg, dir, env)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	expected := "traefik def456 /opt/repo canary"
	if strings.TrimSpace(result.Stdout) != expected {
		t.Errorf("expected %q, got %q", expected, strings.TrimSpace(result.Stdout))
	}
}

func TestRunTimeout(t *testing.T) {
	dir := t.TempDir()
	cfg := Config{Command: "sleep 60", Timeout: 50 * time.Millisecond}
	env := Env{}

	_, err := Run(context.Background(), cfg, dir, env)
	if err == nil {
		t.Fatal("expected timeout error")
	}
	if !strings.Contains(err.Error(), "signal") && !strings.Contains(err.Error(), "killed") && !strings.Contains(err.Error(), "context deadline exceeded") {
		t.Errorf("expected timeout-related error, got: %v", err)
	}
}

func TestRunContextCancel(t *testing.T) {
	dir := t.TempDir()
	cfg := Config{Command: "sleep 60", Timeout: 10 * time.Second}
	env := Env{}

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()

	_, err := Run(ctx, cfg, dir, env)
	if err == nil {
		t.Fatal("expected error from cancelled context")
	}
}

func TestRunEmptyCommand(t *testing.T) {
	dir := t.TempDir()
	cfg := Config{Command: "", Timeout: 5 * time.Second}
	env := Env{}

	_, err := Run(context.Background(), cfg, dir, env)
	if err == nil {
		t.Fatal("expected error for empty command")
	}
}

func TestRunBadDirectory(t *testing.T) {
	cfg := Config{Command: "echo hi", Timeout: 5 * time.Second}
	env := Env{}

	_, err := Run(context.Background(), cfg, "/nonexistent/dir/"+t.Name(), env)
	if err == nil {
		t.Fatal("expected error for nonexistent directory")
	}
}

func TestRunDefaultTimeout(t *testing.T) {
	dir := t.TempDir()
	cfg := Config{Command: "echo ok", Timeout: 0} // zero means use default
	env := Env{}

	result, err := Run(context.Background(), cfg, dir, env)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(result.Stdout, "ok") {
		t.Errorf("expected stdout 'ok', got %q", result.Stdout)
	}
}
```

- [ ] **Step 3: Run tests to verify they fail**

Run: `cd /Users/mohitsharma44/devel/worktrees/dockcd-planning-7e7 && go test -race -v ./internal/hooks/`
Expected: Compilation error — `Run` function doesn't exist yet

- [ ] **Step 4: Implement Run()**

```go
// Add to internal/hooks/hooks.go after the type definitions:

// Run executes a hook command via sh -c in the given directory.
// It injects dockcd environment variables and enforces a timeout.
func Run(ctx context.Context, cfg Config, dir string, env Env) (Result, error) {
	if cfg.Command == "" {
		return Result{}, fmt.Errorf("hook command is empty")
	}

	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = DefaultTimeout
	}

	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, "sh", "-c", cfg.Command)
	cmd.Dir = dir

	// Inherit parent environment and add dockcd-specific vars.
	cmd.Env = append(cmd.Environ(),
		"DOCKCD_STACK_NAME="+env.StackName,
		"DOCKCD_COMMIT="+env.Commit,
		"DOCKCD_REPO_DIR="+env.RepoDir,
		"DOCKCD_BRANCH="+env.Branch,
	)

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()

	result := Result{
		Stdout: stdout.String(),
		Stderr: stderr.String(),
	}

	if err != nil {
		return result, fmt.Errorf("hook %q: %w\nstderr: %s", cfg.Command, err, stderr.String())
	}
	return result, nil
}
```

- [ ] **Step 5: Run tests to verify they pass**

Run: `cd /Users/mohitsharma44/devel/worktrees/dockcd-planning-7e7 && go test -race -v ./internal/hooks/`
Expected: All 8 tests PASS

- [ ] **Step 6: Commit**

```bash
cd /Users/mohitsharma44/devel/worktrees/dockcd-planning-7e7
git add internal/hooks/hooks.go internal/hooks/hooks_test.go
git commit -m "feat: add hook execution package with timeout and env injection"
```

---

## Task 2: Config — Add Hook, Branch, and Notification Fields

**Files:**
- Modify: `internal/config/config.go`
- Modify: `internal/config/config_test.go`
- Modify: `examples/gitops.yaml`

### Verification Spec

**Functional:**
- `Config` gains a top-level `Branch` field (string, optional, defaults to "")
- `Config` gains a top-level `Notifications` field (slice of `NotificationConfig`)
- `Stack` gains a `Branch` field (string, optional)
- `Stack` gains a `Hooks` field containing optional `PreDeploy` and `PostDeploy` hook configs
- `HookConfig` has `Command` (string) and `Timeout` (duration, default 30s)
- `NotificationConfig` has `Name`, `Type` (slack/webhook), `URL`, `Events` ([]string)
- `os.ExpandEnv` is applied to the raw YAML bytes before unmarshaling (for `${VAR}` substitution)
- Validation rejects: hook with empty command, notification with invalid type, notification with empty URL after expansion, notification with invalid event names
- Validation accepts: stacks with no hooks (optional), stacks with only pre_deploy, stacks with only post_deploy
- `Stack.Branch` defaults to `Config.Branch` at resolve time (not in config.go — that's the reconciler's job, config just stores the raw value)
- Backward compatible: existing configs without hooks/notifications/branch still load correctly

**Non-functional:**
- No new dependencies added
- Existing tests still pass unchanged
- Table-driven test pattern maintained

**Edge cases to test:**
- Hook with timeout "0s" (valid — means use default)
- Hook with only pre_deploy, no post_deploy
- Hook with only post_deploy, no pre_deploy
- Notification URL containing `${UNSET_VAR}` expands to empty string → validation error
- Config with no notifications section (valid)
- Config with empty notifications list (valid)
- Invalid notification type (e.g., "discord") → validation error
- Invalid event name → validation error
- Env var substitution in non-URL fields works too (e.g., repo URL)

---

- [ ] **Step 1: Write failing tests for new config fields**

Add to `internal/config/config_test.go`:

```go
func TestLoadHooksConfig(t *testing.T) {
	tests := []struct {
		name    string
		yaml    string
		wantErr string
	}{
		{
			name: "valid hooks",
			yaml: `
repo: git@github.com:user/infra.git
base_path: /opt/stacks
hosts:
  server01:
    stacks:
      - name: app
        path: app/
        hooks:
          pre_deploy:
            command: "sops-decrypt.sh"
            timeout: 10s
          post_deploy:
            command: "cleanup.sh"
`,
			wantErr: "",
		},
		{
			name: "pre_deploy only",
			yaml: `
repo: git@github.com:user/infra.git
base_path: /opt/stacks
hosts:
  server01:
    stacks:
      - name: app
        path: app/
        hooks:
          pre_deploy:
            command: "decrypt.sh"
`,
			wantErr: "",
		},
		{
			name: "post_deploy only",
			yaml: `
repo: git@github.com:user/infra.git
base_path: /opt/stacks
hosts:
  server01:
    stacks:
      - name: app
        path: app/
        hooks:
          post_deploy:
            command: "notify.sh"
`,
			wantErr: "",
		},
		{
			name: "hook with empty command",
			yaml: `
repo: git@github.com:user/infra.git
base_path: /opt/stacks
hosts:
  server01:
    stacks:
      - name: app
        path: app/
        hooks:
          pre_deploy:
            command: ""
`,
			wantErr: "hook command is required",
		},
		{
			name: "no hooks (backward compat)",
			yaml: `
repo: git@github.com:user/infra.git
base_path: /opt/stacks
hosts:
  server01:
    stacks:
      - name: app
        path: app/
`,
			wantErr: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := writeTemp(t, tt.yaml)
			_, err := Load(path)
			if tt.wantErr != "" {
				if err == nil {
					t.Fatalf("expected error containing %q, got nil", tt.wantErr)
				}
				if !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("expected error containing %q, got %q", tt.wantErr, err.Error())
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}

func TestLoadHookTimeout(t *testing.T) {
	yaml := `
repo: git@github.com:user/infra.git
base_path: /opt/stacks
hosts:
  server01:
    stacks:
      - name: app
        path: app/
        hooks:
          pre_deploy:
            command: "decrypt.sh"
            timeout: 10s
`
	path := writeTemp(t, yaml)
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	stack := cfg.Hosts["server01"].Stacks[0]
	if stack.Hooks == nil || stack.Hooks.PreDeploy == nil {
		t.Fatal("expected hooks.pre_deploy to be set")
	}
	if stack.Hooks.PreDeploy.Timeout != 10*time.Second {
		t.Errorf("expected timeout 10s, got %v", stack.Hooks.PreDeploy.Timeout)
	}
}

func TestLoadBranchConfig(t *testing.T) {
	yaml := `
repo: git@github.com:user/infra.git
base_path: /opt/stacks
branch: main
hosts:
  server01:
    stacks:
      - name: app
        path: app/
        branch: canary
      - name: web
        path: web/
`
	path := writeTemp(t, yaml)
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Branch != "main" {
		t.Errorf("expected top-level branch 'main', got %q", cfg.Branch)
	}
	stacks := cfg.Hosts["server01"].Stacks
	if stacks[0].Branch != "canary" {
		t.Errorf("expected app branch 'canary', got %q", stacks[0].Branch)
	}
	if stacks[1].Branch != "" {
		t.Errorf("expected web branch empty (inherit), got %q", stacks[1].Branch)
	}
}

func TestLoadNotifications(t *testing.T) {
	tests := []struct {
		name    string
		yaml    string
		wantErr string
	}{
		{
			name: "valid slack notification",
			yaml: `
repo: git@github.com:user/infra.git
base_path: /opt/stacks
notifications:
  - name: slack-alerts
    type: slack
    url: "https://hooks.slack.com/services/T/B/xxx"
    events: [deploy_success, deploy_failure]
hosts:
  server01:
    stacks:
      - name: app
        path: app/
`,
			wantErr: "",
		},
		{
			name: "valid webhook notification",
			yaml: `
repo: git@github.com:user/infra.git
base_path: /opt/stacks
notifications:
  - name: ntfy
    type: webhook
    url: "https://ntfy.sh/dockcd"
    events: [deploy_failure]
hosts:
  server01:
    stacks:
      - name: app
        path: app/
`,
			wantErr: "",
		},
		{
			name: "invalid notification type",
			yaml: `
repo: git@github.com:user/infra.git
base_path: /opt/stacks
notifications:
  - name: bad
    type: discord
    url: "https://example.com"
    events: [deploy_failure]
hosts:
  server01:
    stacks:
      - name: app
        path: app/
`,
			wantErr: "invalid notification type",
		},
		{
			name: "empty URL",
			yaml: `
repo: git@github.com:user/infra.git
base_path: /opt/stacks
notifications:
  - name: bad
    type: slack
    url: ""
    events: [deploy_failure]
hosts:
  server01:
    stacks:
      - name: app
        path: app/
`,
			wantErr: "notification URL is required",
		},
		{
			name: "invalid event name",
			yaml: `
repo: git@github.com:user/infra.git
base_path: /opt/stacks
notifications:
  - name: bad
    type: slack
    url: "https://hooks.slack.com/services/T/B/xxx"
    events: [invalid_event]
hosts:
  server01:
    stacks:
      - name: app
        path: app/
`,
			wantErr: "invalid event type",
		},
		{
			name: "no notifications (backward compat)",
			yaml: `
repo: git@github.com:user/infra.git
base_path: /opt/stacks
hosts:
  server01:
    stacks:
      - name: app
        path: app/
`,
			wantErr: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := writeTemp(t, tt.yaml)
			_, err := Load(path)
			if tt.wantErr != "" {
				if err == nil {
					t.Fatalf("expected error containing %q, got nil", tt.wantErr)
				}
				if !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("expected error containing %q, got %q", tt.wantErr, err.Error())
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}

func TestLoadEnvVarSubstitution(t *testing.T) {
	t.Setenv("TEST_REPO_URL", "git@github.com:user/infra.git")
	yaml := `
repo: "${TEST_REPO_URL}"
base_path: /opt/stacks
hosts:
  server01:
    stacks:
      - name: app
        path: app/
`
	path := writeTemp(t, yaml)
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Repo != "git@github.com:user/infra.git" {
		t.Errorf("expected expanded repo URL, got %q", cfg.Repo)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd /Users/mohitsharma44/devel/worktrees/dockcd-planning-7e7 && go test -race -v ./internal/config/ -run "TestLoadHooks|TestLoadBranch|TestLoadNotif|TestLoadEnvVar"`
Expected: Compilation errors — new fields don't exist

- [ ] **Step 3: Add new types to config.go**

Add the following types and modify existing structs in `internal/config/config.go`:

```go
// Add to imports: "os"  (already present)

// Valid notification types.
var validNotificationTypes = map[string]bool{
	"slack":   true,
	"webhook": true,
}

// Valid event types for notifications.
var validEventTypes = map[string]bool{
	"deploy_success":     true,
	"deploy_failure":     true,
	"rollback_success":   true,
	"rollback_failure":   true,
	"reconcile_start":    true,
	"reconcile_complete": true,
	"force_deploy":       true,
	"stack_suspended":    true,
	"stack_resumed":      true,
	"hook_failure":       true,
}

// HookConfig describes a single hook command.
type HookConfig struct {
	Command string        `yaml:"command"`
	Timeout time.Duration `yaml:"timeout"`
}

// Hooks holds optional pre/post deploy hooks for a stack.
type Hooks struct {
	PreDeploy  *HookConfig `yaml:"pre_deploy"`
	PostDeploy *HookConfig `yaml:"post_deploy"`
}

// NotificationConfig describes a notification target.
type NotificationConfig struct {
	Name   string   `yaml:"name"`
	Type   string   `yaml:"type"`
	URL    string   `yaml:"url"`
	Events []string `yaml:"events"`
}
```

Modify `Config` struct to add:
```go
type Config struct {
	Repo          string                `yaml:"repo"`
	BasePath      string                `yaml:"base_path"`
	Branch        string                `yaml:"branch"`
	Notifications []NotificationConfig  `yaml:"notifications"`
	Hosts         map[string]HostConfig `yaml:"hosts"`
}
```

Modify `Stack` struct to add `Branch` and `Hooks` fields, and update the `UnmarshalYAML` raw struct:
```go
type Stack struct {
	Name               string         `yaml:"name"`
	Path               string         `yaml:"path"`
	Branch             string         `yaml:"branch"`
	DependsOn          []string       `yaml:"depends_on"`
	PostDeployDelay    time.Duration  `yaml:"post_deploy_delay"`
	HealthCheckTimeout *time.Duration `yaml:"health_check_timeout"`
	AutoRollback       *bool          `yaml:"auto_rollback"`
	Hooks              *Hooks         `yaml:"hooks"`
}
```

Update `UnmarshalYAML` to handle the new fields in the raw struct:
```go
func (s *Stack) UnmarshalYAML(value *yaml.Node) error {
	var raw struct {
		Name               string   `yaml:"name"`
		Path               string   `yaml:"path"`
		Branch             string   `yaml:"branch"`
		DependsOn          []string `yaml:"depends_on"`
		PostDeployDelay    string   `yaml:"post_deploy_delay"`
		HealthCheckTimeout string   `yaml:"health_check_timeout"`
		AutoRollback       *bool    `yaml:"auto_rollback"`
		Hooks              *struct {
			PreDeploy *struct {
				Command string `yaml:"command"`
				Timeout string `yaml:"timeout"`
			} `yaml:"pre_deploy"`
			PostDeploy *struct {
				Command string `yaml:"command"`
				Timeout string `yaml:"timeout"`
			} `yaml:"post_deploy"`
		} `yaml:"hooks"`
	}
	if err := value.Decode(&raw); err != nil {
		return err
	}
	s.Name = raw.Name
	s.Path = raw.Path
	s.Branch = raw.Branch
	s.DependsOn = raw.DependsOn
	s.AutoRollback = raw.AutoRollback

	if raw.PostDeployDelay != "" {
		d, err := time.ParseDuration(raw.PostDeployDelay)
		if err != nil {
			return fmt.Errorf("invalid post_deploy_delay %q for stack %q: %w", raw.PostDeployDelay, raw.Name, err)
		}
		s.PostDeployDelay = d
	}
	if raw.HealthCheckTimeout != "" {
		d, err := time.ParseDuration(raw.HealthCheckTimeout)
		if err != nil {
			return fmt.Errorf("invalid health_check_timeout %q for stack %q: %w", raw.HealthCheckTimeout, raw.Name, err)
		}
		timeout := d
		s.HealthCheckTimeout = &timeout
	}

	if raw.Hooks != nil {
		s.Hooks = &Hooks{}
		if raw.Hooks.PreDeploy != nil {
			h := &HookConfig{Command: raw.Hooks.PreDeploy.Command}
			if raw.Hooks.PreDeploy.Timeout != "" {
				d, err := time.ParseDuration(raw.Hooks.PreDeploy.Timeout)
				if err != nil {
					return fmt.Errorf("invalid pre_deploy timeout %q for stack %q: %w", raw.Hooks.PreDeploy.Timeout, raw.Name, err)
				}
				h.Timeout = d
			}
			s.Hooks.PreDeploy = h
		}
		if raw.Hooks.PostDeploy != nil {
			h := &HookConfig{Command: raw.Hooks.PostDeploy.Command}
			if raw.Hooks.PostDeploy.Timeout != "" {
				d, err := time.ParseDuration(raw.Hooks.PostDeploy.Timeout)
				if err != nil {
					return fmt.Errorf("invalid post_deploy timeout %q for stack %q: %w", raw.Hooks.PostDeploy.Timeout, raw.Name, err)
				}
				h.Timeout = d
			}
			s.Hooks.PostDeploy = h
		}
	}
	return nil
}
```

- [ ] **Step 4: Add env var expansion and validation to Load()**

Modify the `Load` function:
```go
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading config file: %w", err)
	}

	// Expand environment variables before parsing.
	expanded := os.ExpandEnv(string(data))

	var cfg Config
	if err := yaml.Unmarshal([]byte(expanded), &cfg); err != nil {
		return nil, fmt.Errorf("parsing config file: %w", err)
	}

	if err := validate(&cfg); err != nil {
		return nil, fmt.Errorf("validating config: %w", err)
	}

	return &cfg, nil
}
```

Add notification and hook validation to `validate()`:
```go
// Add at the end of the validate() function, before the final return:

	// Validate notifications.
	for i, n := range cfg.Notifications {
		if !validNotificationTypes[n.Type] {
			return fmt.Errorf("notification[%d] %q: invalid notification type %q (must be slack or webhook)", i, n.Name, n.Type)
		}
		if n.URL == "" {
			return fmt.Errorf("notification[%d] %q: notification URL is required", i, n.Name)
		}
		for _, event := range n.Events {
			if !validEventTypes[event] {
				return fmt.Errorf("notification[%d] %q: invalid event type %q", i, n.Name, event)
			}
		}
	}

// Add inside the stack validation loop (after path check):

			if st.Hooks != nil {
				if st.Hooks.PreDeploy != nil && st.Hooks.PreDeploy.Command == "" {
					return fmt.Errorf("host %q: stack %q: pre_deploy hook command is required", hostName, st.Name)
				}
				if st.Hooks.PostDeploy != nil && st.Hooks.PostDeploy.Command == "" {
					return fmt.Errorf("host %q: stack %q: post_deploy hook command is required", hostName, st.Name)
				}
			}
```

- [ ] **Step 5: Run all config tests**

Run: `cd /Users/mohitsharma44/devel/worktrees/dockcd-planning-7e7 && go test -race -v ./internal/config/`
Expected: All tests PASS (both new and existing)

- [ ] **Step 6: Update examples/gitops.yaml**

```yaml
repo: git@github.com:user/infra.git
base_path: /opt/stacks
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

hosts:
  server01:
    stacks:
      - name: monitoring
        path: monitoring/
        hooks:
          pre_deploy:
            command: "sops-decrypt.sh"
            timeout: 15s
      - name: app
        path: app/
        depends_on: [monitoring]
        post_deploy_delay: 30s
        health_check_timeout: 90s
        auto_rollback: true
        hooks:
          pre_deploy:
            command: "sops-decrypt.sh"
```

- [ ] **Step 7: Run full test suite to confirm no regressions**

Run: `cd /Users/mohitsharma44/devel/worktrees/dockcd-planning-7e7 && go test -race ./...`
Expected: All tests PASS

- [ ] **Step 8: Commit**

```bash
cd /Users/mohitsharma44/devel/worktrees/dockcd-planning-7e7
git add internal/config/config.go internal/config/config_test.go examples/gitops.yaml
git commit -m "feat: add hooks, branch, and notification config fields with env var substitution"
```

---

## Task 3: Integrate Hooks into Deploy Lifecycle

**Files:**
- Modify: `internal/deploy/graph.go` (add hooks fields to `deploy.Stack`)
- Modify: `internal/deploy/compose.go` (run hooks before/after compose)
- Modify: `internal/deploy/compose_test.go` (test hook integration)
- Modify: `internal/reconciler/reconciler.go` (pass hooks through to deploy, log hook output)
- Modify: `internal/reconciler/reconciler_test.go` (test hooks in reconciler)

### Verification Spec

**Functional:**
- `deploy.Stack` gains `PreDeployHook` and `PostDeployHook` fields (pointers to `hooks.Config`)
- `deploy.Deploy()` accepts a `hooks.Env` parameter (or gets it via a new field)
- Pre-deploy hook runs before `docker compose pull`, in the stack's directory
- Post-deploy hook runs after `docker compose up -d`, in the stack's directory
- If pre-deploy hook fails, deploy is skipped entirely and error is returned
- If post-deploy hook fails, error is returned but deploy already happened (not rolled back)
- If no hooks are configured, behavior is identical to current (backward compat)
- Reconciler passes hook env vars (stack name, commit, repo dir, branch) through to deploy
- Reconciler logs hook stdout at info level, stderr at warn level
- During rollback, pre-deploy hook runs again (for the old commit's files)

**Non-functional:**
- Existing compose_test.go tests still pass unchanged
- No additional goroutines or channels introduced
- Hook failures are distinguishable from compose failures in error messages

**Edge cases to test:**
- Stack with pre_deploy hook only
- Stack with post_deploy hook only
- Stack with both hooks
- Stack with no hooks (regression test — existing behavior)
- Pre-deploy hook failure skips compose entirely (verify zero compose calls)
- Post-deploy hook failure after successful compose (verify compose was called)

---

- [ ] **Step 1: Add hook fields to deploy.Stack**

Modify `internal/deploy/graph.go`:

```go
// Add import for hooks package
import (
	"fmt"
	"time"

	"github.com/mohitsharma44/dockcd/internal/hooks"
)

// Add to Stack struct:
type Stack struct {
	Name               string
	Path               string
	DependsOn          []string
	HealthCheckTimeout time.Duration
	AutoRollback       bool
	PreDeployHook      *hooks.Config
	PostDeployHook     *hooks.Config
}
```

- [ ] **Step 2: Write failing tests for hook integration in deploy**

Add to `internal/deploy/compose_test.go`:

```go
func TestDeployRunsPreDeployHook(t *testing.T) {
	dir := t.TempDir()
	var calls []string
	runner := func(ctx context.Context, d, name string, args ...string) error {
		calls = append(calls, name+" "+strings.Join(args, " "))
		return nil
	}

	stack := Stack{
		Name: "web",
		Path: ".",
		PreDeployHook: &hooks.Config{Command: "echo pre", Timeout: 5 * time.Second},
	}

	env := hooks.Env{StackName: "web", Commit: "abc", RepoDir: dir, Branch: "main"}
	err := Deploy(context.Background(), stack, dir, runner, env)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Should have: pre-hook ran (not tracked in calls since it uses sh -c),
	// then compose pull + compose up.
	if len(calls) != 2 {
		t.Fatalf("expected 2 compose calls, got %d: %v", len(calls), calls)
	}
}

func TestDeployPreHookFailureSkipsCompose(t *testing.T) {
	dir := t.TempDir()
	var calls []string
	runner := func(ctx context.Context, d, name string, args ...string) error {
		calls = append(calls, name)
		return nil
	}

	stack := Stack{
		Name: "web",
		Path: ".",
		PreDeployHook: &hooks.Config{Command: "exit 1", Timeout: 5 * time.Second},
	}

	env := hooks.Env{StackName: "web", Commit: "abc", RepoDir: dir, Branch: "main"}
	err := Deploy(context.Background(), stack, dir, runner, env)
	if err == nil {
		t.Fatal("expected error from failed pre-deploy hook")
	}

	// Compose should NOT have been called.
	if len(calls) != 0 {
		t.Errorf("expected 0 compose calls after pre-hook failure, got %d", len(calls))
	}
}

func TestDeployPostHookFailureStillDeployed(t *testing.T) {
	dir := t.TempDir()
	var calls []string
	runner := func(ctx context.Context, d, name string, args ...string) error {
		calls = append(calls, name+" "+strings.Join(args, " "))
		return nil
	}

	stack := Stack{
		Name: "web",
		Path: ".",
		PostDeployHook: &hooks.Config{Command: "exit 1", Timeout: 5 * time.Second},
	}

	env := hooks.Env{StackName: "web", Commit: "abc", RepoDir: dir, Branch: "main"}
	err := Deploy(context.Background(), stack, dir, runner, env)
	if err == nil {
		t.Fatal("expected error from failed post-deploy hook")
	}

	// Compose pull + up should have been called before the hook failed.
	if len(calls) != 2 {
		t.Errorf("expected 2 compose calls, got %d: %v", len(calls), calls)
	}
}

func TestDeployNoHooksBackwardCompat(t *testing.T) {
	dir := t.TempDir()
	var calls []string
	runner := func(ctx context.Context, d, name string, args ...string) error {
		calls = append(calls, name)
		return nil
	}

	stack := Stack{Name: "web", Path: "."}
	env := hooks.Env{}
	err := Deploy(context.Background(), stack, dir, runner, env)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(calls) != 2 {
		t.Fatalf("expected 2 calls (pull + up), got %d", len(calls))
	}
}
```

- [ ] **Step 3: Run tests to verify they fail**

Run: `cd /Users/mohitsharma44/devel/worktrees/dockcd-planning-7e7 && go test -race -v ./internal/deploy/ -run "TestDeployRuns|TestDeployPre|TestDeployPost|TestDeployNoHooks"`
Expected: Compilation errors — `Deploy` signature doesn't accept `hooks.Env`

- [ ] **Step 4: Modify Deploy() to integrate hooks**

Update `internal/deploy/compose.go`:

```go
package deploy

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/mohitsharma44/dockcd/internal/hooks"
)

// CommandRunner is a function that runs a shell command in a directory.
type CommandRunner func(ctx context.Context, dir string, name string, args ...string) error

// Deploy runs optional hooks and docker compose pull + up for a single stack.
func Deploy(ctx context.Context, stack Stack, repoDir string, run CommandRunner, env hooks.Env) error {
	if filepath.IsAbs(stack.Path) {
		return fmt.Errorf("stack %q: path must be relative, got %q", stack.Name, stack.Path)
	}

	dir := filepath.Join(repoDir, stack.Path)

	// Verify the resolved path is still within repoDir.
	resolvedDir, err := filepath.Abs(dir)
	if err != nil {
		return fmt.Errorf("stack %q: resolving path: %w", stack.Name, err)
	}
	resolvedRepo, err := filepath.Abs(repoDir)
	if err != nil {
		return fmt.Errorf("stack %q: resolving repo dir: %w", stack.Name, err)
	}
	if !strings.HasPrefix(resolvedDir, resolvedRepo) {
		return fmt.Errorf("stack %q: path %q escapes repo directory", stack.Name, stack.Path)
	}

	// Pre-deploy hook.
	if stack.PreDeployHook != nil {
		if _, err := hooks.Run(ctx, *stack.PreDeployHook, dir, env); err != nil {
			return fmt.Errorf("stack %q: pre-deploy hook: %w", stack.Name, err)
		}
	}

	if err := run(ctx, dir, "docker", "compose", "-p", stack.Name, "pull"); err != nil {
		return fmt.Errorf("stack %q: compose pull: %w", stack.Name, err)
	}

	if err := run(ctx, dir, "docker", "compose", "-p", stack.Name, "up", "-d", "--remove-orphans"); err != nil {
		return fmt.Errorf("stack %q: compose up: %w", stack.Name, err)
	}

	// Post-deploy hook.
	if stack.PostDeployHook != nil {
		if _, err := hooks.Run(ctx, *stack.PostDeployHook, dir, env); err != nil {
			return fmt.Errorf("stack %q: post-deploy hook: %w", stack.Name, err)
		}
	}

	return nil
}
```

- [ ] **Step 5: Update all callers of Deploy() to pass hooks.Env**

Update `internal/deploy/runner.go`:
```go
package deploy

import (
	"context"
	"fmt"

	"github.com/mohitsharma44/dockcd/internal/hooks"
	"golang.org/x/sync/errgroup"
)

// RunGroups deploys stacks group by group. Groups run sequentially,
// but stacks within a group run in parallel.
func RunGroups(ctx context.Context, groups [][]Stack, repoDir string, run CommandRunner, env hooks.Env) error {
	for i, group := range groups {
		g, ctx := errgroup.WithContext(ctx)
		for _, stack := range group {
			stack := stack
			g.Go(func() error {
				return Deploy(ctx, stack, repoDir, run, env)
			})
		}
		if err := g.Wait(); err != nil {
			return fmt.Errorf("group %d: %w", i+1, err)
		}
	}
	return nil
}
```

Update `internal/reconciler/reconciler.go` — the `deployStack` method must build and pass `hooks.Env`, and `configToDeployStacks`/`filterChangedStacks` must map hook config:

In `configToDeployStacks` and `filterChangedStacks`, add hook mapping:
```go
// In filterChangedStacks, when building deploy.Stack:
var preHook, postHook *hooks.Config
if cs.Hooks != nil {
	if cs.Hooks.PreDeploy != nil {
		preHook = &hooks.Config{Command: cs.Hooks.PreDeploy.Command, Timeout: cs.Hooks.PreDeploy.Timeout}
	}
	if cs.Hooks.PostDeploy != nil {
		postHook = &hooks.Config{Command: cs.Hooks.PostDeploy.Command, Timeout: cs.Hooks.PostDeploy.Timeout}
	}
}
stacks = append(stacks, deploy.Stack{
	Name:               cs.Name,
	Path:               filepath.Join(r.basePath, cs.Path),
	DependsOn:          deps,
	HealthCheckTimeout: cs.HealthTimeout(),
	AutoRollback:       cs.RollbackEnabled(),
	PreDeployHook:      preHook,
	PostDeployHook:     postHook,
})
```

Similarly update `configToDeployStacks`.

In `deployStack`, construct and pass `hooks.Env`:
```go
func (r *Reconciler) deployStack(ctx context.Context, stack deploy.Stack, commitHash string) error {
	env := hooks.Env{
		StackName: stack.Name,
		Commit:    commitHash,
		RepoDir:   r.repoDir,
		Branch:    "", // will be populated by multi-branch in Plan 2
	}

	if err := deploy.Deploy(ctx, stack, r.repoDir, r.runner, env); err != nil {
		r.markStackHealth(ctx, stack.Name, false)
		return err
	}
	// ... rest unchanged
```

Also update the rollback method to pass env:
```go
// In rollback(), the rollback deploy call:
rollbackStack := deploy.Stack{Name: stack.Name, Path: "."}
if err := deploy.Deploy(ctx, rollbackStack, tmpDir, r.runner, hooks.Env{
	StackName: stack.Name,
	Commit:    lastGood,
	RepoDir:   r.repoDir,
}); err != nil {
```

- [ ] **Step 6: Fix existing tests that call Deploy() without env parameter**

Update all existing test calls to `Deploy()` and `RunGroups()` in:
- `internal/deploy/compose_test.go` — add `hooks.Env{}` as the last parameter
- `internal/deploy/runner_test.go` — add `hooks.Env{}` as the last parameter

- [ ] **Step 7: Run full test suite**

Run: `cd /Users/mohitsharma44/devel/worktrees/dockcd-planning-7e7 && go test -race ./...`
Expected: All tests PASS

- [ ] **Step 8: Commit**

```bash
cd /Users/mohitsharma44/devel/worktrees/dockcd-planning-7e7
git add internal/deploy/ internal/reconciler/ 
git commit -m "feat: integrate pre/post deploy hooks into deploy lifecycle"
```

---

## Task 4: Event Bus for Notifications and SSE

**Files:**
- Create: `internal/notify/event.go`
- Create: `internal/notify/event_test.go`

### Verification Spec

**Functional:**
- `EventType` is a string type with constants for all 10 event types
- `Event` struct holds Type, Timestamp, Stack, Host, Commit, Branch, Message, Metadata
- `EventBus` is a struct with `Emit(event)` and `Subscribe() <-chan Event` methods
- `Emit` is non-blocking — if no subscribers, event is dropped
- Multiple subscribers each get their own copy of every event
- `Close()` closes all subscriber channels
- Subscribers that fall behind get events dropped (buffered channel, not blocking sender)

**Non-functional:**
- Thread-safe: `Emit`, `Subscribe`, `Close` can be called concurrently
- No goroutine leaks: `Close` cleans up everything
- Buffer size: 64 events per subscriber (configurable if needed)

**Edge cases to test:**
- Emit with no subscribers (no panic, no block)
- Multiple subscribers receive same event
- Close then Emit (no panic)
- Subscribe after Close (returns closed channel)
- Slow subscriber doesn't block Emit or other subscribers

---

- [ ] **Step 1: Write failing tests**

```go
// internal/notify/event_test.go
package notify

import (
	"testing"
	"time"
)

func TestEventBusEmitToSubscriber(t *testing.T) {
	bus := NewEventBus()
	defer bus.Close()

	ch := bus.Subscribe()
	event := Event{Type: EventDeploySuccess, Stack: "web", Message: "ok"}
	bus.Emit(event)

	select {
	case got := <-ch:
		if got.Type != EventDeploySuccess {
			t.Errorf("expected EventDeploySuccess, got %v", got.Type)
		}
		if got.Stack != "web" {
			t.Errorf("expected stack 'web', got %q", got.Stack)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for event")
	}
}

func TestEventBusMultipleSubscribers(t *testing.T) {
	bus := NewEventBus()
	defer bus.Close()

	ch1 := bus.Subscribe()
	ch2 := bus.Subscribe()

	event := Event{Type: EventDeployFailure, Stack: "db"}
	bus.Emit(event)

	for i, ch := range []<-chan Event{ch1, ch2} {
		select {
		case got := <-ch:
			if got.Type != EventDeployFailure {
				t.Errorf("subscriber %d: expected EventDeployFailure, got %v", i, got.Type)
			}
		case <-time.After(time.Second):
			t.Fatalf("subscriber %d: timed out", i)
		}
	}
}

func TestEventBusEmitNoSubscribers(t *testing.T) {
	bus := NewEventBus()
	defer bus.Close()

	// Should not panic or block.
	bus.Emit(Event{Type: EventDeploySuccess})
}

func TestEventBusCloseClosesChannels(t *testing.T) {
	bus := NewEventBus()
	ch := bus.Subscribe()
	bus.Close()

	// Channel should be closed.
	_, ok := <-ch
	if ok {
		t.Error("expected channel to be closed")
	}
}

func TestEventBusEmitAfterClose(t *testing.T) {
	bus := NewEventBus()
	bus.Close()

	// Should not panic.
	bus.Emit(Event{Type: EventDeploySuccess})
}

func TestEventBusSlowSubscriberDoesNotBlock(t *testing.T) {
	bus := NewEventBus()
	defer bus.Close()

	_ = bus.Subscribe() // slow subscriber — never reads
	ch2 := bus.Subscribe()

	// Fill beyond buffer.
	for i := 0; i < 100; i++ {
		bus.Emit(Event{Type: EventDeploySuccess, Message: "event"})
	}

	// Fast subscriber should still get events (may miss some due to slow sub dropping).
	select {
	case <-ch2:
		// got at least one event — good
	case <-time.After(time.Second):
		t.Fatal("fast subscriber should receive events even with slow subscriber")
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd /Users/mohitsharma44/devel/worktrees/dockcd-planning-7e7 && go test -race -v ./internal/notify/`
Expected: Compilation error — package doesn't exist

- [ ] **Step 3: Implement event types and EventBus**

```go
// internal/notify/event.go
package notify

import (
	"sync"
	"time"
)

// EventType represents a dockcd lifecycle event.
type EventType string

const (
	EventDeploySuccess     EventType = "deploy_success"
	EventDeployFailure     EventType = "deploy_failure"
	EventRollbackSuccess   EventType = "rollback_success"
	EventRollbackFailure   EventType = "rollback_failure"
	EventReconcileStart    EventType = "reconcile_start"
	EventReconcileComplete EventType = "reconcile_complete"
	EventForceDeploy       EventType = "force_deploy"
	EventStackSuspended    EventType = "stack_suspended"
	EventStackResumed      EventType = "stack_resumed"
	EventHookFailure       EventType = "hook_failure"
)

// Event represents a single lifecycle event.
type Event struct {
	Type      EventType         `json:"type"`
	Timestamp time.Time         `json:"timestamp"`
	Stack     string            `json:"stack,omitempty"`
	Host      string            `json:"host,omitempty"`
	Commit    string            `json:"commit,omitempty"`
	Branch    string            `json:"branch,omitempty"`
	Message   string            `json:"message,omitempty"`
	Metadata  map[string]string `json:"metadata,omitempty"`
}

const subscriberBuffer = 64

// EventBus distributes events to all subscribers.
type EventBus struct {
	mu          sync.RWMutex
	subscribers []chan Event
	closed      bool
}

// NewEventBus creates a new EventBus.
func NewEventBus() *EventBus {
	return &EventBus{}
}

// Subscribe returns a channel that receives all future events.
func (b *EventBus) Subscribe() <-chan Event {
	b.mu.Lock()
	defer b.mu.Unlock()

	ch := make(chan Event, subscriberBuffer)
	if b.closed {
		close(ch)
		return ch
	}
	b.subscribers = append(b.subscribers, ch)
	return ch
}

// Emit sends an event to all subscribers. Non-blocking: if a subscriber's
// buffer is full, the event is dropped for that subscriber.
func (b *EventBus) Emit(event Event) {
	b.mu.RLock()
	defer b.mu.RUnlock()

	if b.closed {
		return
	}

	for _, ch := range b.subscribers {
		select {
		case ch <- event:
		default:
			// subscriber buffer full — drop event
		}
	}
}

// Close closes all subscriber channels. Safe to call multiple times.
func (b *EventBus) Close() {
	b.mu.Lock()
	defer b.mu.Unlock()

	if b.closed {
		return
	}
	b.closed = true
	for _, ch := range b.subscribers {
		close(ch)
	}
}
```

- [ ] **Step 4: Run tests**

Run: `cd /Users/mohitsharma44/devel/worktrees/dockcd-planning-7e7 && go test -race -v ./internal/notify/`
Expected: All 6 tests PASS

- [ ] **Step 5: Commit**

```bash
cd /Users/mohitsharma44/devel/worktrees/dockcd-planning-7e7
git add internal/notify/event.go internal/notify/event_test.go
git commit -m "feat: add event bus for lifecycle event distribution"
```

---

## Task 5: Suspend/Resume State in Git Poller

**Files:**
- Modify: `internal/git/poller.go`
- Modify: `internal/git/poller_test.go`
- Modify: `internal/reconciler/reconciler.go`
- Modify: `internal/reconciler/reconciler_test.go`

### Verification Spec

**Functional:**
- `State` struct gains `SuspendedStacks []string` field
- `Poller` gains `SuspendStack(name)` / `ResumeStack(name)` / `IsSuspended(name) bool` methods
- `SuspendStack` adds to the set and persists to state file
- `ResumeStack` removes from the set and persists; also marks stack as "needs reconcile"
- `IsSuspended` returns true if stack is in suspended set
- `SuspendStack` on already-suspended stack is a no-op (no error)
- `ResumeStack` on non-suspended stack is a no-op (no error)
- Suspended stacks survive restart (persisted in state file JSON)
- `LoadState` reads suspended stacks from state file
- Backward compat: state file without `suspended_stacks` field loads fine (empty set)
- `Poller` gains `NeedsReconcile(name) bool` / `ClearNeedsReconcile(name)` for resume-triggered deploy
- `GitPoller` interface in reconciler gains: `SuspendStack`, `ResumeStack`, `IsSuspended`, `NeedsReconcile`, `ClearNeedsReconcile`
- Reconciler skips suspended stacks in `filterChangedStacks`
- Reconciler checks `NeedsReconcile` during poll to deploy freshly resumed stacks

**Non-functional:**
- All state mutations protected by existing mutex
- Existing state file migration (legacy format) still works

**Edge cases to test:**
- Suspend, restart (reload state), verify still suspended
- Resume a stack that was never suspended (no-op)
- Suspend stack, change detected, verify it's NOT deployed
- Resume stack, verify next reconcile deploys it

---

- [ ] **Step 1: Write failing tests for poller suspend/resume**

Add to `internal/git/poller_test.go`:

```go
func TestSuspendResumePersistence(t *testing.T) {
	dir := t.TempDir()
	stateFile := filepath.Join(dir, ".dockcd_state")

	p := NewPoller("", dir)
	p.SetStateFile(stateFile)
	p.lastHash = "abc123"

	// Suspend a stack.
	if err := p.SuspendStack("web"); err != nil {
		t.Fatalf("SuspendStack: %v", err)
	}
	if !p.IsSuspended("web") {
		t.Error("expected web to be suspended")
	}

	// Reload state in a new poller.
	p2 := NewPoller("", dir)
	p2.SetStateFile(stateFile)
	if err := p2.LoadState(); err != nil {
		t.Fatalf("LoadState: %v", err)
	}
	if !p2.IsSuspended("web") {
		t.Error("expected web to be suspended after reload")
	}

	// Resume.
	if err := p2.ResumeStack("web"); err != nil {
		t.Fatalf("ResumeStack: %v", err)
	}
	if p2.IsSuspended("web") {
		t.Error("expected web to NOT be suspended after resume")
	}
	if !p2.NeedsReconcile("web") {
		t.Error("expected web to need reconcile after resume")
	}
}

func TestSuspendIdempotent(t *testing.T) {
	dir := t.TempDir()
	p := NewPoller("", dir)
	p.SetStateFile(filepath.Join(dir, ".dockcd_state"))
	p.lastHash = "abc"

	if err := p.SuspendStack("web"); err != nil {
		t.Fatal(err)
	}
	if err := p.SuspendStack("web"); err != nil {
		t.Fatal(err)
	}
	if !p.IsSuspended("web") {
		t.Error("expected suspended")
	}
}

func TestResumeNotSuspended(t *testing.T) {
	dir := t.TempDir()
	p := NewPoller("", dir)
	p.SetStateFile(filepath.Join(dir, ".dockcd_state"))
	p.lastHash = "abc"

	// Resume a stack that was never suspended — should be a no-op.
	if err := p.ResumeStack("web"); err != nil {
		t.Fatal(err)
	}
	if p.IsSuspended("web") {
		t.Error("should not be suspended")
	}
}

func TestBackwardCompatNoSuspendedField(t *testing.T) {
	dir := t.TempDir()
	stateFile := filepath.Join(dir, ".dockcd_state")

	// Write state file without suspended_stacks field.
	data := `{"last_hash":"abc","last_successful_commits":{"web":"abc"}}`
	if err := os.WriteFile(stateFile, []byte(data), 0644); err != nil {
		t.Fatal(err)
	}

	p := NewPoller("", dir)
	p.SetStateFile(stateFile)
	if err := p.LoadState(); err != nil {
		t.Fatalf("LoadState: %v", err)
	}
	if p.IsSuspended("web") {
		t.Error("should not be suspended with legacy state file")
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd /Users/mohitsharma44/devel/worktrees/dockcd-planning-7e7 && go test -race -v ./internal/git/ -run "TestSuspend|TestResume|TestBackwardCompat"`
Expected: Compilation errors

- [ ] **Step 3: Implement suspend/resume in Poller**

Add to `internal/git/poller.go`:

Add `SuspendedStacks` to the `State` struct:
```go
type State struct {
	LastHash              string            `json:"last_hash"`
	LastSuccessfulCommits map[string]string `json:"last_successful_commits"`
	SuspendedStacks       []string          `json:"suspended_stacks,omitempty"`
}
```

Add `needsReconcile` field to `Poller`:
```go
type Poller struct {
	repoURL        string
	localDir       string
	lastHash       string
	stateFile      string
	branch         string
	state          State
	needsReconcile map[string]bool
	mu             sync.Mutex
}
```

Initialize `needsReconcile` in `NewPoller`:
```go
func NewPoller(repoURL, localDir string) *Poller {
	return &Poller{
		repoURL:        repoURL,
		localDir:       localDir,
		state:          State{LastSuccessfulCommits: make(map[string]string)},
		needsReconcile: make(map[string]bool),
	}
}
```

Add methods:
```go
// SuspendStack marks a stack as suspended and persists state.
func (p *Poller) SuspendStack(name string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, s := range p.state.SuspendedStacks {
		if s == name {
			return nil // already suspended
		}
	}
	p.state.SuspendedStacks = append(p.state.SuspendedStacks, name)
	return p.saveStateLocked()
}

// ResumeStack removes a stack from the suspended set and marks it as needing reconcile.
func (p *Poller) ResumeStack(name string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	filtered := p.state.SuspendedStacks[:0]
	found := false
	for _, s := range p.state.SuspendedStacks {
		if s == name {
			found = true
			continue
		}
		filtered = append(filtered, s)
	}
	if !found {
		return nil // wasn't suspended
	}
	p.state.SuspendedStacks = filtered
	p.needsReconcile[name] = true
	return p.saveStateLocked()
}

// IsSuspended returns true if the stack is currently suspended.
func (p *Poller) IsSuspended(name string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, s := range p.state.SuspendedStacks {
		if s == name {
			return true
		}
	}
	return false
}

// NeedsReconcile returns true if the stack was recently resumed and needs deployment.
func (p *Poller) NeedsReconcile(name string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.needsReconcile[name]
}

// ClearNeedsReconcile clears the needs-reconcile flag for a stack.
func (p *Poller) ClearNeedsReconcile(name string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	delete(p.needsReconcile, name)
}
```

- [ ] **Step 4: Update GitPoller interface and reconciler**

Add new methods to the `GitPoller` interface in `internal/reconciler/reconciler.go`:
```go
type GitPoller interface {
	Clone() error
	Fetch() (bool, error)
	ChangedStacks(sinceHash string, stackPaths map[string]string) ([]string, error)
	LastHash() string
	LoadState() error
	LastSuccessfulCommit(stack string) string
	SetLastSuccessfulCommit(stack, hash string) error
	ExtractAtCommit(commit, pathPrefix, destDir string) error
	SuspendStack(name string) error
	ResumeStack(name string) error
	IsSuspended(name string) bool
	NeedsReconcile(name string) bool
	ClearNeedsReconcile(name string)
}
```

Update `filterChangedStacks` to skip suspended stacks:
```go
func (r *Reconciler) filterChangedStacks(changedNames []string) []deploy.Stack {
	changedSet := make(map[string]bool, len(changedNames))
	for _, name := range changedNames {
		changedSet[name] = true
	}

	var stacks []deploy.Stack
	for _, cs := range r.hostStacks {
		if !changedSet[cs.Name] {
			continue
		}
		// Skip suspended stacks.
		if r.poller.IsSuspended(cs.Name) {
			r.logger.Info("skipping suspended stack", "stack", cs.Name)
			continue
		}
		// ... rest unchanged
```

Update `mockPoller` in `reconciler_test.go` to implement new interface methods (stub implementations returning defaults).

- [ ] **Step 5: Write reconciler test for suspend behavior**

Add to `internal/reconciler/reconciler_test.go`:

```go
func TestReconcileSkipsSuspendedStack(t *testing.T) {
	stacks := []config.Stack{
		{Name: "traefik", Path: "server04/traefik"},
		{Name: "vault", Path: "server04/vault"},
	}

	poller := &mockPoller{
		fetchChanged:    true,
		lastHash:        "def456",
		changedStacks:   []string{"traefik", "vault"},
		suspendedStacks: map[string]bool{"vault": true},
	}

	var mu sync.Mutex
	var calls []deployCall
	r, _ := testReconciler(t, poller, stacks, mockRunner(&mu, &calls))

	if err := r.reconcile(context.Background()); err != nil {
		t.Fatalf("reconcile failed: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	// Only traefik should be deployed (2 calls: pull + up).
	if len(calls) != 2 {
		t.Fatalf("expected 2 deploy calls (traefik only), got %d", len(calls))
	}
	for _, c := range calls {
		if c.dir != "/opt/repo/docker/stacks/server04/traefik" {
			t.Errorf("unexpected deploy dir: %s", c.dir)
		}
	}
}
```

Add `suspendedStacks` and `needsReconcileSet` fields to `mockPoller` and implement the interface methods.

- [ ] **Step 6: Run full test suite**

Run: `cd /Users/mohitsharma44/devel/worktrees/dockcd-planning-7e7 && go test -race ./...`
Expected: All tests PASS

- [ ] **Step 7: Commit**

```bash
cd /Users/mohitsharma44/devel/worktrees/dockcd-planning-7e7
git add internal/git/poller.go internal/git/poller_test.go internal/reconciler/reconciler.go internal/reconciler/reconciler_test.go
git commit -m "feat: add suspend/resume with state persistence and reconciler filtering"
```

---

## Task 6: Server API Endpoints

**Files:**
- Modify: `internal/server/server.go`
- Modify: `internal/server/server_test.go`

### Verification Spec

**Functional:**
- `GET /api/v1/stacks` returns JSON array of all stacks with status, health, branch, commit, suspended flag, last deploy time
- `GET /api/v1/stacks/{name}` returns single stack detail; 404 if not found
- `POST /api/v1/reconcile` triggers full reconcile, returns 202 with reconcile ID
- `POST /api/v1/reconcile/{name}` triggers single-stack reconcile, returns 202; 404 if stack not found
- `POST /api/v1/stacks/{name}/suspend` suspends a stack, returns updated stack object; 404 if not found
- `POST /api/v1/stacks/{name}/resume` resumes a stack, returns updated stack object; 404 if not found
- `GET /api/v1/reconcile/{id}/events` streams SSE events for a reconcile operation
- `GET /api/v1/logs` streams SSE log events from ring buffer
- `GET /api/v1/version` returns server version and go version
- Existing `/healthz` and `/metrics` unchanged

**Non-functional:**
- All handlers tested with httptest (no real network)
- SSE endpoints set correct headers (`Content-Type: text/event-stream`, `Cache-Control: no-cache`)
- Reconcile endpoint is async — handler returns immediately, reconcile runs in background
- Server needs access to: stack config, poller (for suspend/resume), reconciler (for triggering reconcile), event bus (for SSE)

**Edge cases to test:**
- GET /api/v1/stacks when no stacks configured (empty array, not null)
- GET /api/v1/stacks/nonexistent (404 with JSON error body)
- POST /api/v1/reconcile/nonexistent (404)
- Suspend already-suspended stack (200, idempotent)
- Resume non-suspended stack (200, idempotent)
- SSE client disconnect (server-side cleanup, no goroutine leak)

---

- [ ] **Step 1: Define StackInfo and expanded Status types**

The Server needs richer state. Add a `StackState` type and expand `Status` to track per-stack info:

```go
// Add to internal/server/server.go:

// StackState tracks the runtime state of a single stack.
type StackState struct {
	Name       string    `json:"name"`
	Host       string    `json:"host"`
	Branch     string    `json:"branch"`
	Status     string    `json:"status"`     // Ready, Failed, Suspended, Reconciling
	Health     string    `json:"health"`     // Healthy, Degraded, Unknown
	LastDeploy time.Time `json:"last_deploy"`
	Commit     string    `json:"commit"`
	Suspended  bool      `json:"suspended"`
}
```

Add fields to `Status` for stack tracking and new dependencies:
```go
type Status struct {
	mu         sync.RWMutex
	lastSync   time.Time
	commit     string
	stacks     map[string]*StackState
}

// UpdateStack updates the state for a single stack.
func (s *Status) UpdateStack(name string, state *StackState) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.stacks == nil {
		s.stacks = make(map[string]*StackState)
	}
	s.stacks[name] = state
}

// GetStacks returns a snapshot of all stack states.
func (s *Status) GetStacks() []StackState {
	s.mu.RLock()
	defer s.mu.RUnlock()
	result := make([]StackState, 0, len(s.stacks))
	for _, st := range s.stacks {
		result = append(result, *st)
	}
	return result
}

// GetStack returns a single stack state, or nil if not found.
func (s *Status) GetStack(name string) *StackState {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if st, ok := s.stacks[name]; ok {
		cp := *st
		return &cp
	}
	return nil
}
```

- [ ] **Step 2: Write failing tests for new endpoints**

Add to `internal/server/server_test.go`:

```go
func TestGetStacksEmpty(t *testing.T) {
	status := &Status{}
	srv := New(":0", status, nil, nil, nil, nil)

	req := httptest.NewRequest("GET", "/api/v1/stacks", nil)
	rec := httptest.NewRecorder()
	srv.httpServer.Handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	var resp struct {
		Stacks []StackState `json:"stacks"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatal(err)
	}
	if resp.Stacks == nil {
		t.Error("expected empty array, not null")
	}
}

func TestGetStacksWithData(t *testing.T) {
	status := &Status{}
	status.UpdateStack("web", &StackState{
		Name: "web", Host: "server01", Status: "Ready", Health: "Healthy",
		Commit: "abc123", Branch: "main",
	})

	srv := New(":0", status, nil, nil, nil, nil)

	req := httptest.NewRequest("GET", "/api/v1/stacks", nil)
	rec := httptest.NewRecorder()
	srv.httpServer.Handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	var resp struct {
		Stacks []StackState `json:"stacks"`
	}
	json.NewDecoder(rec.Body).Decode(&resp)
	if len(resp.Stacks) != 1 {
		t.Fatalf("expected 1 stack, got %d", len(resp.Stacks))
	}
	if resp.Stacks[0].Name != "web" {
		t.Errorf("expected name 'web', got %q", resp.Stacks[0].Name)
	}
}

func TestGetStackNotFound(t *testing.T) {
	status := &Status{}
	srv := New(":0", status, nil, nil, nil, nil)

	req := httptest.NewRequest("GET", "/api/v1/stacks/nonexistent", nil)
	rec := httptest.NewRecorder()
	srv.httpServer.Handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", rec.Code)
	}
}

func TestGetStackFound(t *testing.T) {
	status := &Status{}
	status.UpdateStack("web", &StackState{Name: "web", Status: "Ready"})
	srv := New(":0", status, nil, nil, nil, nil)

	req := httptest.NewRequest("GET", "/api/v1/stacks/web", nil)
	rec := httptest.NewRecorder()
	srv.httpServer.Handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
}

func TestVersionEndpoint(t *testing.T) {
	srv := New(":0", &Status{}, nil, nil, nil, nil)

	req := httptest.NewRequest("GET", "/api/v1/version", nil)
	rec := httptest.NewRecorder()
	srv.httpServer.Handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	var resp map[string]string
	json.NewDecoder(rec.Body).Decode(&resp)
	if _, ok := resp["server"]; !ok {
		t.Error("expected 'server' in version response")
	}
	if _, ok := resp["go"]; !ok {
		t.Error("expected 'go' in version response")
	}
}
```

- [ ] **Step 3: Run tests to verify they fail**

Run: `cd /Users/mohitsharma44/devel/worktrees/dockcd-planning-7e7 && go test -race -v ./internal/server/ -run "TestGetStacks|TestGetStack|TestVersion"`
Expected: Compilation errors — `New()` signature changed, new types don't exist

- [ ] **Step 4: Implement new API endpoints**

This is a significant expansion of `server.go`. The `New()` function signature grows to accept dependencies needed by API handlers:

```go
// Reconciler interface for triggering reconciles from the API.
type ReconcileTriggerer interface {
	TriggerReconcile(ctx context.Context, stackName string) (string, error) // returns reconcile ID
	TriggerReconcileAll(ctx context.Context) (string, error)
}

// Suspender interface for suspend/resume from the API.
type Suspender interface {
	SuspendStack(name string) error
	ResumeStack(name string) error
	IsSuspended(name string) bool
}

func New(addr string, status *Status, logger *slog.Logger, suspender Suspender, triggerer ReconcileTriggerer, eventBus *notify.EventBus) *Server {
```

Register all new routes:
```go
mux.Handle("GET /metrics", promhttp.Handler())
mux.HandleFunc("GET /healthz", s.handleHealthz)
mux.HandleFunc("GET /api/v1/stacks", s.handleGetStacks)
mux.HandleFunc("GET /api/v1/stacks/{name}", s.handleGetStack)
mux.HandleFunc("POST /api/v1/stacks/{name}/suspend", s.handleSuspend)
mux.HandleFunc("POST /api/v1/stacks/{name}/resume", s.handleResume)
mux.HandleFunc("POST /api/v1/reconcile", s.handleReconcileAll)
mux.HandleFunc("POST /api/v1/reconcile/{name}", s.handleReconcile)
mux.HandleFunc("GET /api/v1/reconcile/{id}/events", s.handleReconcileEvents)
mux.HandleFunc("GET /api/v1/logs", s.handleLogs)
mux.HandleFunc("GET /api/v1/version", s.handleVersion)
```

Implement each handler. The full implementations are straightforward JSON encode/decode handlers. The SSE endpoints (`/logs`, `/reconcile/{id}/events`) use `flusher, ok := w.(http.Flusher)` to stream.

- [ ] **Step 5: Update existing New() call sites**

Update `cmd/dockcd/main.go` where `server.New()` is called to pass the new parameters (nil for now — will be wired in the CLI refactor task).

- [ ] **Step 6: Run full test suite**

Run: `cd /Users/mohitsharma44/devel/worktrees/dockcd-planning-7e7 && go test -race ./...`
Expected: All tests PASS

- [ ] **Step 7: Commit**

```bash
cd /Users/mohitsharma44/devel/worktrees/dockcd-planning-7e7
git add internal/server/ cmd/dockcd/main.go
git commit -m "feat: add API v1 endpoints for stacks, reconcile, suspend, resume, version"
```

---

## Task 7: Reconciler — Async Reconcile Triggering and Event Emission

**Files:**
- Modify: `internal/reconciler/reconciler.go`
- Modify: `internal/reconciler/reconciler_test.go`

### Verification Spec

**Functional:**
- Reconciler gains `TriggerReconcile(ctx, stackName) (string, error)` method — triggers async single-stack deploy
- Reconciler gains `TriggerReconcileAll(ctx) (string, error)` method — triggers async full deploy
- Both return a reconcile ID (e.g., `"rec-<unix-timestamp>"`)
- Reconcile runs in a background goroutine
- Reconciler emits events on the EventBus at each lifecycle point: reconcile start, hook start/complete, deploy start, health check, rollback, reconcile complete
- Reconciler accepts an `*notify.EventBus` in its Config
- `TriggerReconcile` for a suspended stack returns an error
- `TriggerReconcile` for a non-existent stack returns an error
- Duplicate concurrent reconciles for the same stack are rejected (already reconciling)

**Non-functional:**
- Async reconcile goroutines clean up on context cancellation
- No data races between poll-triggered reconcile and API-triggered reconcile (mutex or channel serialization)

---

- [ ] **Step 1: Write failing tests**

Add to `internal/reconciler/reconciler_test.go`:

```go
func TestTriggerReconcileNonExistentStack(t *testing.T) {
	poller := &mockPoller{lastHash: "abc123"}
	r, _ := testReconciler(t, poller, []config.Stack{{Name: "web", Path: "web"}}, nil)

	_, err := r.TriggerReconcile(context.Background(), "nonexistent")
	if err == nil {
		t.Fatal("expected error for nonexistent stack")
	}
}

func TestTriggerReconcileSuspendedStack(t *testing.T) {
	poller := &mockPoller{
		lastHash:        "abc123",
		suspendedStacks: map[string]bool{"web": true},
	}
	r, _ := testReconciler(t, poller, []config.Stack{{Name: "web", Path: "web"}}, nil)

	_, err := r.TriggerReconcile(context.Background(), "web")
	if err == nil {
		t.Fatal("expected error for suspended stack")
	}
}

func TestTriggerReconcileReturnsID(t *testing.T) {
	poller := &mockPoller{lastHash: "abc123"}
	r, _ := testReconciler(t, poller, []config.Stack{{Name: "web", Path: "web"}}, nil)

	id, err := r.TriggerReconcile(context.Background(), "web")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if id == "" {
		t.Error("expected non-empty reconcile ID")
	}
	if !strings.HasPrefix(id, "rec-") {
		t.Errorf("expected ID prefix 'rec-', got %q", id)
	}
}
```

- [ ] **Step 2: Implement TriggerReconcile and TriggerReconcileAll**

Add to `internal/reconciler/reconciler.go`:

```go
import (
	// add:
	"strconv"
	"github.com/mohitsharma44/dockcd/internal/notify"
)

// Add EventBus to Config and Reconciler:
// Config.EventBus *notify.EventBus
// Reconciler.eventBus *notify.EventBus

func (r *Reconciler) TriggerReconcile(ctx context.Context, stackName string) (string, error) {
	// Verify stack exists.
	var found bool
	for _, s := range r.hostStacks {
		if s.Name == stackName {
			found = true
			break
		}
	}
	if !found {
		return "", fmt.Errorf("stack %q not found", stackName)
	}

	if r.poller.IsSuspended(stackName) {
		return "", fmt.Errorf("stack %q is suspended", stackName)
	}

	id := "rec-" + strconv.FormatInt(time.Now().UnixNano(), 10)

	go func() {
		r.logger.Info("force reconcile triggered", "stack", stackName, "id", id)
		// Fetch latest
		if _, err := r.poller.Fetch(); err != nil {
			r.logger.Error("force reconcile fetch failed", "stack", stackName, "error", err)
			return
		}
		// Build and deploy single stack
		stacks := r.filterStackByName(stackName)
		if len(stacks) == 0 {
			return
		}
		groups, err := deploy.BuildGraph(stacks)
		if err != nil {
			r.logger.Error("force reconcile graph failed", "stack", stackName, "error", err)
			return
		}
		commitHash := r.poller.LastHash()
		if err := r.deployGroups(ctx, groups, commitHash); err != nil && !errors.Is(err, ErrRolledBack) {
			r.logger.Error("force reconcile deploy failed", "stack", stackName, "error", err)
		}
		r.status.Update(time.Now(), commitHash)
	}()

	return id, nil
}

func (r *Reconciler) TriggerReconcileAll(ctx context.Context) (string, error) {
	id := "rec-" + strconv.FormatInt(time.Now().UnixNano(), 10)

	go func() {
		r.logger.Info("force reconcile all triggered", "id", id)
		if _, err := r.poller.Fetch(); err != nil {
			r.logger.Error("force reconcile fetch failed", "error", err)
			return
		}
		if err := r.deployAll(ctx); err != nil {
			r.logger.Error("force reconcile deploy failed", "error", err)
		}
		r.status.Update(time.Now(), r.poller.LastHash())
	}()

	return id, nil
}

// filterStackByName returns a single stack as a deploy.Stack slice (for force-deploy).
func (r *Reconciler) filterStackByName(name string) []deploy.Stack {
	for _, cs := range r.hostStacks {
		if cs.Name == name {
			return r.configToDeployStacks([]config.Stack{cs})
		}
	}
	return nil
}
```

- [ ] **Step 3: Add event emission at lifecycle points**

Add helper method:
```go
func (r *Reconciler) emit(eventType notify.EventType, stack, commit, msg string) {
	if r.eventBus == nil {
		return
	}
	r.eventBus.Emit(notify.Event{
		Type:      eventType,
		Timestamp: time.Now(),
		Stack:     stack,
		Host:      r.hostname,
		Commit:    commit,
		Message:   msg,
	})
}
```

Add `r.emit(...)` calls at key points in `reconcile()`, `deployStack()`, `rollback()`.

- [ ] **Step 4: Run full test suite**

Run: `cd /Users/mohitsharma44/devel/worktrees/dockcd-planning-7e7 && go test -race ./...`
Expected: All tests PASS

- [ ] **Step 5: Commit**

```bash
cd /Users/mohitsharma44/devel/worktrees/dockcd-planning-7e7
git add internal/reconciler/
git commit -m "feat: add async reconcile triggering and event emission"
```

---

## Task 8: CLI Subcommand Router and Serve Command

**Files:**
- Modify: `cmd/dockcd/main.go` (refactor to subcommand router)
- Create: `cmd/dockcd/serve.go` (extract daemon startup)

### Verification Spec

**Functional:**
- `dockcd` with no args starts the daemon (backward compat)
- `dockcd serve` starts the daemon
- `dockcd serve --config`, `--host`, `--log-format`, `--metrics-port`, `--repo-dir`, `--poll-interval`, `--initial-sync` all work as before
- Unknown subcommand prints usage and exits 2
- `dockcd version` prints version (even without a server running)
- All existing flags on the daemon still work identically

**Non-functional:**
- Existing systemd units and Dockerfiles work unchanged (no args = serve)
- No new dependencies (stdlib flag only)
- Clean separation: main.go is <30 lines, serve.go contains daemon logic

**Edge cases:**
- `dockcd --help` prints usage for all subcommands
- `dockcd serve --help` prints serve-specific flags
- `dockcd unknowncommand` exits 2 with error message

---

- [ ] **Step 1: Create serve.go with extracted daemon logic**

Move the body of `main()` from `main.go` into a `runServe(args []string) int` function in `cmd/dockcd/serve.go`. The function accepts os.Args[2:] (or os.Args[1:] if no subcommand), parses flags, and returns an exit code.

```go
// cmd/dockcd/serve.go
package main

import (
	// all current imports from main.go
)

func runServe(args []string) int {
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	configPath := fs.String("config", "gitops.yaml", "path to gitops config file")
	// ... all current flags ...
	fs.Parse(args)

	// ... entire current main() body, but return 0/1 instead of os.Exit ...
}
```

- [ ] **Step 2: Refactor main.go to subcommand router**

```go
// cmd/dockcd/main.go
package main

import (
	"fmt"
	"os"
)

// Version is set at build time via -ldflags.
var Version = "dev"

func main() {
	if len(os.Args) < 2 {
		// No subcommand — default to serve (backward compat).
		os.Exit(runServe(os.Args[1:]))
	}

	switch os.Args[1] {
	case "serve":
		os.Exit(runServe(os.Args[2:]))
	case "version":
		os.Exit(runVersion(os.Args[2:]))
	case "get":
		os.Exit(runGet(os.Args[2:]))
	case "reconcile":
		os.Exit(runReconcile(os.Args[2:]))
	case "suspend":
		os.Exit(runSuspend(os.Args[2:]))
	case "resume":
		os.Exit(runResume(os.Args[2:]))
	case "logs":
		os.Exit(runLogs(os.Args[2:]))
	case "stats":
		os.Exit(runStats(os.Args[2:]))
	default:
		// Check if it looks like a flag (e.g. --config) — treat as serve.
		if len(os.Args[1]) > 0 && os.Args[1][0] == '-' {
			os.Exit(runServe(os.Args[1:]))
		}
		fmt.Fprintf(os.Stderr, "unknown command: %s\n", os.Args[1])
		printUsage()
		os.Exit(2)
	}
}

func printUsage() {
	fmt.Fprintln(os.Stderr, `Usage: dockcd <command> [flags]

Commands:
  serve       Start the dockcd daemon (default if no command given)
  get         Get stack status (get stacks | get stack <name>)
  reconcile   Trigger reconciliation (reconcile stacks | reconcile stack <name>)
  suspend     Suspend a stack (suspend stack <name>)
  resume      Resume a stack (resume stack <name>)
  logs        Stream server logs
  stats       Show metrics summary
  version     Show client and server version`)
}
```

- [ ] **Step 3: Create stub functions for unimplemented commands**

Create minimal stubs for `runGet`, `runReconcile`, `runSuspend`, `runResume`, `runLogs`, `runStats` that print "not implemented" and return 1. These will be implemented in subsequent tasks.

```go
// cmd/dockcd/get.go
package main

import "fmt"

func runGet(args []string) int {
	fmt.Println("not implemented")
	return 1
}
```

(Similar for each command file.)

- [ ] **Step 4: Create version command**

```go
// cmd/dockcd/version.go
package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"runtime"
)

func runVersion(args []string) int {
	fmt.Printf("dockcd client: %s (go: %s)\n", Version, runtime.Version())

	// Try to reach server for server version.
	serverURL := resolveServerURL(args)
	resp, err := http.Get(serverURL + "/api/v1/version")
	if err != nil {
		fmt.Println("server: unreachable")
		return 0
	}
	defer resp.Body.Close()

	var ver map[string]string
	if err := json.NewDecoder(resp.Body).Decode(&ver); err != nil {
		fmt.Println("server: error reading version")
		return 0
	}
	fmt.Printf("dockcd server: %s (go: %s)\n", ver["server"], ver["go"])
	return 0
}
```

- [ ] **Step 5: Verify build and backward compat**

Run: `cd /Users/mohitsharma44/devel/worktrees/dockcd-planning-7e7 && go build ./cmd/dockcd/`
Expected: Builds successfully

Run: `cd /Users/mohitsharma44/devel/worktrees/dockcd-planning-7e7 && go test -race ./...`
Expected: All tests PASS

- [ ] **Step 6: Commit**

```bash
cd /Users/mohitsharma44/devel/worktrees/dockcd-planning-7e7
git add cmd/dockcd/
git commit -m "feat: refactor to subcommand router with serve extraction and version command"
```

---

## Task 9: HTTP Client Package and CLI Output Formatting

**Files:**
- Create: `internal/client/client.go`
- Create: `internal/client/client_test.go`
- Create: `cmd/dockcd/output.go`

### Verification Spec

**Functional:**
- `client.Client` wraps HTTP calls to the dockcd server API
- `Client.GetStacks() ([]StackState, error)`
- `Client.GetStack(name) (*StackState, error)` — returns typed error for 404
- `Client.Reconcile(name) (ReconcileResponse, error)` — single stack
- `Client.ReconcileAll() (ReconcileResponse, error)` — all stacks
- `Client.Suspend(name) (*StackState, error)`
- `Client.Resume(name) (*StackState, error)`
- `Client.StreamEvents(reconcileID) (<-chan Event, error)` — SSE stream
- `Client.Version() (VersionResponse, error)`
- All methods handle non-2xx responses with descriptive errors
- `output.go` provides `PrintStacksTable(stacks, noColor)` and `PrintJSON(v)`

**Non-functional:**
- Client tested against httptest servers (no real network)
- Client respects context for cancellation
- Table output aligns columns properly
- Color output disabled when `NO_COLOR` is set or `--no-color` flag

---

- [ ] **Step 1: Write failing tests for client**

```go
// internal/client/client_test.go
package client

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/mohitsharma44/dockcd/internal/server"
)

func TestGetStacks(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/stacks" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		json.NewEncoder(w).Encode(map[string]any{
			"stacks": []server.StackState{
				{Name: "web", Status: "Ready", Health: "Healthy"},
			},
		})
	}))
	defer ts.Close()

	c := New(ts.URL)
	stacks, err := c.GetStacks()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(stacks) != 1 {
		t.Fatalf("expected 1 stack, got %d", len(stacks))
	}
	if stacks[0].Name != "web" {
		t.Errorf("expected name 'web', got %q", stacks[0].Name)
	}
}

func TestGetStackNotFound(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		json.NewEncoder(w).Encode(map[string]string{"error": "not found"})
	}))
	defer ts.Close()

	c := New(ts.URL)
	_, err := c.GetStack("missing")
	if err == nil {
		t.Fatal("expected error for 404")
	}
}

func TestReconcile(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" {
			t.Errorf("expected POST, got %s", r.Method)
		}
		w.WriteHeader(http.StatusAccepted)
		json.NewEncoder(w).Encode(map[string]string{"id": "rec-123", "status": "queued"})
	}))
	defer ts.Close()

	c := New(ts.URL)
	resp, err := c.Reconcile("web")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.ID != "rec-123" {
		t.Errorf("expected ID 'rec-123', got %q", resp.ID)
	}
}
```

- [ ] **Step 2: Implement client package**

```go
// internal/client/client.go
package client

import (
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/mohitsharma44/dockcd/internal/server"
)

// Client is an HTTP client for the dockcd API.
type Client struct {
	baseURL    string
	httpClient *http.Client
}

// ReconcileResponse is returned by the reconcile endpoint.
type ReconcileResponse struct {
	ID     string `json:"id"`
	Status string `json:"status"`
	Stack  string `json:"stack,omitempty"`
}

// VersionResponse is returned by the version endpoint.
type VersionResponse struct {
	Server string `json:"server"`
	Go     string `json:"go"`
}

// New creates a Client for the given server URL.
func New(baseURL string) *Client {
	return &Client{baseURL: baseURL, httpClient: &http.Client{}}
}

func (c *Client) GetStacks() ([]server.StackState, error) {
	resp, err := c.httpClient.Get(c.baseURL + "/api/v1/stacks")
	if err != nil {
		return nil, fmt.Errorf("connecting to server: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("server returned %d", resp.StatusCode)
	}

	var result struct {
		Stacks []server.StackState `json:"stacks"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decoding response: %w", err)
	}
	return result.Stacks, nil
}

func (c *Client) GetStack(name string) (*server.StackState, error) {
	resp, err := c.httpClient.Get(c.baseURL + "/api/v1/stacks/" + name)
	if err != nil {
		return nil, fmt.Errorf("connecting to server: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return nil, fmt.Errorf("stack %q not found", name)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("server returned %d", resp.StatusCode)
	}

	var state server.StackState
	if err := json.NewDecoder(resp.Body).Decode(&state); err != nil {
		return nil, fmt.Errorf("decoding response: %w", err)
	}
	return &state, nil
}

func (c *Client) Reconcile(name string) (ReconcileResponse, error) {
	resp, err := c.httpClient.Post(c.baseURL+"/api/v1/reconcile/"+name, "application/json", nil)
	if err != nil {
		return ReconcileResponse{}, fmt.Errorf("connecting to server: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return ReconcileResponse{}, fmt.Errorf("stack %q not found", name)
	}
	if resp.StatusCode != http.StatusAccepted {
		return ReconcileResponse{}, fmt.Errorf("server returned %d", resp.StatusCode)
	}

	var result ReconcileResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return ReconcileResponse{}, fmt.Errorf("decoding response: %w", err)
	}
	return result, nil
}

func (c *Client) ReconcileAll() (ReconcileResponse, error) {
	resp, err := c.httpClient.Post(c.baseURL+"/api/v1/reconcile", "application/json", nil)
	if err != nil {
		return ReconcileResponse{}, fmt.Errorf("connecting to server: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusAccepted {
		return ReconcileResponse{}, fmt.Errorf("server returned %d", resp.StatusCode)
	}

	var result ReconcileResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return ReconcileResponse{}, fmt.Errorf("decoding response: %w", err)
	}
	return result, nil
}

func (c *Client) Suspend(name string) (*server.StackState, error) {
	resp, err := c.httpClient.Post(c.baseURL+"/api/v1/stacks/"+name+"/suspend", "application/json", nil)
	if err != nil {
		return nil, fmt.Errorf("connecting to server: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return nil, fmt.Errorf("stack %q not found", name)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("server returned %d", resp.StatusCode)
	}

	var state server.StackState
	if err := json.NewDecoder(resp.Body).Decode(&state); err != nil {
		return nil, fmt.Errorf("decoding response: %w", err)
	}
	return &state, nil
}

func (c *Client) Resume(name string) (*server.StackState, error) {
	resp, err := c.httpClient.Post(c.baseURL+"/api/v1/stacks/"+name+"/resume", "application/json", nil)
	if err != nil {
		return nil, fmt.Errorf("connecting to server: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return nil, fmt.Errorf("stack %q not found", name)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("server returned %d", resp.StatusCode)
	}

	var state server.StackState
	if err := json.NewDecoder(resp.Body).Decode(&state); err != nil {
		return nil, fmt.Errorf("decoding response: %w", err)
	}
	return &state, nil
}

func (c *Client) Version() (VersionResponse, error) {
	resp, err := c.httpClient.Get(c.baseURL + "/api/v1/version")
	if err != nil {
		return VersionResponse{}, fmt.Errorf("connecting to server: %w", err)
	}
	defer resp.Body.Close()

	var result VersionResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return VersionResponse{}, fmt.Errorf("decoding response: %w", err)
	}
	return result, nil
}
```

- [ ] **Step 3: Implement output.go**

```go
// cmd/dockcd/output.go
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/mohitsharma44/dockcd/internal/server"
)

func useColor() bool {
	return os.Getenv("NO_COLOR") == ""
}

func colorize(s, color string) string {
	if !useColor() {
		return s
	}
	return color + s + "\033[0m"
}

func statusIcon(status string) string {
	switch status {
	case "Ready":
		return colorize("✓", "\033[32m")
	case "Failed":
		return colorize("✗", "\033[31m")
	case "Suspended":
		return colorize("⏸", "\033[33m")
	case "Reconciling":
		return colorize("↻", "\033[34m")
	default:
		return " "
	}
}

func printStacksTable(stacks []server.StackState) {
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 3, ' ', 0)
	fmt.Fprintln(w, "NAME\tBRANCH\tSTATUS\tHEALTH\tLAST DEPLOY\tCOMMIT")
	for _, s := range stacks {
		lastDeploy := "--"
		if !s.LastDeploy.IsZero() {
			lastDeploy = s.LastDeploy.Format("2006-01-02 15:04:05")
		}
		commit := s.Commit
		if len(commit) > 7 {
			commit = commit[:7]
		}
		health := s.Health
		if health == "" {
			health = "--"
		}
		fmt.Fprintf(w, "%s %s\t%s\t%s\t%s\t%s\t%s\n",
			statusIcon(s.Status), s.Name, s.Branch, s.Status, health, lastDeploy, commit)
	}
	w.Flush()
}

func printJSON(v any) {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	enc.Encode(v)
}

// resolveServerURL extracts --server flag or falls back to env/default.
func resolveServerURL(args []string) string {
	for i, arg := range args {
		if arg == "--server" && i+1 < len(args) {
			return args[i+1]
		}
		if strings.HasPrefix(arg, "--server=") {
			return strings.TrimPrefix(arg, "--server=")
		}
	}
	if url := os.Getenv("DOCKCD_SERVER"); url != "" {
		return url
	}
	return "http://localhost:9092"
}
```

- [ ] **Step 4: Run tests**

Run: `cd /Users/mohitsharma44/devel/worktrees/dockcd-planning-7e7 && go test -race ./...`
Expected: All tests PASS

- [ ] **Step 5: Commit**

```bash
cd /Users/mohitsharma44/devel/worktrees/dockcd-planning-7e7
git add internal/client/ cmd/dockcd/output.go
git commit -m "feat: add HTTP client package and CLI output formatting"
```

---

## Task 10: Implement CLI Commands (get, reconcile, suspend, resume, logs, stats)

**Files:**
- Modify: `cmd/dockcd/get.go`
- Modify: `cmd/dockcd/reconcile.go`
- Modify: `cmd/dockcd/suspend.go`
- Create: `cmd/dockcd/resume.go` (if not already a stub)
- Modify: `cmd/dockcd/logs.go`
- Modify: `cmd/dockcd/stats.go`

### Verification Spec

**Functional:**
- `dockcd get stacks` prints table of all stacks
- `dockcd get stacks --output json` prints JSON
- `dockcd get stacks --watch` polls and refreshes (basic implementation)
- `dockcd get stack <name>` prints single stack detail
- `dockcd reconcile stack <name>` triggers async reconcile, streams SSE output by default
- `dockcd reconcile stack <name> --no-wait` returns immediately after 202
- `dockcd reconcile stacks` triggers full reconcile
- `dockcd suspend stack <name>` suspends and prints updated state
- `dockcd resume stack <name>` resumes and prints updated state
- `dockcd logs` streams SSE logs
- `dockcd stats` fetches /metrics and prints formatted summary
- All commands respect `--server` flag and `DOCKCD_SERVER` env var
- Missing subresource (e.g., `dockcd get` with no args) prints usage and exits 2
- Server unreachable prints clear error message

**Non-functional:**
- Consistent exit codes: 0=success, 1=failure, 2=usage error
- SSE streaming handles Ctrl+C gracefully (clean disconnect)
- `--no-color` flag and `NO_COLOR` env var disable color everywhere

---

- [ ] **Step 1: Implement runGet**

```go
// cmd/dockcd/get.go
package main

import (
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/mohitsharma44/dockcd/internal/client"
)

func runGet(args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "Usage: dockcd get <stacks|stack <name>> [flags]")
		return 2
	}

	switch args[0] {
	case "stacks":
		return runGetStacks(args[1:])
	case "stack":
		if len(args) < 2 {
			fmt.Fprintln(os.Stderr, "Usage: dockcd get stack <name> [flags]")
			return 2
		}
		return runGetStack(args[1], args[2:])
	default:
		fmt.Fprintf(os.Stderr, "unknown resource: %s\nUsage: dockcd get <stacks|stack <name>>\n", args[0])
		return 2
	}
}

func runGetStacks(args []string) int {
	fs := flag.NewFlagSet("get stacks", flag.ExitOnError)
	outputFmt := fs.String("output", "table", "output format: table or json")
	watch := fs.Bool("watch", false, "poll and refresh")
	server := fs.String("server", resolveServerURL(nil), "dockcd server URL")
	fs.Parse(args)

	c := client.New(*server)

	for {
		stacks, err := c.GetStacks()
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			return 1
		}

		if *outputFmt == "json" {
			printJSON(stacks)
		} else {
			printStacksTable(stacks)
		}

		if !*watch {
			return 0
		}
		time.Sleep(2 * time.Second)
		fmt.Print("\033[2J\033[H") // clear screen
	}
}

func runGetStack(name string, args []string) int {
	fs := flag.NewFlagSet("get stack", flag.ExitOnError)
	outputFmt := fs.String("output", "table", "output format: table or json")
	server := fs.String("server", resolveServerURL(nil), "dockcd server URL")
	fs.Parse(args)

	c := client.New(*server)
	stack, err := c.GetStack(name)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}

	if *outputFmt == "json" {
		printJSON(stack)
	} else {
		printStacksTable([]server.StackState{*stack})
	}
	return 0
}
```

- [ ] **Step 2: Implement runReconcile**

```go
// cmd/dockcd/reconcile.go
package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/mohitsharma44/dockcd/internal/client"
)

func runReconcile(args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "Usage: dockcd reconcile <stacks|stack <name>> [flags]")
		return 2
	}

	switch args[0] {
	case "stacks":
		return runReconcileAll(args[1:])
	case "stack":
		if len(args) < 2 {
			fmt.Fprintln(os.Stderr, "Usage: dockcd reconcile stack <name> [flags]")
			return 2
		}
		return runReconcileStack(args[1], args[2:])
	default:
		fmt.Fprintf(os.Stderr, "unknown resource: %s\n", args[0])
		return 2
	}
}

func runReconcileStack(name string, args []string) int {
	fs := flag.NewFlagSet("reconcile stack", flag.ExitOnError)
	noWait := fs.Bool("no-wait", false, "return immediately without streaming progress")
	server := fs.String("server", resolveServerURL(nil), "dockcd server URL")
	fs.Parse(args)

	c := client.New(*server)
	resp, err := c.Reconcile(name)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}

	fmt.Printf("%s triggering reconciliation for stack %s\n",
		colorize("►", "\033[34m"), name)

	if *noWait {
		fmt.Printf("reconcile queued: %s\n", resp.ID)
		return 0
	}

	// TODO: Stream SSE events from /api/v1/reconcile/{id}/events
	// For now, print the ID and return.
	fmt.Printf("reconcile started: %s (use 'dockcd get stack %s' to check status)\n", resp.ID, name)
	return 0
}

func runReconcileAll(args []string) int {
	fs := flag.NewFlagSet("reconcile stacks", flag.ExitOnError)
	noWait := fs.Bool("no-wait", false, "return immediately")
	server := fs.String("server", resolveServerURL(nil), "dockcd server URL")
	fs.Parse(args)

	c := client.New(*server)
	resp, err := c.ReconcileAll()
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}

	fmt.Printf("%s triggering reconciliation for all stacks\n",
		colorize("►", "\033[34m"))

	if *noWait {
		fmt.Printf("reconcile queued: %s\n", resp.ID)
		return 0
	}

	fmt.Printf("reconcile started: %s\n", resp.ID)
	return 0
}
```

- [ ] **Step 3: Implement runSuspend and runResume**

```go
// cmd/dockcd/suspend.go
package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/mohitsharma44/dockcd/internal/client"
)

func runSuspend(args []string) int {
	if len(args) < 2 || args[0] != "stack" {
		fmt.Fprintln(os.Stderr, "Usage: dockcd suspend stack <name> [flags]")
		return 2
	}
	name := args[1]

	fs := flag.NewFlagSet("suspend", flag.ExitOnError)
	server := fs.String("server", resolveServerURL(nil), "dockcd server URL")
	fs.Parse(args[2:])

	c := client.New(*server)
	stack, err := c.Suspend(name)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}

	fmt.Printf("%s stack %q suspended\n", colorize("⏸", "\033[33m"), stack.Name)
	return 0
}

func runResume(args []string) int {
	if len(args) < 2 || args[0] != "stack" {
		fmt.Fprintln(os.Stderr, "Usage: dockcd resume stack <name> [flags]")
		return 2
	}
	name := args[1]

	fs := flag.NewFlagSet("resume", flag.ExitOnError)
	server := fs.String("server", resolveServerURL(nil), "dockcd server URL")
	fs.Parse(args[2:])

	c := client.New(*server)
	stack, err := c.Resume(name)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}

	fmt.Printf("%s stack %q resumed\n", colorize("✓", "\033[32m"), stack.Name)
	return 0
}
```

- [ ] **Step 4: Implement runLogs and runStats (basic)**

```go
// cmd/dockcd/logs.go
package main

import (
	"bufio"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/signal"
)

func runLogs(args []string) int {
	fs := flag.NewFlagSet("logs", flag.ExitOnError)
	server := fs.String("server", resolveServerURL(nil), "dockcd server URL")
	fs.Parse(args)

	resp, err := http.Get(*server + "/api/v1/logs")
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	defer resp.Body.Close()

	// Handle Ctrl+C.
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt)

	scanner := bufio.NewScanner(resp.Body)
	for scanner.Scan() {
		select {
		case <-sig:
			return 0
		default:
		}
		line := scanner.Text()
		if len(line) > 5 && line[:5] == "data:" {
			fmt.Println(line[6:])
		}
	}
	return 0
}
```

```go
// cmd/dockcd/stats.go
package main

import (
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
)

func runStats(args []string) int {
	fs := flag.NewFlagSet("stats", flag.ExitOnError)
	server := fs.String("server", resolveServerURL(nil), "dockcd server URL")
	fs.Parse(args)

	resp, err := http.Get(*server + "/metrics")
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	defer resp.Body.Close()

	// For now, pipe through. Can add formatting later.
	io.Copy(os.Stdout, resp.Body)
	return 0
}
```

- [ ] **Step 5: Verify build**

Run: `cd /Users/mohitsharma44/devel/worktrees/dockcd-planning-7e7 && go build ./cmd/dockcd/ && go test -race ./...`
Expected: Build succeeds, all tests PASS

- [ ] **Step 6: Commit**

```bash
cd /Users/mohitsharma44/devel/worktrees/dockcd-planning-7e7
git add cmd/dockcd/
git commit -m "feat: implement CLI commands (get, reconcile, suspend, resume, logs, stats)"
```

---

## Task 11: Wire Everything Together in serve.go

**Files:**
- Modify: `cmd/dockcd/serve.go`

### Verification Spec

**Functional:**
- `dockcd serve` creates and wires: EventBus, Reconciler (with EventBus), Server (with Suspender, ReconcileTriggerer, EventBus)
- Poller is passed as Suspender to Server
- Reconciler is passed as ReconcileTriggerer to Server
- EventBus is shared between Reconciler and Server
- Graceful shutdown closes EventBus
- All existing behavior preserved (flags, env vars, signal handling)
- `-ldflags "-X main.Version=x.y.z"` sets the version for the version endpoint

**Non-functional:**
- Single clean wiring point — no circular dependencies
- Build still works: `go build ./cmd/dockcd/`

---

- [ ] **Step 1: Update serve.go wiring**

In `runServe()`, after creating the poller and before creating the server, create the EventBus and pass it through:

```go
// Create event bus.
eventBus := notify.NewEventBus()

// Create reconciler with event bus.
rec, err := reconciler.New(reconciler.Config{
	// ... existing fields ...
	EventBus: eventBus,
})

// Create server with suspend/resume and reconcile trigger capabilities.
srv := server.New(
	fmt.Sprintf(":%d", *metricsPort),
	status,
	logger,
	poller,   // Suspender
	rec,      // ReconcileTriggerer
	eventBus,
)

// In shutdown section, add:
eventBus.Close()
```

- [ ] **Step 2: Verify full build and test suite**

Run: `cd /Users/mohitsharma44/devel/worktrees/dockcd-planning-7e7 && go build -ldflags "-X main.Version=0.2.0" ./cmd/dockcd/ && go test -race ./...`
Expected: Build succeeds, all tests PASS

- [ ] **Step 3: Commit**

```bash
cd /Users/mohitsharma44/devel/worktrees/dockcd-planning-7e7
git add cmd/dockcd/serve.go
git commit -m "feat: wire event bus, suspend/resume, and reconcile trigger into serve command"
```

---

## Task 12: Integration Tests for Hooks and Suspend/Resume

**Files:**
- Modify: `internal/reconciler/reconciler_integration_test.go`

### Verification Spec

**Functional:**
- Integration test: stack with pre_deploy hook — hook runs before compose, hook env vars are set
- Integration test: pre_deploy hook failure prevents deploy
- Integration test: suspended stack is skipped during reconcile
- Integration test: resumed stack is deployed on next reconcile
- Integration test: force reconcile via TriggerReconcile deploys a single stack

**Non-functional:**
- Tests use real git repos (via testutil)
- Tests use recording runners (no real docker compose)
- All tests pass with `-race` flag
- Tests are tagged `//go:build integration`

---

- [ ] **Step 1: Write integration tests**

Add to `internal/reconciler/reconciler_integration_test.go`:

```go
func TestIntegrationPreDeployHookRuns(t *testing.T) {
	bareDir := testutil.InitBareRepo(t)
	workDir := testutil.CloneAndSetup(t, bareDir)

	// Create a stack with a hook script.
	testutil.CommitFile(t, workDir, "stacks/web/compose.yaml",
		"services:\n  web:\n    image: nginx", "add web stack")
	testutil.CommitFile(t, workDir, "stacks/web/hook.sh",
		"#!/bin/sh\necho \"HOOK: $DOCKCD_STACK_NAME $DOCKCD_COMMIT\"", "add hook")

	cloneDir := filepath.Join(t.TempDir(), "dockcd-clone")
	poller := git.NewPoller(bareDir, cloneDir)
	poller.SetStateFile(filepath.Join(cloneDir, ".dockcd_state"))

	hookTimeout := 5 * time.Second
	stacks := []config.Stack{{
		Name: "web",
		Path: "stacks/web",
		Hooks: &config.Hooks{
			PreDeploy: &config.HookConfig{Command: "sh hook.sh", Timeout: hookTimeout},
		},
	}}

	var mu sync.Mutex
	var calls []deployCall
	runner := func(ctx context.Context, dir, name string, args ...string) error {
		mu.Lock()
		defer mu.Unlock()
		calls = append(calls, deployCall{dir: dir, name: name, args: args})
		return nil
	}

	status := &server.Status{}
	rec, err := New(Config{
		Poller:       poller,
		HostStacks:   stacks,
		Hostname:     "integration-test",
		RepoDir:      cloneDir,
		PollInterval: 100 * time.Millisecond,
		Runner:       runner,
		Status:       status,
	})
	if err != nil {
		t.Fatalf("New() failed: %v", err)
	}

	if err := rec.initRepo(); err != nil {
		t.Fatalf("initRepo failed: %v", err)
	}

	if err := rec.deployAll(context.Background()); err != nil {
		t.Fatalf("deployAll failed: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	// Hook runs via sh -c (not tracked in calls). Compose pull + up = 2 calls.
	if len(calls) != 2 {
		t.Fatalf("expected 2 compose calls, got %d", len(calls))
	}
}

func TestIntegrationSuspendSkipsStack(t *testing.T) {
	bareDir := testutil.InitBareRepo(t)
	workDir := testutil.CloneAndSetup(t, bareDir)
	testutil.CommitFile(t, workDir, "stacks/web/compose.yaml",
		"services:\n  web:\n    image: nginx", "add web")

	cloneDir := filepath.Join(t.TempDir(), "dockcd-clone")
	poller := git.NewPoller(bareDir, cloneDir)
	poller.SetStateFile(filepath.Join(cloneDir, ".dockcd_state"))

	var mu sync.Mutex
	var calls []deployCall
	runner := func(ctx context.Context, dir, name string, args ...string) error {
		mu.Lock()
		defer mu.Unlock()
		calls = append(calls, deployCall{dir: dir, name: name, args: args})
		return nil
	}

	status := &server.Status{}
	rec, err := New(Config{
		Poller:       poller,
		HostStacks:   []config.Stack{{Name: "web", Path: "stacks/web"}},
		Hostname:     "integration-test",
		RepoDir:      cloneDir,
		PollInterval: 100 * time.Millisecond,
		Runner:       runner,
		Status:       status,
	})
	if err != nil {
		t.Fatalf("New() failed: %v", err)
	}

	if err := rec.initRepo(); err != nil {
		t.Fatalf("initRepo failed: %v", err)
	}

	// Suspend the stack.
	if err := poller.SuspendStack("web"); err != nil {
		t.Fatalf("SuspendStack: %v", err)
	}

	// Push a change.
	testutil.CommitFile(t, workDir, "stacks/web/compose.yaml",
		"services:\n  web:\n    image: nginx:v2", "update web")

	// Reconcile should skip suspended stack.
	if err := rec.reconcile(context.Background()); err != nil {
		t.Fatalf("reconcile failed: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(calls) != 0 {
		t.Errorf("expected 0 deploy calls for suspended stack, got %d", len(calls))
	}
}
```

- [ ] **Step 2: Run integration tests**

Run: `cd /Users/mohitsharma44/devel/worktrees/dockcd-planning-7e7 && go test -tags integration -race -v ./internal/reconciler/`
Expected: All integration tests PASS

- [ ] **Step 3: Commit**

```bash
cd /Users/mohitsharma44/devel/worktrees/dockcd-planning-7e7
git add internal/reconciler/reconciler_integration_test.go
git commit -m "test: add integration tests for hooks and suspend/resume"
```

---

## Task 13: Update CI Pipeline for Docker Build

**Files:**
- Modify: `.github/workflows/ci.yaml`

### Verification Spec

**Functional:**
- CI workflow adds a `docker build` step after tests pass
- Build uses `docker build -t dockcd:ci .`
- No push — build verification only
- Build step runs on ubuntu-latest (Docker is available)

**Non-functional:**
- Existing CI steps unchanged
- New step only runs if tests pass (sequential dependency)

---

- [ ] **Step 1: Add docker build step to CI**

Add after the existing `Build` step in `.github/workflows/ci.yaml`:

```yaml
      - name: Docker build
        run: docker build -t dockcd:ci .
```

- [ ] **Step 2: Commit**

```bash
cd /Users/mohitsharma44/devel/worktrees/dockcd-planning-7e7
git add .github/workflows/ci.yaml
git commit -m "ci: add Docker build verification step"
```

---

## Summary of tasks and dependencies

```
Task 1: Hook execution package          (independent)
Task 2: Config changes                   (independent)
Task 3: Hook deploy integration          (depends on 1, 2)
Task 4: Event bus                        (independent)
Task 5: Suspend/resume in poller         (independent)
Task 6: Server API endpoints             (depends on 4, 5)
Task 7: Async reconcile + events         (depends on 4, 6)
Task 8: CLI subcommand router            (independent)
Task 9: HTTP client + output             (depends on 6)
Task 10: CLI command implementations     (depends on 8, 9)
Task 11: Wire everything in serve.go     (depends on 3, 6, 7, 10)
Task 12: Integration tests               (depends on 3, 5, 11)
Task 13: CI Docker build                 (independent)
```

**Parallelizable groups:**
- Group A: Tasks 1, 2, 4, 5, 8, 13 (all independent)
- Group B: Tasks 3, 6 (after their deps)
- Group C: Tasks 7, 9 (after group B)
- Group D: Tasks 10, 11 (after group C)
- Group E: Task 12 (after group D)
