package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/Nathandela/swarm/internal/adapter"
	"github.com/Nathandela/swarm/internal/protocol"
)

const (
	claudeListTimeout = 15 * time.Second
	claudeStopTimeout = 30 * time.Second
	maxClaudeOutput   = 8 << 20
)

type claudeAgentSession struct {
	ID        string `json:"id"`
	SessionID string `json:"sessionId"`
	Name      string `json:"name"`
	Cwd       string `json:"cwd"`
	Kind      string `json:"kind"`
	State     string `json:"state"`
	StartedAt int64  `json:"startedAt"`
}

type claudeSessionSource interface {
	List(all bool) ([]claudeAgentSession, error)
	Stop() error
}

type execClaudeSessionSource struct {
	binary string
}

func newExecClaudeSessionSource() (claudeSessionSource, error) {
	binary, err := exec.LookPath("claude")
	if err != nil {
		return nil, fmt.Errorf("find claude: %w", err)
	}
	return execClaudeSessionSource{binary: binary}, nil
}

func (s execClaudeSessionSource) List(all bool) ([]claudeAgentSession, error) {
	args := []string{"agents", "--json"}
	if all {
		args = append(args, "--all")
	}
	output, err := runClaudeCommand(s.binary, claudeListTimeout, args...)
	if err != nil {
		return nil, fmt.Errorf("list Claude sessions: %w", err)
	}
	var sessions []claudeAgentSession
	if err := json.Unmarshal(output, &sessions); err != nil {
		return nil, fmt.Errorf("decode `claude agents --json`: %w", err)
	}
	return sessions, nil
}

func (s execClaudeSessionSource) Stop() error {
	_, err := runClaudeCommand(s.binary, claudeStopTimeout, "daemon", "stop", "--any")
	if err != nil {
		return fmt.Errorf("stop Claude background daemon: %w", err)
	}
	return nil
}

func runClaudeCommand(binary string, timeout time.Duration, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	var output cappedCommandOutput
	cmd := exec.CommandContext(ctx, binary, args...)
	cmd.Stdout = &output
	cmd.Stderr = &output
	err := cmd.Run()
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return nil, fmt.Errorf("timed out after %s", timeout)
	}
	if output.truncatedOutput() {
		return nil, fmt.Errorf("output exceeded %d bytes", maxClaudeOutput)
	}
	data := output.bytes()
	if err != nil {
		message := strings.TrimSpace(string(data))
		if message != "" {
			return nil, fmt.Errorf("%w: %s", err, message)
		}
		return nil, err
	}
	return data, nil
}

// cappedCommandOutput bounds both provider stdout and stderr while reporting
// successful writes to the child-copy goroutines, discarding only bytes beyond
// the cap. The mutex covers stdout/stderr copies that may arrive concurrently.
type cappedCommandOutput struct {
	mu        sync.Mutex
	buf       bytes.Buffer
	truncated bool
}

func (w *cappedCommandOutput) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	remaining := maxClaudeOutput - w.buf.Len()
	if remaining > 0 {
		keep := len(p)
		if keep > remaining {
			keep = remaining
		}
		_, _ = w.buf.Write(p[:keep])
	}
	if len(p) > remaining {
		w.truncated = true
	}
	return len(p), nil
}

func (w *cappedCommandOutput) bytes() []byte {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]byte(nil), w.buf.Bytes()...)
}

func (w *cappedCommandOutput) truncatedOutput() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.truncated
}

// runReattach adopts sessions discovered by another local supervisor. The first
// provider is Claude Agent View; the source seam keeps discovery independent of
// swarm's protocol and makes the takeover ordering unit-testable.
func runReattach(args []string, c agentClient, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("reattach", flag.ContinueOnError)
	fs.SetOutput(stderr)
	cli := fs.String("cli", "", "provider CLI to import (currently claude)")
	all := fs.Bool("all", false, "include completed and stopped background sessions")
	takeOver := fs.Bool("take-over", false, "stop the provider's background supervisor before adopting live sessions")
	dryRun := fs.Bool("dry-run", false, "show eligible sessions without stopping or launching anything")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() != 0 || *cli != "claude" {
		_, _ = fmt.Fprintln(stderr, "reattach: use `swarm reattach --cli claude [--all] [--take-over] [--dry-run]`")
		return 2
	}
	capable, ok := c.(interface{ Capabilities() []string })
	if !ok || !slices.Contains(capable.Capabilities(), protocol.CapExternalResume) {
		_, _ = fmt.Fprintln(stderr, "reattach: the running swarm daemon does not support external resume; restart it with this CLI version")
		return 1
	}
	source, err := newExecClaudeSessionSource()
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "reattach: %v\n", err)
		return 1
	}
	return runClaudeReattach(*all, *takeOver, *dryRun, source, c, stdout, stderr)
}

