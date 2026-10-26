package skeleton

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"

	"github.com/Nathandela/swarm/internal/persist"
	"github.com/Nathandela/swarm/internal/protocol"
	"github.com/Nathandela/swarm/internal/status"
)

// The owner lifecycle fence precedes the resume mutex, matching auth resume.
func (a *coreAPI) deleteDiscussion(id string) error {
	a.handoffMu.Lock()
	defer a.handoffMu.Unlock()
	a.externalResumeMu.Lock()
	defer a.externalResumeMu.Unlock()
	a.workspaceMu.Lock()
	defer a.workspaceMu.Unlock()
	roster := a.core.List()
	attempts := persist.DiscussionAttempts(roster, id)
	if len(attempts) == 0 {
		// A completed deletion is retryable, but an unregistered state child
		// has no discussion owner authorizing removal (including shared stores).
		if !persist.ValidID(id) || a.stateDir == "" {
			return fmt.Errorf("delete: unknown discussion %q", id)
		}
		if _, err := os.Lstat(filepath.Join(a.stateDir, id)); !os.IsNotExist(err) {
			return fmt.Errorf("delete: unregistered state path for %q cannot be removed", id)
		}
		return nil
	}
	visible, _ := persist.ProjectDiscussions(roster)
	current := false
	for _, m := range visible {
		current = current || m.ID == id
	}
	if !current {
		hiddenOnly := true
		for _, m := range attempts {
			hiddenOnly = hiddenOnly && m.RosterHidden && (m.Status.Process == status.ProcessExited || m.Status.Process == status.ProcessLost)
		}
		if !hiddenOnly {
			return fmt.Errorf("delete: session %q is retained history; delete the current discussion instead", id)
		}
	}
	members := make(map[string]bool, len(attempts))
	for _, m := range attempts {
		if len(attempts) > 1 && m.Status.Process == status.ProcessRunning {
			return fmt.Errorf("delete: discussion still has running attempt %q; stop it first", m.ID)
		}
		members[m.ID] = true
	}
	for _, m := range attempts {
		var owned []string
		if a.stateDir != "" {
			owned = append(owned, filepath.Join(a.stateDir, m.ID))
		}
		if m.LaunchOptions[protocol.OptionWorktree] == "true" {
			checkout := m.AgentCwd
			if checkout == "" {
				checkout = filepath.Join(m.Cwd, ".swarm", "worktrees", m.ID)
			}
			owned = append(owned, checkout)
		}
		for _, path := range owned {
			root, err := canonicalCleanupPath(path)
			if err != nil {
				return err
			}
			for _, other := range roster {
				if members[other.ID] || other.ProviderCwd() == "" {
					continue
				}
				cwd, err := canonicalCleanupPath(other.ProviderCwd())
				if err != nil {
					return err
				}
				if cleanupContains(root, cwd) {
					return fmt.Errorf("delete: workspace %q is still used by session %q", path, other.ID)
				}
			}
		}
	}
	// Farthest first keeps every surviving attempt connected to the selected row
	// after a partial failure, so retry can still find the whole remaining family.
	adjacent := make(map[string][]string)
	for _, m := range attempts {
		if parent := m.ResumedFrom; parent != "" {
			adjacent[m.ID] = append(adjacent[m.ID], parent)
			adjacent[parent] = append(adjacent[parent], m.ID)
		}
	}
	distance := map[string]int{id: 0}
	queue := []string{id}
	for head := 0; head < len(queue); head++ {
		for _, next := range adjacent[queue[head]] {
			if _, seen := distance[next]; !seen {
				distance[next] = distance[queue[head]] + 1
				queue = append(queue, next)
			}
		}
	}
	sort.Slice(attempts, func(i, j int) bool {
		if distance[attempts[i].ID] != distance[attempts[j].ID] {
			return distance[attempts[i].ID] > distance[attempts[j].ID]
		}
		return attempts[i].ID < attempts[j].ID
	})
	defer a.pokeWatch()
	for _, m := range attempts {
		// Planned removals remain consumers until their own deletion succeeds.
		if err := a.preserveLegacyHandoffInputs([]persist.Meta{m}, a.core.List(), map[string]bool{m.ID: true}); err != nil {
			return err
		}
		if err := a.core.Delete(m.ID); err != nil {
			return fmt.Errorf("delete: attempt %q: %w", m.ID, err)
		}
	}
	return nil
}

func cleanupContains(root, cwd string) bool {
	rootInfo, _ := os.Stat(root)
	for path := cwd; ; path = filepath.Dir(path) {
		if path == root {
			return true
		}
		if info, err := os.Stat(path); err == nil && rootInfo != nil && os.SameFile(rootInfo, info) {
			return true
		}
		if filepath.Dir(path) == path {
			return false
		}
	}
}

