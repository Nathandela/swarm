package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/protocol"
	"github.com/Nathandela/swarm/internal/status"
)

func layoutSession(id, name, cwd, tag string, created, activity, entered time.Time) protocol.SessionView {
	s := sWorking("endpoint/"+id, "claude", cwd, id, time.Minute)
	s.Name = name
	s.Tag = tag
	s.CreatedAt = created
	s.LastActivity = activity
	s.GroupEnteredAt = entered
	return s
}

func displayedIDs(m generalModel) []string {
	var ids []string
	for _, section := range m.sectionOrder() {
		for _, s := range m.sessions {
			if m.groupKey(s) == section {
				ids = append(ids, s.ID)
			}
		}
	}
	return ids
}

func TestSessionLayoutGroupingModesAndSelectionIdentity(t *testing.T) {
	base := time.Date(2026, 8, 27, 10, 0, 0, 0, time.UTC)
	m := newGeneralModel([]protocol.SessionView{
		layoutSession("b", "Beta", "/code/repo-b", "backend", base, base.Add(time.Hour), base.Add(2*time.Hour)),
		layoutSession("a", "Alpha", "/code/repo-a", "frontend", base.Add(time.Minute), base.Add(2*time.Hour), base.Add(time.Hour)),
		layoutSession("c", "Gamma", "/code/repo-a", "", base.Add(2*time.Minute), base.Add(3*time.Hour), base.Add(3*time.Hour)),
	})

	// Default behavior stays status + newest category arrival.
	if got := strings.Join(displayedIDs(m), ","); got != "endpoint/c,endpoint/b,endpoint/a" {
		t.Fatalf("default display order = %s", got)
	}
	m.sel = 1 // select b
	m.setLayout(groupByRepo, m.ordering)
	if m.grouping != groupByRepo || strings.Join(m.sections, ",") != "repo-a,repo-b" {
		t.Fatalf("repo grouping = mode %v sections %v", m.grouping, m.sections)
	}
	if got := m.selectedID(); got != "endpoint/b" {
		t.Fatalf("repo regroup moved selection identity to %q", got)
	}

	m.setLayout(groupByTag, m.ordering)
	if m.grouping != groupByTag || strings.Join(m.sections, ",") != "backend,frontend,(untagged)" {
		t.Fatalf("tag grouping = mode %v sections %v", m.grouping, m.sections)
	}
	if got := m.selectedID(); got != "endpoint/b" {
		t.Fatalf("tag regroup moved selection identity to %q", got)
	}
}

func TestSessionLayoutOrderingModes(t *testing.T) {
	base := time.Date(2026, 8, 27, 10, 0, 0, 0, time.UTC)
	m := newGeneralModel([]protocol.SessionView{
		layoutSession("a", "Zulu", "/code/r", "", base, base.Add(3*time.Hour), base.Add(time.Hour)),
		layoutSession("b", "Alpha", "/code/r", "", base.Add(2*time.Hour), base.Add(time.Hour), base.Add(3*time.Hour)),
	})

	checks := []struct {
		mode orderingMode
		want string
	}{
		{orderByArrival, "endpoint/b,endpoint/a"},
		{orderByActivity, "endpoint/a,endpoint/b"},
		{orderByCreated, "endpoint/b,endpoint/a"},
		{orderByName, "endpoint/b,endpoint/a"},
	}
	for _, tc := range checks {
		m.ordering = tc.mode
		m.refreshLayout("")
		if got := strings.Join(displayedIDs(m), ","); got != tc.want {
			t.Errorf("ordering %v = %s, want %s", tc.mode, got, tc.want)
		}
	}
}

func TestSessionTagEditIsBehavioral(t *testing.T) {
	s := sWorking("endpoint/a", "claude", "/code/repo", "working", time.Minute)
	f := newFakeClient(s)
	m := newModel(t, f, detectMixed())

	m = send(m, keyRune('t'))
	m = sendType(m, "urgent")
	updated, cmd := m.Update(keyEnter)
	msg := cmd()
	if _, ok := msg.(tagDoneMsg); !ok {
		t.Fatalf("tag submit command returned %T", msg)
	}
	m, _ = updated.Update(msg)
	if calls := f.taggedCalls(); len(calls) != 1 || calls[0] != (tagCall{id: s.ID, tag: "urgent"}) {
		t.Fatalf("SetTag calls = %+v", calls)
	}
	if tagged, _ := m.(rootModel).general.sessionByID(s.ID); tagged.Tag != "urgent" {
		t.Fatalf("optimistic tag = %q, want urgent", tagged.Tag)
	}
}