func runClaudeReattach(all, takeOver, dryRun bool, source claudeSessionSource, c agentClient, stdout, stderr io.Writer) int {
	discovered, err := source.List(all)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "reattach: %v\n", err)
		return 1
	}

	seen := make(map[string]struct{}, len(discovered))
	eligible := make([]claudeAgentSession, 0, len(discovered))
	hasLive := false
	failures := 0
	for _, session := range discovered {
		if session.Kind != "background" || !all && claudeStateSettled(session.State) {
			continue
		}
		if _, duplicate := seen[session.SessionID]; duplicate {
			continue
		}
		seen[session.SessionID] = struct{}{}
		if !adapter.IsCanonicalConversationID(session.SessionID) {
			_, _ = fmt.Fprintf(stderr, "reattach: skip %q: invalid Claude session id\n", session.Name)
			failures++
			continue
		}
		if strings.TrimSpace(session.Cwd) == "" {
			_, _ = fmt.Fprintf(stderr, "reattach: skip %q: cwd is empty\n", session.Name)
			failures++
			continue
		}
		cwd, err := filepath.Abs(session.Cwd)
		if err != nil {
			_, _ = fmt.Fprintf(stderr, "reattach: skip %q: invalid cwd: %v\n", session.Name, err)
			failures++
			continue
		}
		info, err := os.Stat(cwd)
		if err != nil || !info.IsDir() {
			_, _ = fmt.Fprintf(stderr, "reattach: skip %q: cwd %q is not an existing directory\n", session.Name, cwd)
			failures++
			continue
		}
		session.Cwd = cwd
		eligible = append(eligible, session)
		if !claudeStateSettled(session.State) {
			hasLive = true
		}
	}

	if hasLive && !takeOver && !dryRun {
		_, _ = fmt.Fprintln(stderr, "reattach: live Claude background sessions found; rerun with --take-over to stop Claude's supervisor before adopting them")
		return 1
	}
	if dryRun {
		for _, session := range eligible {
			_, _ = fmt.Fprintf(stdout, "would reattach %s %q (%s)\n", session.State, session.Name, session.SessionID)
		}
		_, _ = fmt.Fprintf(stdout, "%d Claude background session(s) eligible\n", len(eligible))
		if failures != 0 {
			return 1
		}
		return 0
	}
	if hasLive {
		if err := source.Stop(); err != nil {
			_, _ = fmt.Fprintf(stderr, "reattach: %v\n", err)
			return 1
		}
	}

	for _, session := range eligible {
		id, _, err := c.Launch(protocol.LaunchReq{
			Agent: "claude",
			Name:  session.Name,
			Cwd:   session.Cwd,
			Env:   os.Environ(),
			Cols:  80,
			Rows:  24,
			Options: map[string]string{
				protocol.OptionResumeConversationID: session.SessionID,
			},
		})
		if err != nil {
			_, _ = fmt.Fprintf(stderr, "reattach: %q: %v\n", session.Name, err)
			failures++
			continue
		}
		_, _ = fmt.Fprintf(stdout, "reattached %q as %s\n", session.Name, id)
	}
	_, _ = fmt.Fprintf(stdout, "%d Claude background session(s) processed\n", len(eligible))
	if failures != 0 {
		return 1
	}
	return 0
}

func claudeStateSettled(state string) bool {
	switch strings.ToLower(strings.TrimSpace(state)) {
	case "done", "stopped", "complete", "completed", "failed", "cancelled", "canceled":
		return true
	default:
		return false
	}
}
