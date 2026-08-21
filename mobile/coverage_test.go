package swarmmobile_test

// FAILING-FIRST (TDD RED, GG-5) guard for PB-BIND-3: the facade covers every capability
// the v1 screens need, and the mapping is a checked-in traceability table rather than a
// claim. "Any screen element with no method is a coverage failure" is the requirement's
// own wording, so the table is enforced in BOTH directions -- a required element with no
// method fails, and an exported entry point in no row fails too (an untraced surface is
// API the app can call that no screen asked for).

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// requiredScreenElements is PB-BIND-3's enumeration, transcribed, plus the three
// elements the adjacent requirements make unavoidable for a coherent surface
// (op.outcome, key_custody.install, callbacks). It is hard-coded HERE, not read from
// the TSV, so deleting a row from the table cannot make the requirement disappear.
var requiredScreenElements = []string{
	// pairing (QR decode, short code, SAS, confirm, cancel)
	"pairing.qr_decode",
	// The ten-character spelling, ADR-007 B140 (agents-tracker-tr0n): required so the
	// fence DEMANDS the row -- a surface that dropped BeginPairingWithCode would fail
	// here, not just lose an untraced-verb row.
	"pairing.short_code",
	"pairing.sas",
	"pairing.confirm",
	"pairing.cancel",
	// roster + presence, sessions with Group
	"roster",
	"sessions_with_group",
	"presence",
	// journal read/subscribe
	"journal.read",
	"journal.subscribe",
	// snapshot peek
	"snapshot.peek",
	// terminal_watch / terminal_unwatch -- first-class verbs the live tail depends on
	"terminal_watch",
	"terminal_unwatch",
	// WAVE R8 (ADR-017): the capability-routed terminal fallback. Three elements rather
	// than one, because the ADR binds three INDEPENDENT authorities and collapsing them
	// into a single row would let one arrive without the others: a watch that grants no
	// input authority, an explicit and revocable control ceremony bound to the session
	// INSTANCE, and a live-only raw-input plane that never buffers a byte.
	// The routed destination itself: R8's central claim is that the MACHINE chooses the
	// surface and the phone reads that choice, so the element is the choice.
	"session.destination",
	"terminal_view.watch",
	"terminal_control.enter",
	"terminal_control.input",
	// ADR-017 amendment T8-b: backgrounding is a severance trigger IN ITS OWN RIGHT, so
	// the app needs a verb for it. It is enumerated here rather than left to the TSV so
	// deleting the row cannot delete the requirement -- and the requirement is what stops
	// the phone core's two Background methods going back to having no production caller,
	// which is the state that made T8-b unreachable in the shipped app.
	"lifecycle.background",
	// take_control acquire/release, input + resize
	"take_control.acquire",
	"take_control.release",
	"input.send",
	"input.resize",
	// launch, interrupt/kill, revoke, kill switch
	"launch",
	"interrupt",
	"kill",
	"revoke",
	"kill_switch",
	// push-token registration and push preferences
	"push.token_register",
	"push.preferences",
	// connection/stale state, resync
	"connection_state",
	"stale_state",
	"resync",
	// state lifecycle (Start/Stop/restore)
	"lifecycle.start",
	"lifecycle.stop",
	"lifecycle.restore",
	// adjacent requirements that have nowhere else to land on this surface
	"op.outcome",          // PB-SYNC-2 / PB-STATE-1, and the S7 ReplyCache.Take residual
	"key_custody.install", // PB-KEY-1's single documented crossing
	// PB-KEY-7's lock purge and PB-SEC-2's fresh unwrap, as ONE element because a lock with no
	// way back is a brick and a way back with no lock is nothing (ADR-007 B35/B36). It has no
	// screen of its own on purpose: the trigger is the Android lifecycle, not a control a person
	// presses, and PB-SEC-11 forbids the one exported component from reaching either verb.
	"key_custody.lock",
	"callbacks", // PB-BIND-6's delivery plane
	// The Machines pane's own identity field (bead agents-tracker-xtj). It is here rather than
	// folded into an existing element because the name is not presence, not pairing state and
	// not the machine's id -- it is what this phone called itself at enrolment, and the reason
	// it needs a verb at all is that a screen typing the literal would be rendering a Go
	// constant as though the wire had carried it.
	"paired_device",

	// ADDED BY SLICE S16, additively. This list is PB-BIND-3's enumeration transcribed, and
	// it is hard-coded here so deleting a row from the TSV cannot make a requirement vanish --
	// which means it must GROW when the product does, or the reverse check ("the table
	// invented a screen") fires on every legitimately new element and the diagnosis it offers
	// ("or the requirement list is stale") is the correct one.
	//
	// S16 owns their behaviour and states the case for each in mobile/s16_screencoverage_test.go;
	// they are named here so S8's two directions keep meeting in the middle.
	"error_class",                 // PB-APP-9/PB-APP-10: without a classifier every failure looks like every other
	"clock_verdict",               // PB-APP-8/PB-TIME-1: push-only, so a screen opened afterwards cannot render it
	"resync.pending",              // PB-APP-8/PB-SYNC-3: the fourth state, orthogonal to staleness
	"input.undelivered_clear",     // PB-INPUT-1: a ledger that only grows, with nothing to acknowledge it
	"pairing.confirm_destination", // PB-PAIR-6: the destination was joined before it was displayed
	"pairing.sas_mismatch",        // PB-PAIR-5/PB-SAS-3: the only button was Cancel
	"pairing.resume",              // PB-PAIR-4: the state machine did not survive a process death

	// ADDED BY SLICE S17, additively and for the same reason S16's block above states.
	// mobile/s17_screencoverage_test.go states the case for it and hard-codes it there too, so
	// this list and that one keep meeting in the middle.
	"push.notification", // PB-PUSH-4/PB-PUSH-3: a push arrived and no verb decided what to render

	// ADDED BY THE INTERACTION PROGRAM (ADR-009), additively and for the same reason. ADR-009
	// makes the transcript the phone's PRIMARY surface -- items and nothing else, no terminal
	// grid -- so the two verbs that serve it are screen elements in exactly the sense this list
	// enumerates. mobile/interaction_screencoverage_test.go states the case for each and
	// hard-codes them there too, so this list and that one keep meeting in the middle.
	"transcript.read",              // ADR-009: the chat itself, which no existing verb serves
	"transcript.approvals_pending", // IS-LIFE-3: a card the machine is blocked on, kept answerable
	"transcript.approve",           // IS-LIFE-4: ANSWERING one -- a card with no answering verb leaves the machine blocked

	// ADDED BY agents-tracker-ksvb.1, additively and for the reason S16's block states.
	//
	// It is its OWN element rather than a second method on paired_device, which is the row it
	// looks most like. That one is the name THIS PHONE gave itself, is a Go constant, and the
	// wire never returns it; this is the name the MACHINE holds, is a wire fact delivered in
	// the pairing payload, and an owner can change it. paired_device's own note names the
	// distinction and reserves this verb by describing it. Folding them together would put a
	// constant and a wire fact behind one element, which is how a screen ends up rendering the
	// first while believing it has the second.
	"machine_name",

	// ADDED BY ADR-016 W2, additively and for the same reason "callbacks" (PB-BIND-6) is
	// here: a platform-wiring verb the Android app calls once at startup, not a control a
	// person presses, but an entry point all the same and one no other element covers.
	"platform_trust", // ADR-016 W2: installs the Android RelayTrust chain-trust delegate

	// ADDED BY ADR-015 WAVE R3 ROUND 4, additively and for the same reason S16's block
	// states. Both are entry points no other element covers, and both are the kind this list
	// exists to make undeletable -- a verb the shipped app must reach, whose absence is
	// invisible from Go.
	//
	// push.gateway_registration is a DIFFERENT server from push.token_register: that row is
	// the relay's token, this one is the gateway installation ADR-015 makes every WakeV1
	// depend on. A phone that told only the relay is silently unreachable by the new path.
	//
	// push.drop_diagnosis is not a control a person presses; it is what an operator reads
	// when the wake path is dead, and its whole content is the distinction between a machine
	// clock running ahead (every wake correctly refused, forever) and forged wakes.
	"push.gateway_registration",
	"push.drop_diagnosis",

	// ADDED BY WAVE R4 (ADR-018, bead agents-tracker-hggx.5), additively and for the
	// reason S16's block states: the list must GROW when the product does, or the reverse
	// check fires on every legitimately new element. mobile/r4_screencoverage_test.go
	// states the case for each and hard-codes them there too, so this list and that one
	// keep meeting in the middle. Every one is a control or fact a user touches on the R4
	// machine-switcher and global-inbox screens.
	"machines.list",              // ADR-018 MM3: the switcher's row set, four facts per row
	"machines.add",               // ADR-018 MM6: adds BESIDE existing pairings, never replaces
	"machines.select",            // ADR-018 MM3: feeds the deterministic least-recently-viewed policy
	"machines.forget",            // ADR-018 MM7: phone-side removal of exactly one pairing
	"inbox.global",               // ADR-018 MM4: rows keyed (machine_id, session_id), never folded
	"machines.connection_health", // ADR-018 MM5/MM8: one row's degradation degrades that row only
	"machines.stale_age",         // ADR-018 MM3: a parked row visibly shows its last-sync age
	"machines.recovery",          // ADR-018 MM8: recovery screens routed per machine

	// ADDED BY WAVE R5 (ADR-007 B144(b), bead agents-tracker-hggx.6), additively and for
	// the reason S16's block states. mobile/r5_launchpresets_test.go states the case for
	// each and hard-codes them there too, so this list and that one keep meeting in the
	// middle. The phone SELECTS and CONFIRMS a machine-authored preset; it never composes
	// argv, cwd, env, or options -- these two elements are that whole surface.
	"launch.presets", // playbook 4.3: the machine-published preset list the selection screen renders
	"launch.confirm", // playbook 4.3: one signed session_launch at the confirmed revision

	// ADDED BY WAVE R6 (Mirror M2.4, bead agents-tracker-hggx.7), additively and for the
	// reason S16's block states. mobile/r6_chatverbs_test.go states the case and pins the
	// verb's behaviour (live-only, structural refusals, the stale_turn class); the row's
	// note carries the disclosed view-side residual.
	"composer.send", // ADR-009 (8) / IS-LIFE-5: one structured message as the signed composer_send op

	// ADDED BY THE WAVE R6 REVIEW FIX-PACK (finding B6), additively and for the same reason.
	// ADR-014 §1 stated as an ACCEPTED DECISION that interaction_history and
	// interaction_detail "are gateway-routed", and the route did not exist at any layer:
	// internal/remotegw had no arm and no action constant, so a phone-issued read was
	// refused "unsupported command action", and M3.1/M3.3 were unreachable from a handset
	// while being marked GREEN. Building the gateway arm without a producer would have moved
	// the same unreachable claim one layer over, so the facade half is named here.
	"transcript.history", // ADR-014 / IS-CAP-2: page the transcript's past, and fetch one item's full body
}