func TestSessionLayoutRemoveDropsEmptySection(t *testing.T) {
	m := newGeneralModel([]protocol.SessionView{
		sWorking("endpoint/w", "claude", "/code/w", "working", time.Minute),
		sCompleted("endpoint/c", "codex", "/code/c", "exit 0", time.Minute),
	})
	m.remove("endpoint/c")
	if got := stripANSI(m.view()); strings.Contains(got, "COMPLETED") {
		t.Fatalf("empty COMPLETED section still rendered after remove:\n%s", got)
	}
	m.remove("endpoint/w")
	if got := stripANSI(m.view()); strings.Contains(got, "WORKING") {
		t.Fatalf("empty WORKING section still rendered on an empty board:\n%s", got)
	}
}

// grouped builds a session in a fixed display group, carrying the repo/tag cell
// metadata and a name so the ordering key inside a status block is observable. It
// goes through the per-group builders so every fixture carries a raw status that
// status.Derive really produces for that group.
func grouped(id, name, cwd, tag string, g status.Group, created time.Time) protocol.SessionView {
	build := sWorking
	switch g {
	case status.GroupNeedsInput:
		build = sNeedsInput
	case status.GroupReadyForReview:
		build = sReview
	case status.GroupCompleted:
		build = sCompleted
	}
	s := build("endpoint/"+id, "claude", cwd, id, 0)
	s.Name = name
	s.Tag = tag
	s.CreatedAt = created
	s.LastActivity = created
	s.GroupEnteredAt = created
	return s
}

// Inside a repo or tag cell, rows are blocked by status in the same fixed order the
// status sections use (needs input, working, ready for review, completed), and the
// chosen ordering stays the key WITHIN each block.
func TestSessionLayoutSubOrdersCellsByStatus(t *testing.T) {
	base := time.Date(2026, 8, 27, 10, 0, 0, 0, time.UTC)
	sessions := []protocol.SessionView{
		grouped("done", "Delta", "/code/repo-a", "backend", status.GroupCompleted, base.Add(4*time.Hour)),
		grouped("work-z", "Zulu", "/code/repo-a", "backend", status.GroupWorking, base.Add(3*time.Hour)),
		grouped("review", "Charlie", "/code/repo-a", "backend", status.GroupReadyForReview, base.Add(2*time.Hour)),
		grouped("work-b", "Bravo", "/code/repo-a", "backend", status.GroupWorking, base.Add(time.Hour)),
		grouped("needs", "Alpha", "/code/repo-a", "backend", status.GroupNeedsInput, base),
	}

	for _, grouping := range []groupingMode{groupByRepo, groupByTag} {
		m := newGeneralModel(sessions)
		m.setLayout(grouping, orderByName)
		want := "endpoint/needs,endpoint/work-b,endpoint/work-z,endpoint/review,endpoint/done"
		if got := strings.Join(displayedIDs(m), ","); got != want {
			t.Errorf("grouping %v order name = %s, want %s", grouping, got, want)
		}

		m.setLayout(grouping, orderByCreated)
		want = "endpoint/needs,endpoint/work-z,endpoint/work-b,endpoint/review,endpoint/done"
		if got := strings.Join(displayedIDs(m), ","); got != want {
			t.Errorf("grouping %v order created = %s, want %s", grouping, got, want)
		}
	}
}

// Making status the first sort key must not disturb status grouping: its sections are
// already partitioned by status, so within a section the chosen ordering still decides.
// This pins the equivalence that lets the comparator apply the rank unconditionally.
func TestSessionLayoutStatusGroupingKeepsChosenOrder(t *testing.T) {
	base := time.Date(2026, 8, 27, 10, 0, 0, 0, time.UTC)
	m := newGeneralModel([]protocol.SessionView{
		grouped("work-z", "Zulu", "/code/repo-a", "", status.GroupWorking, base.Add(3*time.Hour)),
		grouped("work-b", "Bravo", "/code/repo-a", "", status.GroupWorking, base.Add(time.Hour)),
		grouped("needs", "Alpha", "/code/repo-a", "", status.GroupNeedsInput, base),
	})
	m.setLayout(groupByStatus, orderByCreated)
	want := "endpoint/needs,endpoint/work-z,endpoint/work-b"
	if got := strings.Join(displayedIDs(m), ","); got != want {
		t.Errorf("status grouping order created = %s, want %s", got, want)
	}
}
