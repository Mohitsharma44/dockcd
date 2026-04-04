package hooks

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"
)

func TestRunSuccess(t *testing.T) {
	cfg := Config{Command: "echo hello"}
	env := Env{StackName: "web", Commit: "abc123", RepoDir: "/opt/repo", Branch: "main"}

	res, err := Run(context.Background(), cfg, t.TempDir(), env)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := strings.TrimSpace(res.Stdout); got != "hello" {
		t.Errorf("stdout = %q, want %q", got, "hello")
	}
}

func TestRunStdoutAndStderr(t *testing.T) {
	cfg := Config{Command: "echo out && echo err >&2"}
	env := Env{}

	res, err := Run(context.Background(), cfg, t.TempDir(), env)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(res.Stdout, "out") {
		t.Errorf("stdout = %q, want it to contain %q", res.Stdout, "out")
	}
	if !strings.Contains(res.Stderr, "err") {
		t.Errorf("stderr = %q, want it to contain %q", res.Stderr, "err")
	}
}

func TestRunExitCode1(t *testing.T) {
	cfg := Config{Command: "echo fail_output >&2; exit 1"}
	env := Env{}

	res, err := Run(context.Background(), cfg, t.TempDir(), env)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "exit") {
		t.Errorf("error = %q, want it to mention exit", err.Error())
	}
	if !strings.Contains(res.Stderr, "fail_output") {
		t.Errorf("stderr = %q, want it to contain %q", res.Stderr, "fail_output")
	}
}

func TestRunEnvVarsInjected(t *testing.T) {
	cfg := Config{Command: "echo $DOCKCD_STACK_NAME $DOCKCD_COMMIT $DOCKCD_REPO_DIR $DOCKCD_BRANCH"}
	env := Env{StackName: "mystack", Commit: "deadbeef", RepoDir: "/srv/repo", Branch: "deploy"}

	res, err := Run(context.Background(), cfg, t.TempDir(), env)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	got := strings.TrimSpace(res.Stdout)
	want := "mystack deadbeef /srv/repo deploy"
	if got != want {
		t.Errorf("stdout = %q, want %q", got, want)
	}
}

func TestRunInheritsParentEnv(t *testing.T) {
	const key = "DOCKCD_TEST_INHERIT_VAR"
	const val = "inherited_value"
	t.Setenv(key, val)

	cfg := Config{Command: "echo $" + key}
	env := Env{}

	res, err := Run(context.Background(), cfg, t.TempDir(), env)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := strings.TrimSpace(res.Stdout); got != val {
		t.Errorf("stdout = %q, want %q", got, val)
	}
}

func TestRunTimeout(t *testing.T) {
	cfg := Config{
		Command: "sleep 60",
		Timeout: 100 * time.Millisecond,
	}
	env := Env{}

	start := time.Now()
	_, err := Run(context.Background(), cfg, t.TempDir(), env)
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "context") && !strings.Contains(err.Error(), "timeout") &&
		!strings.Contains(err.Error(), "signal: killed") && !strings.Contains(err.Error(), "signal: terminated") {
		t.Errorf("error = %q, want it to indicate timeout/cancellation", err.Error())
	}
	if elapsed > 5*time.Second {
		t.Errorf("command took %v, expected it to be killed quickly", elapsed)
	}
}

func TestRunContextCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())

	cfg := Config{Command: "sleep 60"}
	env := Env{}

	// Cancel after a short delay.
	go func() {
		time.Sleep(100 * time.Millisecond)
		cancel()
	}()

	start := time.Now()
	_, err := Run(ctx, cfg, t.TempDir(), env)
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if elapsed > 5*time.Second {
		t.Errorf("command took %v, expected it to be killed quickly", elapsed)
	}
}

func TestRunEmptyCommand(t *testing.T) {
	cfg := Config{Command: ""}
	env := Env{}

	_, err := Run(context.Background(), cfg, t.TempDir(), env)
	if err == nil {
		t.Fatal("expected error for empty command, got nil")
	}
	if !strings.Contains(err.Error(), "empty") {
		t.Errorf("error = %q, want it to mention empty", err.Error())
	}
}

func TestRunBadDirectory(t *testing.T) {
	cfg := Config{Command: "echo hello"}
	env := Env{}

	_, err := Run(context.Background(), cfg, "/nonexistent/path/that/should/not/exist", env)
	if err == nil {
		t.Fatal("expected error for bad directory, got nil")
	}
}

func TestRunDefaultTimeout(t *testing.T) {
	// Verify that a zero timeout doesn't mean "no timeout" by checking
	// that the command runs successfully with the default (30s is plenty
	// for a quick echo).
	cfg := Config{Command: "echo default_timeout_ok", Timeout: 0}
	env := Env{}

	res, err := Run(context.Background(), cfg, t.TempDir(), env)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(res.Stdout, "default_timeout_ok") {
		t.Errorf("stdout = %q, want it to contain default_timeout_ok", res.Stdout)
	}
}

func TestRunWorkingDirectory(t *testing.T) {
	dir := t.TempDir()
	// Create a marker file in the temp dir.
	if err := os.WriteFile(dir+"/marker.txt", []byte("found"), 0644); err != nil {
		t.Fatalf("setup: %v", err)
	}

	cfg := Config{Command: "cat marker.txt"}
	env := Env{}

	res, err := Run(context.Background(), cfg, dir, env)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := strings.TrimSpace(res.Stdout); got != "found" {
		t.Errorf("stdout = %q, want %q", got, "found")
	}
}
