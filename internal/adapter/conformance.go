package adapter

// Conformance is the T-1 behavioral freeze: the portable, in-process checks any
// Adapter must satisfy. CheckConformance is PURE — it returns every violation as
// an error whose text names the failing method or field — so the harness itself
// is unit-testable. Conformance is the thin *testing.T wrapper real adapters
// call from their package tests.
//
// The checks here are exactly those provable in-process against the interface:
// Name non-emptiness/stability, argv shape + determinism, the options schema's
// self-consistency, signal-source kinds, resume consistency, and
// ExtractConversationID totality. The syscall-level "opens no fd/socket" and the
// import-boundary checks are inherently CI/review checks and live in the test
// layer (see conformance_test.go's review-only enumeration and
// refadapter/refadapter_test.go).

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"
)

// probeConversationID is the id CheckConformance feeds Resume/Extract while
// probing; it is arbitrary but fixed so the checks are deterministic.
const probeConversationID = "conformance-probe-id"

// shellPrograms are argv[0] basenames that mean "this argv would be interpreted
// by a shell" — the exact thing an adapter must never do (core exec's argv
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
// via t.Error. It is the entrypoint real adapters call from their tests.
func Conformance(t *testing.T, a Adapter) {
	t.Helper()
	for _, err := range CheckConformance(a) {
		t.Error(err)
	}
}

// CheckConformance returns every conformance violation in a, or an empty slice
// for a fully conformant adapter. It is pure: it calls only the adapter's pure
// methods, opens nothing, and mutates no shared state.
func CheckConformance(a Adapter) []error {
	var errs []error
	errs = append(errs, checkName(a)...)
	errs = append(errs, checkCommand(a)...)
	errs = append(errs, checkOptions(a)...)
	errs = append(errs, checkSignalSources(a)...)
	errs = append(errs, checkResume(a)...)
	errs = append(errs, checkExtract(a)...)
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

// checkCommand: argv is non-empty, argv[0] is a bare program (never a shell,
// never a metacharacter-bearing single string), and Command is deterministic.
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
	if base := filepath.Base(argv[0]); shellPrograms[base] {
		errs = append(errs, fmt.Errorf("Command() argv[0] %q is a shell; argv must be exec'd directly, never through a shell", argv[0]))
	}
	if strings.ContainsAny(argv[0], shellMetachars) {
		errs = append(errs, fmt.Errorf("Command() argv[0] %q must be a bare program path, not a shell-interpretable argv string", argv[0]))
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

// checkExtract: ExtractConversationID is total (never panics on nil/garbage
// grid+tail) and ok==true implies a non-empty id.
func checkExtract(a Adapter) []error {
	var errs []error
	probes := [][]byte{
		nil,
		{},
		[]byte("\x00\xff not an id"),
		[]byte("conv-id=" + probeConversationID),
	}
	for _, tail := range probes {
		id, ok, panicked := extractSafe(a, tail)
		if panicked {
			errs = append(errs, fmt.Errorf("ExtractConversationID panicked on tail %q; it must be total", tail))
			continue
		}
		if ok && id == "" {
			errs = append(errs, fmt.Errorf("ExtractConversationID returned ok with an empty id on tail %q; ok must imply a non-empty id", tail))
		}
	}
	return errs
}

// extractSafe calls ExtractConversationID with a nil grid under a recover so a
// panicking extractor is reported as a violation rather than crashing the suite.
func extractSafe(a Adapter, tail []byte) (id string, ok, panicked bool) {
	defer func() {
		if r := recover(); r != nil {
			panicked = true
		}
	}()
	id, ok = a.ExtractConversationID(nil, tail)
	return
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
