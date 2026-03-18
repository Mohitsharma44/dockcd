package config

import (
	"fmt"
	"os"
	"time"

	"gopkg.in/yaml.v3"
)

type Config struct {
	Repo     string                `yaml:"repo"`
	BasePath string                `yaml:"base_path"`
	Hosts    map[string]HostConfig `yaml:"hosts"`
}

type HostConfig struct {
	Stacks []Stack `yaml:"stacks"`
}

type Stack struct {
	Name               string         `yaml:"name"`
	Path               string         `yaml:"path"`
	DependsOn          []string       `yaml:"depends_on"`
	PostDeployDelay    time.Duration  `yaml:"post_deploy_delay"`
	HealthCheckTimeout *time.Duration `yaml:"health_check_timeout"`
	AutoRollback       *bool          `yaml:"auto_rollback"`
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
		Name               string   `yaml:"name"`
		Path               string   `yaml:"path"`
		DependsOn          []string `yaml:"depends_on"`
		PostDeployDelay    string   `yaml:"post_deploy_delay"`
		HealthCheckTimeout string   `yaml:"health_check_timeout"`
		AutoRollback       *bool    `yaml:"auto_rollback"`
	}
	if err := value.Decode(&raw); err != nil {
		return err
	}
	s.Name = raw.Name
	s.Path = raw.Path
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
	return nil
}

var ErrCycleDetected = fmt.Errorf("dependency cycle detected")

func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading config file: %w", err)
	}

	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
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
