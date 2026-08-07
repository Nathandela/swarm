package adapter

// Conformance is the T-1 behavioral freeze: the portable, in-process checks any
// Adapter must satisfy. CheckConformance is PURE — it returns every violation as
// an error whose text names the failing method or field — so the harness itself
// is unit-testable. Conformance is the thin *testing.T wrapper real adapters
// call from their package tests.
//
// The checks here are exactly those provable in-process against the interface:
// Name non-emptiness/stability, the pure detection descriptors (Binary,
// VersionArgs, ParseVersion), argv shape + determinism, the options schema's
// self-consistency, signal-source kinds, resume consistency, and
// ExtractConversationID totality. The goroutine-neutrality and
// parallel-determinism checks (against the adapter under test) live in the
// Conformance wrapper since they need *testing.T; the syscall-level "opens no
// fd/socket" fd-count signal and the import-boundary checks are inherently
// CI/review checks and live in the test layer (fd-count in conformance_test.go,
// the source grep in boundary_test.go and refadapter/refadapter_test.go).
//
// ADR-010 adds the OPTIONAL structured-capture obligations at the foot of this
// file. They run inside CheckConformance, so every adapter already calling
// Conformance(t, a) gets them for free and an adapter that opts out of capture
// entirely passes them trivially.

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"github.com/Nathandela/swarm/internal/vt"
)

// probeConversationID is the id CheckConformance feeds Resume/Extract while
// probing; it is arbitrary but fixed so the checks are deterministic.
const probeConversationID = "conformance-probe-id"

// shellPrograms are argv basenames that mean "this argv would be interpreted by
// a shell" — the exact thing an adapter must never do (core exec's argv
// directly, never through a shell).
var shellPrograms = map[string]bool{
	"sh": true, "bash": true, "zsh": true, "dash": true, "ash": true,
	"ksh": true, "csh": true, "tcsh": true, "fish": true, "busybox": true,
	"cmd": true, "cmd.exe": true, "powershell": true, "powershell.exe": true,
	"pwsh": true, "pwsh.exe": true,
}

// shellMetachars are the bytes whose presence in argv[0] means it is a
// shell-interpretable string rather than a bare program path.
const shellMetachars = " \t\r\n&|;<>$`()"

// Conformance runs the conformance suite against a and reports each violation
// via t.Error. It is the entrypoint real adapters call from their tests. Beyond
// the pure CheckConformance rules it runs the goroutine-neutrality and
// parallel-determinism checks against THIS adapter (not a fixed baseline), so a
// real adapter that leaks a goroutine or races on shared state is caught here.
// Both use only runtime introspection — no fd/exec — so the contract package
// stays I/O-free (the fd-count signal, which needs /dev/fd, is a test-layer
// check in conformance_test.go).
func Conformance(t *testing.T, a Adapter) {
	t.Helper()
	for _, err := range CheckConformance(a) {
		t.Error(err)
	}
	checkGoroutineNeutral(t, a)
	checkParallelDeterministic(t, a)
}

// CheckConformance returns every conformance violation in a, or an empty slice
// for a fully conformant adapter. It is pure: it calls only the adapter's pure
// methods, opens nothing, and mutates no shared state. Every check runs against
// the ADAPTER UNDER TEST (a), so a real adapter's own extractor/descriptors are
// probed, not a fixed baseline.
func CheckConformance(a Adapter) []error {
	var errs []error
	errs = append(errs, checkName(a)...)
	errs = append(errs, checkDetect(a)...)
	errs = append(errs, checkCommand(a)...)
	errs = append(errs, checkOptions(a)...)
	errs = append(errs, checkSignalSources(a)...)
	errs = append(errs, checkResume(a)...)
	errs = append(errs, checkExtract(a)...)
	errs = append(errs, checkInteractionSource(a)...)
	return errs
}

// checkName: Name is non-empty and stable across calls.
func checkName(a Adapter) []error {
	n1 := a.Name()
	if n1 == "" {
		return []error{fmt.Errorf("Name() is empty; an adapter's name must be non-empty")}
	}
	if n2 := a.Name(); n2 != n1 {
		return []error{fmt.Errorf("Name() is not stable: %q then %q", n1, n2)}
	}
	return nil
}

