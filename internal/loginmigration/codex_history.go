package loginmigration

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

const maxRolloutHeader = 1 << 20
const maxRolloutHeaders = 128 << 20

type codexRollout struct{ Path, Thread, Physical, Parent, Base, Mode string }

func canonicalConversation(id string) bool { return len(id) == 36 && uuidRE.FindString(id) == id }
func rolloutIDs(path string) (string, string, error) {
	name := strings.TrimSuffix(filepath.Base(path), ".zst")
	ids := uuidRE.FindAllString(name, -1)
	if len(ids) == 1 && strings.HasSuffix(name, ids[0]+".jsonl") {
		return ids[0], ids[0], nil
	}
	if len(ids) == 2 && strings.HasSuffix(name, ids[0]+"_"+ids[1]+".jsonl") {
		return ids[0], ids[1], nil
	}
	return "", "", ErrUnsupported
}
func rolloutHeader(path string) ([]byte, error) {
	if safePath(path) != nil {
		return nil, ErrUnsupported
	}
	root, err := os.OpenRoot(filepath.Dir(path))
	if err != nil {
		return nil, err
	}
	defer func() { _ = root.Close() }()
	name := filepath.Base(path)
	before, err := root.Lstat(name)
	if err != nil || !owned(before, false) || !before.Mode().IsRegular() {
		return nil, ErrUnsupported
	}
	file, err := root.OpenFile(name, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	defer func() { _ = file.Close() }()
	after, err := file.Stat()
	if err != nil || !os.SameFile(before, after) {
		return nil, ErrUnsupported
	}
	// Aborted native creation can leave an empty placeholder, never usable history.
	if before.Size() == 0 && after.Size() == 0 {
		if strings.HasSuffix(path, ".jsonl") {
			return nil, nil
		}
		return nil, ErrUnsupported
	}
	var reader io.Reader = file
	if strings.HasSuffix(path, ".zst") {
		binary, lookErr := exec.LookPath("zstd")
		if lookErr != nil {
			return nil, fmt.Errorf("%w: zstd is required to inspect compressed native rollout headers", ErrUnsupported)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		command := exec.CommandContext(ctx, binary, "-dc")
		command.Stdin = file
		pipe, pipeErr := command.StdoutPipe()
		if pipeErr != nil {
			return nil, pipeErr
		}
		if err = command.Start(); err != nil {
			return nil, err
		}
		defer func() { cancel(); _ = command.Wait() }()
		reader = pipe
	}
	line, err := bufio.NewReader(io.LimitReader(reader, maxRolloutHeader+1)).ReadBytes('\n')
	if err != nil && !errors.Is(err, io.EOF) || len(line) == 0 || len(line) > maxRolloutHeader {
		return nil, ErrUnsupported
	}
	return line, nil
}

// Dependency fields and physical filename suffixes follow rust-v0.163.0,
// commit 83086188ae0614bd882818ff1aa23f3194962012 protocol.rs and rollout/list.rs.
func parseCodexRollout(path string, raw []byte) (codexRollout, error) {
	node := codexRollout{Path: path}
	thread, physical, err := rolloutIDs(path)
	if err != nil {
		return node, err
	}
	node.Physical = physical
	var line struct {
		Type    string `json:"type"`
		Payload struct {
			ID     string          `json:"id"`
			Parent *string         `json:"parent_thread_id"`
			Source json.RawMessage `json:"source"`
			Mode   string          `json:"history_mode"`
			Base   json.RawMessage `json:"history_base"`
		} `json:"payload"`
	}
	if validJSON(raw) != nil || json.Unmarshal(raw, &line) != nil || line.Type != "session_meta" || !canonicalConversation(line.Payload.ID) || line.Payload.ID != thread {
		return node, ErrUnsupported
	}
	node.Thread = thread
	node.Mode = line.Payload.Mode
	if line.Payload.Parent != nil {
		node.Parent = *line.Payload.Parent
		if !canonicalConversation(node.Parent) {
			return node, ErrUnsupported
		}
	}
	var source map[string]json.RawMessage
	if len(line.Payload.Source) > 0 && line.Payload.Source[0] == '{' {
		if json.Unmarshal(line.Payload.Source, &source) != nil {
			return node, ErrUnsupported
		}
		if subagent, exists := source["subagent"]; exists && len(subagent) > 0 && subagent[0] == '{' {
			var variants map[string]json.RawMessage
			if json.Unmarshal(subagent, &variants) != nil {
				return node, ErrUnsupported
			}
			if spawn, exists := variants["thread_spawn"]; exists {
				var detail struct {
					Parent string `json:"parent_thread_id"`
					Depth  int    `json:"depth"`
				}
				if json.Unmarshal(spawn, &detail) != nil || !canonicalConversation(detail.Parent) || detail.Depth <= 0 || detail.Depth > 1024 || node.Parent != "" && node.Parent != detail.Parent {
					return node, ErrUnsupported
				}
				node.Parent = detail.Parent
			}
		}
	}
	if node.Parent == node.Thread {
		return node, ErrUnsupported
	}
	if b := line.Payload.Base; len(b) > 0 && string(b) != "null" {
		var base struct {
			ID      string `json:"thread_id"`
			Ordinal uint64 `json:"end_ordinal_exclusive"`
			Offset  uint64 `json:"end_byte_offset"`
		}
		var fields map[string]json.RawMessage
		if json.Unmarshal(b, &base) != nil || json.Unmarshal(b, &fields) != nil || !canonicalConversation(base.ID) || fields["end_ordinal_exclusive"] == nil || fields["end_byte_offset"] == nil {
			return node, ErrUnsupported
		}
		node.Base = base.ID
	}
	return node, nil
}
func acyclic(edges map[string]string) error {
	done := map[string]bool{}
	for start := range edges {
		path := map[string]bool{}
		current := start
		for current != "" && !done[current] {
			if path[current] {
				return ErrUnsupported
			}
			path[current] = true
			current = edges[current]
		}
		for id := range path {
			done[id] = true
		}
	}
	return nil
}

// Read only bounded headers up front, then select descendants and immutable
// history-base rollouts. Unrelated owner conversations are never copied.
func codexHistoryClosure(sources []contextSource, wanted map[string]bool) (map[string]bool, error) {
	selected := map[string]bool{}
	if len(wanted) == 0 {
		return selected, nil
	}
	nodes := map[string]codexRollout{}
	threads := map[string][]string{}
	physical := map[string][]string{}
	children := map[string][]string{}
	seen := map[string]bool{}
	visited := 0
	headerBytes := 0
	for _, source := range sources {
		home, err := filepath.EvalSymlinks(source.Home)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, err
		}
		if seen[home] {
			continue
		}
		seen[home] = true
		for _, directory := range []string{"sessions", "archived_sessions"} {
			err = filepath.WalkDir(filepath.Join(home, directory), func(path string, entry fs.DirEntry, walkErr error) error {
				if errors.Is(walkErr, os.ErrNotExist) {
					return nil
				}
				if walkErr != nil {
					return walkErr
				}
				visited++
				if visited > 200000 {
					return ErrUnsupported
				}
				if entry.IsDir() {
					return nil
				}
				name := entry.Name()
				if !strings.HasSuffix(name, ".jsonl") && !strings.HasSuffix(name, ".jsonl.zst") {
					return nil
				}
				if strings.HasSuffix(name, ".zst") {
					if info, statErr := os.Lstat(strings.TrimSuffix(path, ".zst")); statErr == nil {
						if !info.Mode().IsRegular() {
							return ErrUnsupported
						}
						return nil
					} else if !errors.Is(statErr, os.ErrNotExist) {
						return statErr
					}
				}
				raw, readErr := rolloutHeader(path)
				if readErr != nil {
					return readErr
				}
				if len(raw) == 0 {
					return nil
				}
				headerBytes += len(raw)
				if headerBytes > maxRolloutHeaders || len(nodes) >= 100000 {
					return ErrUnsupported
				}
				node, parseErr := parseCodexRollout(path, raw)
				if parseErr != nil {
					return parseErr
				}
				nodes[path] = node
				threads[node.Thread] = append(threads[node.Thread], path)
				physical[node.Physical] = append(physical[node.Physical], path)
				if node.Parent != "" {
					children[node.Parent] = append(children[node.Parent], node.Thread)
				}
				return nil
			})
			if err != nil {
				return nil, err
			}
		}
	}
	threadQueue := []string{}
	for id := range wanted {
		threadQueue = append(threadQueue, id)
	}
	threadSeen := map[string]bool{}
	baseQueue := []string{}
	parents := map[string]string{}
	bases := map[string]string{}
	add := func(path string) error {
		node := nodes[path]
		if node.Mode != "" && node.Mode != "legacy" && node.Mode != "paginated" {
			return ErrUnsupported
		}
		if node.Base != "" && node.Mode != "paginated" {
			return ErrUnsupported
		}
		if old, exists := parents[node.Thread]; exists && old != node.Parent {
			return ErrHistoryConflict
		}
		parents[node.Thread] = node.Parent
		if old, exists := bases[node.Physical]; exists && old != node.Base {
			return ErrHistoryConflict
		}
		bases[node.Physical] = node.Base
		if !selected[path] {
			selected[path] = true
			if node.Base != "" {
				baseQueue = append(baseQueue, node.Base)
			}
		}
		return nil
	}
	for len(threadQueue) > 0 {
		id := threadQueue[0]
		threadQueue = threadQueue[1:]
		if threadSeen[id] {
			continue
		}
		threadSeen[id] = true
		if len(threads[id]) == 0 {
			return nil, fmt.Errorf("%w: retained native rollout header is missing", ErrUnsupported)
		}
		for _, path := range threads[id] {
			if err := add(path); err != nil {
				return nil, err
			}
		}
		threadQueue = append(threadQueue, children[id]...)
	}
	baseSeen := map[string]bool{}
	for len(baseQueue) > 0 {
		id := baseQueue[0]
		baseQueue = baseQueue[1:]
		if baseSeen[id] {
			continue
		}
		baseSeen[id] = true
		if len(physical[id]) == 0 {
			return nil, fmt.Errorf("%w: native history-base rollout is missing", ErrUnsupported)
		}
		for _, path := range physical[id] {
			if err := add(path); err != nil {
				return nil, err
			}
		}
	}
	if acyclic(parents) != nil || acyclic(bases) != nil {
		return nil, ErrUnsupported
	}
	return selected, nil
}
