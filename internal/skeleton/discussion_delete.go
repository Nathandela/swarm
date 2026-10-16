package skeleton

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/Nathandela/swarm/internal/persist"
	"github.com/Nathandela/swarm/internal/protocol"
	"github.com/Nathandela/swarm/internal/status"
)

// The owner lifecycle fence precedes the resume mutex, matching auth resume.
func (a *coreAPI) deleteDiscussion(id string) error {
	a.externalResumeMu.Lock()
	defer a.externalResumeMu.Unlock()
	a.workspaceMu.Lock()
	defer a.workspaceMu.Unlock()
	roster := a.core.List()
	attempts := persist.DiscussionAttempts(roster, id)
	if len(attempts) == 0 {
		return a.core.Delete(id)
	}
	visible, _ := persist.ProjectDiscussions(roster)
	current := false
	for _, m := range visible {
		current = current || m.ID == id
	}
	if !current {
		return fmt.Errorf("delete: session %q is retained history; delete the current discussion instead", id)
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
	path, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	var suffix []string
	for {
		resolved, err := filepath.EvalSymlinks(path)
		if err == nil {
			for i := len(suffix) - 1; i >= 0; i-- {
				resolved = filepath.Join(resolved, suffix[i])
			}
			return resolved, nil
		}
		if !os.IsNotExist(err) || filepath.Dir(path) == path {
			return "", fmt.Errorf("delete: resolve workspace %q: %w", path, err)
		}
		suffix = append(suffix, filepath.Base(path))
		path = filepath.Dir(path)
	}
}