// checkDetect: the pure detection descriptors are well-formed — Binary is
// non-empty, VersionArgs is non-nil, and ParseVersion is PURE + TOTAL (never
// panics on any string, deterministic, and ok implies a non-empty version).
// Detection I/O itself is the CORE Detect function's job (driven by a
// HostProber); the adapter only supplies these descriptors.
func checkDetect(a Adapter) []error {
	var errs []error
	if a.Binary() == "" {
		errs = append(errs, fmt.Errorf("Binary() is empty; an adapter must name the executable to detect on PATH"))
	}
	if strings.ContainsAny(a.Binary(), shellMetachars) {
		errs = append(errs, fmt.Errorf("Binary() %q must be a bare executable name, not a shell-interpretable string", a.Binary()))
	}
	if a.VersionArgs() == nil {
		errs = append(errs, fmt.Errorf("VersionArgs() is nil; declare the args that print the version (an empty slice is allowed, nil is not)"))
	}
	for _, out := range parseVersionProbes {
		v1, ok1, panicked := parseVersionSafe(a, out)
		if panicked {
			errs = append(errs, fmt.Errorf("ParseVersion panicked on %q; it must be total (never panic on any string)", truncate(out)))
			continue
		}
		if ok1 && v1 == "" {
			errs = append(errs, fmt.Errorf("ParseVersion returned ok with an empty version on %q; ok must imply a non-empty version", truncate(out)))
		}
		v2, ok2, _ := parseVersionSafe(a, out)
		if v1 != v2 || ok1 != ok2 {
			errs = append(errs, fmt.Errorf("ParseVersion is not deterministic on %q: (%q,%v) then (%q,%v)", truncate(out), v1, ok1, v2, ok2))
		}
	}
	return errs
}

// parseVersionProbes is the garbage/edge-input battery ParseVersion must survive
// without panicking — the in-suite totality proof for arbitrary adapters (the
// real extractor is additionally fuzzed in refadapter). It mixes empty,
// truncated, non-ASCII, and unbounded inputs plus realistic version banners.
var parseVersionProbes = []string{
	"",
	" ",
	".",
	"v",
	"1",
	"1.",
	".2.3",
	"1.2.3",
	"v0.0.0-rc1",
	"claude 1.2.3 (build 456)",
	"no version at all",
	"多字节 バージョン 1.2.3",
	"\x00\xff\x1b[garbage",
	strings.Repeat("9", 4096),
}

// parseVersionSafe calls ParseVersion under a recover so a panicking parser is
// reported as a violation rather than crashing the suite.
func parseVersionSafe(a Adapter, out string) (version string, ok, panicked bool) {
	defer func() {
		if r := recover(); r != nil {
			panicked = true
		}
	}()
	version, ok = a.ParseVersion(out)
	return
}

// truncate bounds a probe string in an error message so an unbounded input does
// not produce an unbounded message.
func truncate(s string) string {
	const max = 48
	if len(s) > max {
		return s[:max] + "..."
	}
	return s
}

// checkCommand: argv is non-empty, argv[0] is a bare program, NO argv element is
// a shell (so the command cannot be routed through a shell at any position), and
// Command is deterministic.
func checkCommand(a Adapter) []error {
	spec := LaunchSpec{
		Cwd:           "/conformance/cwd",
		Options:       probeOptions(a.Options()),
		InitialPrompt: "probe",
	}
	argv, err := a.Command(spec)
	if err != nil {
		return []error{fmt.Errorf("Command() returned an error on a valid spec: %w", err)}
	}
	var errs []error
	if len(argv) == 0 {
		errs = append(errs, fmt.Errorf("Command() returned an empty argv; it must return at least the program (the command)"))
		return errs // nothing more to inspect
	}
	// argv[0] is the program path: it must be a bare path, not itself a
	// shell-interpretable command string carrying metacharacters.
	if strings.ContainsAny(argv[0], shellMetachars) {
		errs = append(errs, fmt.Errorf("Command() argv[0] %q must be a bare program path, not a shell-interpretable argv string", argv[0]))
	}
	// No argv element may be a shell. Core exec's argv directly, so a shell at
	// ANY position — argv[0], an `env sh -c` router, or a shell dropped into a
	// later arg — means the command would be interpreted by a shell.
	for i, arg := range argv {
		if shellPrograms[filepath.Base(arg)] {
			errs = append(errs, fmt.Errorf("Command() argv[%d] %q is a shell; argv routes through a shell but must be exec'd directly", i, arg))
		}
	}
	if argv2, err2 := a.Command(spec); err2 != nil || !equalArgv(argv, argv2) {
		errs = append(errs, fmt.Errorf("Command() is not deterministic: %v then %v (err %v)", argv, argv2, err2))
	}
	return errs
}

