package supervise

// FAILING-FIRST tests for PB-LIFE-1/-2/-6 on the flagship install path: Homebrew.
//
// THE HOLE THESE CLOSE. .goreleaser.yaml puts swarm-remote in the SAME archive as swarm so
// that an installed swarm always has a gateway binary to point its unit at. But a cask
// links onto PATH only what it DECLARES: everything else in the archive is downloaded,
// staged under the Caskroom, and never linked. With the cask left at its default the
// generated file carries `binary "swarm"` and nothing else, so on every Homebrew machine
// `swarm remote init` fails to resolve the gateway, installs no unit, and `swarm remote
// pair` then advises `swarm remote init` -- advice that can never succeed.
//
// release_test.go parses the homebrew_casks block already and asserts NOTHING about it,
// which is exactly the coverage gap that let this ship. These tests assert the property
// the archive/cask pair has to satisfy, DERIVED from the archive contents rather than from
// a hardcoded list, so adding a fourth binary keeps them honest.
//
// INTENDED PRODUCTION (RED):
//
//	homebrew_casks:
//	  - name: swarm
//	    ids: [swarm]
//	    binaries: [swarm, swarm-remote]   # every binary the `swarm` archive ships
//
// No YAML dependency is added, for the reason release_test.go gives: the module has none,
// and a release manifest is not worth one. The scan below reads the list blocks it needs
// and ignores everything else.

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// releaseEntry is one item of a top-level list block (one archive, one cask) reduced to
// what these tests need: the name it is identified by, and the string lists it declares.
type releaseEntry struct {
	name  string
	lists map[string][]string
}

// scanListBlock reads the entries of one top-level list block. Entries are the "- " lines
// at the block's own indentation; a deeper "- " line is an item of the last `key:` seen.
// Both YAML list forms are understood -- the block form and the inline `key: [a, b]` --
// so the assertions do not depend on how the manifest happens to be formatted.
func scanListBlock(t *testing.T, path, block string) []releaseEntry {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	var entries []releaseEntry
	cur, listKey := "", ""
	entryIndent := -1
	for _, raw := range strings.Split(string(b), "\n") {
		line := strings.TrimRight(raw, " \t")
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		indent := len(line) - len(strings.TrimLeft(line, " "))
		if indent == 0 {
			cur, listKey, entryIndent = strings.TrimSuffix(trimmed, ":"), "", -1
			continue
		}
		if cur != block {
			continue
		}
		if item, ok := strings.CutPrefix(trimmed, "- "); ok {
			switch {
			case entryIndent == -1 || indent == entryIndent:
				entryIndent = indent
				entries = append(entries, releaseEntry{lists: map[string][]string{}})
				listKey = ""
				trimmed = item // the first field rides on the dash: `- id: swarm`
			case listKey != "" && len(entries) > 0:
				e := &entries[len(entries)-1]
				e.lists[listKey] = append(e.lists[listKey], strings.TrimSpace(item))
				continue
			default:
				continue
			}
		}
		key, val, ok := strings.Cut(trimmed, ":")
		if !ok || len(entries) == 0 {
			continue
		}
		key, val = strings.TrimSpace(key), strings.TrimSpace(val)
		e := &entries[len(entries)-1]
		switch {
		case val == "":
			listKey = key
		case strings.HasPrefix(val, "[") && strings.HasSuffix(val, "]"):
			for _, item := range strings.Split(strings.Trim(val, "[]"), ",") {
				if item = strings.TrimSpace(item); item != "" {
					e.lists[key] = append(e.lists[key], item)
				}
			}
			listKey = ""
		default:
			// First `id:`/`name:` wins: nested blocks (a cask's repository:) carry their own.
			if (key == "id" || key == "name") && e.name == "" {
				e.name = val
			}
			listKey = ""
		}
	}
	return entries
}

