package swarmmobile_test

// FAILING-FIRST (TDD RED, GG-5) for the screen elements slice S16 adds, traced the way
// PB-BIND-3 already requires every other element to be.
//
// WHY THIS IS A SEPARATE FILE FROM coverage_test.go. That file is S8's and its
// requiredScreenElements list is S8's transcription of PB-BIND-3. Editing a shipped slice's
// enumeration from inside another slice is how a traceability table stops being reviewable --
// and it would put S16's RED inside S8's test names, where an auditor reading the S8 evidence
// would find failures the S8 evidence never mentions. The elements below are S16's, they
// point at the SAME checked-in table, and both directions are enforced exactly as S8 does it.
//
// Every one of them is a control a user touches, and every one has a facade verb missing
// today; the failure message on each says which screen loses what.

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// s16ScreenElements are the elements the v1 screens need that no shipped slice traced,
// because the verb behind each is one S16 owes. Hard-coded HERE, not read from the TSV, for
// the reason S8 states: deleting a row from the table must not make the requirement vanish.
var s16ScreenElements = map[string]string{
	// PB-APP-9's rendered states. Without a classifier the screen has an exception message
	// and no way to route it, so every failure looks like every other failure.
	"error_class": "PB-APP-9,PB-APP-10",

	// PB-APP-8's clock verdict, the residual S16 inherits. Push-only today, so a screen that
	// opens after the event -- which on Android is most of them -- cannot render it.
	"clock_verdict": "PB-APP-8,PB-TIME-1",

	// PB-APP-8's fourth state. StreamState answers live/stale; a repair in flight is a third
	// fact and PB-SYNC-3 forbids expressing it by clearing the stale mark.
	"resync.pending": "PB-APP-8,PB-SYNC-3",

	// PB-INPUT-1's second residual: a ledger that only grows, with nothing to acknowledge it.
	"input.undelivered_clear": "PB-INPUT-1,PB-APP-8",

	// PB-PAIR-6's display-and-confirm step. BeginPairing dials on its second statement today,
	// so the destination is joined before the user has seen it.
	"pairing.confirm_destination": "PB-PAIR-6",

	// PB-PAIR-5's SAS mismatch. The only button available today is Cancel, which records a
	// suspected man-in-the-middle as "I changed my mind".
	"pairing.sas_mismatch": "PB-PAIR-5,PB-SAS-3",

	// PB-PAIR-4's persisted state machine and its deadline. Both are in-memory today, so a
	// process death during the SAS step leaves the next launch unable to tell a fresh install
	// from a pairing the machine may have committed.
	"pairing.resume": "PB-PAIR-4,PB-PAIR-5",
}

func TestS16_EveryNewScreenElementIsTracedToAFacadeMethod(t *testing.T) {
	src := loadFacade(t)
	rows := s16CoverageRows(t, src.Dir)

	have := map[string]bool{}
	for _, s := range exportedSurface(src) {
		switch s.Kind {
		case "func":
			have[s.Name] = true
		case "method", "field":
			have[s.Owner+"."+s.Name] = true
		}
	}

	elements := make([]string, 0, len(s16ScreenElements))
	for el := range s16ScreenElements {
		elements = append(elements, el)
	}
	sort.Strings(elements)

	for _, el := range elements {
		row, ok := rows[el]
		if !ok {
			t.Errorf("PB-BIND-3 COVERAGE FAILURE: screen element %q (%s) has no row in "+
				"screen_coverage.tsv. The verb behind it is one S16 owes; the row is what stops "+
				"it shipping as an untraced method or not shipping at all",
				el, s16ScreenElements[el])
			continue
		}
		if len(row) == 0 {
			t.Errorf("PB-BIND-3 COVERAGE FAILURE: screen element %q has a row and no facade "+
				"method (%s)", el, s16ScreenElements[el])
			continue
		}
		for _, m := range row {
			if !have[m] {
				t.Errorf("PB-BIND-3: screen_coverage.tsv maps %q to %q, which the facade does not "+
					"export", el, m)
			}
		}
	}
}

// s16CoverageRows parses the checked-in table into element -> methods. It is a private
// reader rather than a call into S8's loadCoverageTable so that a change to S8's row schema
// surfaces here as a parse difference rather than as a silently-empty map.
func s16CoverageRows(t *testing.T, dir string) map[string][]string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(dir, "screen_coverage.tsv"))
	if err != nil {
		t.Fatalf("PB-BIND-3 requires a checked-in traceability table: %v", err)
	}
	out := map[string][]string{}
	for _, line := range strings.Split(string(raw), "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		cols := strings.Split(line, "\t")
		el := strings.TrimSpace(cols[0])
		var methods []string
		if len(cols) > 1 {
			for _, m := range strings.Split(cols[1], ",") {
				if m = strings.TrimSpace(m); m != "" {
					methods = append(methods, m)
				}
			}
		}
		out[el] = methods
	}
	if len(out) == 0 {
		t.Fatalf("screen_coverage.tsv has no rows; the guard would be vacuous")
	}
	return out
}

// PASSES TODAY, AND THAT IS NOT COVERAGE -- labelled so no evidence line can count it as
// earned. B8 holds at HEAD: the golden's three []byte occurrences are all inbound parameters.
// The fence is written now, with the RED tests it guards, because S16 adds more verbs to this
// surface than any other slice in the phase and an outbound key crossing is the one mistake
// that cannot be walked back after an APK ships.
//
// TestS16_TheBoundSurfaceStillReturnsNoKeyMaterial re-states ADR-007 B8 over the verbs S16
// adds, because this slice widens the facade more than any other in the phase.
//
// B8: the key crossing is SINGLE and INBOUND, and the matrix may only ever NARROW. The
// golden holds exactly three []byte occurrences today and all three are inbound parameters
// (InstallWakeKey, InstallContentKey, SendInput). No BOUND METHOD may RETURN []byte -- a
// return travels Go -> Java, which is the outbound direction B8 forbids.
//
// It is checked from the GOLDEN rather than from the parsed source so that it reads the same
// artifact a reviewer signs off, and so a verb added without regenerating the golden fails
// PB-BIND-7 first.
func TestS16_TheBoundSurfaceStillReturnsNoKeyMaterial(t *testing.T) {
	src := loadFacade(t)
	raw, err := os.ReadFile(filepath.Join(src.Dir, "testdata", "exported_surface.golden"))
	if err != nil {
		t.Fatalf("read the pinned surface: %v", err)
	}

	var offenders []string
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "method ") && !strings.HasPrefix(line, "func ") {
			continue
		}
		// The results are whatever follows the LAST ")" that closes the parameter list.
		open := strings.Index(line, "(")
		if open < 0 {
			continue
		}
		depth, close := 0, -1
		for i := open; i < len(line); i++ {
			switch line[i] {
			case '(':
				depth++
			case ')':
				depth--
				if depth == 0 {
					close = i
				}
			}
			if close >= 0 {
				break
			}
		}
		if close < 0 {
			continue
		}
		if strings.Contains(line[close:], "[]byte") {
			offenders = append(offenders, line)
		}
	}
	if len(offenders) > 0 {
		t.Errorf("ADR-007 B8: %d bound entry point(s) RETURN []byte:\n\t%s\n"+
			"A result travels Go -> Java, which is outbound. The single permitted key crossing "+
			"is inbound only, and the matrix may narrow but never widen -- so a verb that would "+
			"return key material is the wrong verb, not a verb that needs a caveat.",
			len(offenders), strings.Join(offenders, "\n\t"))
	}
}
