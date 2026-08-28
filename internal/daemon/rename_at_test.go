package daemon

import (
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
	if err := d.RenameAt(id, "from the cli", at); err != nil {
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
	if err := d.RenameAt(id, "keep", time.Now().Add(time.Hour)); err != nil {
		t.Fatalf("RenameAt: %v", err)
	}
	if got, _ := d.Get(id); !got.NameSetAt.Equal(first.NameSetAt) {
		t.Fatalf("a same-name rename moved NameSetAt from %v to %v", first.NameSetAt, got.NameSetAt)
	}
}
