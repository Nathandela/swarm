package adapter

// FAILING-FIRST (TDD RED, GG-5) for Wave R7's ONE new adapter-contract extension --
// ADR-010's amendment of 2026-08-20 ("an adapter may DESCRIBE its session backend, and
// still gains no fd"), Mirror M4.1. Bead: agents-tracker-hggx.8.
//
// Undefined symbols -> compile-fail RED is expected and valid here; the R6 precedent for
// this package's own RED style is internal/skeleton/r6_*_test.go.
//
// THE CONTRACT these tests freeze:
//
//	type BackendSpec struct{ SocketPath string }
//	type BackendPlan struct{ Program string; Args []string; AgentArgs []string }
//	type BackendSource interface{ Backend(spec BackendSpec) (BackendPlan, bool) }
//	func AsBackendSource(a Adapter) (BackendSource, bool)
//
// and the SECOND new seam the same wave needs, for the opposite reason -- ADR-013 §R7.5's
// "the keystroke composer becomes an EXPLICIT optional adapter seam, exactly like
// TurnInterrupter, so that ABSENCE IS THE REFUSAL":
//
//	type KeystrokeComposer interface{ ComposerKeys(text string) []byte }
//	func AsKeystrokeComposer(a Adapter) (KeystrokeComposer, bool)
//
// WHY THERE IS NO argv[0]. The rejected draft of the ADR carried an `Argv` whose first
// element the adapter chose, and its conformance obligation ("Argv references the
// SocketPath the core supplied") was satisfied by
// `Argv: {"/bin/sh", "-c", "rm -rf / #" + spec.SocketPath}`. `Program` is a program NAME
// the CORE resolves through HostProber.LookPath -- Detect's own discipline -- so the
// shell-injection shape is gone by construction rather than by promise. Two of the three
// checks the core performs on top of that are fenced HERE, because ResolveBackend is
// core-side-in-the-contract-package exactly as Detect(a, HostProber) already is: 9a (the
// program resolves through the prober) and 9c (no element names an absolute path outside the
// session dir). 9b -- exec'd DIRECTLY, never through a shell -- is the shim's and is fenced
// in internal/shim/r7_backend_test.go, where the exec.Cmd is actually built.

import (
	"reflect"
	"strings"
	"testing"
)

// r7BackendStub is an Adapter that DESCRIBES a backend. It is the fixture the contract is
// exercised against; the real one is internal/adapter/codex's.
type r7BackendStub struct {
	Adapter // embedded so only the extension is spelled out here
	plan    BackendPlan
	ok      bool
}

func (s r7BackendStub) Backend(spec BackendSpec) (BackendPlan, bool) {
	if !s.ok {
		return BackendPlan{}, false
	}
	p := s.plan
	p.Args = append([]string(nil), p.Args...)
	p.AgentArgs = append([]string(nil), p.AgentArgs...)
	return p, true
}

// TestR7BackendSource_AsBackendSourceDiscoversTheSeamByTypeAssertion is the discovery
// rule every extension in this file already follows: the frozen Adapter method set gains
// NOTHING, and the seam is found by type assertion (ADR-010 Non-goals).
func TestR7BackendSource_AsBackendSourceDiscoversTheSeamByTypeAssertion(t *testing.T) {
	with := r7BackendStub{
		ok:   true,
		plan: BackendPlan{Program: "codex", Args: []string{"app-server"}},
	}
	if _, ok := AsBackendSource(with); !ok {
		t.Fatal("AsBackendSource refused an adapter that implements Backend; the seam is " +
			"discovered by type assertion exactly as AsInteractionSource/AsTurnInterrupter are")
	}
	// baseAdapter is the package's existing do-nothing Adapter (stubs_test.go).
	if _, ok := AsBackendSource(baseAdapter{}); ok {
		t.Fatal("AsBackendSource claimed a seam on an adapter that implements none; " +
			"ABSENCE IS A SIGNAL (ADR-010 §5) and a false positive here starts a process nobody described")
	}
}

// TestR7BackendSource_OkFalseIsTheOrdinaryCaseAndNeverADefect pins ADR-010 amendment
// property 3. Most CLIs need no side process; an adapter that answers false is complete.
func TestR7BackendSource_OkFalseIsTheOrdinaryCaseAndNeverADefect(t *testing.T) {
	none := r7BackendStub{ok: false}
	plan, ok := none.Backend(BackendSpec{SocketPath: "/tmp/s/codex.sock"})
	if ok {
		t.Fatal("an adapter that needs no backend answered ok==true")
	}
	if plan.Program != "" || len(plan.Args) != 0 || len(plan.AgentArgs) != 0 {
		t.Errorf("ok==false returned a non-zero plan %+v; the core must be able to ignore the "+
			"plan entirely when ok is false", plan)
	}
}

