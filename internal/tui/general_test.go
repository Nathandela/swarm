package tui

import (
	"strings"
	"testing"
	"time"

	teatest "github.com/charmbracelet/x/exp/teatest/v2"
)

// fullBoard returns one session per display group (the ui-preview set).
func fullBoard() *fakeClient {
	return newFakeClient(
		sNeedsInput("endpoint/s1", "claude", "~/Code/quanthome-api", "Permission: run db migration?", 12*time.Minute),
		sWorking("endpoint/s2", "codex", "~/Code/agents-tracker", "Writing adapter fixture tests", 3*time.Minute),
		sReview("endpoint/s3", "claude", "~/Code/mcp-soml", "Turn finished, review the diff", 1*time.Hour),
		sCompleted("endpoint/s4", "gemini", "~/Code/scratch", "exit 0", 2*time.Hour),
	)
}

// E7.2 / V-1 — the four groups render in fixed order: Needs input, Working,
// Ready for review, Completed.

func TestGeneral_GroupsInFixedOrder(t *testing.T) {
	m := newModel(t, fullBoard(), detectMixed())
	v := view(m)

	order := []string{"NEEDS INPUT", "WORKING", "READY FOR REVIEW", "COMPLETED"}
	prev := -1
	for _, label := range order {
		i := strings.Index(v, label)
		if i < 0 {
			t.Fatalf("missing group header %q in:\n%s", label, v)
		}
		if i <= prev {
			t.Fatalf("group header %q out of order (index %d after %d) in:\n%s", label, i, prev, v)
		}
		prev = i
	}
}

// E7.2 — a group with no sessions is omitted (matches the ui-preview look).

func TestGeneral_EmptyGroupsOmitted(t *testing.T) {
	f := newFakeClient(sWorking("endpoint/s1", "codex", "~/Code/x", "compiling", 3*time.Minute))
	m := newModel(t, f, detectMixed())
	v := view(m)

	if !strings.Contains(v, "WORKING") {
		t.Fatalf("expected WORKING header, got:\n%s", v)
	}
	for _, absent := range []string{"NEEDS INPUT", "READY FOR REVIEW", "COMPLETED"} {
		if strings.Contains(v, absent) {
			t.Fatalf("empty group %q must be omitted, got:\n%s", absent, v)
		}
	}
}

// E7.2 / V-4 — each row shows agent, shortened cwd, status, elapsed time, and the
// grid-derived summary. All five fields must appear on the row's own line.

func TestGeneral_RowShowsAllFields(t *testing.T) {
	cwd := homeDir(t) + "/Code/quanthome-api"
	f := newFakeClient(sNeedsInput("endpoint/s1", "claude", cwd, "Permission: run db migration?", 12*time.Minute))
	m := newModel(t, f, detectMixed())
	v := view(m)

	row := lineContaining(v, "Permission: run db migration?") // (5) summary locates the row
	if row == "" {
		t.Fatalf("no row carried the summary; view:\n%s", v)
	}
	if !strings.Contains(row, "claude") { // (1) agent
		t.Errorf("row missing agent name:\n%s", row)
	}
	if !strings.Contains(row, "~/Code/quanthome-api") { // (2) home-shortened cwd
		t.Errorf("row missing home-shortened cwd (~/Code/quanthome-api):\n%s", row)
	}
	if !strings.Contains(row, "needs input") { // (3) per-row status (lowercased group label)
		t.Errorf("row missing status token 'needs input':\n%s", row)
	}
	if !elapsedRe.MatchString(row) { // (4) elapsed/last-activity token
		t.Errorf("row missing elapsed-time token (e.g. 12m):\n%s", row)
	}
}

// E7.2 — the home directory is shortened to ~ in the cwd column.

func TestGeneral_CwdShortenedToTilde(t *testing.T) {
	cwd := homeDir(t) + "/Code/nathan-site"
	f := newFakeClient(sWorking("endpoint/s1", "agy", cwd, "refactoring nav", 41*time.Second))
	m := newModel(t, f, detectMixed())
	v := view(m)

	if strings.Contains(v, cwd) {
		t.Fatalf("cwd should be home-shortened, not shown absolute (%s):\n%s", cwd, v)
	}
	if !strings.Contains(v, "~/Code/nathan-site") {
		t.Fatalf("expected ~/Code/nathan-site, got:\n%s", v)
	}
}

// E7.2 — GOLDEN: the populated general view matches the approved ui-preview look.
// Regenerate with `go test ./internal/tui/ -update`; a human diffs the golden
// against docs/design/ui-preview.html (screens tab). The golden is ANSI-stripped
// and time-normalized so it is readable and wall-clock-independent.

func TestGoldenGeneralView(t *testing.T) {
	tm := startTM(t, New(fullBoard(), detectMixed()))
	waitContains(t, tm, "COMPLETED") // all four groups painted
	quitTM(t, tm)

	got := normalizeTimes(finalView(t, tm))
	teatest.RequireEqualOutput(t, []byte(got))
}