// checkOptions: the options schema is self-consistent — unique non-empty keys,
// choice options carry Choices, and a declared Default is valid for a choice.
func checkOptions(a Adapter) []error {
	var errs []error
	seen := make(map[string]bool)
	for _, o := range a.Options() {
		if o.Key == "" {
			errs = append(errs, fmt.Errorf("Options(): an option has an empty key"))
			continue
		}
		if seen[o.Key] {
			errs = append(errs, fmt.Errorf("Options(): duplicate option key %q", o.Key))
		}
		seen[o.Key] = true
		if o.Type == "choice" && len(o.Choices) == 0 {
			errs = append(errs, fmt.Errorf("Options(): option %q has Type \"choice\" but no Choices", o.Key))
		}
		if o.Type == "choice" && o.Default != "" && !contains(o.Choices, o.Default) {
			errs = append(errs, fmt.Errorf("Options(): option %q has Default %q, which is not one of its Choices", o.Key, o.Default))
		}
	}
	return errs
}

// checkSignalSources: every declared source Kind is one of hook/event/heuristic.
func checkSignalSources(a Adapter) []error {
	var errs []error
	for _, s := range a.SignalSources() {
		switch s.Kind {
		case "hook", "event", "heuristic":
		default:
			errs = append(errs, fmt.Errorf("SignalSources(): invalid Kind %q (want hook|event|heuristic)", s.Kind))
		}
	}
	return errs
}

// checkResume: Resume with no id yields an empty argv, and a non-empty resume
// argv always carries the id it is resuming.
func checkResume(a Adapter) []error {
	var errs []error
	if argv, err := a.Resume(ResumeSpec{Cwd: "/conformance/cwd"}); err != nil {
		errs = append(errs, fmt.Errorf("Resume() returned an error for an empty id: %w", err))
	} else if len(argv) != 0 {
		errs = append(errs, fmt.Errorf("Resume() returned a non-empty argv %v with no ConversationID; it must resume nothing", argv))
	}
	argv, err := a.Resume(ResumeSpec{Cwd: "/conformance/cwd", ConversationID: probeConversationID})
	if err != nil {
		errs = append(errs, fmt.Errorf("Resume() returned an error for a valid id: %w", err))
	} else if len(argv) != 0 && !contains(argv, probeConversationID) {
		errs = append(errs, fmt.Errorf("Resume() argv %v omits the ConversationID %q it is meant to resume", argv, probeConversationID))
	}
	return errs
}

// checkExtract: ExtractConversationID is total (never panics) and ok==true
// implies a non-empty id. Totality is probed across the FULL grid domain — a nil
// grid, a non-nil zero-value grid (&vt.Snap{}, whose Lines is nil), and a
// populated grid — crossed with degenerate/garbage tails. A nil-only probe would
// green-light an extractor that panics the moment it touches a real grid.
func checkExtract(a Adapter) []error {
	var errs []error
	tails := [][]byte{
		nil,
		{},
		[]byte("\x00\xff not an id"),
		[]byte("conv-id=" + probeConversationID),
	}
	grids := []*vt.Snap{
		nil,
		{}, // non-nil but zero-value: Lines is nil
		{Version: 1, Cols: 3, Rows: 1, CursorVisible: true, Lines: []vt.Line{{Runs: []vt.Run{{Text: "a", Width: 1}, {Text: "b", Width: 1}, {Text: "c", Width: 1}}}}},
	}
	for gi, grid := range grids {
		for _, tail := range tails {
			id, ok, panicked := extractSafe(a, grid, tail)
			if panicked {
				errs = append(errs, fmt.Errorf("ExtractConversationID panicked on grid #%d, tail %q; it must be total on any grid (nil, empty, or populated)", gi, tail))
				continue
			}
			if ok && id == "" {
				errs = append(errs, fmt.Errorf("ExtractConversationID returned ok with an empty id on grid #%d, tail %q; ok must imply a non-empty id", gi, tail))
			}
		}
	}
	return errs
}

