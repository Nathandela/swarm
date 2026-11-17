package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/Nathandela/swarm/internal/adapter"
	"github.com/Nathandela/swarm/internal/protocol"
)

const (
	maxCodexReattachEntries = 16384
	maxCodexReattachFiles   = 4096
	maxCodexReattachRecord  = 64 << 10
	maxCodexReattachBytes   = 16 << 20
)

type codexReattachSession struct {
	ID, Cwd  string
	Archived bool
}

// Codex has no equivalent of Claude's agents/daemon-stop commands. Only native
// rollout metadata is read here; neither status nor exclusive ownership can be
// inferred from an old transcript. --take-over is the owner's acknowledgement
// that the original CLIs have already stopped, never a process-killing operation.
func runCodexReattach(all, takeOver, dryRun bool, selected string, c agentClient, stdout, stderr io.Writer) int {
	home, err := os.UserHomeDir()
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "reattach: find home: %v\n", err)
		return 1
	}
	nativeHome := filepath.Join(home, ".codex")
	discoveryHome := os.Getenv("CODEX_HOME")
	if discoveryHome == "" {
		discoveryHome = nativeHome
	}
	if !dryRun {
		if os.Getenv("CODEX_PROFILE") != "" {
			_, _ = fmt.Fprintln(stderr, "reattach: CODEX_PROFILE is unsupported for external resume; unset it before importing")
			return 1
		}
		want, wantErr := filepath.EvalSymlinks(nativeHome)
		got, gotErr := filepath.EvalSymlinks(discoveryHome)
		if filepath.Clean(discoveryHome) != filepath.Clean(nativeHome) && (wantErr != nil || gotErr != nil || want != got) {
			_, _ = fmt.Fprintln(stderr, "reattach: custom CODEX_HOME cannot be preserved by the daemon; use the default ~/.codex home (custom homes can be inspected with --dry-run)")
			return 1
		}
	}
	sessions, err := discoverCodexReattach(discoveryHome, all, selected)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "reattach: discover Codex sessions: %v\n", err)
		return 1
	}
	if selected != "" && len(sessions) == 0 {
		_, _ = fmt.Fprintf(stderr, "reattach: Codex session %s was not found; use --all to include archived sessions\n", selected)
		return 1
	}
	if dryRun {
		for _, session := range sessions {
			if session.Archived {
				_, _ = fmt.Fprintf(stdout, "archived codex %s (cwd %q); unarchive before importing\n", session.ID, session.Cwd)
			} else {
				_, _ = fmt.Fprintf(stdout, "would reattach codex %s (cwd %q)\n", session.ID, session.Cwd)
			}
		}
		_, _ = fmt.Fprintf(stdout, "%d Codex session(s) eligible; native history does not establish whether an original CLI is still running\n", len(sessions))
		return 0
	}
	for _, session := range sessions {
		if session.Archived {
			_, _ = fmt.Fprintf(stderr, "reattach: Codex session %s is archived; run `codex unarchive %s` before importing (no sessions launched)\n", session.ID, session.ID)
			return 1
		}
	}
	if len(sessions) > 0 && !takeOver {
		_, _ = fmt.Fprintln(stderr, "reattach: stop the original Codex CLI sessions, then rerun with --take-over to confirm; swarm cannot stop Codex automatically (use --session ID to select one)")
		return 1
	}
	failures := 0
	for _, session := range sessions {
		id, _, err := c.Launch(protocol.LaunchReq{
			Agent: "codex", Name: "codex " + session.ID[:8], Cwd: session.Cwd,
			Env: os.Environ(), Cols: 80, Rows: 24,
			Options: map[string]string{protocol.OptionResumeConversationID: session.ID},
		})
		if err != nil {
			_, _ = fmt.Fprintf(stderr, "reattach: %s: %v\n", session.ID, err)
			failures++
			continue
		}
		_, _ = fmt.Fprintf(stdout, "reattached Codex %s as %s\n", session.ID, id)
	}
	_, _ = fmt.Fprintf(stdout, "%d Codex session(s) processed\n", len(sessions))
	if failures != 0 {
		return 1
	}
	return 0
}

