package teammatemanager

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/Nathandela/swarm/internal/persist"
)

// The wrapper delegates every operation except the selected parked manager's resume frontend.
func prepareHostedResumeFailures(t *testing.T, root string) func(state, session string) func(string) {
	t.Helper()
	v := os.Getenv("SWARM_TEAMMATE_TEST_RESUME_FAILURES")
	if v == "" {
		return nil
	}
	if v != "1" && v != "2" {
		t.Fatal("SWARM_TEAMMATE_TEST_RESUME_FAILURES must be 1 or 2")
	}
	binary, err := exec.LookPath("codex")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(binary, filepath.Join(root, "native-codex")); err != nil {
		t.Fatal(err)
	}
	const shim = `#!/bin/sh
dir=${0%/*}
if [ "$1" = resume ] && [ -s "$dir/target" ] && [ "$2" = "$(cat "$dir/target")" ]; then
	for marker in "$dir"/inject-[01]; do
		if mv "$marker" "$marker.used" 2>/dev/null; then
			echo "fixture: selected manager frontend exited" >&2
			exit 1
		fi
	done
fi
exec "$dir/native-codex" "$@"
`
	if err := os.WriteFile(filepath.Join(root, "codex"), []byte(shim), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", root+string(os.PathListSeparator)+os.Getenv("PATH"))
	return func(state, session string) func(string) {
		t.Helper()
		nativeID := func(id string) string {
			t.Helper()
			raw, err := os.ReadFile(filepath.Join(state, filepath.Base(id), "meta.json"))
			if err != nil {
				t.Fatal(err)
			}
			var m persist.Meta
			if err := json.Unmarshal(raw, &m); err != nil || m.ConversationID == "" {
				t.Fatal("missing native manager identity", err)
			}
			return m.ConversationID
		}
		old := nativeID(session)
		if err := os.WriteFile(filepath.Join(root, "target"), []byte(old), 0600); err != nil {
			t.Fatal(err)
		}
		count, _ := strconv.Atoi(v)
		for i := range count {
			if err := os.WriteFile(filepath.Join(root, "inject-"+strconv.Itoa(i)), nil, 0600); err != nil {
				t.Fatal(err)
			}
		}
		return func(next string) {
			t.Helper()
			for i := range count {
				if _, err := os.Stat(filepath.Join(root, "inject-"+strconv.Itoa(i)+".used")); err != nil {
					t.Fatal("resume injection missed", err)
				}
			}
			if (nativeID(next) == old) != (count == 1) {
				t.Fatal("unexpected native history after failed resumes")
			}
		}
	}
}