type coverageRow struct {
	Element string
	Methods []string
	Req     string
	Note    string
	Line    int
}

func TestPBBIND3_EveryScreenElementHasAFacadeMethod(t *testing.T) {
	src := loadFacade(t)
	rows := loadCoverageTable(t, src.Dir)

	byElement := map[string]coverageRow{}
	for _, r := range rows {
		if prev, dup := byElement[r.Element]; dup {
			t.Errorf("screen_coverage.tsv:%d: element %q duplicated (first at line %d)", r.Line, r.Element, prev.Line)
		}
		byElement[r.Element] = r
	}

	for _, el := range requiredScreenElements {
		row, ok := byElement[el]
		if !ok {
			t.Errorf("PB-BIND-3 COVERAGE FAILURE: screen element %q has no row in screen_coverage.tsv", el)
			continue
		}
		if len(row.Methods) == 0 {
			t.Errorf("PB-BIND-3 COVERAGE FAILURE: screen element %q has no facade method (line %d)", el, row.Line)
		}
	}

	for _, r := range rows {
		known := false
		for _, el := range requiredScreenElements {
			if el == r.Element {
				known = true
				break
			}
		}
		if !known {
			t.Errorf("screen_coverage.tsv:%d: element %q is not in requiredScreenElements; either "+
				"the table invented a screen or the requirement list is stale", r.Line, r.Element)
		}
	}
}