// TestR7BackendSource_ObligationEight_ADeclaredBackendNamesANonEmptyProgram is
// conformance obligation 8 verbatim: "A declared backend that starts nothing is a session
// whose agent will attach to a socket nobody serves."
func TestR7BackendSource_ObligationEight_ADeclaredBackendNamesANonEmptyProgram(t *testing.T) {
	bad := r7BackendStub{ok: true, plan: BackendPlan{Program: "", Args: []string{"app-server"}}}
	if err := CheckBackendPlan(bad, BackendSpec{SocketPath: "/tmp/s/codex.sock"}); err == nil {
		t.Fatal("CheckBackendPlan accepted a declared backend with an EMPTY Program; obligation 8 " +
			"exists because such a session's agent attaches to a socket nobody serves")
	}
	good := r7BackendStub{ok: true, plan: BackendPlan{
		Program:   "codex",
		Args:      []string{"app-server", "--listen", "unix:///tmp/s/codex.sock"},
		AgentArgs: []string{"--remote", "unix:///tmp/s/codex.sock"},
	}}
	if err := CheckBackendPlan(good, BackendSpec{SocketPath: "/tmp/s/codex.sock"}); err != nil {
		t.Fatalf("CheckBackendPlan rejected a well-formed plan: %v", err)
	}
}

// TestR7BackendSource_ObligationSeven_BackendIsPureAndTotalOnInteractionsTerms is
// conformance obligation 7: deterministic, never panics on an empty or pathological
// SocketPath, performs no I/O.
func TestR7BackendSource_ObligationSeven_BackendIsPureAndTotalOnInteractionsTerms(t *testing.T) {
	src := r7BackendStub{ok: true, plan: BackendPlan{
		Program: "codex", Args: []string{"app-server", "--listen"},
	}}
	pathological := []string{
		"",
		strings.Repeat("/a", 4096),
		"\x00\x01\x02",
		"unix://",
		"../../../../etc/passwd",
		strings.Repeat("ÿ", 1<<12),
	}
	for _, p := range pathological {
		func() {
			defer func() {
				if r := recover(); r != nil {
					t.Fatalf("Backend panicked on SocketPath %q: %v; obligation 7 makes it TOTAL "+
						"on Interactions' terms", p, r)
				}
			}()
			a, _ := src.Backend(BackendSpec{SocketPath: p})
			b, _ := src.Backend(BackendSpec{SocketPath: p})
			if a.Program != b.Program || len(a.Args) != len(b.Args) {
				t.Errorf("Backend is not deterministic for SocketPath %q: %+v vs %+v", p, a, b)
			}
		}()
	}
}

// TestR7BackendSource_TheNewTypesTripNoBannedIOToken keeps the E9.2 grep honest across the
// extension. BackendSpec/BackendPlan are pure data; a plan that named a net.Dial or an
// exec.Command would mean the descriptor had grown an fd.
func TestR7BackendSource_TheNewTypesTripNoBannedIOToken(t *testing.T) {
	// The scan itself is TestContractPackage_NoIOInSource's; this test exists so the
	// obligation is named where the extension is, and it fails loudly if the shared
	// scanner ever stops covering this package's production files.
	scanBannedIO(t, ".")
}

// TestR7BackendSource_ObligationNineA_ProgramResolvesThroughTheCoresLookPath is check 9a:
// the CORE calls HostProber.LookPath(plan.Program) -- the same discipline
// Detect(a, HostProber) uses (adapter.go:126-147) -- and refuses a plan whose Program
// contains a path separator or whose resolution fails. The adapter NAMES a program; it never
// names a path.
func TestR7BackendSource_ObligationNineA_ProgramResolvesThroughTheCoresLookPath(t *testing.T) {
	prober := fakeHostProber{path: "/usr/local/bin/codex"}
	spec := BackendSpec{SocketPath: "/state/01JSESSION/codex.sock"}
	src := r7BackendStub{ok: true, plan: BackendPlan{
		Program:   "codex",
		Args:      []string{"app-server", "--listen", "unix://" + spec.SocketPath},
		AgentArgs: []string{"--remote", "unix://" + spec.SocketPath},
	}}

	res, ok, err := ResolveBackend(src, prober, spec, "/state/01JSESSION")
	if err != nil || !ok {
		t.Fatalf("ResolveBackend on a well-formed plan: ok=%v err=%v", ok, err)
	}
	if res.Program != "/usr/local/bin/codex" {
		t.Errorf("resolved Program = %q, want the LookPath result; a core that exec'd the NAME "+
			"would search PATH again at spawn time, in the shim's environment rather than the "+
			"daemon's", res.Program)
	}

	// A Program with a separator is refused OUTRIGHT, before any LookPath: that is the
	// difference between naming a program and naming a path, and it is what stops an adapter
	// pointing the core at anything on disk.
	for _, bad := range []string{"/bin/sh", "./codex", "../codex", "dir/codex"} {
		withPath := r7BackendStub{ok: true, plan: BackendPlan{Program: bad}}
		if _, _, err := ResolveBackend(withPath, prober, spec, "/state/01JSESSION"); err == nil {
			t.Errorf("ResolveBackend accepted Program %q, which contains a path separator", bad)
		}
	}
	// An unresolvable program is refused rather than deferred to a spawn failure nobody reads.
	missing := r7BackendStub{ok: true, plan: BackendPlan{Program: "codex"}}
	if _, _, err := ResolveBackend(missing, fakeHostProber{lookErr: errR7NoSuchProgram}, spec, "/state/01JSESSION"); err == nil {
		t.Error("ResolveBackend accepted a Program the prober could not resolve")
	}
}