// Resolve existing ancestors too: an ended session's cwd may no longer exist.
func canonicalCleanupPath(path string) (string, error) {
	var suffix []string
	for {
		// A trailing slash makes Lstat follow a dangling symlink instead of seeing it.
		if len(path) > 1 {
			path = strings.TrimRight(path, string(filepath.Separator))
			if path == "" {
				path = string(filepath.Separator)
			}
		}
		resolved, err := filepath.EvalSymlinks(path)
		if err == nil {
			resolved, err = filepath.Abs(resolved)
			if err != nil {
				return "", err
			}
			for i := len(suffix) - 1; i >= 0; i-- {
				resolved = filepath.Join(resolved, suffix[i])
			}
			return resolved, nil
		}
		if !os.IsNotExist(err) {
			return "", fmt.Errorf("delete: resolve workspace %q: %w", path, err)
		}
		// Strip only the last raw component: Dir/Abs would clean symlink/..
		// before the existing ancestor can be resolved physically.
		if _, statErr := os.Lstat(path); !os.IsNotExist(statErr) {
			return "", fmt.Errorf("delete: resolve workspace %q: %w", path, err)
		}
		parent, base := filepath.Split(strings.TrimRight(path, string(filepath.Separator)))
		if base == "" || base == "." || base == ".." {
			return "", fmt.Errorf("delete: resolve workspace %q: %w", path, err)
		}
		suffix = append(suffix, base)
		path = parent
		if path == "" {
			path = "."
		}
	}
}

// Legacy inputs lived in their source attempt. Uncertain retained dependencies
// refuse deletion rather than relocating files or rewriting historical prompts.
func (a *coreAPI) preserveLegacyHandoffInputs(attempts, roster []persist.Meta, members map[string]bool) error {
	if a.stateDir == "" {
		return nil
	}
	for _, source := range attempts {
		sourceDir := filepath.Join(a.stateDir, source.ID)
		entries, err := os.ReadDir(sourceDir)
		if err != nil {
			return fmt.Errorf("delete: inspect legacy handoff inputs for %q: %w", source.ID, err)
		}
		var legacyNames []string
		for _, entry := range entries {
			if strings.HasPrefix(entry.Name(), "native-history-") {
				legacyNames = append(legacyNames, entry.Name())
			}
		}
		if len(legacyNames) == 0 {
			continue
		}
		canonical, err := canonicalCleanupPath(sourceDir)
		if err != nil {
			return err
		}
		for _, other := range roster {
			if members[other.ID] {
				continue
			}
			if other.SpawnedFrom == source.ID && other.SpawnIntent == protocol.SpawnIntentHandoff {
				return fmt.Errorf("delete: legacy handoff inputs for %q may still be used by session %q", source.ID, other.ID)
			}
			root, err := os.OpenRoot(filepath.Join(a.stateDir, other.ID))
			if err != nil {
				return fmt.Errorf("delete: cannot confirm legacy handoff dependencies for session %q: %w", other.ID, err)
			}
			file, err := root.OpenFile("shim-launch.json", os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
			_ = root.Close()
			if err != nil {
				return fmt.Errorf("delete: cannot confirm legacy handoff dependencies for session %q: %w", other.ID, err)
			}
			info, statErr := file.Stat()
			var raw []byte
			if statErr == nil && safeHandoffFile(info) {
				raw, err = io.ReadAll(io.LimitReader(file, openCodeHistoryMaxBytes+1))
			} else {
				err = fmt.Errorf("unsafe launch configuration")
			}
			_ = file.Close()
			var launch struct {
				SessionID string   `json:"session_id"`
				Argv      []string `json:"argv"`
			}
			var config any
			if err != nil || len(raw) > openCodeHistoryMaxBytes || json.Unmarshal(raw, &config) != nil || json.Unmarshal(raw, &launch) != nil || launch.SessionID != other.ID || len(launch.Argv) == 0 {
				return fmt.Errorf("delete: unreadable legacy handoff dependencies for session %q", other.ID)
			}
			var references func(any) bool
			references = func(value any) bool {
				switch value := value.(type) {
				case string:
					// A legacy filename is immutable evidence even through a directory alias.
					for _, name := range legacyNames {
						if strings.Contains(value, name) {
							return true
						}
					}
					return strings.Contains(value, sourceDir+string(filepath.Separator)) || strings.Contains(value, canonical+string(filepath.Separator))
				case []any:
					for _, item := range value {
						if references(item) {
							return true
						}
					}
				case map[string]any:
					for _, item := range value {
						if references(item) {
							return true
						}
					}
				}
				return false
			}
			if config == nil || references(config) {
				return fmt.Errorf("delete: legacy handoff inputs for %q are still referenced or ambiguous in session %q", source.ID, other.ID)
			}
		}
	}
	return nil
}
