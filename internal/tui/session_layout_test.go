package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/protocol"
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
