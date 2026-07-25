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
	// pairing (QR decode, SAS, confirm, cancel)
	"pairing.qr_decode",
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
	"callbacks",           // PB-BIND-6's delivery plane
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
