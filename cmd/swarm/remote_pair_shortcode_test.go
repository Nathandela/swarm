package main

// ADR-007 B140 at the terminal: the short code is only real once `swarm remote pair` PRINTS
// it. The owner's report that forced it (agents-tracker-tr0n, verbatim intent): the
// 133-character payload "is not possibly written by a human". These tests pin the line the
// human reads, and its absence when an older daemon's PairView carries no code -- a client
// newer than its daemon must degrade to the old output, not print an empty prompt.

import (
	"bytes"
	"strings"
	"testing"

	"github.com/Nathandela/swarm/internal/remote/pairing"
)

func runPairWithView(t *testing.T, qr, shortCode string) string {
	t.Helper()
	dir := shortStateDir(t)
	host := newScriptedPairingHost()
	host.view.QR = qr
	host.view.ShortCode = shortCode
	startFakePairingDaemon(t, dir, host)

	var stdout, stderr bytes.Buffer
	if exit := runRemotePair(nil, strings.NewReader("y\n"), &stdout, &stderr); exit != 0 {
		t.Fatalf("runRemotePair exit = %d, want 0; stderr=%q", exit, stderr.String())
	}
	return stdout.String()
}

func TestRemotePair_PrintsTheShortCode(t *testing.T) {
	t.Setenv("TERM", "xterm-256color")
	t.Setenv("COLUMNS", "80")
	t.Setenv("LINES", "24")

	out := runPairWithView(t, realPairingPayload(t), "K73-M2QF-9TD")
	want := "Type this code on your phone to pair: K73-M2QF-9TD"
	if !strings.Contains(out, want) {
		t.Fatalf("`swarm remote pair` never printed the short code line %q.\nOutput:\n%s", want, out)
	}
}

func TestRemotePair_ShortCodePrintsEvenWhereNoSymbolCanBeDrawn(t *testing.T) {
	// The fallback path is where manual entry is the ONLY path, so the code matters most
	// exactly where the symbol cannot be drawn.
	t.Setenv("TERM", "dumb")

	out := runPairWithView(t, realPairingPayload(t), "K73-M2QF-9TD")
	if !strings.Contains(out, "K73-M2QF-9TD") {
		t.Fatalf("the no-symbol fallback dropped the short code, leaving only the "+
			"133-character payload.\nOutput:\n%s", out)
	}
}

func TestRemotePair_OmitsTheShortCodeLineForADaemonThatPredatesIt(t *testing.T) {
	t.Setenv("TERM", "xterm-256color")
	t.Setenv("COLUMNS", "80")
	t.Setenv("LINES", "24")

	out := runPairWithView(t, realPairingPayload(t), "")
	if strings.Contains(out, "Type this code on your phone") {
		t.Fatalf("an empty PairView.ShortCode still printed a code prompt -- a client newer "+
			"than its daemon invents a line with nothing to type.\nOutput:\n%s", out)
	}
}

func TestRemotePair_SpellsTheRelayAddressForTheFirstRunTypist(t *testing.T) {
	// The phone's first-run prompt asks for the relay address once (agents-tracker-3fkm) --
	// so the machine that sent the user there must SAY it. It lives inside the payload and
	// the symbol; a person completing a first pairing by typing has neither.
	t.Setenv("TERM", "xterm-256color")
	t.Setenv("COLUMNS", "80")
	t.Setenv("LINES", "24")

	payload := realPairingPayload(t)
	qp, err := pairing.DecodeQR(payload)
	if err != nil {
		t.Fatal(err)
	}
	out := runPairWithView(t, payload, "K73-M2QF-9TD")
	if !strings.Contains(out, "relay: "+qp.RelayURL) {
		t.Fatalf("`swarm remote pair` never spells the relay address %q, so the phone's "+
			"first-run prompt asks a question the machine does not answer.\nOutput:\n%s",
			qp.RelayURL, out)
	}
}
