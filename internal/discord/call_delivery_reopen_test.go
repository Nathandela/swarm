package discord

import (
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	bolt "go.etcd.io/bbolt"
)

func TestCallDeliveryKeepsExactChunkReceiptsThroughCompactionAndReopen(t *testing.T) {
	dir := t.TempDir()
	store, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	d := TeammateDiscussion{InstallationID: "test", ID: "discussion", Route: Route{ChannelID: "201"}}
	if err := store.teammateBind(teammateBinding{TeammateDiscussion: d, Opened: true}); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	p := TeammatePublication{ID: "pub1", AdmissionID: "dv_first", Sequence: 7, Body: strings.Repeat("x", 1901)}
	if err := store.teammateQueueOutput(d, p, now); err != nil {
		t.Fatal(err)
	}
	rows, err := store.teammateOutputs(d.InstallationID, d.ID)
	if err != nil || len(rows) != 1 || len(rows[0].Chunks) != 2 {
		t.Fatalf("queue %d: %v", len(rows), err)
	}
	for i, id := range []string{"301", "302"} {
		err = store.teammateChangeOutput(d.InstallationID, rows[0], func(tx *bolt.Tx, k []byte, current *teammateOutput) error {
			return teammateFinishChunk(tx, k, d.InstallationID, current, i, id, i == 1, now)
		})
		if err != nil {
			t.Fatal(err)
		}
		ids, err := store.confirmedPublication(d, p)
		if err != nil || (i == 0 && len(ids) != 0) || (i == 1 && !slices.Equal(ids, []string{"301", "302"})) {
			t.Fatalf("chunk %d: %v %v", i, ids, err)
		}
	}
	if err = store.teammateAckOutputs(d, []string{p.ID}); err != nil {
		t.Fatal(err)
	}
	later := now.Add(GuardTTL + time.Second)
	if err = store.teammateCompactOutbox(d, later, nil); err != nil {
		t.Fatal(err)
	}
	if err = store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	ids, err := store.confirmedPublication(d, p)
	if err != nil || !slices.Equal(ids, []string{"301", "302"}) {
		t.Fatalf("reopened receipt: %v %v", ids, err)
	}
	if err = store.teammateQueueOutput(d, p, later); err != nil {
		t.Fatal(err)
	}
	changed := p
	changed.Body += " changed"
	if err = store.teammateQueueOutput(d, changed, later); !errors.Is(err, ErrConflict) {
		t.Fatalf("mutated replay: %v", err)
	}
	independent := p
	independent.ID = "pub2"
	independent.AdmissionID = "dv_second"
	independent.Sequence++
	if err = store.teammateQueueOutput(d, independent, later); err != nil {
		t.Fatal(err)
	}
	rows, err = store.teammateOutputs(d.InstallationID, d.ID)
	if err != nil || len(rows) != 1 || rows[0].Publication.ID != independent.ID {
		t.Fatalf("independent request queue: %v %v", rows, err)
	}
}
