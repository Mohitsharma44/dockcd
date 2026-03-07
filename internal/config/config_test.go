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
