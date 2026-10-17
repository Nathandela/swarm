package adapter

// ADR-010 amendment 2026-08-20 (Wave R7, Mirror M4.1): an adapter may DESCRIBE its
// session backend, and still gains no fd.
//
// Some CLIs expose their structured plane over a SIDE PROCESS rather than over hooks:
// `codex app-server --listen unix://PATH` serves a JSON-RPC/WebSocket endpoint and the
// TUI attaches to it with `--remote unix://PATH`. The adapter is the only party that
// knows those two command lines; the CORE is the only party allowed to spawn anything.
// So this is the same trick Command/Resume already play -- the adapter returns a
// DESCRIPTOR, the core executes it (ADR-001 / E9.2).
//
// WHY THERE IS NO argv[0]. An earlier draft carried an `Argv` whose first element the
// adapter chose, and its conformance obligation ("Argv references the SocketPath the core
// supplied") was satisfiable by `{"/bin/sh", "-c", "rm -rf / #" + spec.SocketPath}`.
// `Program` is a program NAME the core resolves through HostProber.LookPath -- Detect's own
// discipline -- so the shell-injection shape is gone by construction rather than by promise.

import (
	"fmt"
	"path/filepath"
	"strings"
)

// BackendSpec is what the CORE supplies to an adapter that describes a backend. The
// socket path is the core's, because the core owns the session dir.
type BackendSpec struct {
	// SocketPath is the per-session backend endpoint, inside the session state dir.
	SocketPath string
	// Cwd is the core's final agent working directory, independent of adapter argv.
	Cwd string
	// AgentArgv is the already composed standalone command, including argv[0].
	AgentArgv []string
}

// BackendPlan is the adapter's pure description of the side process, plus the arguments
// the AGENT needs in order to attach to it.
//
// There is deliberately no argv[0]: Program is a NAME, resolved by the core.
type BackendPlan struct {
	// Program is the backend executable's NAME on PATH (e.g. "codex"). It must contain
	// no path separator: naming a program and naming a path are different acts, and only
	// the first is the adapter's.
	Program string
	// Args are the backend process's arguments (argv[1:]).
	Args []string
	// AgentArgs are appended VERBATIM to the agent's own argv so it attaches to the
	// backend the core started. Empty is legal.
	AgentArgs []string
	// AgentCommandArgs optionally replaces argv[1:] when the backend is attached.
	// An unavailable backend always uses the original standalone command.
	AgentCommandArgs []string
}

// BackendSource is the OPTIONAL extension a CLI whose structured plane lives in a side
// process implements. Discovered by TYPE ASSERTION like every other extension here; the
// frozen Adapter method set gains nothing.
type BackendSource interface {
	// Backend describes the side process for one session, or reports ok==false.
	//
	// ok == false IS THE ORDINARY CASE and never a defect: most CLIs need no backend, and
	// an adapter that answers false is complete and fully supported (ADR-010 §5's posture).
	//
	// PURE and TOTAL on Interactions' terms: deterministic, never panics on an empty or
	// pathological SocketPath, and it performs no I/O.
	Backend(spec BackendSpec) (BackendPlan, bool)
}

// AsBackendSource reports whether a describes a session backend.
func AsBackendSource(a Adapter) (BackendSource, bool) {
	src, ok := a.(BackendSource)
	return src, ok
}

// KeystrokeComposer is the OPTIONAL seam an adapter implements when a remote message may be
// delivered by TYPING IT INTO THE CLI'S OWN COMPOSER -- the mechanism Wave R6 shipped for
// Claude Code, where the CLI's prompt is a terminal input region and nothing else exists.
//
// It is spelled as a seam for exactly one reason: ABSENCE IS THE REFUSAL. Before R7 the
// daemon wrote text plus a CR into the PTY for EVERY provider with no seam and no provider
// check anywhere on the path, so a phone send to a Codex session typed into the Codex TUI --
// the thing playbook §8.2 forbids in as many words. With the seam, a CLI that must never be
// typed at simply implements nothing, and the daemon refuses rather than inventing a
// keystroke (ADR-010 §5, IS-TOOL-2's never-guess posture one layer down).
type KeystrokeComposer interface {
	// ComposerKeys is the byte sequence that enters text into the CLI's own composer,
	// written to the session's PTY verbatim. The daemon supplies the submit framing.
	ComposerKeys(text string) []byte
}

// AsKeystrokeComposer reports whether a proves a keystroke composer seam.
func AsKeystrokeComposer(a Adapter) (KeystrokeComposer, bool) {
	kc, ok := a.(KeystrokeComposer)
	return kc, ok
}

// CheckBackendPlan is conformance obligation 8: a DECLARED backend must name a non-empty
// Program. A declared backend that starts nothing is a session whose agent attaches to a
// socket nobody serves.
func CheckBackendPlan(src BackendSource, spec BackendSpec) error {
	plan, ok := src.Backend(spec)
	if !ok {
		return nil // obligation 3: no backend is the ordinary case
	}
	if plan.Program == "" {
		return fmt.Errorf("the adapter declares a backend with an empty Program; a declared backend " +
			"that starts nothing is a session whose agent attaches to a socket nobody serves")
	}
	return nil
}

