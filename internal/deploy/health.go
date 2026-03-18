package deploy

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"time"
)

// OutputRunner runs a command and returns its combined output.
type OutputRunner func(ctx context.Context, dir, name string, args ...string) ([]byte, error)

// ContainerStatus represents one entry from `docker compose ps --format json`.
type ContainerStatus struct {
	Name   string `json:"Name"`
	State  string `json:"State"`
	Health string `json:"Health"`
}

// HealthCheck polls `docker compose ps` until all containers are running
// (and healthy, if they define a HEALTHCHECK) or the timeout expires.
func HealthCheck(ctx context.Context, dir string, timeout time.Duration, run OutputRunner) error {
	deadline := time.After(timeout)
	// Check immediately, then every 5 seconds.
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	var lastErr error
	for {
		healthy, err := checkContainers(ctx, dir, run)
		if err == nil && healthy {
			return nil
		}
		if err != nil {
			lastErr = err
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-deadline:
			if lastErr != nil {
				return fmt.Errorf("health check timed out after %s: %w", timeout, lastErr)
			}
			return fmt.Errorf("health check timed out after %s: containers not healthy", timeout)
		case <-ticker.C:
		}
	}
}

func checkContainers(ctx context.Context, dir string, run OutputRunner) (bool, error) {
	out, err := run(ctx, dir, "docker", "compose", "ps", "--format", "json")
	if err != nil {
		return false, fmt.Errorf("compose ps: %w", err)
	}

	containers, err := parseComposePS(out)
	if err != nil {
		return false, err
	}

	if len(containers) == 0 {
		return false, fmt.Errorf("no containers found")
	}

	for _, c := range containers {
		if c.State != "running" {
			return false, nil
		}
		if c.Health != "" && c.Health != "healthy" {
			return false, nil
		}
	}
	return true, nil
}

// parseComposePS parses the NDJSON output from `docker compose ps --format json`.
func parseComposePS(data []byte) ([]ContainerStatus, error) {
	var containers []ContainerStatus
	scanner := bufio.NewScanner(bytes.NewReader(data))
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		var c ContainerStatus
		if err := json.Unmarshal(line, &c); err != nil {
			return nil, fmt.Errorf("parsing container status: %w", err)
		}
		containers = append(containers, c)
	}
	return containers, scanner.Err()
}
