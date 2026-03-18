package deploy

import (
	"context"
	"fmt"
	"testing"
	"time"
)

func TestParseComposePS(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		want    int
		wantErr bool
	}{
		{
			name:  "single healthy container",
			input: `{"Name":"web-1","State":"running","Health":"healthy"}` + "\n",
			want:  1,
		},
		{
			name: "multiple containers",
			input: `{"Name":"web-1","State":"running","Health":"healthy"}
{"Name":"db-1","State":"running","Health":""}
`,
			want: 2,
		},
		{
			name:  "empty output",
			input: "",
			want:  0,
		},
		{
			name:    "invalid json",
			input:   "not json\n",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			containers, err := parseComposePS([]byte(tt.input))
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if len(containers) != tt.want {
				t.Errorf("expected %d containers, got %d", tt.want, len(containers))
			}
		})
	}
}

func TestCheckContainersHealthy(t *testing.T) {
	run := func(ctx context.Context, dir, name string, args ...string) ([]byte, error) {
		return []byte(`{"Name":"web-1","State":"running","Health":"healthy"}
{"Name":"db-1","State":"running","Health":""}
`), nil
	}

	healthy, err := checkContainers(context.Background(), "/tmp", run)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !healthy {
		t.Error("expected healthy")
	}
}

func TestCheckContainersUnhealthy(t *testing.T) {
	run := func(ctx context.Context, dir, name string, args ...string) ([]byte, error) {
		return []byte(`{"Name":"web-1","State":"running","Health":"healthy"}
{"Name":"db-1","State":"exited","Health":""}
`), nil
	}

	healthy, err := checkContainers(context.Background(), "/tmp", run)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if healthy {
		t.Error("expected unhealthy")
	}
}

func TestCheckContainersHealthCheckUnhealthy(t *testing.T) {
	run := func(ctx context.Context, dir, name string, args ...string) ([]byte, error) {
		return []byte(`{"Name":"web-1","State":"running","Health":"unhealthy"}` + "\n"), nil
	}

	healthy, err := checkContainers(context.Background(), "/tmp", run)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if healthy {
		t.Error("expected unhealthy when Health=unhealthy")
	}
}

func TestCheckContainersEmpty(t *testing.T) {
	run := func(ctx context.Context, dir, name string, args ...string) ([]byte, error) {
		return []byte(""), nil
	}

	_, err := checkContainers(context.Background(), "/tmp", run)
	if err == nil {
		t.Fatal("expected error for empty containers")
	}
}

func TestCheckContainersCommandError(t *testing.T) {
	run := func(ctx context.Context, dir, name string, args ...string) ([]byte, error) {
		return nil, fmt.Errorf("command failed")
	}

	_, err := checkContainers(context.Background(), "/tmp", run)
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestHealthCheckTimeout(t *testing.T) {
	// Runner always returns unhealthy.
	run := func(ctx context.Context, dir, name string, args ...string) ([]byte, error) {
		return []byte(`{"Name":"web-1","State":"exited","Health":""}` + "\n"), nil
	}

	err := HealthCheck(context.Background(), "/tmp", 50*time.Millisecond, run)
	if err == nil {
		t.Fatal("expected timeout error")
	}
}

func TestHealthCheckSucceeds(t *testing.T) {
	run := func(ctx context.Context, dir, name string, args ...string) ([]byte, error) {
		return []byte(`{"Name":"web-1","State":"running","Health":""}` + "\n"), nil
	}

	err := HealthCheck(context.Background(), "/tmp", 5*time.Second, run)
	if err != nil {
		t.Fatalf("expected health check to pass: %v", err)
	}
}
