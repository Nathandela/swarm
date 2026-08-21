package schema

// WAVE R8 / SLICE S4 -- TerminalViewV1 ON THE WIRE, AND THE PROFILE THAT BOUNDS IT.
// Failing-first (TDD RED, GG-5).
//
// ADR-017 T4 rules the read path: "full coalesced snapshots, each carrying a monotonically
// increasing revision, the session instance, rows/columns, UTF-8 text, and a reset/resync
// marker", with "size, line and rate bounds declared in the remote profile (T5), so a phone
// knows the ceiling it is rendering under rather than discovering it".
//
// Amendment T4-a adds the field a revision alone cannot replace. The render loop is PER
// INVOCATION (`RenderTerminal` builds a fresh emulator on every call) and the gateway's
// watcher RE-RUNS it after every transport hiccup (`internal/remotegw/terminal_watcher.go:
// 149-165` loops `RunTerminal` forever with a backoff). A counter that restarts at 1 while
// the phone holds revision N makes the phone's "drop anything not greater" rule discard
// every subsequent snapshot -- and the user is left looking at a plausible, wrong, FROZEN
// screen with no error anywhere. So a snapshot carries a VIEW EPOCH minted per render-loop
// start as well as a revision monotonic within it, and the phone's rule is: differing epoch
// = hard reset; same epoch = strictly greater revision only.
//
// Amendment T5-a rules the zero values. No RemoteProfileV1 field carries omitempty and the
// production profile is built with three fields set and the rest zero (`cmd/swarm-remote/
// config.go:141-147`), so zero is what a phone actually receives today. It must read
// `terminal_view_version == 0` as "no fallback exists", `capability_record_version == 0` as
// "record untrusted", and any zero bound as "clamp to a conservative built-in" -- never as
// "unlimited", which is the reading that turns a wiring gap into an unbounded render.
//
// THE SEAMS (undefined symbols -> compile-fail RED):
//
//	type TerminalViewV1 struct {
//	    Session, SessionInstance string
//	    ViewEpoch, Revision      uint64
//	    Reset                    bool
//	    Cols, Rows               int
//	    Lines                    []string
//	    RenderedAt               time.Time
//	}
//	const CurrentTerminalViewVersion = 1
//	RemoteProfileV1 gains: TerminalViewMaxLineBytes, TerminalViewMaxRows, TerminalViewMaxRateHz
//	func (RemoteProfileV1) TerminalViewBounds() TerminalViewBounds  // zero -> conservative built-in
//	func (RemoteProfileV1) OffersTerminalView() bool                // version 0 -> false
//	func (RemoteProfileV1) TrustsCapabilityRecord() bool            // version 0 -> false

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"
)

// readSchemaSource reads one file of this package as text, for the two obligations that are
// about what a file SAYS rather than what it does.
func readSchemaSource(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(name)
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	return string(b)
}

// TestR8View_CarriesEpochRevisionInstanceAndReset is T4 + T4-a's shape, asserted as WIRE
// NAMES rather than as Go field names: the phone decodes these keys, and a rename that keeps
// the Go field would be invisible to every Go-side test but would blank the screen.
func TestR8View_CarriesEpochRevisionInstanceAndReset(t *testing.T) {
	v := TerminalViewV1{
		Session:         "sess1",
		SessionInstance: "inst-a",
		ViewEpoch:       7,
		Revision:        1,
		Reset:           true,
		Cols:            80,
		Rows:            24,
		Lines:           []string{"hello"},
		RenderedAt:      time.Unix(1755600000, 0).UTC(),
	}
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal TerminalViewV1: %v", err)
	}
	for _, key := range []string{
		`"session"`, `"session_instance"`, `"view_epoch"`, `"revision"`,
		`"reset"`, `"cols"`, `"rows"`, `"lines"`, `"rendered_at"`,
	} {
		if !strings.Contains(string(b), key) {
			t.Errorf("ADR-017 T4/T4-a: TerminalViewV1 does not carry %s on the wire: %s", key, b)
		}
	}
}

// TestR8View_RenderedAtIsPresentSoStalenessIsDerivable is amendment T4-b's evidence-honesty
// half, and it is a wire obligation rather than a UI one.
//
// "The fallback screen displays a staleness indicator derived from the snapshot's OWN AGE, so
// 'the machine went quiet' is never rendered as 'the terminal is idle'." A phone cannot
// derive that from arrival time: a replayed backlog arrives all at once, and a relay that
// held frames delivers old content at a new instant. The age has to come from the machine's
// own clock, on the snapshot.
func TestR8View_RenderedAtIsPresentSoStalenessIsDerivable(t *testing.T) {
	var decoded TerminalViewV1
	raw := `{"session":"s","session_instance":"i","view_epoch":1,"revision":1,"reset":true,` +
		`"cols":80,"rows":24,"lines":["x"],"rendered_at":"2026-08-20T10:00:00Z"}`
	if err := json.Unmarshal([]byte(raw), &decoded); err != nil {
		t.Fatalf("decode TerminalViewV1: %v", err)
	}
	if decoded.RenderedAt.IsZero() {
		t.Errorf("ADR-017 T4-b: TerminalViewV1 carries no machine-authored render time, so the phone " +
			"can only date a snapshot by when it ARRIVED. A replayed backlog arrives all at once and a " +
			"held relay delivers old content at a new instant, so arrival time renders a quiet machine " +
			"as an idle terminal -- the exact lie the staleness indicator exists to prevent.")
	}
}

