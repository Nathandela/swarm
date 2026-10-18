package idempotency

import (
	"errors"
	"testing"
	"time"
)

func TestNativeInputOutcomeRetention(t *testing.T) {
	admitted := `{"version":1,"receipt":{"status":"admitted"}}`
	completed := `{"version":1,"receipt":{"status":"admitted"},"outcome":"completed","outcome_source":"live","outcome_at":100}`
	cases := []struct {
		name, action, request, evidence string
		retain                          bool
	}{
		{"owner admitted", "semantic_send", `{"message_id":"msg_owned"}`, admitted, true},
		{"phone admitted", "composer_send", `{"message_id":"msg_owned"}`, admitted, true},
		{"missing evidence", "semantic_send", `{}`, "", true},
		{"malformed envelope", "semantic_send", `{}`, `[]`, true},
		{"missing receipt", "semantic_send", `{}`, `{"version":1,"outcome":"completed","outcome_source":"live","outcome_at":100}`, true},
		{"unqualified source", "semantic_send", `{}`, `{"version":1,"receipt":{"status":"admitted"},"outcome":"completed","outcome_source":"guessed","outcome_at":100}`, true},
		{"invalid terminal time", "semantic_send", `{}`, `{"version":1,"receipt":{"status":"admitted"},"outcome":"completed","outcome_source":"live","outcome_at":0}`, true},
		{"unresolved compaction", "semantic_send", `{}`, `{"version":1,"receipt":{"status":"admitted"},"outcome":"compacted","outcome_source":"live","outcome_at":100}`, true},
		{"qualified owner terminal", "semantic_send", `{}`, completed, false},
		{"qualified phone terminal", "composer_send", `{}`, completed, false},
		{"generic owner delivery", "semantic_send", "", `{}`, false},
		{"generic phone delivery", "composer_send", "", `{}`, false},
		{"native decision", "native_decide", `{}`, admitted, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			now := time.Date(2026, 10, 18, 12, 0, 0, 0, time.UTC)
			opts := Options{TTL: 24 * time.Hour, MaxEntries: 1, Clock: func() time.Time { return now }}
			directory := t.TempDir()
			store, err := OpenWithOptions(directory, opts)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = store.f.Close() }()
			if _, _, err = store.PrepareBound("native", tc.action, "worker", "instance", "hash"); err != nil {
				t.Fatal(err)
			}
			if tc.request != "" {
				if err = store.SetRequest("native", []byte(tc.request)); err != nil {
					t.Fatal(err)
				}
			}
			if err = store.Begin("native"); err != nil {
				t.Fatal(err)
			}
			if tc.evidence != "" {
				if err = store.SetEvidence("native", []byte(tc.evidence)); err != nil {
					t.Fatal(err)
				}
			}
			if err = store.Complete("native", []byte(`{"ok":true}`)); err != nil {
				t.Fatal(err)
			}
			now = now.Add(25 * time.Hour)
			if err = store.Compact(); err != nil {
				t.Fatal(err)
			}
			if err = store.f.Close(); err != nil {
				t.Fatal(err)
			}
			store, err = OpenWithOptions(directory, opts)
			if err != nil {
				t.Fatal(err)
			}
			if _, retained := store.Get("native"); retained != tc.retain {
				t.Fatalf("native outcome retention=%v, want %v", retained, tc.retain)
			}
			_, _, err = store.PrepareBound("later", "composer_send", "worker", "instance", "next")
			if tc.retain && !errors.Is(err, ErrCapacity) {
				t.Fatalf("unfinished native input lost hard backpressure: %v", err)
			}
			if !tc.retain && err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestNativeInputQualifiedOutcomeStartsExpiryAfterSettlement(t *testing.T) {
	for _, outcome := range []string{"completed", "failed", "cancelled", "declined"} {
		t.Run(outcome, func(t *testing.T) {
			now := time.Date(2026, 10, 18, 12, 0, 0, 0, time.UTC)
			store, err := OpenWithOptions(t.TempDir(), Options{TTL: 24 * time.Hour, Clock: func() time.Time { return now }})
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = store.f.Close() }()
			if _, _, err = store.PrepareBound("op", "semantic_send", "worker", "instance", "hash"); err != nil {
				t.Fatal(err)
			}
			if err = store.SetRequest("op", []byte(`{"message_id":"msg_owned"}`)); err != nil {
				t.Fatal(err)
			}
			if err = store.Begin("op"); err != nil {
				t.Fatal(err)
			}
			admitted := []byte(`{"version":1,"receipt":{"status":"admitted"}}`)
			if err = store.SetEvidence("op", admitted); err != nil {
				t.Fatal(err)
			}
			if err = store.Complete("op", []byte(`{"ok":true}`)); err != nil {
				t.Fatal(err)
			}
			now = now.Add(25 * time.Hour)
			if err = store.Compact(); err != nil {
				t.Fatal(err)
			}
			final := []byte(`{"version":1,"receipt":{"status":"admitted"},"outcome":"` + outcome + `","outcome_source":"reconciled","outcome_at":100}`)
			if err = store.CompareEvidence("op", admitted, final); err != nil {
				t.Fatal(err)
			}
			if err = store.Compact(); err != nil {
				t.Fatal(err)
			}
			if _, ok := store.Get("op"); !ok {
				t.Fatal("fresh terminal checkpoint expired immediately")
			}
			now = now.Add(25 * time.Hour)
			if err = store.Compact(); err != nil {
				t.Fatal(err)
			}
			if _, ok := store.Get("op"); ok {
				t.Fatal("qualified settled input never expired")
			}
		})
	}
}