func TestPBBIND3_EveryTracedMethodExistsOnTheFacade(t *testing.T) {
	src := loadFacade(t)
	rows := loadCoverageTable(t, src.Dir)

	have := map[string]bool{}
	for _, s := range exportedSurface(src) {
		switch s.Kind {
		case "func":
			have[s.Name] = true
		case "method", "field":
			have[s.Owner+"."+s.Name] = true
		}
	}

	for _, r := range rows {
		for _, m := range r.Methods {
			if !have[m] {
				t.Errorf("PB-BIND-3: screen_coverage.tsv:%d maps element %q to %q, which the facade "+
					"does not export", r.Line, r.Element, m)
			}
		}
	}
}

// TestPBBIND3_NoUntracedEntryPoint is the reverse direction. A bound surface that grows
// methods no screen asked for is how a facade acquires dead verbs and accidental
// secrets; PB-BIND-4 is the other half of the same discipline.
func TestPBBIND3_NoUntracedEntryPoint(t *testing.T) {
	src := loadFacade(t)
	rows := loadCoverageTable(t, src.Dir)

	traced := map[string]bool{}
	for _, r := range rows {
		for _, m := range r.Methods {
			traced[m] = true
		}
	}

	var untraced []string
	for _, s := range entryPoints(src) {
		key := s.Name
		if s.Kind == "method" {
			key = s.Owner + "." + s.Name
		}
		if !traced[key] {
			untraced = append(untraced, key)
		}
	}
	sort.Strings(untraced)
	if len(untraced) > 0 {
		t.Errorf("PB-BIND-3: %d exported entry point(s) appear in no screen_coverage.tsv row:\n\t%s",
			len(untraced), strings.Join(untraced, "\n\t"))
	}
}

