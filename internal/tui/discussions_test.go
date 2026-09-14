package tui

import (
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/protocol"
)

func TestResumeProjectionFollowsSelectionAndKeepsLateEventsHidden(t *testing.T) {
	a := sCompleted("endpoint/a", "codex", "/repo", "", time.Minute)
	other := sCompleted("endpoint/other", "codex", "/repo", "", time.Hour)
	m := newGeneralModel([]protocol.SessionView{a, other})
	m.restoreSel(a.ID)
	b := sCompleted("endpoint/b", "codex", "/repo", "failed retry", 0)
	b.Supersedes = []string{a.ID}
	a.SupersededBy = b.ID
	m.apply(a) // source event can precede the replacement event
	if len(m.sessions) != 2 || m.selectedID() != a.ID {
		t.Fatal("source disappeared before replacement arrived")
	}
	m.apply(b)
	if len(m.sessions) != 2 || m.selectedID() != b.ID {
		t.Fatalf("replacement duplicated/moved selection: %+v", m.sessions)
	}
	a.SupersededBy = b.ID
	m.apply(a)
	if len(m.sessions) != 2 || m.selectedID() != b.ID {
		t.Fatal("late source event reappeared")
	}
	if m.isTombstoned(a.ID) {
		t.Fatal("projection tombstoned history")
	}
	c := b
	c.ID = "endpoint/c"
	c.Supersedes = []string{a.ID, b.ID}
	m.apply(c)
	if len(m.sessions) != 2 || m.selectedID() != c.ID {
		t.Fatal("repeated failure multiplied rows")
	}
}

func TestDiscussionRosterFiltersArchiveAndInitialDuplicates(t *testing.T) {
	a := sCompleted("endpoint/a", "codex", "/repo", "", time.Minute)
	b := a
	b.ID = "endpoint/b"
	a.SupersededBy = b.ID
	archived := a
	archived.ID = "endpoint/archived"
	archived.SupersededBy = ""
	archived.RosterHidden = true
	m := newGeneralModel([]protocol.SessionView{a, b, archived})
	if len(m.sessions) != 1 || m.sessions[0].ID != b.ID {
		t.Fatal("initial roster included history")
	}
	m.apply(archived)
	if len(m.sessions) != 1 {
		t.Fatal("archive event resurrected history")
	}
}