// TestR8Profile_ZeroVersionMeansNoFallbackNotUnlimited is amendment T5-a.
//
// This is the one that a reader will want to argue with, so the argument is written down: a
// zero `terminal_view_version` is what the SHIPPING machine sends today, because
// `resolveGatewayParams` constructs `RemoteProfileV1` with three ADR-016 fields and leaves
// five zero. If a phone reads zero as "unversioned, therefore current", every machine in the
// field is retro-declared to support a view it has never rendered.
func TestR8Profile_ZeroVersionMeansNoFallbackNotUnlimited(t *testing.T) {
	var zero RemoteProfileV1
	if zero.OffersTerminalView() {
		t.Errorf("ADR-017 T5-a: a zero-valued profile claims to offer TerminalView. Zero is what the " +
			"production profile ships today (cmd/swarm-remote/config.go:141-147 sets three fields and " +
			"leaves the rest zero), so this reading retro-declares every deployed machine.")
	}
	if zero.TrustsCapabilityRecord() {
		t.Errorf("ADR-017 T5-a: a zero-valued profile trusts the capability record. An untrusted record " +
			"composes with T2-a into ONE predicate -- status card, both verbs refused.")
	}
	offering := RemoteProfileV1{Version: CurrentProfileVersion, TerminalViewVersion: CurrentTerminalViewVersion, CapabilityRecordVersion: 1}
	if !offering.OffersTerminalView() || !offering.TrustsCapabilityRecord() {
		t.Errorf("a profile that declares both versions must offer the view and trust the record, or R8 " +
			"ships a fallback nothing can reach")
	}
}

// TestR8Profile_ZeroBoundsClampToAConservativeBuiltIn is T5-a's second half. Zero must mean
// "clamp", never "unlimited": the bound's whole job is to tell a phone the ceiling it is
// rendering under BEFORE it renders, and "unlimited" is the answer that makes the bound
// decorative on exactly the machines that never set it.
func TestR8Profile_ZeroBoundsClampToAConservativeBuiltIn(t *testing.T) {
	var zero RemoteProfileV1
	b := zero.TerminalViewBounds()
	if b.MaxLineBytes <= 0 {
		t.Errorf("ADR-017 T5-a: a zero max-line-bytes bound resolved to %d. Zero means clamp to a "+
			"conservative built-in; a non-positive ceiling is 'unlimited' wearing a number.", b.MaxLineBytes)
	}
	if b.MaxRows <= 0 {
		t.Errorf("ADR-017 T5-a: a zero max-rows bound resolved to %d; want a conservative built-in", b.MaxRows)
	}
	if b.MaxRateHz <= 0 {
		t.Errorf("ADR-017 T5-a: a zero rate bound resolved to %d. The append budget is 8/s COMBINED per "+
			"target (ADR-009:156-165) and the terminal stream spends from it, so an unbounded rate is a "+
			"budget the journal then loses.", b.MaxRateHz)
	}
	// A machine that DOES declare bounds is believed, but never above the built-in ceiling:
	// the profile is machine-authored and a compromised or skewed machine must not be able to
	// raise the phone's own ceiling by declaring a larger one.
	huge := RemoteProfileV1{TerminalViewMaxLineBytes: 1 << 30, TerminalViewMaxRows: 1 << 20, TerminalViewMaxRateHz: 1000}
	hb := huge.TerminalViewBounds()
	if hb.MaxLineBytes > b.MaxLineBytes || hb.MaxRows > b.MaxRows || hb.MaxRateHz > b.MaxRateHz {
		t.Errorf("ADR-017 T5/T5-a: a machine-declared bound RAISED the phone's ceiling (%+v vs built-in "+
			"%+v). The profile tells a phone the machine's ceiling; the phone's own ceiling is not the "+
			"machine's to move, or a compromised machine grants itself an unbounded render.", hb, b)
	}
}

// TestR8Profile_NoTerminalViewFieldCarriesOmitempty is the same rule RemoteProfileV1 already
// states for itself, extended to the fields this wave adds: "an absent key must stay
// distinguishable from a legitimately-zero one" (profile.go:22-24). With T5-a reading zero as
// fail-closed, that distinction is what separates "this machine has no fallback" from "this
// machine did not send a profile at all".
func TestR8Profile_NoTerminalViewFieldCarriesOmitempty(t *testing.T) {
	b, err := json.Marshal(RemoteProfileV1{})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	for _, key := range []string{
		`"terminal_view_max_line_bytes"`, `"terminal_view_max_rows"`, `"terminal_view_max_rate_hz"`,
	} {
		if !strings.Contains(string(b), key) {
			t.Errorf("ADR-017 T5: %s is absent from a zero-valued profile (%s), which means it carries "+
				"omitempty. No field on this struct may: it rides EVERY reconcile and a phone routes on it.",
				key, b)
		}
	}
}

// TestR8Profile_StaleCommentAboutADR016IsRepaired is the drive-by D-ZERO names, and it is a
// correctness obligation rather than tidying: profile.go:22-23 tells the next reader that
// ADR-016's three fields "are not yet declared here", nineteen lines above the three fields.
// A reader who believes it adds them a second time.
func TestR8Profile_StaleCommentAboutADR016IsRepaired(t *testing.T) {
	src := readSchemaSource(t, "profile.go")
	if strings.Contains(src, "are not yet declared here") {
		t.Errorf("profile.go still says ADR-016's three fields \"are not yet declared here\", nineteen " +
			"lines above RelayTLSPolicy/RelayHost/RelaySPKIPin. A comment that contradicts the struct it " +
			"documents is how a field gets added twice.")
	}
}
