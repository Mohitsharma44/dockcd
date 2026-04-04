package config

import (
	"fmt"
	"os"
	"time"

	"gopkg.in/yaml.v3"
)

// validNotificationTypes holds the accepted notification backend types.
var validNotificationTypes = map[string]bool{
	"slack":   true,
	"webhook": true,
}

// validEventTypes holds the accepted event names for notifications.
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

// HookConfig describes a single lifecycle hook.
type HookConfig struct {
	Command string        `yaml:"command"`
	Timeout time.Duration `yaml:"timeout"`
}

// Hooks groups the pre- and post-deploy hooks for a stack.
type Hooks struct {
	PreDeploy  *HookConfig `yaml:"pre_deploy"`
	PostDeploy *HookConfig `yaml:"post_deploy"`
}

// NotificationConfig describes a single notification target.
type NotificationConfig struct {
	Name   string   `yaml:"name"`
	Type   string   `yaml:"type"`
	URL    string   `yaml:"url"`
	Events []string `yaml:"events"`
}

// Config is the top-level configuration for dockcd.
type Config struct {
	Repo          string                `yaml:"repo"`
	BasePath      string                `yaml:"base_path"`
	Branch        string                `yaml:"branch"`
	Notifications []NotificationConfig  `yaml:"notifications"`
	Hosts         map[string]HostConfig `yaml:"hosts"`
}

// HostConfig holds the list of stacks for a single host.
type HostConfig struct {
	Stacks []Stack `yaml:"stacks"`
}

// Stack describes a single Docker Compose stack managed by dockcd.
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

// RollbackEnabled returns whether auto-rollback is enabled for this stack.
// Defaults to true if not explicitly set.
func (s *Stack) RollbackEnabled() bool {
	if s.AutoRollback == nil {
		return true
	}
	return *s.AutoRollback
}

// HealthTimeout returns the health check timeout.
// Defaults to 60s if not set. Returns 0 if explicitly set to 0 (disables health check).
func (s *Stack) HealthTimeout() time.Duration {
	if s.HealthCheckTimeout == nil {
		return 60 * time.Second
	}
	return *s.HealthCheckTimeout
}

func (s *Stack) UnmarshalYAML(value *yaml.Node) error {
	var raw struct {
		Name               string `yaml:"name"`
		Path               string `yaml:"path"`
		Branch             string `yaml:"branch"`
		DependsOn          []string `yaml:"depends_on"`
		PostDeployDelay    string   `yaml:"post_deploy_delay"`
		HealthCheckTimeout string   `yaml:"health_check_timeout"`
		AutoRollback       *bool    `yaml:"auto_rollback"`
		Hooks              *struct {
			PreDeploy  *struct {
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
			hc := &HookConfig{Command: raw.Hooks.PreDeploy.Command}
			if raw.Hooks.PreDeploy.Timeout != "" {
				d, err := time.ParseDuration(raw.Hooks.PreDeploy.Timeout)
				if err != nil {
					return fmt.Errorf("invalid pre_deploy hook timeout %q for stack %q: %w", raw.Hooks.PreDeploy.Timeout, raw.Name, err)
				}
				hc.Timeout = d
			}
			s.Hooks.PreDeploy = hc
		}
		if raw.Hooks.PostDeploy != nil {
			hc := &HookConfig{Command: raw.Hooks.PostDeploy.Command}
			if raw.Hooks.PostDeploy.Timeout != "" {
				d, err := time.ParseDuration(raw.Hooks.PostDeploy.Timeout)
				if err != nil {
					return fmt.Errorf("invalid post_deploy hook timeout %q for stack %q: %w", raw.Hooks.PostDeploy.Timeout, raw.Name, err)
				}
				hc.Timeout = d
			}
			s.Hooks.PostDeploy = hc
		}
	}

	return nil
}

var ErrCycleDetected = fmt.Errorf("dependency cycle detected")

// Load reads, env-expands, parses, and validates the config at path.
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading config file: %w", err)
	}

	// Expand ${VAR} and $VAR references before YAML parsing.
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

func validate(cfg *Config) error {
	if cfg.Repo == "" {
		return fmt.Errorf("repo is required")
	}
	if cfg.BasePath == "" {
		return fmt.Errorf("base_path is required")
	}

	for hostName, host := range cfg.Hosts {
		if len(host.Stacks) == 0 {
			return fmt.Errorf("host %q must have at least one stack", hostName)
		}

		stackNames := make(map[string]bool, len(host.Stacks))
		for _, st := range host.Stacks {
			if st.Name == "" {
				return fmt.Errorf("host %q: stack name is required", hostName)
			}
			if st.Path == "" {
				return fmt.Errorf("host %q: stack %q path is required", hostName, st.Name)
			}
			if st.Hooks != nil {
				if st.Hooks.PreDeploy != nil && st.Hooks.PreDeploy.Command == "" {
					return fmt.Errorf("host %q: stack %q: pre_deploy hook command must not be empty", hostName, st.Name)
				}
				if st.Hooks.PostDeploy != nil && st.Hooks.PostDeploy.Command == "" {
					return fmt.Errorf("host %q: stack %q: post_deploy hook command must not be empty", hostName, st.Name)
				}
			}
			stackNames[st.Name] = true
		}
		for _, st := range host.Stacks {
			for _, dep := range st.DependsOn {
				if !stackNames[dep] {
					return fmt.Errorf("host %q: stack %q depends on non-existent stack %q", hostName, st.Name, dep)
				}
			}
		}

		if err := detectCycles(host.Stacks); err != nil {
			return fmt.Errorf("host %q: %w", hostName, err)
		}
	}

	for i, n := range cfg.Notifications {
		if !validNotificationTypes[n.Type] {
			return fmt.Errorf("notification %d (%q): invalid notification type %q", i, n.Name, n.Type)
		}
		if n.URL == "" {
			return fmt.Errorf("notification %d (%q): notification URL must not be empty", i, n.Name)
		}
		for _, ev := range n.Events {
			if !validEventTypes[ev] {
				return fmt.Errorf("notification %d (%q): invalid notification event %q", i, n.Name, ev)
			}
		}
	}

	return nil
}

func detectCycles(stacks []Stack) error {
	adj := make(map[string][]string, len(stacks))

	for _, st := range stacks {
		adj[st.Name] = st.DependsOn
	}

	const (
		white = 0
		gray  = 1
		black = 2
	)
	color := make(map[string]int, len(stacks))

	var dfs func(name string) error
	dfs = func(name string) error {
		color[name] = gray
		for _, dep := range adj[name] {
			switch color[dep] {
			case gray:
				return fmt.Errorf("%w: %s -> %s", ErrCycleDetected, name, dep)
			case white:
				if err := dfs(dep); err != nil {
					return err
				}
			}
		}
		color[name] = black
		return nil
	}

	for _, st := range stacks {
		if color[st.Name] == white {
			if err := dfs(st.Name); err != nil {
				return err
			}
		}
	}
	return nil
}
