package swarmmobile

// FAILING-FIRST (TDD RED, GG-5) for Wave R5 deliverables 2+4's FACADE half (bead
// agents-tracker-hggx.6): the phone SELECTS and CONFIRMS a machine-authored preset;
// it never composes argv, cwd, env, or options. The facade surface this file freezes:
//
//   - App.LaunchPresets() (*PresetList, error): the machine-authored preset list as
//     the screens render it -- PresetList.Count/At (the gomobile collection idiom of
//     MachineList/SessionList) over PresetInfo{ID, DisplayName, Provider,
//     WorkspacePath, Revision, Worktree bool}. The list is WHAT THE MACHINE PUBLISHED
//     and nothing else: a phone with nothing published renders an honest empty state
//     (ADR-007 B135's rule -- no Go-constant defaults dressed up as wire facts).
//   - App.SessionLaunch(presetID, presetRevision, prompt string) (*Op, error): one
//     signed session_launch op over the selected preset at the CONFIRMED revision.
//     The revision parameter is the staleness binding: the screen passes exactly what
//     it displayed, so an intervening machine-side edit answers stale_preset rather
//     than silently launching different policy.
//
// Refusals BEFORE composition (playbook :780-781 "offline targets ... refuse before
// argv composition" -- phone-side, the refusal precedes even the op's creation):
//
//   - offline target: ErrClassOffline, live-only exactly like Approve (ADR-007 B43:
//     no offline queue CAN exist for signed ops); nothing sealed, no durable send-seq
//     burnt, PendingOpCount stays 0.
//   - structurally invalid selection (empty preset id / revision):
//     ErrClassInvalidRequest before any signing.
//
// The screen-coverage ledger (PB-BIND-3) must name the new surface: rows for the
// preset list and the confirm verb, so coverage_test.go's bidirectional fence admits
// the two new facade entry points -- an exported verb no screen asked for is exactly
// what that fence exists to catch.
//
// This file must fail to compile (App.LaunchPresets / App.SessionLaunch undefined)
// until the GREEN slice adds the surface; the tsv test then fails behaviorally until
// the ledger rows exist.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestR5Facade_SessionLaunchOfflineIsRefusedBeforeAnythingIsComposed: approveApp's
// world -- machine destination and content key present, NO connection. The launch is
// refused with the offline class and nothing was created: no pending op, no burnt
// seq, nothing to replay later against a preset that may have changed meanwhile.
func TestR5Facade_SessionLaunchOfflineIsRefusedBeforeAnythingIsComposed(t *testing.T) {
	a := approveApp(t)

	op, err := a.SessionLaunch("preset-api", "rev-1", "fix the flaky test")
	if err == nil {
		t.Fatalf("SessionLaunch succeeded with no connection (op %+v); launch is live-only -- "+
			"an offline launch op re-sent later could land on a re-authored preset", op)
	}
	if !strings.Contains(err.Error(), ErrClassOffline) {
		t.Errorf("offline SessionLaunch error = %q, want the %s class so the screen renders the "+
			"offline state rather than a bug report", err, ErrClassOffline)
	}
	n, cerr := a.PendingOpCount()
	if cerr != nil {
		t.Fatalf("PendingOpCount: %v", cerr)
	}
	if n != 0 {
		t.Errorf("a refused offline launch left %d ops in flight, want 0: nothing may have been "+
			"sealed or queued", n)
	}
}

// TestR5Facade_SessionLaunchValidatesItsSelectionBeforeSigning: an empty preset id or
// an empty revision is a screen bug, refused ErrClassInvalidRequest -- the facade
// never signs a launch naming no preset or no confirmed revision (a revisionless
// launch would be un-refusable as stale, which is the whole staleness contract).
func TestR5Facade_SessionLaunchValidatesItsSelectionBeforeSigning(t *testing.T) {
	a := approveApp(t)

	for _, tc := range []struct{ name, id, rev string }{
		{"empty-preset-id", "", "rev-1"},
		{"empty-revision", "preset-api", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := a.SessionLaunch(tc.id, tc.rev, "")
			if err == nil {
				t.Fatalf("SessionLaunch(%q, %q) succeeded; want ErrClassInvalidRequest", tc.id, tc.rev)
			}
			if !strings.Contains(err.Error(), ErrClassInvalidRequest) {
				t.Errorf("SessionLaunch(%q, %q) error = %q, want the %s class", tc.id, tc.rev, err, ErrClassInvalidRequest)
			}
		})
	}
}

// TestR5Facade_LaunchPresetsListIsMachineAuthoredNotInvented: with nothing published
// by the machine, the preset list is EMPTY -- Count 0, no fabricated "default"
// preset. The empty state is the screens' to render honestly (the phone cannot know
// what the machine would allow).
func TestR5Facade_LaunchPresetsListIsMachineAuthoredNotInvented(t *testing.T) {
	a := approveApp(t)

	list, err := a.LaunchPresets()
	if err != nil {
		t.Fatalf("LaunchPresets: %v", err)
	}
	n, err := list.Count()
	if err != nil {
		t.Fatalf("PresetList.Count: %v", err)
	}
	if n != 0 {
		p, _ := list.At(0)
		t.Errorf("LaunchPresets returned %d presets on a phone the machine published none to; "+
			"first is %+v -- a preset the machine did not author must never be renderable, let "+
			"alone confirmable", n, p)
	}
}

// TestR5Facade_ScreenCoverageNamesTheLaunchPresetSurface: PB-BIND-3's ledger gains
// the two rows the launch screens need -- the preset list and the confirm verb --
// each naming its facade methods, so the bidirectional coverage fence traces the new
// surface instead of flagging it as untraced (or worse, never seeing it at all).
func TestR5Facade_ScreenCoverageNamesTheLaunchPresetSurface(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("screen_coverage.tsv"))
	if err != nil {
		t.Fatalf("read screen_coverage.tsv: %v", err)
	}
	tsv := string(raw)

	for _, want := range []struct{ element, method string }{
		{"launch.presets", "App.LaunchPresets"},
		{"launch.confirm", "App.SessionLaunch"},
	} {
		row := ""
		for _, line := range strings.Split(tsv, "\n") {
			if strings.HasPrefix(line, want.element+"\t") {
				row = line
				break
			}
		}
		if row == "" {
			t.Errorf("screen_coverage.tsv has no %q row; the launch UX's affordances must be "+
				"traced element -> facade method like every other screen's", want.element)
			continue
		}
		if !strings.Contains(row, want.method) {
			t.Errorf("screen_coverage.tsv row %q does not name %s in its methods column:\n%s",
				want.element, want.method, row)
		}
	}
}
