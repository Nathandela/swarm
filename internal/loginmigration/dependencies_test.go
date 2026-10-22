package loginmigration

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/Nathandela/swarm/internal/processcontain"
	"github.com/Nathandela/swarm/internal/procstart"
)

const childConversation = "223e4567-e89b-42d3-a456-426614174013"
const grandchildConversation = "323e4567-e89b-42d3-a456-426614174013"
const otherConversation = "423e4567-e89b-42d3-a456-426614174013"
const physicalConversation = "523e4567-e89b-42d3-a456-426614174013"

func rolloutFixture(t *testing.T, home, thread, physical string, extra map[string]any) string {
	t.Helper()
	payload := map[string]any{"id": thread}
	for k, v := range extra {
		payload[k] = v
	}
	suffix := thread
	if physical != "" {
		suffix += "_" + physical
	}
	path := filepath.Join(home, "sessions", "2026", "10", "22", "rollout-2026-10-22T00-00-00-"+suffix+".jsonl")
	put(t, path, map[string]any{"type": "session_meta", "payload": payload})
	return path
}
func TestCodexHistoryDependencyClosure(t *testing.T) {
	f := newFixture(t)
	root := rolloutFixture(t, f.home, conversation, "", nil)
	child := rolloutFixture(t, f.home, childConversation, "", map[string]any{"parent_thread_id": conversation})
	grandchild := rolloutFixture(t, f.home, grandchildConversation, "", map[string]any{"source": map[string]any{"subagent": map[string]any{"thread_spawn": map[string]any{"parent_thread_id": childConversation, "depth": 2}}}})
	unrelated := rolloutFixture(t, f.home, otherConversation, "", nil)
	reverted := rolloutFixture(t, f.home, conversation, physicalConversation, map[string]any{"history_mode": "paginated", "history_base": map[string]any{"thread_id": conversation, "end_ordinal_exclusive": 1, "end_byte_offset": 1}})
	selected, err := codexHistoryClosure([]contextSource{{Home: f.home}}, map[string]bool{conversation: true})
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{root, child, grandchild, reverted} {
		if !selected[path] {
			t.Fatalf("required native rollout omitted: %s", filepath.Base(path))
		}
	}
	if selected[unrelated] || len(selected) != 4 {
		t.Fatalf("unrelated history selected: %d", len(selected))
	}
}
func TestCodexHistoryBaseSelectsPhysicalPrefixOnly(t *testing.T) {
	f := newFixture(t)
	root := rolloutFixture(t, f.home, conversation, "", map[string]any{"history_mode": "paginated", "history_base": map[string]any{"thread_id": physicalConversation, "end_ordinal_exclusive": 1, "end_byte_offset": 1}})
	base := rolloutFixture(t, f.home, otherConversation, physicalConversation, nil)
	unrelated := rolloutFixture(t, f.home, otherConversation, "", nil)
	selected, err := codexHistoryClosure([]contextSource{{Home: f.home}}, map[string]bool{conversation: true})
	if err != nil {
		t.Fatal(err)
	}
	if !selected[root] || !selected[base] || selected[unrelated] || len(selected) != 2 {
		t.Fatal("physical prefix closure differs")
	}
}
func TestCodexHistoryRejectsMalformedDependencies(t *testing.T) {
	for _, kind := range []string{"parent", "contradictory-parent", "cycle", "missing-base", "base-cycle", "header-id", "unknown-mode", "invalid-base-count"} {
		t.Run(kind, func(t *testing.T) {
			f := newFixture(t)
			extra := map[string]any{}
			switch kind {
			case "parent":
				extra["parent_thread_id"] = "malformed"
			case "contradictory-parent":
				extra["parent_thread_id"] = childConversation
				extra["source"] = map[string]any{"subagent": map[string]any{"thread_spawn": map[string]any{"parent_thread_id": otherConversation, "depth": 1}}}
			case "cycle":
				extra["parent_thread_id"] = childConversation
				rolloutFixture(t, f.home, childConversation, "", map[string]any{"parent_thread_id": conversation})
			case "missing-base":
				extra["history_mode"] = "paginated"
				extra["history_base"] = map[string]any{"thread_id": physicalConversation, "end_ordinal_exclusive": 1, "end_byte_offset": 1}
			case "base-cycle":
				extra["history_mode"] = "paginated"
				extra["history_base"] = map[string]any{"thread_id": conversation, "end_ordinal_exclusive": 1, "end_byte_offset": 1}
			case "header-id":
				extra["id"] = otherConversation
			case "unknown-mode":
				extra["history_mode"] = "future-unsupported-mode"
			case "invalid-base-count":
				extra["history_mode"] = "paginated"
				extra["history_base"] = map[string]any{"thread_id": physicalConversation, "end_ordinal_exclusive": -1, "end_byte_offset": 1}
			}
			rolloutFixture(t, f.home, conversation, "", extra)
			if _, err := codexHistoryClosure([]contextSource{{Home: f.home}}, map[string]bool{conversation: true}); err == nil {
				t.Fatal("unsafe dependency accepted")
			}
		})
	}
}
func TestCodexCompressedHistoryHeaderAndPlainPreference(t *testing.T) {
	binary, err := exec.LookPath("zstd")
	if err != nil {
		t.Skip("native zstd unavailable")
	}
	if err := exec.Command(binary, "--version").Run(); err != nil {
		t.Skip("native zstd cannot execute on this host")
	}
	f := newFixture(t)
	root := rolloutFixture(t, f.home, conversation, "", nil)
	raw, err := os.ReadFile(root)
	if err != nil {
		t.Fatal(err)
	}
	command := exec.Command(binary, "-q", "-c")
	command.Stdin = bytes.NewReader(raw)
	compressed, err := command.Output()
	if err != nil {
		t.Fatal(err)
	}
	put(t, root+".zst", compressed)
	selected, err := codexHistoryClosure([]contextSource{{Home: f.home}}, map[string]bool{conversation: true})
	if err != nil || !selected[root] || len(selected) != 1 {
		t.Fatalf("plain preference: %v", err)
	}
	if err = os.Remove(root); err != nil {
		t.Fatal(err)
	}
	selected, err = codexHistoryClosure([]contextSource{{Home: f.home}}, map[string]bool{conversation: true})
	if err != nil || !selected[root+".zst"] || len(selected) != 1 {
		t.Fatalf("compressed header: %v", err)
	}
}
func TestCleanStartupDoesNotConvertOrdinaryLiveSessions(t *testing.T) {
	for _, emptyRegistry := range []bool{false, true} {
		t.Run(fmt.Sprint(emptyRegistry), func(t *testing.T) {
			f := newFixture(t)
			if err := os.RemoveAll(filepath.Join(f.state, "accounts")); err != nil {
				t.Fatal(err)
			}
			if emptyRegistry {
				put(t, filepath.Join(f.state, "accounts", "registry.json"), map[string]any{"schema_version": 1, "accounts": map[string]any{}})
			}
			start, err := procstart.StartTime(os.Getpid())
			if err != nil {
				t.Fatal(err)
			}
			put(t, filepath.Join(f.state, f.id, "meta.json"), map[string]any{"schema_version": 1, "id": f.id, "agent_type": "codex", "shim_pid": os.Getpid(), "shim_start_time": start})
			put(t, filepath.Join(f.state, "devices", "meta.json"), map[string]any{"schema_version": 17, "device_ids": []string{"test"}})
			checkExternalWriters = func(string) error { t.Fatal("ordinary startup scanned native writers"); return nil }
			if err = RunLocked(f.state); err != nil {
				t.Fatal(err)
			}
			if _, err = os.Stat(filepath.Join(f.state, "logins")); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("ordinary startup created login state")
			}
		})
	}
}

