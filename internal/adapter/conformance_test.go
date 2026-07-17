package adapter

// E9.1 / E9.2 / T-1 — the conformance SUITE that any adapter must pass. This is
// the T-1 FREEZE: getting this boundary right before any real CLI adapter (T-1
// Claude, Codex ...) is the whole point of Epic 9. A conformance suite is only
// worth anything if it has TEETH — it must PASS a conformant adapter and FAIL a
// defective one — so this file drives the harness against baseAdapter (passes)
// and against a battery of single-defect violators (each must fail on its own
// rule).
//
// FROZEN-API REFINEMENT (pinned by the test designer, see final report):
// the frozen `func Conformance(t *testing.T, a Adapter)` is split into
//
//	func CheckConformance(a Adapter) []error   // PURE: returns every violation
//	func Conformance(t *testing.T, a Adapter)  // thin *testing.T wrapper
//
// so the harness itself is unit-testable (assert the returned []error) without
// faking *testing.T. Conformance delegates to CheckConformance and surfaces
// each error via t.Error; real adapters (Epic 10/11) call Conformance from
// their package tests. CheckConformance holds ONLY portable, in-process checks
// (determinism, argv shape, schema self-consistency, signal kinds, resume
// consistency, extract totality). The syscall-level "opens no fd/socket" and
// import-boundary checks are inherently CI/review checks and live in the test
// layer (fd/goroutine neutrality below; source-grep + import-list in
// refadapter/refadapter_test.go) — see the review-only enumeration at the foot
// of this file.

import (
	"os"
	"runtime"
	"strings"
	"testing"
)

// errsContain reports whether any error's text contains sub (case-insensitive).
// The conformance contract is that each violation names its failing method or
// field in human-readable text; tests key off that keyword, not an exact string.
func errsContain(errs []error, sub string) bool {
	sub = strings.ToLower(sub)
	for _, e := range errs {
		if e != nil && strings.Contains(strings.ToLower(e.Error()), sub) {
			return true
		}
	}
	return false
}

// TestConformance_AcceptsConformantAdapter — the harness must find ZERO
// violations in a fully conformant strategy object.
func TestConformance_AcceptsConformantAdapter(t *testing.T) {
	if errs := CheckConformance(baseAdapter{}); len(errs) != 0 {
		t.Fatalf("conformant adapter reported %d violation(s): %v", len(errs), errs)
	}
}

// TestConformance_Wrapper_PassesForGoodAdapter — Conformance(t, good) must not
// fail the test. It runs in a subtest so a (wrong) failure is contained and
// reported rather than aborting the whole file.
func TestConformance_Wrapper_PassesForGoodAdapter(t *testing.T) {
	ok := t.Run("good", func(st *testing.T) { Conformance(st, baseAdapter{}) })
	if !ok {
		t.Error("Conformance() failed a conformant adapter")
	}
}

// TestConformance_RejectsViolations — each single-defect adapter must produce
// at least one violation naming its broken rule. This is the teeth test: a
// harness that green-lights any of these is worthless.
func TestConformance_RejectsViolations(t *testing.T) {
	cases := []struct {
		name    string
		adapter Adapter
		keyword string // substring the violation message must contain
	}{
		{"empty-name", emptyName{}, "name"},
		{"unstable-name", &unstableName{}, "name"},
		{"empty-binary", emptyBinary{}, "binary"},
		{"nil-version-args", nilVersionArgs{}, "versionargs"},
		{"parseversion-panics", panicParseVersion{}, "parseversion"},
		{"parseversion-nondeterministic", &nondeterministicParseVersion{}, "determin"},
		{"parseversion-ok-empty", okEmptyParseVersion{}, "parseversion"},
		{"shell-argv0", shellCommand{}, "shell"},
		{"env-shell-router", envShellCommand{}, "shell"},
		{"shell-as-later-arg", shellAsLaterArgCommand{}, "shell"},
		{"single-string-argv", singleStringCommand{}, "argv"},
		{"empty-command", emptyCommand{}, "command"},
		{"nondeterministic-command", &nondeterministicCommand{}, "determin"},
		{"required-default-invalid", badDefaultOption{}, "default"},
		{"choice-without-choices", emptyChoiceOption{}, "choice"},
		{"duplicate-option-key", dupKeyOption{}, "key"},
		{"bad-signal-kind", badSignalKind{}, "kind"},
		{"resume-without-id", resumeWithoutID{}, "resume"},
		{"resume-omits-id", resumeOmitsID{}, "resume"},
		{"extract-panics", panicExtract{}, "extract"},
		{"extract-panics-non-nil-grid", panicOnNonNilGrid{}, "extract"},
		{"extract-ok-empty", okButEmptyExtract{}, "extract"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			errs := CheckConformance(tc.adapter)
			if len(errs) == 0 {
				t.Fatalf("%s: harness reported NO violation (expected one about %q)", tc.name, tc.keyword)
			}
			if !errsContain(errs, tc.keyword) {
				t.Errorf("%s: violations %v do not mention %q", tc.name, errs, tc.keyword)
			}
		})
	}
}

