package e2e

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// ElementRef is a single interactive element indexed in an accessibility snapshot.
type ElementRef struct {
	Name string `json:"name"`
	Role string `json:"role"`
}

// SnapshotResult represents the structured response of `orca snapshot --json`.
type SnapshotResult struct {
	Origin        string                `json:"origin"`
	Refs          map[string]ElementRef `json:"refs"`
	Snapshot      string                `json:"snapshot"`
	BrowserPageID string                `json:"browserPageId"`
}

// OrcaClient defines the operations needed to interact with Orca Browser.
type OrcaClient interface {
	CheckStatus() error
	TabCreate(url string) (string, error)
	TabClose(pageID string) error
	Goto(pageID, url string) error
	Snapshot(pageID string) (*SnapshotResult, error)
	Click(pageID, element string) error
	Fill(pageID, element, value string) error
	Keypress(pageID, key string) error
	Eval(pageID, expression string) (string, error)
	Screenshot(pageID string) ([]byte, error)
}

// RealOrcaClient invokes the `orca` binary via CLI.
type RealOrcaClient struct {
	BinaryPath string
	Timeout    time.Duration
}

// NewRealOrcaClient returns a client targeting the system's `orca` CLI.
func NewRealOrcaClient() *RealOrcaClient {
	bin, err := exec.LookPath("orca")
	if err != nil {
		bin = "orca"
	}
	return &RealOrcaClient{
		BinaryPath: bin,
		Timeout:    30 * time.Second,
	}
}

type orcaGenericResponse struct {
	OK     bool            `json:"ok"`
	Result json.RawMessage `json:"result"`
	Error  string          `json:"error,omitempty"`
}

func (c *RealOrcaClient) execOrca(args ...string) ([]byte, error) {
	timeout := c.Timeout
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, c.BinaryPath, args...)
	out, err := cmd.Output()
	if err != nil {
		if ctx.Err() != nil {
			return nil, fmt.Errorf("orca command %v timed out after %v", args, timeout)
		}
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return nil, fmt.Errorf("orca command %v failed (exit %d): %s", args, exitErr.ExitCode(), string(exitErr.Stderr))
		}
		return nil, fmt.Errorf("orca exec %v: %w", args, err)
	}
	return out, nil
}

func (c *RealOrcaClient) execOrcaGeneric(op string, args ...string) error {
	out, err := c.execOrca(args...)
	if err != nil {
		return err
	}
	var resp orcaGenericResponse
	if err := json.Unmarshal(out, &resp); err != nil {
		return fmt.Errorf("parse %s: %w", op, err)
	}
	if !resp.OK {
		return fmt.Errorf("%s failed: %s", op, resp.Error)
	}
	return nil
}

// CheckStatus verifies that Orca runtime is alive and reachable.
func (c *RealOrcaClient) CheckStatus() error {
	return c.execOrcaGeneric("orca status", "status", "--json")
}

// TabCreate opens a new browser tab navigating to the given URL.
func (c *RealOrcaClient) TabCreate(url string) (string, error) {
	args := []string{"tab", "create", "--json"}
	if url != "" {
		args = append(args, "--url", url)
	}
	out, err := c.execOrca(args...)
	if err != nil {
		return "", err
	}
	var resp struct {
		OK     bool `json:"ok"`
		Result struct {
			BrowserPageID string `json:"browserPageId"`
		} `json:"result"`
		Error string `json:"error"`
	}
	if err := json.Unmarshal(out, &resp); err != nil {
		return "", fmt.Errorf("parse tab create: %w", err)
	}
	if !resp.OK || resp.Result.BrowserPageID == "" {
		return "", fmt.Errorf("tab create failed: %s", resp.Error)
	}
	return resp.Result.BrowserPageID, nil
}

// TabClose closes the specified tab.
func (c *RealOrcaClient) TabClose(pageID string) error {
	return c.execOrcaGeneric("tab close", "tab", "close", "--page", pageID, "--json")
}

// Goto navigates the specified tab to the URL.
func (c *RealOrcaClient) Goto(pageID, url string) error {
	return c.execOrcaGeneric("goto", "goto", "--url", url, "--page", pageID, "--json")
}

// Snapshot takes an accessibility tree snapshot with interactive refs.
func (c *RealOrcaClient) Snapshot(pageID string) (*SnapshotResult, error) {
	out, err := c.execOrca("snapshot", "--page", pageID, "--json")
	if err != nil {
		return nil, err
	}
	var resp struct {
		OK     bool           `json:"ok"`
		Result SnapshotResult `json:"result"`
		Error  string         `json:"error"`
	}
	if err := json.Unmarshal(out, &resp); err != nil {
		return nil, fmt.Errorf("parse snapshot: %w", err)
	}
	if !resp.OK {
		return nil, fmt.Errorf("snapshot failed: %s", resp.Error)
	}
	return &resp.Result, nil
}

// Click clicks an element by ref (e.g. e1, e2).
func (c *RealOrcaClient) Click(pageID, element string) error {
	element = strings.TrimPrefix(element, "@")
	return c.execOrcaGeneric("click", "click", "--element", element, "--page", pageID, "--json")
}

// Fill clears and fills an element by ref with the given value.
func (c *RealOrcaClient) Fill(pageID, element, value string) error {
	element = strings.TrimPrefix(element, "@")
	return c.execOrcaGeneric("fill", "fill", "--element", element, "--value", value, "--page", pageID, "--json")
}

// Keypress triggers a key press event in the tab.
func (c *RealOrcaClient) Keypress(pageID, key string) error {
	return c.execOrcaGeneric("keypress", "keypress", "--key", key, "--page", pageID, "--json")
}

// Eval evaluates a JavaScript expression and returns the stringified result.
func (c *RealOrcaClient) Eval(pageID, expression string) (string, error) {
	out, err := c.execOrca("eval", "--expression", expression, "--page", pageID, "--json")
	if err != nil {
		return "", err
	}
	var resp struct {
		OK     bool `json:"ok"`
		Result struct {
			Result any `json:"result"`
		} `json:"result"`
		Error string `json:"error"`
	}
	if err := json.Unmarshal(out, &resp); err != nil {
		return "", fmt.Errorf("parse eval: %w", err)
	}
	if !resp.OK {
		return "", fmt.Errorf("eval failed: %s", resp.Error)
	}
	return fmt.Sprintf("%v", resp.Result.Result), nil
}

// Screenshot captures a viewport screenshot and returns raw PNG bytes.
func (c *RealOrcaClient) Screenshot(pageID string) ([]byte, error) {
	out, err := c.execOrca("screenshot", "--page", pageID, "--json")
	if err != nil {
		return nil, err
	}
	var resp struct {
		OK     bool `json:"ok"`
		Result struct {
			Data   string `json:"data"`
			Format string `json:"format"`
		} `json:"result"`
		Error string `json:"error"`
	}
	if err := json.Unmarshal(out, &resp); err != nil {
		return nil, fmt.Errorf("parse screenshot: %w", err)
	}
	if !resp.OK || resp.Result.Data == "" {
		return nil, fmt.Errorf("screenshot failed: %s", resp.Error)
	}
	raw, err := base64.StdEncoding.DecodeString(resp.Result.Data)
	if err != nil {
		return nil, fmt.Errorf("decode screenshot base64: %w", err)
	}
	return raw, nil
}