// TestR7BackendSource_ObligationNineC_ThePlanMayNameNoPathOutsideTheSessionDir is check 9c,
// and it is the ONLY one of the three a malicious or merely buggy adapter cannot talk its way
// past, because the CORE performs it on data it does not trust.
//
// MUTATION FENCE (ADR-010 obligation 9c names it): delete the containment check and the
// malicious-fixture case below must fail.
func TestR7BackendSource_ObligationNineC_ThePlanMayNameNoPathOutsideTheSessionDir(t *testing.T) {
	prober := fakeHostProber{path: "/usr/local/bin/codex"}
	const dir = "/state/01JSESSION"
	spec := BackendSpec{SocketPath: dir + "/codex.sock"}

	malicious := []BackendPlan{
		{Program: "codex", Args: []string{"--listen", "unix:///tmp/evil.sock"}},
		{Program: "codex", Args: []string{"--listen", "unix://" + spec.SocketPath}, AgentArgs: []string{"--remote", "unix:///tmp/evil.sock"}},
		{Program: "codex", Args: []string{"--listen", "unix://" + dir + "/../../etc/passwd"}},
		{Program: "codex", Args: []string{"--config", "/Users/someone/.codex/auth.json"}},
	}
	for i, plan := range malicious {
		src := r7BackendStub{ok: true, plan: plan}
		if _, _, err := ResolveBackend(src, prober, spec, dir); err == nil {
			t.Errorf("case %d: ResolveBackend accepted a plan naming an absolute path outside the "+
				"session dir: %+v", i, plan)
		}
	}

	// The benign shapes must still pass, or the check is a blanket refusal wearing a
	// containment costume: flags, subcommands and the core's OWN socket, with and without the
	// unix:// scheme prefix.
	benign := BackendPlan{
		Program:   "codex",
		Args:      []string{"app-server", "--listen", "unix://" + spec.SocketPath},
		AgentArgs: []string{"--remote", spec.SocketPath},
	}
	if _, _, err := ResolveBackend(r7BackendStub{ok: true, plan: benign}, prober, spec, dir); err != nil {
		t.Errorf("ResolveBackend refused the benign plan %+v: %v", benign, err)
	}
}

