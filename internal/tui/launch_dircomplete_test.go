package tui

// Directory autocomplete in the new-session launch form. The directory field is
// free text (L-1); typing a path prefix must offer the matching sibling
// directories as a ghost completion plus an inline candidate menu, with the
// arrows accepting the ghost (drill-down) or cycling the menu.
//
// FAILING TESTS ONLY — these are written against the completion state the
// implementer must add to launchModel:
//
//	dirCands  []string  // candidate basenames, ReadDir (sorted) order
//	dirGhost  string    // the completion remainder rendered after the cursor
//	dirParent string    // the TYPED parent prefix, trailing "/" included (never expanded)
//
// The state is recomputed only when the directory field text is EDITED while
// focused (rune, backspace, paste) — never in newLaunchModel, so a freshly
// opened form renders exactly as before.

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

// dirFixture builds the tree the completion tests share:
//
//	alpha/  alphabet/  beta/  beta/inner/  .hidden/  afile.txt
//
// "alpha" is a strict prefix of "alphabet" (common-prefix ghost and menu
// cycling), ".hidden" is a hidden directory, and afile.txt is a plain file.
func dirFixture(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	for _, d := range []string{"alpha", "alphabet", "beta/inner", ".hidden"} {
		if err := os.MkdirAll(filepath.Join(root, d), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", d, err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "afile.txt"), nil, 0o644); err != nil {
		t.Fatalf("write afile.txt: %v", err)
	}
	return root
}

// openDirField opens the launch form and clears the prefilled client cwd (ADR-006),
// leaving the empty directory field focused and ready for a typed path.
func openDirField(t *testing.T) tea.Model {
	t.Helper()
	m := openLaunch(t, newFakeClient())
	for launchOf(m).cwd.text != "" {
		m = send(m, keyBackspace)
	}
	return m
}

// rowUnder returns the rendered line directly below the first line containing sub.
func rowUnder(t *testing.T, v, sub string) string {
	t.Helper()
	lines := strings.Split(v, "\n")
	for i, ln := range lines {
		if strings.Contains(ln, sub) && i+1 < len(lines) {
			return lines[i+1]
		}
	}
	t.Fatalf("no line containing %q (with a line under it) in:\n%s", sub, v)
	return ""
}

// A freshly opened form carries NO completion state: the computation is driven by
// edits only, so opening the form never touches the filesystem and the first paint
// is unchanged (the launch-form golden must stay valid).
func TestDirCompleteFreshFormCarriesNoCompletionState(t *testing.T) {
	m := openLaunch(t, newFakeClient()) // prefilled cwd, directory field focused
	lm := launchOf(m)
	if len(lm.dirCands) != 0 || lm.dirGhost != "" || lm.dirParent != "" {
		t.Fatalf("a fresh form must carry no completion state; cands=%v ghost=%q parent=%q",
			lm.dirCands, lm.dirGhost, lm.dirParent)
	}
	// No candidate menu row: the line under the directory field is still the name field.
	if row := rowUnder(t, view(m), "directory"); !strings.Contains(row, "name") {
		t.Fatalf("a fresh form must not render a candidate row under the directory field, got %q", row)
	}
}

// A single matching directory ghosts the rest of its name plus a trailing "/", so
// accepting it drills straight into that directory. Delivered by PASTE here, which
// must recompute exactly like typing does.
func TestDirCompleteGhostUniqueMatch(t *testing.T) {
	dir := dirFixture(t)
	m := openDirField(t)
	m = send(m, tea.PasteMsg{Content: dir + "/bet"})

	lm := launchOf(m)
	if got := strings.Join(lm.dirCands, ","); got != "beta" {
		t.Fatalf("candidates for %q = [%s], want [beta]", dir+"/bet", got)
	}
	if lm.dirGhost != "a/" {
		t.Fatalf("ghost = %q, want %q (name remainder + trailing slash)", lm.dirGhost, "a/")
	}
	if lm.dirParent != dir+"/" {
		t.Fatalf("parent = %q, want %q (typed form, trailing slash included)", lm.dirParent, dir+"/")
	}

	// A fully typed name still ghosts the separator, so one more Right descends.
	m = sendType(m, "a")
	if g := launchOf(m).dirGhost; g != "/" {
		t.Fatalf("ghost for a fully typed name = %q, want %q", g, "/")
	}

	// No match: no candidates, no ghost.
	m = sendType(m, "zzz")
	lm = launchOf(m)
	if len(lm.dirCands) != 0 || lm.dirGhost != "" {
		t.Fatalf("an unmatched prefix must yield no completion; cands=%v ghost=%q", lm.dirCands, lm.dirGhost)
	}
}

// Several matches ghost only their LONGEST COMMON PREFIX remainder (no trailing
// slash — the name is still ambiguous). Backspace re-drives the computation.
func TestDirCompleteGhostCommonPrefixAndBackspaceRecompute(t *testing.T) {
	dir := dirFixture(t)
	m := openDirField(t)
	m = sendType(m, dir+"/alphab") // unique: alphabet

	if g := launchOf(m).dirGhost; g != "et/" {
		t.Fatalf("ghost for %q = %q, want %q", dir+"/alphab", g, "et/")
	}

	m = send(m, keyBackspace) // back to ".../alpha": alpha AND alphabet match
	lm := launchOf(m)
	if got := strings.Join(lm.dirCands, ","); got != "alpha,alphabet" {
		t.Fatalf("candidates after backspace = [%s], want [alpha,alphabet]", got)
	}
	if lm.dirGhost != "" {
		t.Fatalf("ghost = %q, want empty (common prefix %q is fully typed)", lm.dirGhost, "alpha")
	}

	m = send(m, keyBackspace) // ".../alph": common prefix is still "alpha"
	if g := launchOf(m).dirGhost; g != "a" {
		t.Fatalf("ghost for %q = %q, want %q (common-prefix remainder, no slash)", dir+"/alph", g, "a")
	}
}

// Candidates are DIRECTORIES only, and dotfiles stay hidden until the typed base
// itself starts with a dot.
func TestDirCompleteSkipsFilesAndHiddenEntries(t *testing.T) {
	dir := dirFixture(t)
	m := openDirField(t)
	m = sendType(m, dir+"/") // empty base: every visible directory is a candidate

	if got := strings.Join(launchOf(m).dirCands, ","); got != "alpha,alphabet,beta" {
		t.Fatalf("candidates for an empty base = [%s], want [alpha,alphabet,beta] "+
			"(afile.txt is a file, .hidden is a dotfile)", got)
	}

	m = sendType(m, ".") // an explicit dot opts into the hidden entries
	lm := launchOf(m)
	if got := strings.Join(lm.dirCands, ","); got != ".hidden" {
		t.Fatalf("candidates for base %q = [%s], want [.hidden]", ".", got)
	}
	if lm.dirGhost != "hidden/" {
		t.Fatalf("ghost = %q, want %q", lm.dirGhost, "hidden/")
	}
}

// Text with no "/" has no parent to read: no completion state, no crash.
func TestDirCompleteIgnoresTextWithoutSlash(t *testing.T) {
	m := openDirField(t)
	m = sendType(m, "relative")

	lm := launchOf(m)
	if len(lm.dirCands) != 0 || lm.dirGhost != "" || lm.dirParent != "" {
		t.Fatalf("a slashless path must yield no completion state; cands=%v ghost=%q parent=%q",
			lm.dirCands, lm.dirGhost, lm.dirParent)
	}
	if v := view(m); !strings.Contains(v, "relative") {
		t.Fatalf("the typed text must still render:\n%s", v)
	}
}

// Right ACCEPTS a non-empty ghost — appending it to the text and recomputing
// against the directory just entered (drill-down) — and clears the stale inline
// error left by a refused submit.
func TestDirCompleteRightAcceptsGhostAndDrillsDown(t *testing.T) {
	dir := dirFixture(t)
	m := openDirField(t)
	m = sendType(m, dir+"/bet") // ghost "a/"

	m = send(m, keyEnter) // ".../bet" does not exist: refused inline, no launch
	if launchOf(m).errMsg == "" {
		t.Fatalf("a partial path must be refused inline before the completion is accepted")
	}

	m = send(m, keyRight)
	lm := launchOf(m)
	if lm.cwd.text != dir+"/beta/" {
		t.Fatalf("Right must append the ghost; cwd = %q, want %q", lm.cwd.text, dir+"/beta/")
	}
	if lm.dirGhost != "inner/" {
		t.Fatalf("accepting a ghost must recompute one level down; ghost = %q, want %q", lm.dirGhost, "inner/")
	}
	if lm.errMsg != "" {
		t.Fatalf("a completion keypress must clear the inline error, got %q", lm.errMsg)
	}
}

// Tab has shell-like semantics in the directory field: it accepts the same ghost
// as Right and remains on the directory field rather than navigating the form.
func TestDirCompleteTabAcceptsGhostWithoutChangingFocus(t *testing.T) {
	dir := dirFixture(t)
	m := openDirField(t)
	m = sendType(m, dir+"/bet") // ghost "a/"

	m = send(m, keyTab)
	lm := launchOf(m)
	if lm.cwd.text != dir+"/beta/" {
		t.Fatalf("Tab must append the path ghost; cwd = %q, want %q", lm.cwd.text, dir+"/beta/")
	}
	if !lm.isDir() {
		t.Fatalf("Tab path completion changed focus to %d; want directory field", lm.focus)
	}
	if lm.dirGhost != "inner/" {
		t.Fatalf("Tab completion must drill down and recompute; ghost = %q, want inner/", lm.dirGhost)
	}
}

// Outside the directory field Tab is inert. Up/Down are the only field-navigation
// keys, avoiding accidental option changes when muscle memory asks for completion.
func TestLaunchTabDoesNotNavigateNonDirectoryFields(t *testing.T) {
	m := openLaunch(t, newFakeClient())
	m = send(m, keyDown) // directory -> name
	before := launchOf(m).focus
	m = send(m, keyTab)
	if got := launchOf(m).focus; got != before {
		t.Fatalf("Tab on name changed focus from %d to %d", before, got)
	}
}

// With the ghost exhausted and several candidates left, Right/Left CYCLE the typed
// text through the candidate full paths. The menu stays anchored to the prefix that
// produced it: cycling never re-reads the directory, so the alternatives survive.
func TestDirCompleteArrowsCycleAnchoredCandidates(t *testing.T) {
	dir := dirFixture(t)
	m := openDirField(t)
	m = sendType(m, dir+"/alpha") // alpha + alphabet, ghost exhausted

	if g := launchOf(m).dirGhost; g != "" {
		t.Fatalf("precondition: ghost must be empty, got %q", g)
	}

	m = send(m, keyRight) // current text IS a candidate: step to the next one
	lm := launchOf(m)
	if lm.cwd.text != dir+"/alphabet" {
		t.Fatalf("Right must cycle to the next candidate; cwd = %q, want %q", lm.cwd.text, dir+"/alphabet")
	}
	if got := strings.Join(lm.dirCands, ","); got != "alpha,alphabet" {
		t.Fatalf("cycling must not recompute the candidate set; cands = [%s], want [alpha,alphabet]", got)
	}

	m = send(m, keyRight) // wraps back to the first candidate
	if got := launchOf(m).cwd.text; got != dir+"/alpha" {
		t.Fatalf("Right past the last candidate must wrap; cwd = %q, want %q", got, dir+"/alpha")
	}

	m = send(m, keyLeft) // backward through the same anchored menu
	if got := launchOf(m).cwd.text; got != dir+"/alphabet" {
		t.Fatalf("Left must cycle backward; cwd = %q, want %q", got, dir+"/alphabet")
	}
}

// The ghost renders dimmed after the cursor, and an ambiguous prefix adds ONE
// candidate row directly under the directory field, aligned with the value column.
// The row belongs to the focused field: it goes away on blur, and a lone candidate
// (already fully expressed by the ghost) never earns one.
func TestDirCompleteCandidateRowRendersUnderDirectory(t *testing.T) {
	dir := dirFixture(t)
	m := openDirField(t)
	m = sendType(m, dir+"/alph")

	v := view(m)
	if want := dir + "/alph" + "█" + "a"; !strings.Contains(v, want) {
		t.Fatalf("the focused directory value must render as text + cursor + ghost (%q):\n%s", want, v)
	}
	row := rowUnder(t, v, "directory")
	if indent := strings.Repeat(" ", 2+launchLabelW); !strings.HasPrefix(row, indent) {
		t.Fatalf("the candidate row must be indented to the value column (%d cells), got %q", 2+launchLabelW, row)
	}
	if got := strings.TrimSpace(row); got != "alpha  alphabet" {
		t.Fatalf("candidate row = %q, want %q (basenames joined by two spaces)", got, "alpha  alphabet")
	}

	// Blur: the row belongs to the focused directory field.
	blurred := send(m, keyDown)
	if got := rowUnder(t, view(blurred), "directory"); !strings.Contains(got, "name") {
		t.Fatalf("the candidate row must disappear when the directory field blurs, got %q", got)
	}

	// One candidate: the ghost says it all, so no menu row.
	m2 := openDirField(t)
	m2 = sendType(m2, dir+"/bet")
	if got := rowUnder(t, view(m2), "directory"); !strings.Contains(got, "name") {
		t.Fatalf("a single candidate must not render a menu row, got %q", got)
	}
}

// A crowded menu must not wrap the form: the candidate row clamps to the width left
// after its indent (the agent row's item-5 lesson, applied to the new row).
func TestDirCompleteCandidateRowClampsToWidth(t *testing.T) {
	root := t.TempDir()
	for i := 0; i < 12; i++ {
		if err := os.Mkdir(filepath.Join(root, fmt.Sprintf("verylongdirectoryname%02d", i)), 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
	}
	const narrow = 60
	m := openDirField(t)
	m = send(m, tea.WindowSizeMsg{Width: narrow, Height: testRows})
	m = sendType(m, root+"/verylong")

	if n := len(launchOf(m).dirCands); n != 12 {
		t.Fatalf("precondition: expected 12 candidates, got %d", n)
	}
	row := rowUnder(t, view(m), "directory")
	if w := lipgloss.Width(row); w > narrow {
		t.Fatalf("candidate row width = %d, must clamp to the form width %d; row:\n%q", w, narrow, row)
	}
}

// The footer teaches the arrows exactly when they DO something on the directory
// field, and is otherwise untouched.
func TestDirCompleteHintAdvertisesCompletionKeys(t *testing.T) {
	const tail = " · ↑↓ next · enter launch · esc cancel"

	m := openLaunch(t, newFakeClient()) // prefilled cwd, no completion computed yet
	if got := launchOf(m).hint(); got != "type or paste · "+lineEditFastHint+tail {
		t.Fatalf("hint without completion = %q, want %q", got, "type or paste · "+lineEditFastHint+tail)
	}

	dir := dirFixture(t)
	m = openDirField(t)
	m = sendType(m, dir+"/alph") // ghost "a" AND two candidates
	if got := launchOf(m).hint(); got != "tab/←→ complete · "+lineEditFastHint+tail {
		t.Fatalf("hint with a completion = %q, want %q", got, "tab/←→ complete · "+lineEditFastHint+tail)
	}

	m = send(m, keyDown) // off the directory field: completion keys mean nothing again
	if got := launchOf(m).hint(); got == "tab/←→ complete · "+lineEditFastHint+tail {
		t.Fatalf("the completion hint must not survive a blur, got %q", got)
	}
}

// A "~/" path completes against the home directory but the TYPED text keeps its
// tilde: the expansion is a read-side detail (submit expands), never written back
// into the field by the ghost or the menu.
func TestDirCompleteTildePathKeepsTypedForm(t *testing.T) {
	root := dirFixture(t)
	t.Setenv("HOME", root) // userHome() resolves via os.UserHomeDir -> $HOME

	m := openDirField(t)
	m = sendType(m, "~/bet")

	lm := launchOf(m)
	if lm.dirParent != "~/" {
		t.Fatalf("parent = %q, want %q (the typed form, unexpanded)", lm.dirParent, "~/")
	}
	if lm.dirGhost != "a/" {
		t.Fatalf("ghost = %q, want %q (completed against the expanded home)", lm.dirGhost, "a/")
	}

	m = send(m, keyRight)
	if got := launchOf(m).cwd.text; got != "~/beta/" {
		t.Fatalf("accepting a ghost must keep the tilde; cwd = %q, want %q", got, "~/beta/")
	}
}
