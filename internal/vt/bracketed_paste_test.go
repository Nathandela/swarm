package vt

// FAILING-FIRST for the send-submit defect (ADR-010 Amendment 9): the shim must know whether
// the agent has enabled bracketed paste (DECSET 2004), because a daemon-typed message is only
// read as ONE message by a CLI that parses input key-by-key (codex) when it arrives inside the
// paste markers. The emulator already parses every byte the agent writes, so it is the one
// place that knows the mode without inferring it from the drawn grid.

import (
	"sync"
	"testing"
)

func TestEmulator_TracksBracketedPasteMode(t *testing.T) {
	e := NewEmulator(80, 24)
	defer func() { _ = e.Close() }()

	if e.BracketedPaste() {
		t.Fatal("a fresh emulator reports bracketed paste on; DECSET 2004 defaults to reset")
	}
	e.Feed([]byte("\x1b[?2004h"))
	if !e.BracketedPaste() {
		t.Fatal("after CSI ?2004h the emulator reports bracketed paste off")
	}
	e.Feed([]byte("\x1b[?2004l"))
	if e.BracketedPaste() {
		t.Fatal("after CSI ?2004l the emulator still reports bracketed paste on")
	}
}

// A full reset (RIS) returns every mode to its default; an agent that restarts its UI with
// ESC c and does not re-enable the mode must not be sent paste markers it would print.
func TestEmulator_FullResetClearsBracketedPaste(t *testing.T) {
	e := NewEmulator(80, 24)
	defer func() { _ = e.Close() }()

	e.Feed([]byte("\x1b[?2004h"))
	e.Feed([]byte("\x1bc"))
	if e.BracketedPaste() {
		t.Fatal("after RIS (ESC c) the emulator still reports bracketed paste on")
	}
}

// The mode is read by the shim's input writer WITHOUT taking the emulator's lock: Feed holds
// that lock while the emulator's reply pump waits on the PTY writer, and the writer holds its
// own lock across the submit gap, so a locked read here could deadlock the two. Run under
// -race this proves the read is synchronized independently of Feed.
func TestEmulator_BracketedPasteReadConcurrentWithFeed(t *testing.T) {
	e := NewEmulator(80, 24)
	defer func() { _ = e.Close() }()

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 200; i++ {
			e.Feed([]byte("\x1b[?2004h"))
			e.Feed([]byte("\x1b[?2004l"))
		}
	}()
	for i := 0; i < 200; i++ {
		_ = e.BracketedPaste()
	}
	wg.Wait()
}
