package swarmmobile_test

// FAILING-FIRST (TDD RED, GG-5) for Wave R4 deliverable 3 (bead agents-tracker-hggx.5):
// add/switch/remove computer UX and the global inbox, traced the way PB-BIND-3 already
// requires every screen element to be. Playbook 4.2 (:196-208): "The app has a machine
// switcher and an aggregate inbox: each row names the machine, reachability, last
// successful sync, and count of sessions needing input ... Session identity is always
// (machine_id, session_id) ... Connections beyond the cap use a deterministic
// least-recently-viewed policy and visibly show their last-sync age." ADR-018 MM3/MM4/
// MM7/MM8; recovery screens per machine are REQUIRED, not optional (playbook :575-576).
//
// THE PATTERN IS S16'S (s16_screencoverage_test.go): the elements are hard-coded here,
// not read from the TSV, so deleting a row cannot delete the requirement; both
// directions are enforced -- a missing row fails, and a row naming a verb the facade
// does not export fails.
//
// EVERY ELEMENT BELOW IS A CONTROL OR FACT A USER TOUCHES ON THE R4 SCREENS, and every
// one lacks both a row and a facade verb today: the bound surface is still the scalar
// single-machine App (mobile/types.go Config{StateDir, RelayURL, MachineID} -- "three
// scalars, one destination", ADR-018 Context). The failure message on each names which
// screen loses what.

import (
	"sort"
	"testing"
)

// r4ScreenElements: element -> requirement citation.
var r4ScreenElements = map[string]string{
	// The machine switcher list itself. gomobile has no list type, so the row set
	// crosses as a handle with Count/At, like SessionList. Each row must serve the four
	// facts of playbook :198: machine name, reachability, last successful sync, count
	// of sessions needing input.
	"machines.list": "ADR-018 MM3, playbook 4.2:196-198",

	// Add computer: the new-pairing entry that ADDS a registry entry beside the
	// existing ones -- "The new computer appears without replacing existing pairings"
	// (playbook 4.1:471). Without it, pairing a second machine destroys the first
	// (s9_machineid_test.go's wholesale discard).
	"machines.add": "ADR-018 MM6, playbook 4.1:465-472",

	// Switch computer: select which pairing the session screens read. MarkViewed is
	// what feeds the deterministic least-recently-viewed connection policy.
	"machines.select": "ADR-018 MM3, playbook 4.2:200-202",

	// Forget this computer: the PHONE-side removal of one pairing -- revoke the push
	// address with the installation key, delete that machine's keys and cache, warn
	// that the computer still authorizes the old device id. DISTINCT from revoking a
	// phone from a computer; the two operations have different copy and consequences
	// (playbook 4.2:207-208, 4.9:319-324).
	"machines.forget": "ADR-018 MM7, playbook 4.9:319-324",

	// The global inbox: one aggregate list across every pairing, every row keyed by
	// the tuple (machine_id, session_id) -- two machines may serve the same session id
	// and the same display title without colliding (R4 exit, playbook :765-766).
	"inbox.global": "ADR-018 MM4, playbook 4.2:196-199",

	// Per-machine connection health: each row's reachability state, per pairing --
	// losing or degrading one machine's connection degrades that row only (MM5/MM8).
	"machines.connection_health": "ADR-018 MM5/MM8, playbook 4.2:198",

	// The visible last-sync age on rows beyond the connection cap: "Connections beyond
	// the cap use a deterministic least-recently-viewed policy and visibly show their
	// last-sync age." A parked row rendered as live is the dishonest rendering the cap
	// ruling forbids.
	"machines.stale_age": "ADR-018 MM3, playbook 4.2:200-202",

	// Per-machine recovery: "Process death, app upgrade, backup exclusion, and
	// Keystore invalidation have explicit recovery screens per machine" -- the
	// aggregate inbox says WHICH row is broken and how stale it is (MM8).
	"machines.recovery": "ADR-018 MM8, playbook 6.7:575-576",
}

// TestR4_EveryMultiMachineScreenElementIsTracedToAFacadeMethod: the S16 check, over
// R4's elements. RED until the manager-backed facade exists: no row in
// screen_coverage.tsv names these elements, and the verbs they need are not exported.
func TestR4_EveryMultiMachineScreenElementIsTracedToAFacadeMethod(t *testing.T) {
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

	elements := make([]string, 0, len(r4ScreenElements))
	for el := range r4ScreenElements {
		elements = append(elements, el)
	}
	sort.Strings(elements)

	for _, el := range elements {
		row, ok := rows[el]
		if !ok {
			t.Errorf("PB-BIND-3 COVERAGE FAILURE: R4 screen element %q (%s) has no row in "+
				"screen_coverage.tsv. The verb behind it is one R4 owes; without it the machine "+
				"switcher / global inbox ships untraced or not at all", el, r4ScreenElements[el])
			continue
		}
		if len(row) == 0 {
			t.Errorf("PB-BIND-3 COVERAGE FAILURE: R4 screen element %q has a row and no facade "+
				"method (%s)", el, r4ScreenElements[el])
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