// extractSafe calls ExtractConversationID under a recover so a panicking
// extractor is reported as a violation rather than crashing the suite.
func extractSafe(a Adapter, grid *vt.Snap, tail []byte) (id string, ok, panicked bool) {
	defer func() {
		if r := recover(); r != nil {
			panicked = true
		}
	}()
	id, ok = a.ExtractConversationID(grid, tail)
	return
}

// checkGoroutineNeutral asserts composing argv many times leaks no goroutine —
// a "Command spawns no background work / owns no lifecycle" signal (E9.2). It
// runs against the passed adapter.
func checkGoroutineNeutral(t *testing.T, a Adapter) {
	t.Helper()
	spec := LaunchSpec{Cwd: "/conformance/cwd", Options: probeOptions(a.Options()), InitialPrompt: "probe"}
	runtime.GC()
	g0 := runtime.NumGoroutine()
	for i := 0; i < 2000; i++ {
		if _, err := a.Command(spec); err != nil {
			t.Fatalf("Command error on iteration %d: %v", i, err)
		}
	}
	runtime.GC()
	if g1 := runtime.NumGoroutine(); g1 > g0 {
		t.Errorf("goroutine count grew across 2000 Command calls: %d -> %d (Command must spawn no background work)", g0, g1)
	}
}

// checkParallelDeterministic hammers the pure methods from many goroutines: under
// -race this surfaces shared mutable state, and every goroutine must agree on the
// deterministic argv. It runs against the passed adapter.
func checkParallelDeterministic(t *testing.T, a Adapter) {
	t.Helper()
	spec := LaunchSpec{Cwd: "/conformance/cwd", Options: probeOptions(a.Options())}
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
			_, _ = a.ParseVersion("probe 1.2.3")
			done <- got
		}()
	}
	for i := 0; i < workers; i++ {
		if got := <-done; !equalArgv(got, want) {
			t.Errorf("Command not deterministic across goroutines: %v != %v", got, want)
		}
	}
}

// probeOptions builds a representative option map from an adapter's schema so a
// generic Command probe has plausible values (a declared default, else the first
// choice, else empty).
func probeOptions(opts []OptionSpec) map[string]string {
	m := make(map[string]string, len(opts))
	for _, o := range opts {
		switch {
		case o.Default != "":
			m[o.Key] = o.Default
		case o.Type == "choice" && len(o.Choices) > 0:
			m[o.Key] = o.Choices[0]
		default:
			m[o.Key] = ""
		}
	}
	return m
}

