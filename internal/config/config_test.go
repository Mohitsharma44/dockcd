package config

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func writeTemp(t *testing.T, content string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "gitops.yaml")
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLoad(t *testing.T) {
	tests := []struct {
		name    string
		yaml    string
		wantErr string
	}{
		{
			name: "valid single host",
			yaml: `
repo: git@github.com:user/infra.git
base_path: /opt/stacks
hosts:
  server01:
    stacks:
      - name: monitoring
        path: monitoring/
      - name: app
        path: app/
        depends_on: [monitoring]
        post_deploy_delay: 30s
`,
			wantErr: "",
		},
		{
			name: "valid multiple hosts",
			yaml: `
repo: git@github.com:user/infra.git
base_path: /opt/stacks
hosts:
  server01:
    stacks:
      - name: web
        path: web/
  server02:
    stacks:
      - name: db
        path: db/
`,
			wantErr: "",
		},
		{
			name: "missing repo",
			yaml: `
base_path: /opt/stacks
hosts:
  server01:
    stacks:
      - name: app
        path: app/
`,
			wantErr: "repo is required",
		},
		{
			name: "missing base_path",
			yaml: `
repo: git@github.com:user/infra.git
hosts:
  server01:
    stacks:
      - name: app
        path: app/
`,
			wantErr: "base_path is required",
		},
		{
			name: "empty stacks",
			yaml: `
repo: git@github.com:user/infra.git
base_path: /opt/stacks
hosts:
  server01:
    stacks: []
`,
			wantErr: "at least one stack",
		},
		{
			name: "empty stack name",
			yaml: `
repo: git@github.com:user/infra.git
base_path: /opt/stacks
hosts:
  server01:
    stacks:
      - name: ""
        path: app/
`,
			wantErr: "stack name is required",
		},
		{
			name: "empty stack path",
			yaml: `
repo: git@github.com:user/infra.git
base_path: /opt/stacks
hosts:
  server01:
    stacks:
      - name: app
        path: ""
`,
			wantErr: "path is required",
		},
		{
			name: "depends_on non-existent",
			yaml: `
repo: git@github.com:user/infra.git
base_path: /opt/stacks
hosts:
  server01:
    stacks:
      - name: app
        path: app/
        depends_on: [ghost]
`,
			wantErr: "non-existent stack",
		},
		{
			name: "circular dependency",
			yaml: `
repo: git@github.com:user/infra.git
base_path: /opt/stacks
hosts:
  server01:
    stacks:
      - name: a
        path: a/
        depends_on: [b]
      - name: b
        path: b/
        depends_on: [a]
`,
			wantErr: "cycle detected",
		},
		{
			name: "diamond dependency valid",
			yaml: `
repo: git@github.com:user/infra.git
base_path: /opt/stacks
hosts:
  server01:
    stacks:
      - name: a
        path: a/
        depends_on: [b, c]
      - name: b
        path: b/
        depends_on: [d]
      - name: c
        path: c/
        depends_on: [d]
      - name: d
        path: d/
`,
			wantErr: "",
		},
		{
			name: "post_deploy_delay parsing",
			yaml: `
repo: git@github.com:user/infra.git
base_path: /opt/stacks
hosts:
  server01:
    stacks:
      - name: fast
        path: fast/
        post_deploy_delay: 30s
      - name: slow
        path: slow/
        post_deploy_delay: 1m
`,
			wantErr: "",
		},
		{
			name:    "invalid yaml",
			yaml:    `{{{`,
			wantErr: "parsing config file",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := writeTemp(t, tt.yaml)
			cfg, err := Load(path)

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
			_ = cfg
		})
	}
}

func TestLoadCycleDetectedError(t *testing.T) {
	yaml := `
repo: git@github.com:user/infra.git
base_path: /opt/stacks
hosts:
  server01:
    stacks:
      - name: a
        path: a/
        depends_on: [b]
      - name: b
        path: b/
        depends_on: [a]
`
	path := writeTemp(t, yaml)
	_, err := Load(path)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !errors.Is(err, ErrCycleDetected) {
		t.Fatalf("expected ErrCycleDetected, got %v", err)
	}
}

func TestLoadPostDeployDelay(t *testing.T) {
	yaml := `
repo: git@github.com:user/infra.git
base_path: /opt/stacks
hosts:
  server01:
    stacks:
      - name: fast
        path: fast/
        post_deploy_delay: 30s
      - name: slow
        path: slow/
        post_deploy_delay: 1m
`
	path := writeTemp(t, yaml)
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	stacks := cfg.Hosts["server01"].Stacks
	for _, st := range stacks {
		switch st.Name {
		case "fast":
			if st.PostDeployDelay != 30*time.Second {
				t.Errorf("fast: expected 30s, got %v", st.PostDeployDelay)
			}
		case "slow":
			if st.PostDeployDelay != 1*time.Minute {
				t.Errorf("slow: expected 1m, got %v", st.PostDeployDelay)
			}
		}
	}
}

func TestLoadHooksConfig(t *testing.T) {
	tests := []struct {
		name    string
		yaml    string
		wantErr string
	}{
		{
			name: "valid pre and post deploy hooks",
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
            command: "pre.sh"
            timeout: 10s
          post_deploy:
            command: "post.sh"
            timeout: 20s
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
            command: "pre.sh"
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
            command: "post.sh"
`,
			wantErr: "",
		},
		{
			name: "pre_deploy empty command",
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
			wantErr: "pre_deploy hook command must not be empty",
		},
		{
			name: "post_deploy empty command",
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
            command: ""
`,
			wantErr: "post_deploy hook command must not be empty",
		},
		{
			name: "no hooks backward compat",
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
			cfg, err := Load(path)

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
			_ = cfg
		})
	}
}