// TestGoreleaser_CaskLinksEveryBinaryItsArchiveShips is the assertion release_test.go was
// missing. It is derived, not enumerated: whatever builds an archive collects, the cask
// built from that archive must declare -- because a binary the cask does not declare is a
// binary no `brew install --cask swarm` ever puts on PATH.
func TestGoreleaser_CaskLinksEveryBinaryItsArchiveShips(t *testing.T) {
	root := repoRoot(t)
	path := filepath.Join(root, ".goreleaser.yaml")

	builds, _ := scanGoreleaser(t, path)
	binaryOf := map[string]string{}
	for _, b := range builds {
		binaryOf[b.id] = b.binary
	}

	archives := scanListBlock(t, path, "archives")
	if len(archives) == 0 {
		t.Fatalf("%s declares no archives", filepath.Base(path))
	}
	ships := map[string][]string{} // archive id -> binary names
	var everything []string
	for _, a := range archives {
		for _, id := range a.lists["ids"] {
			bin, ok := binaryOf[id]
			if !ok {
				t.Fatalf("archive %q collects build id %q, which no build declares", a.name, id)
			}
			ships[a.name] = append(ships[a.name], bin)
			everything = append(everything, bin)
		}
	}

	casks := scanListBlock(t, path, "homebrew_casks")
	if len(casks) == 0 {
		t.Fatalf("%s declares no homebrew_casks; PB-LIFE-6 wants a released install path", filepath.Base(path))
	}
	for _, c := range casks {
		var want []string
		if ids := c.lists["ids"]; len(ids) > 0 {
			for _, id := range ids {
				if _, ok := ships[id]; !ok {
					t.Fatalf("cask %q names archive id %q, which no archive declares", c.name, id)
				}
				want = append(want, ships[id]...)
			}
		} else {
			want = everything // goreleaser's default: every archive
		}
		got := c.lists["binaries"]
		if len(got) == 0 {
			if b := c.lists["binary"]; len(b) > 0 {
				got = b
			}
		}
		if !sameStrings(got, want) {
			t.Errorf("cask %q declares binaries %v, but the archives it is built from ship %v.\n"+
				"A cask links ONLY the binaries it declares -- every other one is downloaded into "+
				"the Caskroom and never put on PATH, so `swarm remote init` cannot resolve it and "+
				"PB-LIFE-1/-2 are undelivered on every Homebrew install.", c.name, got, want)
		}
	}
}

// TestGoreleaser_CommentMatchesHowTheGatewayIsResolved: the manifest's own header explains
// WHY swarm-remote rides in the swarm archive, and a release manifest that documents a
// resolution path the CLI does not take is how the next person ships the wrong thing --
// the same class of defect release_test.go's stale-comment check already guards.
func TestGoreleaser_CommentMatchesHowTheGatewayIsResolved(t *testing.T) {
	root := repoRoot(t)
	raw, err := os.ReadFile(filepath.Join(root, ".goreleaser.yaml"))
	if err != nil {
		t.Fatalf("read .goreleaser.yaml: %v", err)
	}
	// The claim that PATH alone resolves the gateway was false for every cask install, and
	// is no longer how the CLI looks for it first.
	if strings.Contains(string(raw), "resolves the\n#                 gateway binary from PATH") {
		t.Errorf(".goreleaser.yaml still says `swarm remote init` resolves the gateway from PATH " +
			"alone; it looks BESIDE its own executable first, which is what makes a cask install work")
	}
}

// TestHomebrewCaskReference_IsNotStale keeps the committed reference cask honest with the
// config that generates it. The file exists so a reader can see what the pipeline emits
// without running it; a copy that shows one linked binary when the config declares two
// teaches the reader the defect this slice fixed.
func TestHomebrewCaskReference_IsNotStale(t *testing.T) {
	root := repoRoot(t)
	ref := filepath.Join(root, "packaging", "homebrew", "swarm.rb")
	b, err := os.ReadFile(ref)
	if os.IsNotExist(err) {
		t.Skip("no committed reference cask; nothing to keep honest")
	}
	if err != nil {
		t.Fatalf("read %s: %v", ref, err)
	}
	casks := scanListBlock(t, filepath.Join(root, ".goreleaser.yaml"), "homebrew_casks")
	if len(casks) == 0 {
		t.Fatalf("no homebrew_casks in .goreleaser.yaml")
	}
	for _, bin := range casks[0].lists["binaries"] {
		if !strings.Contains(string(b), fmt.Sprintf("binary %q", bin)) {
			t.Errorf("%s carries no `binary %q` line, though the config declares it; the committed "+
				"reference is stale", filepath.Join("packaging", "homebrew", "swarm.rb"), bin)
		}
	}
	// The postflight strips the WHOLE staged path (the config's own comment says so,
	// because the archive carries a second binary launchd execs with nobody present to
	// click through a Gatekeeper prompt).
	if strings.Contains(string(b), `staged_path}/swarm"`) {
		t.Errorf("%s still strips quarantine from one named binary; the config strips staged_path",
			filepath.Join("packaging", "homebrew", "swarm.rb"))
	}
}

// sameStrings reports whether two lists hold the same set of values.
func sameStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	x, y := append([]string(nil), a...), append([]string(nil), b...)
	sort.Strings(x)
	sort.Strings(y)
	for i := range x {
		if x[i] != y[i] {
			return false
		}
	}
	return true
}
