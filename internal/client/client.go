// Package client provides an HTTP client for the dockcd server API.
package client

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/mohitsharma44/dockcd/internal/server"
)

// Client communicates with a dockcd server over HTTP.
type Client struct {
	baseURL    string
	httpClient *http.Client
}

// ReconcileResponse is returned by reconcile endpoints (202 Accepted).
type ReconcileResponse struct {
	ID     string `json:"id"`
	Status string `json:"status"`
	Stack  string `json:"stack,omitempty"`
}

// VersionResponse is returned by the /api/v1/version endpoint.
type VersionResponse struct {
	Server string `json:"server"`
	Go     string `json:"go"`
}

// New returns a Client targeting baseURL (e.g. "http://localhost:9092").
func New(baseURL string) *Client {
	return &Client{
		baseURL:    baseURL,
		httpClient: &http.Client{Timeout: 30 * time.Second},
	}
}

// GetStacks returns all stacks tracked by the server.
func (c *Client) GetStacks() ([]server.StackState, error) {
	resp, err := c.httpClient.Get(c.baseURL + "/api/v1/stacks")
	if err != nil {
		return nil, fmt.Errorf("connecting to server: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("server returned %d", resp.StatusCode)
	}

	var payload struct {
		Stacks []server.StackState `json:"stacks"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return nil, fmt.Errorf("decoding response: %w", err)
	}
	return payload.Stacks, nil
}

// GetStack returns a single stack by name.
// Returns an error containing "not found" when the server responds 404.
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

	var stack server.StackState
	if err := json.NewDecoder(resp.Body).Decode(&stack); err != nil {
		return nil, fmt.Errorf("decoding response: %w", err)
	}
	return &stack, nil
}

// Reconcile triggers an async reconciliation for the named stack.
// The server responds 202 Accepted on success.
func (c *Client) Reconcile(name string) (ReconcileResponse, error) {
	resp, err := c.httpClient.Post(c.baseURL+"/api/v1/reconcile/"+name, "application/json", nil)
	if err != nil {
		return ReconcileResponse{}, fmt.Errorf("connecting to server: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusAccepted {
		return ReconcileResponse{}, fmt.Errorf("server returned %d", resp.StatusCode)
	}

	var r ReconcileResponse
	if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
		return ReconcileResponse{}, fmt.Errorf("decoding response: %w", err)
	}
	return r, nil
}

// ReconcileAll triggers an async reconciliation for all stacks.
// The server responds 202 Accepted on success.
func (c *Client) ReconcileAll() (ReconcileResponse, error) {
	resp, err := c.httpClient.Post(c.baseURL+"/api/v1/reconcile", "application/json", nil)
	if err != nil {
		return ReconcileResponse{}, fmt.Errorf("connecting to server: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusAccepted {
		return ReconcileResponse{}, fmt.Errorf("server returned %d", resp.StatusCode)
	}

	var r ReconcileResponse
	if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
		return ReconcileResponse{}, fmt.Errorf("decoding response: %w", err)
	}
	return r, nil
}

// Suspend suspends the named stack, returning the updated state.
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

	var stack server.StackState
	if err := json.NewDecoder(resp.Body).Decode(&stack); err != nil {
		return nil, fmt.Errorf("decoding response: %w", err)
	}
	return &stack, nil
}

// Resume resumes the named stack, returning the updated state.
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

	var stack server.StackState
	if err := json.NewDecoder(resp.Body).Decode(&stack); err != nil {
		return nil, fmt.Errorf("decoding response: %w", err)
	}
	return &stack, nil
}

// Version returns the server and Go runtime version reported by the server.
func (c *Client) Version() (VersionResponse, error) {
	resp, err := c.httpClient.Get(c.baseURL + "/api/v1/version")
	if err != nil {
		return VersionResponse{}, fmt.Errorf("connecting to server: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return VersionResponse{}, fmt.Errorf("server returned %d", resp.StatusCode)
	}

	var v VersionResponse
	if err := json.NewDecoder(resp.Body).Decode(&v); err != nil {
		return VersionResponse{}, fmt.Errorf("decoding response: %w", err)
	}
	return v, nil
}
