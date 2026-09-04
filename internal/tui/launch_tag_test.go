package tui

// A session's tag is set on the board with `t`, which means the tag only ever
// arrives AFTER the session is created -- an owner who launches five sessions to
// group them has to go back and label each one. The new-session form gains the
// same optional entry, so the label can be given at the moment it is decided.
//
// The tag field sits beside the name (both are identity labels the owner types),
// travels on the launch request, and is stamped by the daemon exactly like the
// name -- not applied by a second round trip after the session is already live.

import (
	"path/filepath"
	"strings"
	"testing"
)

// The tag field renders between name and agent, next to the other typed label.
// The rows are located by their LABEL COLUMN, not by any occurrence of the word:
// a field's own placeholder copy mentions other fields ("defaults to agent-dir"),
// so a bare substring search would compare the wrong positions.
func TestLaunchForm_TagFieldRendersBetweenNameAndAgent(t *testing.T) {
	m := openLaunch(t, newFakeClient())
	v := stripANSI(launchOf(m).view())

	ni := fieldRow(t, v, "name")
	gi := fieldRow(t, v, "tag")
	ai := fieldRow(t, v, "agent")
	if ni >= gi || gi >= ai {
		t.Fatalf("tag must sit between name and agent (name=%d tag=%d agent=%d):\n%s", ni, gi, ai, v)
	}
}

// fieldRow is the index of the form line whose label column is exactly label.
func fieldRow(t *testing.T, view, label string) int {
	t.Helper()
	for i, line := range strings.Split(view, "\n") {
		if fields := strings.Fields(line); len(fields) > 0 {
			// The focused row carries a "▌" prefix; the label is the first word after it.
			if fields[0] == label || (len(fields) > 1 && fields[0] == "▌" && fields[1] == label) {
				return i
			}
		}
	}
	t.Fatalf("launch form renders no %q field row:\n%s", label, view)
	return -1
}

// Down from the name lands on the tag, and typed runes reach it -- the field is a
// plain line editor, not a picker.
func TestLaunchForm_TagFieldTakesTypedText(t *testing.T) {
	m := openLaunch(t, newFakeClient())
	m = send(m, keyDown) // directory -> name
	m = send(m, keyDown) // name -> tag
	if !launchOf(m).isTag() {
		t.Fatalf("two Downs from directory must focus the tag field; focus=%d", launchOf(m).focus)
	}
	m = sendType(m, "release")
	if got := launchOf(m).tag.text; got != "release" {
		t.Fatalf("typed tag = %q, want release", got)
	}
	m = send(m, keyBackspace)
	if got := launchOf(m).tag.text; got != "releas" {
		t.Fatalf("after backspace tag = %q, want releas", got)
	}
	// The next Down still reaches the agent picker: the new field displaced
	// nothing, it was inserted.
	m = send(m, keyDown)
	if !launchOf(m).isAgent() {
		t.Fatalf("Down from tag must focus the agent picker; focus=%d", launchOf(m).focus)
	}
}

// A typed tag travels on the launch request itself, so the session is tagged the
// moment it exists.
func TestLaunchSubmit_CarriesTheTypedTag(t *testing.T) {
	f := newFakeClient()
	m := openLaunch(t, f)

	dir := t.TempDir()
	for launchOf(m).cwd.text != "" {
		m = send(m, keyBackspace)
	}
	m = sendType(m, dir)
	m = send(m, keyDown) // directory -> name
	m = send(m, keyDown) // name -> tag
	m = sendType(m, "  release  ")

	_, cmd := m.Update(keyEnter)
	execCmd(cmd)

	reqs := f.launchReqs()
	if len(reqs) != 1 {
		t.Fatalf("expected exactly one launch, got %d", len(reqs))
	}
	// Trimmed at submit, like every other typed label: the surrounding spaces are
	// a typing artefact, never part of the group name the board renders.
	if reqs[0].Tag != "release" {
		t.Fatalf("submitted tag = %q, want release", reqs[0].Tag)
	}
	if want := "claude-" + filepath.Base(dir); reqs[0].Name != want {
		t.Fatalf("the tag disturbed the default name: %q, want %q", reqs[0].Name, want)
	}
}

// An untouched tag field is exactly today's launch: the request carries no tag,
// and the session is untagged.
func TestLaunchSubmit_EmptyTagIsOmitted(t *testing.T) {
	f := newFakeClient()
	m := openLaunch(t, f)

	dir := t.TempDir()
	for launchOf(m).cwd.text != "" {
		m = send(m, keyBackspace)
	}
	m = sendType(m, dir)
	m = send(m, keyDown)
	m = send(m, keyDown)
	m = sendType(m, "   ") // whitespace only is still no tag

	_, cmd := m.Update(keyEnter)
	execCmd(cmd)

	reqs := f.launchReqs()
	if len(reqs) != 1 {
		t.Fatalf("expected exactly one launch, got %d", len(reqs))
	}
	if reqs[0].Tag != "" {
		t.Fatalf("an untyped tag was submitted as %q, want the empty tag", reqs[0].Tag)
	}
}

// A background re-detection reshuffles the option schema under the form. The
// focus must re-anchor on the SEMANTIC field, so a user mid-tag keeps typing
// into the tag rather than into whatever now sits at that index.
func TestLaunchForm_RedetectionKeepsTagFocus(t *testing.T) {
	m := openLaunch(t, newFakeClient())
	m = send(m, keyDown)
	m = send(m, keyDown)
	if !launchOf(m).isTag() {
		t.Fatalf("setup: focus=%d, want the tag field", launchOf(m).focus)
	}
	lm := launchOf(m)
	lm.refreshAgents(detectMixed()())
	if !lm.isTag() {
		t.Fatalf("re-detection moved focus off the tag field; focus=%d", lm.focus)
	}
}

// Paste reaches the tag the way it reaches every other typed field.
func TestLaunchForm_TagAcceptsPaste(t *testing.T) {
	m := openLaunch(t, newFakeClient())
	m = send(m, keyDown)
	m = send(m, keyDown)
	lm := launchOf(m)
	lm.paste("release\ncandidate")
	if got := lm.tag.text; got != "releasecandidate" {
		t.Fatalf("pasted tag = %q, want releasecandidate (newline stripped)", got)
	}
}