// The anchored walk reads only the first metadata record of each matching
// rollout. Directory batches, entries, depth, files and bytes are bounded.
// Directory symlinks are not traversed; nonregular rollouts and inode changes
// are refused before any launch. The selected native root may itself be an alias.
func discoverCodexReattach(home string, all bool, selected string) ([]codexReattachSession, error) {
	root, err := os.OpenRoot(home)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer func() { _ = root.Close() }()
	sessions := make([]codexReattachSession, 0)
	seen := make(map[string]codexReattachSession)
	entries, files, bytesRead := 0, 0, 0
	var walk func(string, int) error
	walk = func(dir string, depth int) error {
		if depth > 8 {
			return errors.New("codex discovery directory depth limit exceeded")
		}
		before, err := root.Lstat(dir)
		if errors.Is(err, os.ErrNotExist) && depth == 0 {
			return nil
		}
		if err != nil {
			return err
		}
		if !before.IsDir() || before.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("unsafe native directory %q", dir)
		}
		f, err := root.OpenFile(dir, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
		if err != nil {
			return err
		}
		defer func() { _ = f.Close() }()
		after, err := f.Stat()
		if err != nil {
			return err
		}
		if !os.SameFile(before, after) {
			return fmt.Errorf("native directory changed: %q", dir)
		}
		for {
			batch, readErr := f.ReadDir(128)
			if readErr != nil && !errors.Is(readErr, io.EOF) {
				return readErr
			}
			for _, entry := range batch {
				entries++
				if entries > maxCodexReattachEntries {
					return errors.New("codex discovery entry limit exceeded")
				}
				path := filepath.Join(dir, entry.Name())
				if entry.IsDir() {
					if err := walk(path, depth+1); err != nil {
						return err
					}
					continue
				}
				if !strings.HasPrefix(entry.Name(), "rollout-") || !strings.HasSuffix(entry.Name(), ".jsonl") {
					continue
				}
				if selected != "" && codexReattachFileID(entry.Name()) != selected {
					continue
				}
				files++
				if files > maxCodexReattachFiles {
					return errors.New("codex discovery file limit exceeded; select one with --session ID")
				}
				session, n, err := readCodexReattachMetadata(root, path)
				if err != nil {
					return fmt.Errorf("%s: %w", path, err)
				}
				bytesRead += n
				if bytesRead > maxCodexReattachBytes {
					return errors.New("codex discovery byte limit exceeded; select one with --session ID")
				}
				if session.ID == "" {
					continue
				}
				session.Archived = strings.HasPrefix(path, "archived_sessions"+string(filepath.Separator))
				if prior, ok := seen[session.ID]; ok {
					if prior.Archived != session.Archived {
						return fmt.Errorf("session %s appears in both active and archived history", session.ID)
					}
					if prior.Cwd != session.Cwd {
						return fmt.Errorf("conflicting native metadata for %s", session.ID)
					}
					continue
				}
				seen[session.ID] = session
				sessions = append(sessions, session)
			}
			if errors.Is(readErr, io.EOF) {
				return nil
			}
		}
	}
	if err := walk("sessions", 0); err != nil {
		return nil, err
	}
	if all {
		if err := walk("archived_sessions", 0); err != nil {
			return nil, err
		}
	}
	return sessions, nil
}

func readCodexReattachMetadata(root *os.Root, path string) (codexReattachSession, int, error) {
	var session codexReattachSession
	before, err := root.Lstat(path)
	if err != nil {
		return session, 0, err
	}
	if !before.Mode().IsRegular() {
		return session, 0, errors.New("rollout is not a regular file")
	}
	f, err := root.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return session, 0, err
	}
	defer func() { _ = f.Close() }()
	after, err := f.Stat()
	if err != nil {
		return session, 0, err
	}
	if !os.SameFile(before, after) || !after.Mode().IsRegular() {
		return session, 0, errors.New("rollout changed during discovery")
	}
	line, err := bufio.NewReaderSize(f, maxCodexReattachRecord+1).ReadSlice('\n')
	if len(line) > maxCodexReattachRecord || errors.Is(err, bufio.ErrBufferFull) {
		return session, 0, errors.New("session metadata exceeds 64 KiB")
	}
	if err != nil && !errors.Is(err, io.EOF) {
		return session, 0, err
	}
	var meta struct {
		Type    string `json:"type"`
		Payload struct {
			ID     string          `json:"id"`
			Cwd    string          `json:"cwd"`
			Source json.RawMessage `json:"source"`
		} `json:"payload"`
	}
	if json.Unmarshal(line, &meta) != nil || meta.Type != "session_meta" || !adapter.IsCanonicalConversationID(meta.Payload.ID) || codexReattachFileID(filepath.Base(path)) != meta.Payload.ID {
		return session, 0, errors.New("invalid Codex session metadata or conversation id")
	}
	if len(meta.Payload.Source) == 0 {
		return session, 0, errors.New("session source is missing")
	}
	// Interactive CLI roots only: internal subagent and other product threads
	// are not independent Codex CLI sessions to import.
	if string(meta.Payload.Source) != `"cli"` {
		return session, len(line), nil
	}
	if !filepath.IsAbs(meta.Payload.Cwd) {
		return session, 0, errors.New("session cwd must be absolute")
	}
	info, err := os.Stat(meta.Payload.Cwd)
	if err != nil || !info.IsDir() {
		return session, 0, errors.New("session cwd is not an existing directory")
	}
	session.ID, session.Cwd = meta.Payload.ID, filepath.Clean(meta.Payload.Cwd)
	return session, len(line), nil
}

// Modern Codex may append an immutable physical rollout UUID after the stable
// thread UUID. Bind metadata to the stable ID in either uncompressed layout.
func codexReattachFileID(name string) string {
	const prefix = "rollout-"
	if !strings.HasPrefix(name, prefix) || !strings.HasSuffix(name, ".jsonl") {
		return ""
	}
	body := strings.TrimSuffix(strings.TrimPrefix(name, prefix), ".jsonl")
	if len(body) < 56 || body[19] != '-' {
		return ""
	}
	id := body[20:56]
	if !adapter.IsCanonicalConversationID(id) {
		return ""
	}
	if len(body) == 56 {
		return id
	}
	if len(body) == 93 && body[56] == '_' && adapter.IsCanonicalConversationID(body[57:]) {
		return id
	}
	return ""
}