func TestLoadHookTimeout(t *testing.T) {
	content := `
repo: git@github.com:user/infra.git
base_path: /opt/stacks
hosts:
  server01:
    stacks:
      - name: app
        path: app/
        hooks:
          pre_deploy:
            command: "pre.sh"
            timeout: 10s
          post_deploy:
            command: "post.sh"
            timeout: 2m
`
	path := writeTemp(t, content)
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	stacks := cfg.Hosts["server01"].Stacks
	if len(stacks) != 1 {
		t.Fatalf("expected 1 stack, got %d", len(stacks))
	}
	st := stacks[0]
	if st.Hooks == nil {
		t.Fatal("expected hooks to be non-nil")
	}
	if st.Hooks.PreDeploy == nil {
		t.Fatal("expected pre_deploy hook to be non-nil")
	}
	if st.Hooks.PreDeploy.Timeout != 10*time.Second {
		t.Errorf("pre_deploy timeout: expected 10s, got %v", st.Hooks.PreDeploy.Timeout)
	}
	if st.Hooks.PostDeploy == nil {
		t.Fatal("expected post_deploy hook to be non-nil")
	}
	if st.Hooks.PostDeploy.Timeout != 2*time.Minute {
		t.Errorf("post_deploy timeout: expected 2m, got %v", st.Hooks.PostDeploy.Timeout)
	}
}

func TestLoadBranchConfig(t *testing.T) {
	content := `
repo: git@github.com:user/infra.git
base_path: /opt/stacks
branch: main
hosts:
  server01:
    stacks:
      - name: app
        path: app/
        branch: feature/dev
      - name: db
        path: db/
`
	path := writeTemp(t, content)
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if cfg.Branch != "main" {
		t.Errorf("top-level branch: expected %q, got %q", "main", cfg.Branch)
	}

	stacks := cfg.Hosts["server01"].Stacks
	for _, st := range stacks {
		switch st.Name {
		case "app":
			if st.Branch != "feature/dev" {
				t.Errorf("app stack branch: expected %q, got %q", "feature/dev", st.Branch)
			}
		case "db":
			if st.Branch != "" {
				t.Errorf("db stack branch: expected empty, got %q", st.Branch)
			}
		}
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
    url: "https://hooks.slack.com/services/xxx"
    events:
      - deploy_success
      - deploy_failure
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
  - name: my-webhook
    type: webhook
    url: "https://example.com/hook"
    events:
      - rollback_success
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
  - name: discord-alerts
    type: discord
    url: "https://discord.com/api/webhooks/xxx"
    events:
      - deploy_success
hosts:
  server01:
    stacks:
      - name: app
        path: app/
`,
			wantErr: "invalid notification type",
		},
		{
			name: "empty notification URL",
			yaml: `
repo: git@github.com:user/infra.git
base_path: /opt/stacks
notifications:
  - name: slack-alerts
    type: slack
    url: ""
    events:
      - deploy_success
hosts:
  server01:
    stacks:
      - name: app
        path: app/
`,
			wantErr: "notification URL must not be empty",
		},
		{
			name: "invalid event name",
			yaml: `
repo: git@github.com:user/infra.git
base_path: /opt/stacks
notifications:
  - name: slack-alerts
    type: slack
    url: "https://hooks.slack.com/services/xxx"
    events:
      - not_a_real_event
hosts:
  server01:
    stacks:
      - name: app
        path: app/
`,
			wantErr: "invalid notification event",
		},
		{
			name: "no notifications section",
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
			cfg, err := Load(path)

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
			_ = cfg
		})
	}
}

func TestLoadEnvVarSubstitution(t *testing.T) {
	t.Setenv("TEST_REPO_URL", "git@github.com:test/repo.git")
	t.Setenv("TEST_SLACK_URL", "https://hooks.slack.com/services/test")

	content := `
repo: ${TEST_REPO_URL}
base_path: /opt/stacks
notifications:
  - name: slack-alerts
    type: slack
    url: "${TEST_SLACK_URL}"
    events:
      - deploy_success
hosts:
  server01:
    stacks:
      - name: app
        path: app/
`
	path := writeTemp(t, content)
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if cfg.Repo != "git@github.com:test/repo.git" {
		t.Errorf("repo: expected expanded value, got %q", cfg.Repo)
	}
	if len(cfg.Notifications) != 1 {
		t.Fatalf("expected 1 notification, got %d", len(cfg.Notifications))
	}
	if cfg.Notifications[0].URL != "https://hooks.slack.com/services/test" {
		t.Errorf("notification URL: expected expanded value, got %q", cfg.Notifications[0].URL)
	}
}

func TestLoadEnvVarSubstitutionUnsetVar(t *testing.T) {
	// Ensure the var is unset
	os.Unsetenv("UNSET_WEBHOOK_URL")

	content := `
repo: git@github.com:user/infra.git
base_path: /opt/stacks
notifications:
  - name: slack-alerts
    type: slack
    url: "${UNSET_WEBHOOK_URL}"
    events:
      - deploy_success
hosts:
  server01:
    stacks:
      - name: app
        path: app/
`
	path := writeTemp(t, content)
	_, err := Load(path)
	if err == nil {
		t.Fatal("expected error for empty URL after env expansion, got nil")
	}
	if !strings.Contains(err.Error(), "notification URL must not be empty") {
		t.Fatalf("expected URL empty error, got %q", err.Error())
	}
}
