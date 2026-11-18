package daemon

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// NameSetAt is the newest-wins clock for a session's label (ADR-022): a CLI-published
// name is adopted only when it is newer than the last name swarm itself stamped.
// Rename stamps now; RenameAt stamps the moment the caller vouches for, which is how
// an adopted CLI name carries the CLI's own timestamp rather than the adoption time.

func TestRename_StampsNameSetAt(t *testing.T) {
	d := openDaemon(t, daemonConfig(t))
	const id = "s1"
	seedRunning(t, d, id, "")
	before := time.Now()
	if err := d.Rename(id, "mine"); err != nil {
		t.Fatalf("Rename: %v", err)
	}
	got, _ := d.Get(id)
	if got.NameSetAt.Before(before) || got.NameSetAt.After(time.Now()) {
		t.Fatalf("NameSetAt = %v, want the rename's own time", got.NameSetAt)
	}
	if disk := scanMetaByID(t, d, id); !disk.NameSetAt.Equal(got.NameSetAt) {
		t.Fatalf("persisted NameSetAt = %v, in-memory %v", disk.NameSetAt, got.NameSetAt)
	}
}

func TestRenameAt_StampsTheGivenTime(t *testing.T) {
	d := openDaemon(t, daemonConfig(t))
	const id = "s1"
	seedRunning(t, d, id, "")
	at := time.UnixMilli(1787894219289)
	if _, err := d.RenameAt(id, "from the cli", at); err != nil {
		t.Fatalf("RenameAt: %v", err)
	}
	got, _ := d.Get(id)
	if got.Name != "from the cli" || !got.NameSetAt.Equal(at) {
		t.Fatalf("got name %q at %v, want %q at %v", got.Name, got.NameSetAt, "from the cli", at)
	}
	if disk := scanMetaByID(t, d, id); !disk.NameSetAt.Equal(at) {
		t.Fatalf("persisted NameSetAt = %v, want %v", disk.NameSetAt, at)
	}
}

func TestRenameAt_SameNameLeavesTheStampAlone(t *testing.T) {
	d := openDaemon(t, daemonConfig(t))
	const id = "s1"
	seedRunning(t, d, id, "keep")
	first, _ := d.Get(id)
	if _, err := d.RenameAt(id, "keep", time.Now().Add(time.Hour)); err != nil {
		t.Fatalf("RenameAt: %v", err)
	}
	if got, _ := d.Get(id); !got.NameSetAt.Equal(first.NameSetAt) {
		t.Fatalf("a same-name rename moved NameSetAt from %v to %v", first.NameSetAt, got.NameSetAt)
	}
}

func TestAgyNameObservationSurvivesRestartAndRetriesStorageFailure(t *testing.T) {
	cfg := daemonConfig(t)
	d := openDaemon(t, cfg)
	const id = "agy1"
	seedRunning(t, d, id, "launch")
	m, _ := d.Get(id)
	m.AgentType = "agy"
	m.NameSetAt = m.CreatedAt
	if err := d.saveMeta(m); err != nil {
		t.Fatal(err)
	}
	adopt := func(title string, at time.Time, wantChanged bool) {
		t.Helper()
		changed, err := d.RenameAt(id, title, at)
		if err != nil || changed != wantChanged {
			t.Fatalf("adopt %q: changed=%t err=%v", title, changed, err)
		}
	}
	at := time.Now()
	adopt("native", at, true)
	if err := d.Rename(id, "mine"); err != nil {
		t.Fatal(err)
	}
	d.abandon()
	d = openDaemon(t, cfg)
	adopt("native", at.Add(time.Second), false)
	if m, _ := d.Get(id); m.Name != "mine" {
		t.Fatalf("after restart: %q", m.Name)
	}
	// Force the atomic metadata rename to fail, then retry exactly the same tick.
	path := filepath.Join(cfg.StateDir, id, "meta.json")
	if err := os.Rename(path, path+".saved"); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(path, 0700); err != nil {
		t.Fatal(err)
	}
	if changed, err := d.RenameAt(id, "next", at.Add(2*time.Second)); err == nil || changed {
		t.Fatalf("failed save: changed=%t err=%v", changed, err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(path+".saved", path); err != nil {
		t.Fatal(err)
	}
	adopt("next", at.Add(2*time.Second), true)
	adopt("older", at, false)
	if m := scanMetaByID(t, d, id); m.Name != "next" || m.AgyTitle != "next" {
		t.Fatalf("persisted name=%q native=%q", m.Name, m.AgyTitle)
	}
}

func TestAgyLegacyManualRenameBaselinesFirstObservation(t *testing.T) {
	d := openDaemon(t, daemonConfig(t))
	const id = "legacy-agy"
	seedRunning(t, d, id, "mine")
	m, _ := d.Get(id)
	m.AgentType = "agy"
	m.NameSetAt = m.CreatedAt.Add(time.Second)
	if err := d.saveMeta(m); err != nil {
		t.Fatal(err)
	}
	if changed, err := d.RenameAt(id, "native", m.NameSetAt.Add(time.Second)); err != nil || changed {
		t.Fatalf("legacy observation: changed=%t err=%v", changed, err)
	}
	if got := scanMetaByID(t, d, id); got.Name != "mine" || got.AgyTitle != "native" {
		t.Fatalf("legacy name=%q baseline=%q", got.Name, got.AgyTitle)
	}
}

func TestRenameAtRejectsStaleClaudeObservation(t *testing.T) {
	d := openDaemon(t, daemonConfig(t))
	seedRunning(t, d, "s1", "first")
	old := time.Now()
	if err := d.Rename("s1", "manual"); err != nil {
		t.Fatal(err)
	}
	if changed, err := d.RenameAt("s1", "stale", old); err != nil || changed {
		t.Fatalf("stale rename changed=%t err=%v", changed, err)
	}
	if got := scanMetaByID(t, d, "s1"); got.Name != "manual" {
		t.Fatalf("persisted name=%q", got.Name)
	}
}

func TestAgyDuplicateTickOrdersDelayedTitlesAcrossRestart(t *testing.T) {
	cfg := daemonConfig(t)
	d := openDaemon(t, cfg)
	const id = "agy-order"
	seedRunning(t, d, id, "launch")
	m, _ := d.Get(id)
	m.AgentType = "agy"
	if err := d.saveMeta(m); err != nil {
		t.Fatal(err)
	}
	// The callback timestamps come from the local hook before socket delivery.
	first := time.Now()
	delayed := first.Add(time.Nanosecond)
	repeat := delayed.Add(time.Nanosecond)
	if changed, err := d.RenameAt(id, "A", first); err != nil || !changed {
		t.Fatal(changed, err)
	}
	if changed, err := d.RenameAt(id, "A", repeat); err != nil || changed {
		t.Fatal(changed, err)
	}
	if got := scanMetaByID(t, d, id); !got.AgyTitleObservedAt.Equal(first) {
		t.Fatal("repeated sample rewrote baseline timestamp")
	}
	reject := func(title string) {
		t.Helper()
		if changed, err := d.RenameAt(id, title, delayed); err != nil || changed {
			t.Fatalf("adopted delayed title %q: changed=%t err=%v", title, changed, err)
		}
	}
	reject("B-before-restart")
	d.abandon()
	d = openDaemon(t, cfg)
	reject("B-after-restart")
	if changed, err := d.RenameAt(id, "C", time.Now()); err != nil || !changed {
		t.Fatal(changed, err)
	}
}
