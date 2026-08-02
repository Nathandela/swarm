package main

// FAILING-FIRST tests for the operator-facing half of ADR-007 B71(1). The protocol layer
// now carries a cause; this is the end it exists for.
//
// `swarm remote pair` prints ONE line -- "remote pair: pairing failed" -- for a declined
// SAS, an expired window, a spent code, a rate limit and a relay-consent abandonment
// alike. On a handset during a closed test that reads as "the product is broken", with
// nothing to act on. Pairing is the first thing every tester does.
//
// The messages are built from a table keyed on protocol.PairFailure constants, never from
// the daemon's error text: the pairing path parses attacker-influenced bytes, and the
// closed set is what keeps them off the owner's terminal (see
// internal/protocol/pairing_cause_test.go's normalisation test for the enforcement).

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"github.com/Nathandela/swarm/internal/remote/pairing"
)

// errUnattributedForTest stands for every failure the daemon reports without a sentinel
// the protocol layer can classify -- today that includes the epoch-rotation abort and the
// grant-write rollback in internal/skeleton/pairing.go.
var errUnattributedForTest = errors.New("something the daemon could not attribute")

// pairCauseCases drives `swarm remote pair` to a real failure of each kind, through the
// scripted host's failWith seam, and states the phrase the operator must be able to act
// on. The phrases are deliberately about the SITUATION, not the code.
var pairCauseCases = []struct {
	name string
	fail error
	// want is a phrase that must appear in what the operator sees. It is the diagnostic
	// content of the line: if it is absent the message did not say what happened.
	want string
}{
	{"confirm timed out", pairing.ErrConfirmTimeout, "in time"},
	{"rate limited", pairing.ErrRateLimited, "too many"},
	{"code already used", pairing.ErrSecretConsumed, "already been used"},
	{"headless refusal", pairing.ErrHeadlessRefused, "local console"},
	{"no relay-route consent", pairing.ErrNoConsent, "consent"},
}

// TestRemotePair_EveryCauseGetsItsOwnMessage is the anti-collapse assertion: five real
// failure causes, five DIFFERENT things printed. A regression that routes any two back
// through one blanket line fails here naming both.
func TestRemotePair_EveryCauseGetsItsOwnMessage(t *testing.T) {
	// seen maps the DIAGNOSTIC line (stderr's last, where the terminal outcome lands) to
	// the case that produced it, so every pair of causes is compared.
	seen := map[string]string{}
	for _, tc := range pairCauseCases {
		var diagnostic string
		t.Run(tc.name, func(t *testing.T) {
			dir := shortStateDir(t)
			host := newScriptedPairingHost()
			host.failWith = tc.fail
			startFakePairingDaemon(t, dir, host)

			var stdout, stderr bytes.Buffer
			exit := runRemotePair(nil, strings.NewReader("y\n"), &stdout, &stderr)
			if exit == 0 {
				t.Fatalf("runRemotePair exit = 0 for %s; want nonzero (nothing paired)", tc.name)
			}
			out := stdout.String() + stderr.String()
			if !strings.Contains(strings.ToLower(out), tc.want) {
				t.Errorf("pair output for %s does not say what happened: want a line carrying %q, got:\n%s",
					tc.name, tc.want, out)
			}
			// The generic line must be gone for a cause the daemon DID attribute.
			if strings.Contains(out, "remote pair: pairing failed\n") {
				t.Errorf("pair output for %s is still the blanket 'pairing failed' line; the cause "+
					"reached the CLI and was thrown away again:\n%s", tc.name, out)
			}
			diagnostic = lastLine(stderr.String())
		})
		if diagnostic == "" {
			continue // the subtest already failed; nothing to compare
		}
		if prev, dup := seen[diagnostic]; dup {
			t.Errorf("%q and %q print the SAME line %q; the operator cannot tell them apart",
				prev, tc.name, diagnostic)
		}
		seen[diagnostic] = tc.name
	}
}

// TestRemotePair_DeclineIsNotAFailure: the operator answering "no" at the SAS gate is the
// gate doing its job -- the one outcome on this path that is not a malfunction. It must
// not be reported with the vocabulary of a breakage, and it belongs on stdout with the
// rest of the ceremony rather than on the error stream.
func TestRemotePair_DeclineIsNotAFailure(t *testing.T) {
	dir := shortStateDir(t)
	host := newScriptedPairingHost()
	host.failWith = pairing.ErrConfirmDeclined
	startFakePairingDaemon(t, dir, host)

	var stdout, stderr bytes.Buffer
	exit := runRemotePair(nil, strings.NewReader("n\n"), &stdout, &stderr)
	if exit == 0 {
		t.Fatalf("runRemotePair(deny) exit = 0; want nonzero (nothing was paired)")
	}
	if !strings.Contains(strings.ToLower(stdout.String()), "declined") {
		t.Errorf("a declined pairing is not reported on stdout; got stdout=%q stderr=%q",
			stdout.String(), stderr.String())
	}
	if strings.Contains(strings.ToLower(stdout.String()+stderr.String()), "fail") {
		t.Errorf("a declined pairing is reported with failure vocabulary; the operator did the thing "+
			"the SAS gate exists for. stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
}

// TestRemotePair_UnattributedFailureSaysSo: when the daemon reports a failure it could
// not attribute, the CLI must say THAT and point somewhere, rather than print a bare
// "pairing failed" that leaves the operator with nothing at all.
func TestRemotePair_UnattributedFailureSaysSo(t *testing.T) {
	dir := shortStateDir(t)
	host := newScriptedPairingHost()
	host.failWith = errUnattributedForTest
	startFakePairingDaemon(t, dir, host)

	var stdout, stderr bytes.Buffer
	if exit := runRemotePair(nil, strings.NewReader("y\n"), &stdout, &stderr); exit == 0 {
		t.Fatalf("runRemotePair exit = 0 on an unattributed failure; want nonzero")
	}
	out := strings.ToLower(stdout.String() + stderr.String())
	if !strings.Contains(out, "log") {
		t.Errorf("an unattributed pairing failure tells the operator nowhere to look; got:\n%s", out)
	}
}

// lastLine returns the final non-empty line of s, which is where the terminal outcome is
// printed. Comparing whole buffers would make every case differ on the QR alone.
func lastLine(s string) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if strings.TrimSpace(lines[i]) != "" {
			return strings.TrimSpace(lines[i])
		}
	}
	return ""
}
