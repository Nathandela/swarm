package daemon

// A launch that carries a tag is tagged from its very first persisted meta: the
// reservation record already holds it, so the session is never briefly untagged
// and a crash between reservation and spawn cannot lose the label.

import (
	"errors"
	"path/filepath"
	"testing"

	"github.com/Nathandela/swarm/internal/persist"
)

// TestLaunch_TaggedSessionIsTaggedInTheRoster is the whole chain: a completed
// launch carrying a tag yields a registry entry and a persisted meta that already
// hold it, with no set_tag anywhere.
func TestLaunch_TaggedSessionIsTaggedInTheRoster(t *testing.T) {
	d := openDaemon(t, daemonConfig(t))

	spec := announceSpec(t, filepath.Join(t.TempDir(), "agent.pid"))
	spec.Tag = "release"
	m, err := d.Launch(spec)
	if err != nil {
		t.Fatalf("Launch: %v", err)
	}
	t.Cleanup(func() { _ = d.Kill(m.ID) })

	if m.Tag != "release" {
		t.Fatalf("returned meta tag = %q, want release", m.Tag)
	}
	if got, ok := d.Get(m.ID); !ok || got.Tag != "release" {
		t.Fatalf("registry meta = %+v, want a release tag", got)
	}
	if disk, err := d.store.Load(m.ID); err != nil || disk.Tag != "release" {
		t.Fatalf("persisted tag = %q, err = %v", disk.Tag, err)
	}
}

func TestLaunch_TagStampedIntoTheReservation(t *testing.T) {
	d := openDaemon(t, daemonConfig(t))

	spec := announceSpec(t, filepath.Join(t.TempDir(), "agent.pid"))
	spec.Tag = "release"

	var reserved persist.Meta
	probe := func(phase launchPhase, m persist.Meta) error {
		if phase == phaseReserved {
			reserved = m
			return errInjectedCrash // stop before anything is spawned
		}
		return nil
	}
	if _, err := d.launch(spec, probe); !errors.Is(err, errInjectedCrash) {
		t.Fatalf("launch error = %v; want the injected crash", err)
	}
	if reserved.Tag != "release" {
		t.Fatalf("reserved meta tag = %q, want release", reserved.Tag)
	}
	disk, err := d.store.Load(reserved.ID)
	if err != nil {
		t.Fatalf("load reserved meta: %v", err)
	}
	if disk.Tag != "release" {
		t.Fatalf("persisted reservation tag = %q, want release", disk.Tag)
	}
}
