package skeleton

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/Nathandela/swarm/internal/persist"
)

func TestAccountOwnerDeleteRemovesRetainedAliasAfterPinnedTarget(t *testing.T) {
	for _, failFirst := range []bool{false, true} {
		t.Run(map[bool]string{false: "missing-target", true: "retry-target-failure"}[failFirst], func(t *testing.T) {
			source := endedCodexSource("source", nil)
			unrelated := endedCodexSource("unrelated", nil)
			api := continuityAPI(t, source, unrelated)
			d := &Daemon{core: api.core, api: api}
			d.restoreAuthRecycle(source.ID)
			rec := accountRotationRecord{OriginalSource: source.ID, SourceID: source.ID, CandidateID: "gone", OwnerTarget: "gone", State: accountOwnerCanceled}
			w := &authWatcher{get: api.core.Get, clearRecycle: d.clearAuthRecycle, state: authWatchState{AccountRotations: map[string]accountRotationRecord{source.ID: rec}}}
			m := &accountRotationManager{w: w, aliases: map[string]string{source.ID: source.ID}}
			var targets []string
			m.endTarget = func(target, action string) error {
				targets = append(targets, target)
				if failFirst && len(targets) == 1 {
					return errors.New("lost target-delete reply")
				}
				return d.withOwnerSessionEnd(target, func() error { return api.deleteDiscussion(target) })
			}
			if failFirst {
				if err := m.ownerEnd(w, source.ID, "delete"); err == nil {
					t.Fatal("target deletion failure reported success")
				}
				if _, err := os.Stat(filepath.Join(api.stateDir, source.ID, "meta.json")); err != nil {
					t.Fatal("failed pinned effect removed source", err)
				}
			}
			if err := m.ownerEnd(w, source.ID, "delete"); err != nil {
				t.Fatal(err)
			}
			for _, meta := range []persist.Meta{source, unrelated} {
				_, err := os.Stat(filepath.Join(api.stateDir, meta.ID, "meta.json"))
				if keep := meta.ID == unrelated.ID; keep != (err == nil) {
					t.Fatalf("unexpected retained attempt %s: %v", meta.ID, err)
				}
			}
			if got := w.state.AccountRotations[source.ID]; !reflect.DeepEqual(got, rec) {
				t.Fatalf("changed cancellation receipt: %+v", got)
			}
			if len(targets) < 2 || targets[len(targets)-2] != rec.OwnerTarget || targets[len(targets)-1] != source.ID {
				t.Fatalf("cleanup changed the pinned effect order: %v", targets)
			}
			if err := m.ownerEnd(w, source.ID, "delete"); err != nil {
				t.Fatalf("deleted alias retry: %v", err)
			}
		})
	}
}

func TestOwnerDeleteRefusesUnregisteredStatePayload(t *testing.T) {
	for _, id := range []string{"accounts", "journal", "remote", "orphan"} {
		t.Run(id, func(t *testing.T) {
			api := continuityAPI(t)
			d := &Daemon{core: api.core, api: api}
			path := filepath.Join(api.stateDir, id, "keep")
			if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte("owned control state"), 0600); err != nil {
				t.Fatal(err)
			}
			if err := d.ownerDelete(id); err == nil {
				t.Fatal("owner delete accepted an unregistered state directory")
			}
			if data, err := os.ReadFile(path); err != nil || string(data) != "owned control state" {
				t.Fatalf("refused deletion changed control state: %q, %v", data, err)
			}
		})
	}
}