// equalArgv reports whether two argv slices are element-wise equal.
func equalArgv(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// contains reports whether s contains v.
func contains(s []string, v string) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------------------
// ADR-010 — the structured-capture conformance obligations.
// ---------------------------------------------------------------------------

// interactionProbes is the body battery Interactions must survive (ADR-010
// conformance obligation 1): a nil body, no body at all, an empty object, a
// truncated body, garbage bytes, a deeply nested body, an oversized body, and
// realistic shaped bodies. It is the Interactions analogue of parseVersionProbes.
var interactionProbes = []HookPayload{
	{},
	{Event: "UserPromptSubmit"},
	{Event: "UserPromptSubmit", Raw: json.RawMessage(``)},
	{Event: "UserPromptSubmit", Raw: json.RawMessage(`{}`)},
	{Event: "UserPromptSubmit", Raw: json.RawMessage(`{"prompt":"probe"}`), ReceivedAtMs: 1},
	{Event: "PreToolUse", Raw: json.RawMessage(`{"tool":"Read","path":"a.go","ref":"t-1"}`)},
	{Event: "PermissionRequest", Raw: json.RawMessage(`{"tool":"Read","ref":"req-1"}`)},
	{Event: "PermissionRequest", Raw: json.RawMessage(`{"tool":"Bash","ref":"bash-1"}`)},
	{Event: "PreToolUse", Raw: json.RawMessage(`{"tool_input":{`)},
	{Event: "\x00\xff", Raw: json.RawMessage("\x00\xff not json")},
	{Event: "PreToolUse", Raw: json.RawMessage(deeplyNestedBody)},
	{Event: "UserPromptSubmit", Raw: json.RawMessage(oversizedBody)},
}

// deeplyNestedBody and oversizedBody are the two unbounded-input probes. The
// oversized one is valid JSON well past interaction-schema.md §5's 8 KiB
// MaxItemBytes — the adapter must not choke on it, and it is the DAEMON that
// truncates (ADR-010 §3).
var (
	deeplyNestedBody = `{"a":` + strings.Repeat("[", 256) + strings.Repeat("]", 256) + `}`
	oversizedBody    = `{"prompt":"` + strings.Repeat("x", 64<<10) + `"}`
)

// checkInteractionSource runs ADR-010's conformance obligations 1, 3 and 5 — the
// half decidable from the interface alone (obligation 3's corpus half is
// CheckInteractionFixture; obligation 2 is the source grep in boundary_test.go).
//
// An adapter that declares no capture and implements no InteractionSource is
// fully conformant and this returns nothing: the extension is OPTIONAL and the
// generic fallback is a supported path, never a defect (ADR-010 §5).
func checkInteractionSource(a Adapter) []error {
	var errs []error
	declared := 0
	for i, s := range a.SignalSources() {
		mode, ok := s.Descriptor[DescriptorCapture]
		if !ok {
			continue
		}
		declared++
		if mode != CaptureRaw {
			errs = append(errs, fmt.Errorf("SignalSources()[%d]: capture %q is not a defined capture mode (want %q); an unrecognized value would silently flatten the body away", i, mode, CaptureRaw))
		}
		if s.Descriptor[descriptorEvent] == "" {
			errs = append(errs, fmt.Errorf("SignalSources()[%d]: declares capture but names no %q; every declared capture key must name a real event row", i, descriptorEvent))
		}
	}

	src, isSource := AsInteractionSource(a)
	switch {
	case declared > 0 && !isSource:
		errs = append(errs, fmt.Errorf("SignalSources() declares %s=%s but the adapter implements no InteractionSource; the body would be preserved for a shaper that does not exist", DescriptorCapture, CaptureRaw))
	case isSource && declared == 0:
		errs = append(errs, fmt.Errorf("implements InteractionSource but declares %s=%s on no event row; shaping would never receive a body", DescriptorCapture, CaptureRaw))
	}
	if !isSource {
		return errs
	}

	for _, p := range interactionProbes {
		got, panicked := interactionsSafe(src, p)
		if panicked {
			errs = append(errs, fmt.Errorf("Interactions panicked on event %q, body %q; it must be total (never panic on a nil, truncated, garbage or unbounded body)", p.Event, truncate(string(p.Raw))))
			continue
		}
		again, _ := interactionsSafe(src, p)
		if !reflect.DeepEqual(got, again) {
			errs = append(errs, fmt.Errorf("Interactions is not deterministic on event %q, body %q: %+v then %+v", p.Event, truncate(string(p.Raw)), got, again))
		}
		errs = append(errs, checkShapedItems(src, got, fmt.Sprintf("event %q", p.Event))...)
	}
	return errs
}

// checkShapedItems validates the items one call shaped: each is structurally
// well-formed, and each approval_request declares an apply mechanism consistent
// with what Decision reports (ADR-010 §4 and conformance obligation 5).
func checkShapedItems(src InteractionSource, items []Interaction, where string) []error {
	var errs []error
	for i, in := range items {
		if err := in.Validate(); err != nil {
			errs = append(errs, fmt.Errorf("Interactions(%s) item %d: %w", where, i, err))
			continue
		}
		if in.Kind != KindApprovalRequest {
			continue
		}
		if in.Mode == "" {
			errs = append(errs, fmt.Errorf("Interactions(%s) item %d: an approval_request declares no mode; the apply mechanism is decided AT CAPTURE, so the daemon knows before the phone renders (ADR-010 §4)", where, i))
			continue
		}
		for _, d := range in.Decisions {
			_, native, panicked := decisionSafe(src, in.Ref, d.ID)
			if panicked {
				errs = append(errs, fmt.Errorf("Decision(%q, %q) panicked; it must be total", in.Ref, d.ID))
				continue
			}
			if in.Mode == ModePromptCard && native {
				errs = append(errs, fmt.Errorf("Interactions(%s) item %d declares mode %s but Decision(%q, %q) reports a native mechanism; the carve-out is the path on which Decision is never called", where, i, ModePromptCard, in.Ref, d.ID))
			}
			if in.Mode == ModeCard && !native {
				errs = append(errs, fmt.Errorf("Interactions(%s) item %d declares mode %s but Decision(%q, %q) reports no native mechanism (ok==false); such a request must declare %s instead", where, i, ModeCard, in.Ref, d.ID, ModePromptCard))
			}
			if in.Mode == ModePromptCard {
				if _, held := in.Keystrokes[d.ID]; !held {
					errs = append(errs, fmt.Errorf("Interactions(%s) item %d declares mode %s but holds no keystroke for decision %q; the decision->keystroke map is produced AT CAPTURE because Decision is never called here (ADR-010 §2/§4)", where, i, ModePromptCard, d.ID))
				}
			}
		}
	}
	return errs
}

// CheckInteractionFixture runs ADR-010's conformance obligations 3 (corpus half)
// and 4 against a recorded fixture: every payload is replayed through the
// adapter's shaper, every shaped item must be well-formed, and any event that
// shapes an item MUST declare capture=raw — at runtime an undeclared event's body
// is flattened to top-level strings before the shaper ever sees it (ADR-010 §6),
// so shaping it here would be proving something the ingest path cannot deliver.
//
// It is PURE: fx is already in memory (the disk read lives in fixtureio, E9.2).
// An adapter with no InteractionSource has nothing to replay and yields nothing.
// The per-CLI GOLDEN comparison is the caller's: it holds the expected items.
func CheckInteractionFixture(a Adapter, fx Fixture) []error {
	src, ok := AsInteractionSource(a)
	if !ok {
		return nil
	}
	captured := make(map[string]bool)
	for _, s := range a.SignalSources() {
		if s.Descriptor[DescriptorCapture] == CaptureRaw && s.Descriptor[descriptorEvent] != "" {
			captured[s.Descriptor[descriptorEvent]] = true
		}
	}
	var errs []error
	for i, hp := range fx.HookPayloads {
		items, panicked := interactionsSafe(src, hp)
		if panicked {
			errs = append(errs, fmt.Errorf("Interactions panicked on %s hook_payloads[%d] (event %q); it must be total", fx.Scenario, i, hp.Event))
			continue
		}
		if len(items) > 0 && !captured[hp.Event] {
			errs = append(errs, fmt.Errorf("%s hook_payloads[%d]: event %q shapes %d item(s) but declares no %s=%s row, so its body is flattened before shaping", fx.Scenario, i, hp.Event, len(items), DescriptorCapture, CaptureRaw))
		}
		errs = append(errs, checkShapedItems(src, items, fmt.Sprintf("%s hook_payloads[%d]", fx.Scenario, i))...)
	}
	return errs
}

// interactionsSafe calls Interactions under a recover so a panicking shaper is
// reported as a violation rather than crashing the suite.
func interactionsSafe(src InteractionSource, p HookPayload) (items []Interaction, panicked bool) {
	defer func() {
		if r := recover(); r != nil {
			panicked = true
		}
	}()
	items = src.Interactions(p)
	return
}

// decisionSafe calls Decision under a recover, on the same terms.
func decisionSafe(src InteractionSource, ref, verdict string) (act DecisionAction, ok, panicked bool) {
	defer func() {
		if r := recover(); r != nil {
			panicked = true
		}
	}()
	act, ok = src.Decision(ref, verdict)
	return
}
