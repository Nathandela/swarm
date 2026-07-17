package refadapter

// E9.5 / T-1 — totality/purity fuzz of the REAL reference adapter's extractor
// (the contract-package fuzz targets a stub; this targets production code). A
// panic or a nondeterministic result on any grid+tail fails.

import (
	"testing"

	"github.com/Nathandela/swarm/internal/adapter/fixtureio"
	"github.com/Nathandela/swarm/internal/vt"
)

func FuzzReferenceExtractConversationIDTotality(f *testing.F) {
	f.Add([]byte("conv-id=11111111-2222-3333-4444-555555555555\r\n"))
	f.Add([]byte("\x1b[2J\x1b[Hno id here"))
	f.Add([]byte{0x00, 0x1b, 0x5b, 0xff})
	f.Add([]byte("conv-id="))

	fx, err := fixtureio.LoadFixture("testdata/reference.json")
	if err != nil {
		f.Fatalf("load reference fixture: %v", err)
	}
	a := New(fx)

	f.Fuzz(func(t *testing.T, feed []byte) {
		emu := vt.NewEmulator(80, 24)
		emu.Feed(feed)
		b, err := emu.Snapshot()
		emu.Close()
		if err != nil {
			t.Fatalf("snapshot: %v", err)
		}
		snap, err := vt.DecodeSnapshot(b)
		if err != nil {
			t.Fatalf("decode: %v", err)
		}
		id1, ok1 := a.ExtractConversationID(snap, feed) // must not panic
		id2, ok2 := a.ExtractConversationID(snap, feed) // deterministic
		if id1 != id2 || ok1 != ok2 {
			t.Fatalf("nondeterministic: (%q,%v) then (%q,%v)", id1, ok1, id2, ok2)
		}
		if ok1 && id1 == "" {
			t.Fatalf("ok=true with empty id")
		}
		_, _ = a.ExtractConversationID(nil, feed) // total with nil grid
	})
}