// ResolveBackend performs the three checks the CORE owes on an adapter's plan, and returns
// the plan with Program resolved to an absolute path. ok==false means the adapter declares
// no backend (and err is nil).
//
// It lives in the contract package for exactly the reason Detect(a, HostProber) does: the
// checks are pure functions of the plan plus a capability interface, so they are testable
// and reusable without this package gaining an fd.
//
//   - 9a: Program resolves through HostProber.LookPath, and a Program containing a path
//     separator is refused OUTRIGHT before any lookup.
//   - 9c: absolute paths stay inside the session dir, except the original resume
//     command's --cd operand may retain the core's exact absolute Cwd. The exception
//     applies only to AgentCommandArgs, never backend or attachment arguments.
//
// 9b -- exec'd DIRECTLY, never through a shell -- belongs to whoever builds the exec.Cmd
// (internal/shim), which is where it is fenced.
func ResolveBackend(src BackendSource, prober HostProber, spec BackendSpec, sessionDir string) (BackendPlan, bool, error) {
	plan, ok := src.Backend(spec)
	if !ok {
		return BackendPlan{}, false, nil
	}
	if plan.Program == "" {
		return BackendPlan{}, false, fmt.Errorf("the adapter declares a backend with an empty Program (obligation 8)")
	}
	if strings.ContainsRune(plan.Program, '/') || strings.ContainsRune(plan.Program, filepath.Separator) {
		return BackendPlan{}, false, fmt.Errorf(
			"backend Program %q contains a path separator; an adapter NAMES a program and never names a "+
				"path (obligation 9a)", plan.Program)
	}
	resolved, err := prober.LookPath(plan.Program)
	if err != nil {
		return BackendPlan{}, false, fmt.Errorf("backend Program %q does not resolve on PATH: %w", plan.Program, err)
	}
	dir := filepath.Clean(sessionDir)
	for _, arg := range append(append([]string(nil), plan.Args...), plan.AgentArgs...) {
		if err := checkContained(arg, dir); err != nil {
			return BackendPlan{}, false, err
		}
	}
	cwdIndex, err := resumeCwdOperand(plan.AgentCommandArgs)
	if err != nil {
		return BackendPlan{}, false, err
	}
	if cwdIndex >= 0 {
		var originalArgs []string
		if len(spec.AgentArgv) > 0 {
			originalArgs = spec.AgentArgv[1:]
		}
		originalIndex, originalErr := resumeCwdOperand(originalArgs)
		if originalErr != nil || originalIndex < 0 || !filepath.IsAbs(spec.Cwd) ||
			plan.AgentCommandArgs[cwdIndex] != spec.Cwd || originalArgs[originalIndex] != spec.Cwd ||
			plan.AgentCommandArgs[1] != originalArgs[1] {
			return BackendPlan{}, false, fmt.Errorf("backend resume --cd must retain the original command's core-supplied absolute cwd (obligation 9c)")
		}
	}
	for i, arg := range plan.AgentCommandArgs {
		if i != cwdIndex {
			if err := checkContained(arg, dir); err != nil {
				return BackendPlan{}, false, err
			}
		}
	}
	out := BackendPlan{
		Program:          resolved,
		Args:             append([]string(nil), plan.Args...),
		AgentArgs:        append([]string(nil), plan.AgentArgs...),
		AgentCommandArgs: append([]string(nil), plan.AgentCommandArgs...),
	}
	return out, true, nil
}

// resumeCwdOperand grants no authority to flag-looking option values or unknown grammar.
func resumeCwdOperand(args []string) (int, error) {
	if len(args) < 2 || args[0] != "resume" {
		return -1, nil
	}
	// Other adapters' replacement commands retain the ordinary containment rule.
	hasCwd := false
	for _, arg := range args[2:] {
		hasCwd = hasCwd || arg == "--cd" || strings.HasPrefix(arg, "--cd=")
	}
	if !hasCwd {
		return -1, nil
	}
	index := -1
	for i := 2; i < len(args); i++ {
		switch args[i] {
		case "-c", "--model", "--sandbox", "--cd":
			if i+1 == len(args) {
				return -1, fmt.Errorf("backend resume option %q has no operand (obligation 9c)", args[i])
			}
			if args[i] == "--cd" {
				if index >= 0 {
					return -1, fmt.Errorf("backend resume repeats --cd (obligation 9c)")
				}
				index = i + 1
			}
			i++
		default:
			return -1, fmt.Errorf("backend resume has unsupported --cd context (obligation 9c)")
		}
	}
	return index, nil
}

// checkContained is obligation 9c for one argument. A relative token (a flag, a
// subcommand) is not a path and is left alone; an absolute one must be the session dir or
// live under it.
func checkContained(arg, dir string) error {
	bare := strings.TrimPrefix(arg, "unix://")
	if !filepath.IsAbs(bare) {
		return nil
	}
	clean := filepath.Clean(bare)
	if clean == dir || strings.HasPrefix(clean, dir+string(filepath.Separator)) {
		return nil
	}
	return fmt.Errorf(
		"backend plan names absolute path %q, which is outside the session dir %q; the core supplies "+
			"the session paths a plan may name (obligation 9c)", clean, dir)
}