func TestResolveBackendResumeCwdIsOnlyTheOriginalTrustedOperand(t *testing.T) {
	const dir, cwd = "/state/session", "/projects/requested"
	command := []string{"resume", "thread", "-c", "check_for_update_on_startup=false", "--cd", cwd, "--model", "model"}
	for _, tc := range []struct {
		name     string
		cwd      string
		original []string
		command  []string
		args     []string
		attach   []string
		wantOK   bool
	}{
		{name: "exact original operand", cwd: cwd, original: command, command: command, wantOK: true},
		{name: "flag-looking model value", cwd: cwd, original: []string{"resume", "thread", "--model", "--cd", "--cd", cwd}, command: []string{"resume", "thread", "--model", "--cd", "--cd", cwd}, wantOK: true},
		{name: "missing trusted cwd", original: command, command: command},
		{name: "relative trusted cwd", cwd: "projects/requested", original: command, command: command},
		{name: "nonclean trusted cwd exact match", cwd: "/projects/other/../requested", original: []string{"resume", "thread", "--cd", "/projects/other/../requested"}, command: []string{"resume", "thread", "--cd", "/projects/other/../requested"}, wantOK: true},
		{name: "nonclean trusted cwd mismatched spelling", cwd: "/projects/other/../requested", original: command, command: command},
		{name: "different final cwd", cwd: "/projects/worktree", original: command, command: command},
		{name: "invented operand", cwd: cwd, original: []string{"resume", "thread"}, command: command},
		{name: "different original operand", cwd: cwd, original: []string{"resume", "thread", "--cd", "/projects/other"}, command: command},
		{name: "different conversation", cwd: cwd, original: []string{"resume", "other-thread", "--cd", cwd}, command: command},
		{name: "different replacement operand", cwd: cwd, original: command, command: []string{"resume", "thread", "--cd", "/projects/other"}},
		{name: "duplicate cwd", cwd: cwd, original: command, command: []string{"resume", "thread", "--cd", cwd, "--cd", cwd}},
		{name: "duplicate contained cwd", cwd: dir, original: []string{"resume", "thread", "--cd", dir}, command: []string{"resume", "thread", "--cd", dir, "--cd", dir}},
		{name: "duplicate original cwd", cwd: cwd, original: []string{"resume", "thread", "--cd", cwd, "--cd", cwd}, command: command},
		{name: "missing operand", cwd: cwd, original: command, command: []string{"resume", "thread", "--cd"}},
		{name: "equals form is not the trusted operand", cwd: cwd, original: command, command: []string{"resume", "thread", "--cd=" + cwd}},
		{name: "model operand is not a flag", cwd: cwd, original: command, command: []string{"resume", "thread", "--model", "--cd", cwd}},
		{name: "original model operand is not a flag", cwd: cwd, original: []string{"resume", "thread", "--model", "--cd", cwd}, command: command},
		{name: "unknown flag context", cwd: cwd, original: command, command: []string{"resume", "thread", "--config", "value", "--cd", cwd}},
		{name: "after option terminator", cwd: cwd, original: command, command: []string{"resume", "thread", "--", "--cd", cwd}},
		{name: "cwd as config value", cwd: cwd, original: command, command: []string{"resume", "thread", "-c", cwd, "--cd", cwd}},
		{name: "cwd in backend args", cwd: cwd, original: command, command: command, args: []string{"--cd", cwd}},
		{name: "cwd in attachment args", cwd: cwd, original: command, command: command, attach: []string{"--cd", cwd}},
		{name: "non-resume replacement", cwd: cwd, original: command, command: []string{"--cd", cwd}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			spec := BackendSpec{SocketPath: dir + "/codex.sock", Cwd: tc.cwd, AgentArgv: append([]string{"codex"}, tc.original...)}
			plan := BackendPlan{Program: "codex", Args: tc.args, AgentArgs: tc.attach, AgentCommandArgs: tc.command}
			original := append([]string(nil), spec.AgentArgv...)
			commandBefore := append([]string(nil), tc.command...)
			got, ok, err := ResolveBackend(r7BackendStub{ok: true, plan: plan}, fakeHostProber{path: "/usr/bin/codex"}, spec, dir)
			if tc.wantOK {
				if err != nil || !ok || !reflect.DeepEqual(got.AgentCommandArgs, tc.command) {
					t.Fatalf("valid trusted cwd plan: ok=%v err=%v args=%v", ok, err, got.AgentCommandArgs)
				}
			} else if err == nil {
				t.Fatal("untrusted cwd argument accepted")
			}
			if !reflect.DeepEqual(spec.AgentArgv, original) || !reflect.DeepEqual(tc.command, commandBefore) {
				t.Fatal("validation mutated the original or replacement command")
			}
		})
	}
}

// errR7NoSuchProgram is the prober failure used above.
var errR7NoSuchProgram = errR7("codex: executable file not found in $PATH")

type errR7 string

func (e errR7) Error() string { return string(e) }

// TestR7KeystrokeComposer_ClaudeProvesTheSeamAndAbsenceIsTheRefusal freezes ADR-013 §R7.5's
// structural fix. TODAY internal/skeleton/chat.go:236 writes `sub.Input([]byte(text))` for
// EVERY provider with no seam and no provider check anywhere on the path, which is why a
// phone send to a Codex session types into the Codex TUI right now -- the thing playbook
// §8.2 forbids in as many words. The fix is not a provider name in the daemon; it is a seam
// whose ABSENCE is the refusal.
func TestR7KeystrokeComposer_ClaudeProvesTheSeamAndAbsenceIsTheRefusal(t *testing.T) {
	if _, ok := AsKeystrokeComposer(baseAdapter{}); ok {
		t.Fatal("AsKeystrokeComposer claimed a seam on an adapter that implements none; the " +
			"whole point of the seam is that absence refuses")
	}
}
