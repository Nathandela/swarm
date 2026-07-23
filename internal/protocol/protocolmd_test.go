package protocol

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// E6.2 / GG-7 — docs/specifications/protocol.md is written at field level and
// versioned; CI diffs its field table against the Go message types (drift fails
// the build). DRIFT-CHECK MECHANISM (pinned): reflect the json tags of the frozen
// wire message types and assert protocol.md documents every one, plus the version
// constant. protocol.md is the IMPLEMENTER's deliverable and is deliberately NOT
// stubbed here, so this test stays RED until it is written with the full field set.

// TestProtocolMD_ExistsAndDocumentsEveryField reflects the pinned message types'
// json tags and asserts each appears in protocol.md (the drift check).
func TestProtocolMD_ExistsAndDocumentsEveryField(t *testing.T) {
	path := filepath.Join(repoRoot(t), "docs", "specifications", "protocol.md")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("protocol.md not found at %s: %v — E6.2 requires it to exist", path, err)
	}
	doc := string(data)

	// The document must state the protocol version.
	if !strings.Contains(doc, "1") || !containsAny(doc, "version", "Version") {
		t.Errorf("protocol.md does not document the protocol version (Version=%d)", Version)
	}

	missing := []string{}
	for _, tag := range wireJSONTags() {
		if !strings.Contains(doc, tag) {
			missing = append(missing, tag)
		}
	}
	if len(missing) != 0 {
		t.Errorf("protocol.md is missing documentation for wire fields %v (GG-7 drift: docs must match the Go message structs)", missing)
	}
}

// TestProtocolMD_DocumentsEveryOp asserts each control op string is documented.
func TestProtocolMD_DocumentsEveryOp(t *testing.T) {
	path := filepath.Join(repoRoot(t), "docs", "specifications", "protocol.md")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("protocol.md not found: %v", err)
	}
	doc := string(data)
	for _, op := range allOps() {
		if !strings.Contains(doc, op) {
			t.Errorf("protocol.md does not document control op %q", op)
		}
	}
}

// wireJSONTags returns the deduped set of direct json tag names across the frozen
// wire message types. This is the exact field set protocol.md must document.
func wireJSONTags() []string {
	types := []reflect.Type{
		reflect.TypeOf(Control{}),
		reflect.TypeOf(SessionView{}),
		reflect.TypeOf(LaunchReq{}),
		reflect.TypeOf(TerminalSnapshot{}),
	}
	seen := map[string]bool{}
	var out []string
	for _, ty := range types {
		for i := 0; i < ty.NumField(); i++ {
			tag := strings.Split(ty.Field(i).Tag.Get("json"), ",")[0]
			if tag == "" || tag == "-" || seen[tag] {
				continue
			}
			seen[tag] = true
			out = append(out, tag)
		}
	}
	return out
}

// allOps is the frozen control-op vocabulary.
func allOps() []string {
	return []string{
		OpHello, OpList, OpLaunch, OpKill, OpDelete,
		OpAttach, OpDetach, OpResize, OpSubscribe,
		OpEvent, OpLease, OpOK, OpError,
	}
}

func containsAny(s string, subs ...string) bool {
	for _, sub := range subs {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}

// repoRoot walks up from the package dir (the test working directory) to the
// module root (the dir holding go.mod).
func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatalf("go.mod not found walking up from the package dir")
		}
		dir = parent
	}
}