// TestPBBIND3_FacadeCannotEnableTheKillSwitch is an adversarial reading of the
// "kill switch" element. protocol/server.go handleRemoteSetControl refuses the remote
// tier BEFORE consulting the backend, with the reason stated in the source: "a remote
// device must never re-enable a switch its owner turned off". A facade method that let
// the phone set it would be a surface-level bypass of that gate (PB-SEC-6), so the
// element is READ-only by construction and this pins it.
func TestPBBIND3_FacadeCannotEnableTheKillSwitch(t *testing.T) {
	src := loadFacade(t)

	banned := []string{"setkillswitch", "enablekillswitch", "setremotecontrol", "enableremotecontrol"}
	for _, s := range entryPoints(src) {
		low := strings.ToLower(s.Name)
		for _, b := range banned {
			if low == b {
				t.Errorf("PB-SEC-6/PB-BIND-3: the facade exports %s, which would let a stolen phone "+
					"re-enable remote control. remote_set_control is owner-tier only by design; the "+
					"phone may READ the kill-switch state and nothing more", s.Line())
			}
		}
	}
}

func loadCoverageTable(t *testing.T, dir string) []coverageRow {
	t.Helper()
	path := filepath.Join(dir, "screen_coverage.tsv")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("PB-BIND-3 requires a checked-in traceability table at %s: %v", path, err)
	}
	var rows []coverageRow
	for i, line := range strings.Split(string(raw), "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		cols := strings.Split(line, "\t")
		row := coverageRow{Element: strings.TrimSpace(cols[0]), Line: i + 1}
		if len(cols) > 1 {
			for _, m := range strings.Split(cols[1], ",") {
				if m = strings.TrimSpace(m); m != "" {
					row.Methods = append(row.Methods, m)
				}
			}
		}
		if len(cols) > 2 {
			row.Req = strings.TrimSpace(cols[2])
		}
		if len(cols) > 3 {
			row.Note = strings.TrimSpace(cols[3])
		}
		if row.Req == "" {
			t.Errorf("screen_coverage.tsv:%d: element %q names no requirement", row.Line, row.Element)
		}
		rows = append(rows, row)
	}
	if len(rows) == 0 {
		t.Fatalf("%s has no rows; the guard would be vacuous", path)
	}
	return rows
}
