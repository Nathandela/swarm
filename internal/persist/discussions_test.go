package persist

import (
	"reflect"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/status"
)

func TestProjectDiscussions(t *testing.T) {
	row := func(id, parent string, n int, running bool) Meta {
		m := Meta{ID: id, AgentType: "codex", Name: "same name", ResumedFrom: parent, CreatedAt: time.Unix(int64(n), 0)}
		m.Status.Process = status.ProcessExited
		if running {
			m.Status.Process = status.ProcessRunning
		}
		return m
	}
	for _, tc := range []struct {
		name   string
		metas  []Meta
		want   []string
		hidden map[string][]string
	}{
		{"retry chain", []Meta{row("a", "", 1, false), row("b", "a", 2, false), row("c", "b", 3, false)}, []string{"c"}, map[string][]string{"c": {"a", "b"}}},
		{"missing parent siblings", []Meta{row("b", "a", 2, false), row("c", "a", 3, false)}, []string{"c"}, map[string][]string{"c": {"a", "b"}}},
		{"running wins failed retry", []Meta{row("a", "", 1, true), row("b", "a", 2, false)}, []string{"a"}, map[string][]string{"a": {"b"}}},
		{"conflicting live actors", []Meta{row("a", "", 1, false), row("b", "a", 2, true), row("c", "a", 3, true)}, []string{"b", "c"}, map[string][]string{"b": {"a"}, "c": {"a"}}},
		{"independent same name", []Meta{row("a", "", 1, false), row("b", "", 2, false)}, []string{"a", "b"}, nil},
		{"cycle terminates", []Meta{row("a", "b", 1, false), row("b", "a", 2, false)}, []string{"b"}, map[string][]string{"b": {"a"}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			before := append([]Meta(nil), tc.metas...)
			got, hidden := ProjectDiscussions(tc.metas)
			var ids []string
			for _, m := range got {
				ids = append(ids, m.ID)
			}
			if !reflect.DeepEqual(ids, tc.want) || !reflect.DeepEqual(hidden, tc.hidden) {
				t.Fatalf("got %v / %v, want %v / %v", ids, hidden, tc.want, tc.hidden)
			}
			if !reflect.DeepEqual(before, tc.metas) {
				t.Fatal("projection mutated raw history")
			}
		})
	}
	t.Run("handoff and cross-agent link remain separate", func(t *testing.T) {
		a, b := row("a", "", 1, false), row("b", "", 2, false)
		b.SpawnedFrom, b.SpawnIntent = "a", "handoff"
		c := row("c", "a", 3, false)
		c.AgentType = "claude"
		got, hidden := ProjectDiscussions([]Meta{a, b, c})
		if len(got) != 3 || len(hidden["c"]) != 0 {
			t.Fatalf("unrelated rows collapsed: %v / %v", got, hidden)
		}
	})
}

func TestProjectDiscussionsArchivedHistoryDoesNotResurrect(t *testing.T) {
	a := Meta{ID: "a", AgentType: "codex", RosterHidden: true, Status: status.Status{Process: status.ProcessExited}}
	b := Meta{ID: "b", AgentType: "codex", ResumedFrom: "a", CreatedAt: time.Unix(2, 0), Status: status.Status{Process: status.ProcessExited}}
	visible, _ := ProjectDiscussions([]Meta{a}) // representative b was explicitly deleted
	if len(visible) != 0 {
		t.Fatal("archived history resurfaced")
	}
	visible, _ = ProjectDiscussions([]Meta{a, b}) // explicit resume from retained history
	if len(visible) != 1 || visible[0].ID != "b" {
		t.Fatal("archived source hid its new resume")
	}
	a.Status.Process = status.ProcessRunning
	visible, _ = ProjectDiscussions([]Meta{a, b})
	if len(visible) != 1 || visible[0].ID != "a" {
		t.Fatal("archive hid a live actor")
	}
}

func TestProjectDiscussionsConflictingConversationIdentityStaysVisible(t *testing.T) {
	row := func(id, parent, conversation string) Meta {
		m := Meta{ID: id, AgentType: "codex", ResumedFrom: parent, ConversationID: conversation, Status: status.Status{Process: status.ProcessExited}}
		if conversation != "" {
			m.CreatedAt = time.Unix(1, 0)
		}
		return m
	}
	for _, tc := range []struct {
		name string
		rows []Meta
	}{
		{"known parent", []Meta{row("a", "", "original"), row("b", "a", "wrong")}},
		{"missing parent siblings", []Meta{row("a", "missing", "original"), row("b", "missing", "wrong")}},
		{"empty parent cannot bridge", []Meta{row("parent", "", ""), row("a", "parent", "original"), row("b", "parent", "wrong")}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for reverse := 0; reverse < 2; reverse++ {
				visible, hidden := ProjectDiscussions(tc.rows)
				seen := map[string]bool{}
				for _, m := range visible {
					seen[m.ConversationID] = true
				}
				if !seen["original"] || !seen["wrong"] {
					t.Fatalf("conflicting conversation hidden: %+v / %v", visible, hidden)
				}
				for i, j := 0, len(tc.rows)-1; i < j; i, j = i+1, j-1 {
					tc.rows[i], tc.rows[j] = tc.rows[j], tc.rows[i]
				}
			}
		})
	}
}