func TestFirstActivationIncludesRetainedOrdinaryHistory(t *testing.T) {
	f := newFixture(t)
	if err := os.RemoveAll(filepath.Join(f.state, "accounts")); err != nil {
		t.Fatal(err)
	}
	rolloutFixture(t, f.home, conversation, "", nil)
	put(t, filepath.Join(f.state, f.id, "meta.json"), map[string]any{"schema_version": 1, "id": f.id, "agent_type": "codex", "conversation_id": conversation, "cwd": f.project, "env": []string{"CODEX_HOME=" + f.home}})
	inputs, err := contextSources(f.state, "codex", nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(inputs) != 1 || inputs[0].Conversation != conversation || inputs[0].Home != f.home {
		t.Fatalf("retained ended conversation omitted: %+v", inputs)
	}
	if err = PrepareProviderContext(f.state, "codex", nil); err != nil {
		t.Fatal(err)
	}
	if err = verifyConversations(providerHome(f.state, "codex"), "codex", inputs); err != nil {
		t.Fatal(err)
	}
}

func TestEmptyContextScopesWriterGuardToPrivateDestination(t *testing.T) {
	for _, kind := range []string{"empty", "empty-target-writer", "nonempty"} {
		t.Run(kind, func(t *testing.T) {
			f := newFixture(t)
			source := filepath.Join(filepath.Dir(f.state), "empty-native")
			if err := os.Mkdir(source, 0700); err != nil {
				t.Fatal(err)
			}
			checkExternalWriters = func(string) error { return ErrLiveWriter }
			if kind == "nonempty" {
				put(t, filepath.Join(source, "config.toml"), "model = \"test\"\n")
			}
			environmentWriters = func(homes, ids []string) ([]processcontain.Identity, error) {
				if len(homes) != 1 || homes[0] != providerHome(f.state, "codex") || len(ids) != 0 {
					t.Fatal("empty publication proof escaped target scope")
				}
				if kind == "empty-target-writer" {
					return []processcontain.Identity{{PID: os.Getpid()}}, nil
				}
				return nil, nil
			}
			err := prepareContext(f.state, "codex", []contextSource{{Home: source}})
			if kind == "empty" && err != nil {
				t.Fatal(err)
			}
			if kind != "empty" && !errors.Is(err, ErrLiveWriter) {
				t.Fatalf("writer not fenced: %v", err)
			}
		})
	}
}