// TestCheckConformance_ExtractTotalityIsProbed — a conformant extractor must be
// probed on the empty/garbage inputs the contract promises it survives; the
// harness proves it does so by NOT flagging baseAdapter (which is total) while
// flagging panicExtract. This guards against a harness that skips the totality
// probe entirely.
func TestCheckConformance_ExtractTotalityIsProbed(t *testing.T) {
	if errsContain(CheckConformance(baseAdapter{}), "extract") {
		t.Error("total extractor was flagged")
	}
	if !errsContain(CheckConformance(panicExtract{}), "extract") {
		t.Error("panicking extractor was NOT flagged — totality probe missing")
	}
}

// TestCheckConformance_ExtractTotalityProbesNonNilGrid — the totality probe must
// feed a NON-NIL grid, not only nil. panicOnNonNilGrid survives every nil-grid
// call but panics the instant it touches &vt.Snap{}; a harness that only ever
// passed nil would green-light it. This is the FIX-B grid-axis regression guard.
func TestCheckConformance_ExtractTotalityProbesNonNilGrid(t *testing.T) {
	if !errsContain(CheckConformance(panicOnNonNilGrid{}), "extract") {
		t.Error("extractor that panics on a non-nil grid was NOT flagged — the totality probe never feeds a non-nil grid")
	}
}

// TestCheckConformance_CommandShellScanCoversAllArgv — the shell check must scan
// EVERY argv element, not just argv[0]: an `env sh -c ...` router and a shell
// dropped into a later arg both route the command through a shell and must be
// rejected. This is the FIX-B argv-scan regression guard.
func TestCheckConformance_CommandShellScanCoversAllArgv(t *testing.T) {
	for _, tc := range []struct {
		name string
		a    Adapter
	}{
		{"env-sh-c", envShellCommand{}},
		{"shell-as-later-arg", shellAsLaterArgCommand{}},
	} {
		if !errsContain(CheckConformance(tc.a), "shell") {
			t.Errorf("%s: a shell reachable past argv[0] was NOT flagged", tc.name)
		}
	}
	if errsContain(CheckConformance(baseAdapter{}), "shell") {
		t.Error("a shell-free adapter was flagged")
	}
}

// TestCheckConformance_DetectDescriptorsAreProbed — the descriptor checks have
// teeth: a total conformant adapter passes, while an empty Binary, nil
// VersionArgs, and a panicking/non-deterministic/ok-empty ParseVersion are each
// flagged. This proves detection purity is enforced through descriptors now that
// Detect is a core function, not an adapter method.
func TestCheckConformance_DetectDescriptorsAreProbed(t *testing.T) {
	if errsContain(CheckConformance(baseAdapter{}), "binary") ||
		errsContain(CheckConformance(baseAdapter{}), "versionargs") ||
		errsContain(CheckConformance(baseAdapter{}), "parseversion") {
		t.Error("conformant descriptors were flagged")
	}
	if !errsContain(CheckConformance(emptyBinary{}), "binary") {
		t.Error("empty Binary() not flagged")
	}
	if !errsContain(CheckConformance(nilVersionArgs{}), "versionargs") {
		t.Error("nil VersionArgs() not flagged")
	}
	if !errsContain(CheckConformance(panicParseVersion{}), "parseversion") {
		t.Error("panicking ParseVersion not flagged — totality probe missing")
	}
}

