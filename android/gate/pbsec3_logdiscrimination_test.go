package gate

// PB-SEC-3, the argument check's DISCRIMINATION, proved on SYNTHETIC sources.
//
// WHY SYNTHETIC AND NOT THE REPOSITORY. s18_sec3_logscan_test.go runs the same check over the
// phone-side trees, and that run can only ever demonstrate ACCEPTANCE: it sees the call sites
// that exist, all of which are meant to be clean, so a check that had stopped rejecting anything
// at all would report the cleanest possible result and pass. The leak shapes below do not exist
// in this codebase and must not; the only way to show the guard would catch them is to hand it
// sources that contain them.
//
// THE DEFECT THIS CLOSES. The check used to substring-match its dangerous-identifier list
// against the entire call source, string literals included, which reads what the author WROTE
// instead of what the device HOLDS:
//
//   - FALSE POSITIVE, live at the time of writing: `Log.w(TAG, "push token fetch failed; ...", e)`
//     in push/PushTokens.kt -- static prose and a Throwable, on a path taken because the fetch
//     failed and therefore no token exists -- was a finding solely for containing the word
//     "token", and both PB-SEC-3 tests were red because of it.
//   - THE HALF THAT MATTERS: a guard that cannot see past a literal catches the leaks polite
//     enough to name themselves in prose beside the value.
//
// The rule is loggedData's: what is interpolated or passed as an argument is DATA, what is
// literal prose is not.
//
// WHAT THIS CANNOT DO, stated so no evidence line claims it. The list is of IDENTIFIERS, so
// `Log.w(TAG, "$t")` where `t` holds a token is invisible to it and stays invisible -- naming
// discipline, not this scan, is what covers that. The discrimination narrows matching, so every
// leak case below is a REGRESSION FENCE: it pins that fixing the false positive did not blow a
// hole in the detection that remains.
//
// THIS FILE NEVER SKIPS: it writes its own sources.

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// pbsec3ScanSynthetic runs the REAL scan -- the real sink patterns, the real comment stripping,
// the real multi-line wholeCall -- over one synthetic Kotlin source. Asserting against
// dangerousLogFindings alone would leave the path from a file on disk to a finding untested, and
// that path is where a regex that stopped matching would hide.
func pbsec3ScanSynthetic(t *testing.T, source string) []logSink {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "Synthetic.kt"), []byte(source), 0o644); err != nil {
		t.Fatalf("writing the synthetic source: %v", err)
	}
	return scanLogSinksIn(t, map[string]string{"synthetic": dir})
}

// TestPBSEC3_TheArgumentCheckReadsWhatIsLoggedNotWhatIsWritten is both directions in one table.
func TestPBSEC3_TheArgumentCheckReadsWhatIsLoggedNotWhatIsWritten(t *testing.T) {
	for _, tc := range []struct {
		name string
		call string
		want []string // the dangerous identifiers this call must be found to LOG
		why  string
	}{
		// ---- prose must not fire ------------------------------------------------
		{
			name: "the live false positive: prose about a token, on the path where none exists",
			call: `Log.w(TAG, "push token fetch failed; this launch registered no token and the ` +
				`phone will not receive background wakes until the next one", e)`,
			want: nil,
			why: "the message is static prose and the only argument is a Throwable from a FAILED " +
				"fetch. Flagging it teaches the reader that this guard fires on the WORD for the " +
				"thing rather than the thing, which is how a guard gets silenced",
		},
		{
			name: "prose naming several of the listed identifiers at once",
			call: `Log.d(TAG, "the snapshot and the payload are sealed before any keystroke or ` +
				`sas is shown")`,
			want: nil,
			why:  "an English sentence about what the code does logs none of the values it names",
		},
		{
			name: "a dotted tail after a braceless template is literal text in Kotlin",
			call: `Log.d(TAG, "epoch $id.token")`,
			want: nil,
			why: `Kotlin interpolates only the identifier: "$id.token" prints id.toString() ` +
				`followed by the characters ".token". Reading the tail as data would put prose ` +
				`back into the match and reopen the false positive above`,
		},

		// ---- the leak shapes must fire ------------------------------------------
		{
			name: "a braceless template interpolating the value",
			call: `Log.d(TAG, "x: $token")`,
			want: []string{"token"},
			why: "the VALUE reaches logcat. A fix that dropped whole string literals rather than " +
				"extracting their templates would miss this and look correct on the repository",
		},
		{
			name: "a braced template interpolating a member",
			call: `Log.d(TAG, "seen: ${resp.token}")`,
			want: []string{"token"},
			why:  "same leak, reached through an expression rather than a bare identifier",
		},
		{
			name: "a bare argument, no literal involved at all",
			call: `Log.d(TAG, tokenVar)`,
			want: []string{"token"},
			why:  "nothing labels this one; the argument IS the value",
		},
		{
			name: "a bare argument beside innocent prose",
			call: `Log.e(TAG, "sealing failed", contentKey)`,
			want: []string{"contentKey"},
			why: "the message is clean and the third argument is the epoch content key. A check " +
				"that only inspected literals would report this line as safe",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sinks := pbsec3ScanSynthetic(t, "package synthetic\n\n"+tc.call+"\n")
			if len(sinks) != 1 {
				t.Fatalf("the scan found %d sink(s) in the synthetic source, want exactly 1. "+
					"Nothing below is an assertion until the call site is found at all -- and a "+
					"scan that stopped matching reports zero findings and passes", len(sinks))
			}

			got := dangerousLogFindings(sinks[0].Call)
			if len(got) == 0 {
				got = nil
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("PB-SEC-3: %s\n\tcall:      %s\n\tlogs data: %q\n\tfound:     %v\n\twant:      %v\n\t%s",
					tc.name, sinks[0].Call, loggedData(sinks[0].Call), got, tc.want, tc.why)
			}
		})
	}
}

// TestPBSEC3_ADangerousArgumentIsStillReportedAcrossAWrappedCall pins the discrimination against
// the defect wholeCall already exists to prevent, one layer down.
//
// The inventory joins a wrapped call's continuation lines precisely so the argument check sees
// every argument rather than only those that fit on one line -- "a guard defeated by pressing
// Enter". loggedData now sits between that join and the verdict, so it gets the same fence: a
// leak on a continuation line, with the first line carrying nothing but innocent prose.
func TestPBSEC3_ADangerousArgumentIsStillReportedAcrossAWrappedCall(t *testing.T) {
	sinks := pbsec3ScanSynthetic(t, "package synthetic\n\n"+
		"Log.w(TAG, \"could not seal the frame for this session; the phone will retry \" +\n"+
		"    \"on the next connection\",\n"+
		"    snapshotText)\n")
	if len(sinks) != 1 {
		t.Fatalf("the scan found %d sink(s), want exactly 1", len(sinks))
	}
	if got := dangerousLogFindings(sinks[0].Call); !reflect.DeepEqual(got, []string{"snapshot"}) {
		t.Errorf("PB-SEC-3: a wrapped call logging a rendered terminal grid on its THIRD line was "+
			"found to log %v, want [snapshot].\n\tcall:      %s\n\tlogs data: %q",
			got, sinks[0].Call, loggedData(sinks[0].Call))
	}
}
