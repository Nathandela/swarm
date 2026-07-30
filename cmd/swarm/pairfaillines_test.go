package main

// THE QUANTIFIER OVER THE OPERATOR-FACING PAIRING LINES.
//
// B71(1) is the finding that a pairing failure must tell the owner WHAT happened and WHAT to do.
// It was closed by writing pairFailureLines -- a hand-maintained table -- against a
// hand-maintained cause vocabulary in internal/protocol, with nothing tying the two together.
//
// So when PB-PAIR-4 added a cause, the table did not gain a row, and reportPairFailure fell
// through to the PairFailInternal line: "pairing failed and the daemon did not report a cause".
// The daemon HAD reported a cause. B71(1) re-opened for the one situation where the desktop and
// the handset disagree about whether anything was paired -- which is the situation that most
// needs words, because the owner is looking at a phone that says it worked.
//
// That is round 6's recorded shape: the requirement's subject is EVERY cause, and the fence's
// subject was the causes whose lines someone remembered to write. The fix is not another row.
// It is a quantifier over protocol.PairFailures(), so the NEXT cause fails here on the day it
// lands rather than reaching an operator as "no cause reported".

import (
	"bytes"
	"strings"
	"testing"

	"github.com/Nathandela/swarm/internal/protocol"
)

// TestPairFailureLines_EveryCauseHasItsOwnLine is the quantifier.
//
// PairFailDeclined is the one exemption and it is asserted rather than skipped: a decline is
// the SAS gate working, so reportPairFailure prints it to stdout with the rest of the ceremony
// and deliberately carries no failure vocabulary.
func TestPairFailureLines_EveryCauseHasItsOwnLine(t *testing.T) {
	causes := protocol.PairFailures()
	if len(causes) < 5 {
		t.Fatalf("protocol.PairFailures() returned %d causes; the vocabulary cannot have shrunk that far, so this fence is measuring nothing", len(causes))
	}

	seen := map[string]protocol.PairFailure{}
	for _, cause := range causes {
		if cause == protocol.PairFailDeclined {
			continue // answered on stdout as part of the ceremony; see reportPairFailure
		}
		line, ok := pairFailureLines[cause]
		if !ok {
			t.Errorf("cause %q has NO operator line, so reportPairFailure falls through to the "+
				"PairFailInternal row and tells the owner the daemon reported no cause -- when it "+
				"reported this one. Every cause in the vocabulary needs its own line (B71(1))", cause)
			continue
		}
		if prev, dup := seen[line]; dup {
			t.Errorf("causes %q and %q render the SAME line; two situations the daemon can tell apart "+
				"must not collapse into one thing the owner is told", prev, cause)
		}
		seen[line] = cause
	}
}

// TestPairFailureLines_EveryLineSaysWhatToDo is the other half of B71(1). "What happened" without
// "what to do" leaves the owner at a handset with nothing to try, which is the state PB-PAIR-4's
// missing row produced.
//
// The check is deliberately coarse -- an imperative the owner can act on -- because a stricter
// match on prose would be a fence on wording rather than on meaning, and would go red every time
// someone reworded a sentence.
func TestPairFailureLines_EveryLineSaysWhatToDo(t *testing.T) {
	remedies := []string{"run `swarm remote pair`", "pair again", "retry", "check", "wait", "has to happen at the machine"}
	for cause, line := range pairFailureLines {
		lower := strings.ToLower(line)
		if !strings.HasPrefix(line, "remote pair: ") {
			t.Errorf("cause %q renders %q, which does not name the verb it came from", cause, line)
		}
		found := false
		for _, r := range remedies {
			if strings.Contains(lower, strings.ToLower(r)) {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("cause %q renders %q, which tells the owner what happened but nothing to DO about it", cause, line)
		}
	}
}

// TestReportPairFailure_TheUnacknowledgedAcceptanceTellsTheOwnerThePhoneMayDisagree is the
// situation-specific half, and it is the reason the generic quantifier above is not enough.
//
// Every other cause leaves both ends agreeing that nothing was paired. This one does not: the
// device pins on the acceptance it received, so the handset may read "paired" while this machine
// claims nothing. An owner told only "nothing was paired" is being told something their phone is
// visibly contradicting, and the remedy -- pair again, no revoke needed, the slot is free -- is
// exactly the thing they will not guess.
func TestReportPairFailure_TheUnacknowledgedAcceptanceTellsTheOwnerThePhoneMayDisagree(t *testing.T) {
	var stdout, stderr bytes.Buffer
	reportPairFailure(protocol.PairFailAcceptUnacknowledged, &stdout, &stderr)
	got := stderr.String()

	if strings.Contains(got, "did not report a cause") {
		t.Fatalf("reportPairFailure rendered the INTERNAL line for an attributed cause: %q", got)
	}
	lower := strings.ToLower(got)
	for _, want := range []string{"phone", "again"} {
		if !strings.Contains(lower, want) {
			t.Errorf("the line for an unacknowledged acceptance is %q; it must mention %q -- the owner is "+
				"standing in front of a handset that may say it paired, and needs to be told the remedy is to "+
				"pair again rather than to hunt for a device id and revoke", got, want)
		}
	}
}