// TestConformance_Command_NoFdOrGoroutineLeak — a "no-fd-ownership" purity
// SIGNAL (E9.2, implicit "adapters own no fds/sockets"). We cannot intercept
// syscalls in-process, so we assert resource-neutrality: composing argv many
// times must leave the open-fd count exactly unchanged and must not leak
// goroutines. This catches an adapter that opens a file/socket in Command; it
// is a signal, not a proof (an open+close within one call is invisible). The
// proof-grade check is the source grep in refadapter/refadapter_test.go.
func TestConformance_Command_NoFdOrGoroutineLeak(t *testing.T) {
	a := baseAdapter{}
	spec := LaunchSpec{Cwd: "/w", Options: map[string]string{"model": "smart"}, InitialPrompt: "hi"}

	runtime.GC()
	fd0 := openFDCount(t)
	g0 := runtime.NumGoroutine()

	for i := 0; i < 2000; i++ {
		if _, err := a.Command(spec); err != nil {
			t.Fatalf("Command error on iteration %d: %v", i, err)
		}
	}

	runtime.GC()
	fd1 := openFDCount(t)
	if fd1 != fd0 {
		t.Errorf("open fd count changed across 2000 Command calls: %d -> %d (an adapter must open no fd/socket)", fd0, fd1)
	}
	if g1 := runtime.NumGoroutine(); g1 > g0 {
		t.Errorf("goroutine count grew across 2000 Command calls: %d -> %d (Command must not spawn background work)", g0, g1)
	}
}

// TestConformance_GoroutineSafe_ParallelInvocation — adapters are documented as
// goroutine-safe strategy objects. Under -race, hammering the pure methods from
// many goroutines surfaces any shared mutable state as a data race, and every
// goroutine must agree on the (deterministic) result.
func TestConformance_GoroutineSafe_ParallelInvocation(t *testing.T) {
	a := baseAdapter{}
	spec := LaunchSpec{Cwd: "/w", Options: map[string]string{"model": "smart"}}
	want, err := a.Command(spec)
	if err != nil {
		t.Fatalf("Command: %v", err)
	}

	const workers = 32
	done := make(chan []string, workers)
	for i := 0; i < workers; i++ {
		go func() {
			got, _ := a.Command(spec)
			_, _ = a.ExtractConversationID(nil, []byte("conv-id=zzz"))
			_ = a.Options()
			_ = a.SignalSources()
			done <- got
		}()
	}
	for i := 0; i < workers; i++ {
		got := <-done
		if strings.Join(got, "\x00") != strings.Join(want, "\x00") {
			t.Errorf("Command not deterministic across goroutines: %v != %v", got, want)
		}
	}
}

// openFDCount returns the number of open file descriptors for this process.
// /dev/fd is present on darwin and linux and lists one entry per open fd; the
// entry created for reading the directory itself cancels out because it is
// opened and closed identically on both sides of the measurement.
func openFDCount(t *testing.T) int {
	t.Helper()
	entries, err := os.ReadDir("/dev/fd")
	if err != nil {
		t.Skipf("cannot read /dev/fd (%v); fd-neutrality check unavailable on this platform", err)
	}
	return len(entries)
}

// ---------------------------------------------------------------------------
// REVIEW-ONLY / CI checks (enumerated, per orchestrator brief) — these cannot
// be enforced purely in-process and are covered elsewhere or by human review:
//
//   1. "Command/Resume open no os.Open/os.Create/net.Dial/net.Listen": true
//      syscall interception is not possible in-process. Approximated here by
//      fd/goroutine neutrality, and enforced by an automated SOURCE GREP over
//      the adapter package's non-test files in
//      refadapter/refadapter_test.go (E9.2: zero hits).
//   2. "Adapter imports only the contract + internal/vt, never
//      daemon/shim/wire/tui": an IMPORT-LIST assertion (go list -deps) in
//      refadapter/refadapter_test.go (T-5 / E9.5).
//   3. "argv is semantically correct for the real CLI": that is
//      CHARACTERIZATION (T-6), not conformance — it is proven against recorded
//      fixtures, not the interface.
// ---------------------------------------------------------------------------
