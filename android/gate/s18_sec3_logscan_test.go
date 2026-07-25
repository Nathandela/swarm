package gate

// FAILING-FIRST (TDD RED, GG-5) tests for PB-SEC-3's LOG half, slice S18:
//
//	"No plaintext session content persisted unencrypted; NO SECRETS OR SESSION CONTENT IN LOGS."
//	Criterion: "Automated log scan + storage assertion (EVIDENCE ARTIFACT REQUIRED, NOT
//	 \"REVIEWED\")."
//
// ---------------------------------------------------------------------------
// THE STORAGE HALF IS S15'S AND IS NOT RE-TESTED HERE.
// ---------------------------------------------------------------------------
//
// Slice S15 sealed the phone's state per tier, and internal/phonecore/s15_statetier_test.go
// holds the assertion: sentinels searched for in EVERY byte form -- raw, base64, hex, decimal,
// both endiannesses -- paired with the POSITIVE half that the material reached the intended
// sealer, because absence alone is a weak assertion (a value that was never written is also
// absent). android/gate/keycustody_test.go reads the bytes on disk for the key half. Restating
// either here would duplicate a shipped slice's enumeration in another slice's test names,
// where an auditor reading the S18 evidence would find assertions the S18 slice did not make.
//
// What S15 did NOT cover, and what this file owes, is the LOG half. It is a separate exposure
// with a separate sink: sealing the state directory says nothing about what the process writes
// to logcat, where a shared Android log buffer, a bug report, and any `adb logcat` from a
// paired workstation can read it.
//
// ---------------------------------------------------------------------------
// WHY THIS IS AN ARTIFACT AND NOT A GREP.
// ---------------------------------------------------------------------------
//
// The criterion says "evidence artifact required, not reviewed" because a scan that reports a
// clean result and leaves nothing behind is indistinguishable from a scan that was never run
// -- and from one whose pattern list quietly stopped matching. So the scan below WRITES its
// finding set to a checked-in file and compares against it, the way mobile/golden_test.go pins
// the bound surface. Regenerate with -update-logscan; regenerating is the point at which
// someone has to justify the diff.
//
// The artifact is the SINK INVENTORY, not the verdict. "No secrets in logs" as a stored boolean
// is the defect this project keeps naming: a value someone can write next to the thing it
// describes. What is stored instead is every logging call site in the phone-side code, with its
// argument -- so a new one is a diff a reviewer sees, and a scan that stopped working shows up
// as an inventory that emptied out.
//
// RED AT THE TIME OF WRITING: no such artifact exists.
//
// THIS FILE NEVER SKIPS: it reads sources in the repository.

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

var updateLogScan = flag.Bool("update-logscan", false,
	"rewrite docs/verification/s18-log-sinks.tsv from the current sources")

func logScanArtifactPath(t *testing.T) string {
	return filepath.Join(repoRoot(t), "docs", "verification", "s18-log-sinks.tsv")
}

// ---------------------------------------------------------------------------
// The scan.
// ---------------------------------------------------------------------------

// logSinkPatterns are the ways phone-side code can put a string somewhere a human or another
// process reads it. Go and Kotlin both, because the phone is both: the core is a Go .so and
// the app is Kotlin, and a leak from either lands in the same logcat buffer.
//
// printStackTrace is on the list and is not paranoia: an exception message in this codebase can
// carry a session id, a device id or a refusal reason derived from content, and a stack trace
// prints the message.
var logSinkPatterns = []struct {
	Lang string
	Re   *regexp.Regexp
}{
	{"kotlin", regexp.MustCompile(`\bLog\.[vdiwe]\s*\(`)},
	{"kotlin", regexp.MustCompile(`\bprintln\s*\(`)},
	{"kotlin", regexp.MustCompile(`\bprint\s*\(`)},
	{"kotlin", regexp.MustCompile(`printStackTrace\s*\(`)},
	{"kotlin", regexp.MustCompile(`System\.(out|err)\.`)},
	{"go", regexp.MustCompile(`\blog\.(Print|Printf|Println|Fatal|Fatalf|Fatalln|Panic|Panicf)\s*\(`)},
	{"go", regexp.MustCompile(`\bfmt\.(Print|Printf|Println|Fprint|Fprintf|Fprintln)\s*\(`)},
	{"go", regexp.MustCompile(`\bos\.(Stdout|Stderr)\b`)},
	{"go", regexp.MustCompile(`\bslog\.(Debug|Info|Warn|Error)\s*\(`)},
}

// phoneSideRoots are the trees that run ON THE HANDSET. The scan is scoped to them on purpose:
// the daemon, the relay server and the TUI all log legitimately and at length, and they run on
// the user's own machine, which is not the device PB-SEC-3 is about. internal/remote/relay is
// excluded for that reason -- relay/server.go's `log` import is the relay SERVER's, which never
// runs on a phone.
func phoneSideRoots(t *testing.T) map[string]string {
	root := repoRoot(t)
	return map[string]string{
		"kotlin-app":   filepath.Join(root, "android", "app", "src", "main", "kotlin"),
		"go-facade":    filepath.Join(root, "mobile"),
		"go-phonecore": filepath.Join(root, "internal", "phonecore"),
	}
}

// logSink is one call site, as the artifact records it.
type logSink struct {
	Area string // which phone-side tree
	File string // repo-relative
	Line int
	Call string // the matched call plus the rest of its line, normalised
}

func (s logSink) row() string {
	return fmt.Sprintf("%s\t%s:%d\t%s", s.Area, s.File, s.Line, s.Call)
}

func scanLogSinks(t *testing.T) []logSink {
	t.Helper()
	var out []logSink
	for area, dir := range phoneSideRoots(t) {
		_ = filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
			if err != nil || info.IsDir() {
				return nil
			}
			isGo := strings.HasSuffix(path, ".go")
			isKt := strings.HasSuffix(path, ".kt")
			if !isGo && !isKt {
				return nil
			}
			// Test sources are excluded: a test that prints a sentinel is not the app logging
			// one, and including them would make the inventory churn on every test edit --
			// which is how an artifact stops being read.
			if strings.HasSuffix(path, "_test.go") ||
				strings.Contains(path, string(filepath.Separator)+"test"+string(filepath.Separator)) {
				return nil
			}
			raw, rerr := os.ReadFile(path)
			if rerr != nil {
				t.Fatalf("PB-SEC-3: cannot read %s: %v", path, rerr)
			}
			lang := "go"
			if isKt {
				lang = "kotlin"
			}
			stripped := stripLineComments(string(raw), lang)
			lines := strings.Split(stripped, "\n")
			for i, line := range lines {
				for _, p := range logSinkPatterns {
					if p.Lang != lang || !p.Re.MatchString(line) {
						continue
					}
					out = append(out, logSink{
						Area: area,
						File: mustRel(t, path),
						Line: i + 1,
						// THE WHOLE CALL, not the matched line. This codebase wraps long
						// argument lists, so a line-scoped capture sees `Log.w(TAG,` and
						// nothing else -- and the argument-inspection test below would then be
						// blind to every logged identifier that happened to sit on the
						// continuation line. That is a guard defeated by pressing Enter.
						Call: wholeCall(lines, i),
					})
					break
				}
			}
			return nil
		})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].File != out[j].File {
			return out[i].File < out[j].File
		}
		return out[i].Line < out[j].Line
	})
	return out
}

// wholeCall joins the matched line with its continuations until the call's parentheses balance,
// so the inventory records every argument rather than only those that fit on one line. It gives
// up after 12 lines: past that the construct is not a log call the argument check can reason
// about, and an unbounded walk would swallow the rest of the file on an unbalanced source.
func wholeCall(lines []string, start int) string {
	depth := 0
	var parts []string
	for i := start; i < len(lines) && i < start+12; i++ {
		parts = append(parts, strings.TrimSpace(lines[i]))
		for _, c := range lines[i] {
			switch c {
			case '(':
				depth++
			case ')':
				depth--
			}
		}
		if depth <= 0 && i > start-1 {
			break
		}
	}
	return strings.Join(strings.Fields(strings.Join(parts, " ")), " ")
}

// stripLineComments removes // comments so a commented-out log call is not inventoried, and so
// a comment that happens to contain "log.Printf" does not become a finding.
func stripLineComments(src, lang string) string {
	var b strings.Builder
	for _, line := range strings.Split(src, "\n") {
		if i := strings.Index(line, "//"); i >= 0 {
			line = line[:i]
		}
		if lang == "kotlin" {
			if i := strings.Index(line, "*"); i == 0 {
				line = ""
			}
		}
		b.WriteString(line)
		b.WriteByte('\n')
	}
	return b.String()
}

// ---------------------------------------------------------------------------
// PB-SEC-3: the artifact.
// ---------------------------------------------------------------------------

// TestPBSEC3_TheLogSinkInventoryIsAnArtifactAndIsCurrent.
//
// The golden is the evidence the criterion demands. A diff means one of two things and both
// need a human: a new logging call site appeared on the phone-side path, or an existing one
// moved. Neither is forbidden -- PushTokens.kt logs a legitimate, content-free line saying push
// is unavailable in this build -- but neither may land unseen.
func TestPBSEC3_TheLogSinkInventoryIsAnArtifactAndIsCurrent(t *testing.T) {
	sinks := scanLogSinks(t)

	var lines []string
	for _, s := range sinks {
		lines = append(lines, s.row())
	}
	got := strings.Join(lines, "\n")
	if got != "" {
		got += "\n"
	}

	path := logScanArtifactPath(t)
	if *updateLogScan {
		header := "# PB-SEC-3 log-sink inventory. GENERATED by `go test ./android/gate/ " +
			"-run TestPBSEC3 -update-logscan`.\n" +
			"# Every logging call site on the PHONE-SIDE path (the Kotlin app, the bound facade,\n" +
			"# the phone core). The daemon, the relay server and the TUI are deliberately out of\n" +
			"# scope: they log at length and they do not run on the handset.\n" +
			"# Columns: area <TAB> file:line <TAB> the call\n"
		if err := os.WriteFile(path, []byte(header+got), 0o644); err != nil {
			t.Fatalf("write %s: %v", mustRel(t, path), err)
		}
		t.Logf("rewrote %s (%d sink(s))", mustRel(t, path), len(sinks))
		return
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("PB-SEC-3: %s does not exist. The criterion is \"automated log scan ... "+
			"EVIDENCE ARTIFACT REQUIRED, NOT 'reviewed'\", and a scan that leaves nothing "+
			"behind is indistinguishable from one that was never run. The scan found %d "+
			"phone-side logging call site(s) just now; run `go test ./android/gate/ -run "+
			"TestPBSEC3 -update-logscan` and review the result: %v",
			mustRel(t, path), len(sinks), err)
	}

	var want []string
	for _, line := range strings.Split(string(raw), "\n") {
		if strings.TrimSpace(line) == "" || strings.HasPrefix(line, "#") {
			continue
		}
		want = append(want, line)
	}
	wantJoined := strings.Join(want, "\n")
	if wantJoined != "" {
		wantJoined += "\n"
	}
	if got != wantJoined {
		t.Errorf("PB-SEC-3: the phone-side log-sink inventory has changed.\n\nRECORDED:\n%s\n"+
			"FOUND NOW:\n%s\nA new logging call site on the handset path needs a reviewer to "+
			"say what it prints. Regenerate with -update-logscan once that is done",
			indent(wantJoined), indent(got))
	}
}

// TestPBSEC3_NoPhoneSideLogCallTakesSessionContentOrSecretMaterial is the assertion the
// inventory exists to make enforceable.
//
// It looks at what each call site's ARGUMENTS are, not merely that it exists. The identifiers
// below are the ones that hold decrypted content or key material in this codebase; a log line
// that interpolates any of them is putting it in a buffer that `adb logcat`, a bug report and
// every app holding READ_LOGS on an older build can read.
//
// LEGITIMATE PASSER TODAY. The phone-side path holds exactly one logging call site
// (android/.../push/PushTokens.kt), and it logs a content-free line saying push is unavailable
// in this build. That is the correct state, and this test is what keeps it: the sink inventory
// above records WHAT EXISTS so a reviewer sees a new one, and this test decides whether what
// exists is safe. The zero-sinks branch is a Fatalf rather than a pass for the reason the
// probe found elsewhere in this slice -- an assertion over an empty set is not a satisfied
// assertion, and a scan that stopped matching would otherwise report the cleanest possible
// result.
func TestPBSEC3_NoPhoneSideLogCallTakesSessionContentOrSecretMaterial(t *testing.T) {
	sinks := scanLogSinks(t)
	if len(sinks) == 0 {
		// Not a pass. Zero sinks means the SCAN found nothing, and the likeliest cause of a
		// scan finding nothing is a scan that stopped working -- the patterns no longer match,
		// or the roots no longer resolve. PushTokens.kt holds a known Log.w today.
		t.Fatalf("PB-SEC-3: the scan found ZERO phone-side logging call sites. At least one is " +
			"known to exist (android/.../push/PushTokens.kt logs that push is unavailable in " +
			"this build), so this is a broken scan rather than a clean codebase -- and a broken " +
			"scan is what makes the assertion below pass while saying nothing")
	}

	// Identifiers that carry decrypted session content, typed input, or key material.
	dangerous := map[string]string{
		"payload":     "the sealed push payload / frame body",
		"plaintext":   "decrypted content",
		"contentKey":  "the epoch content tier key",
		"content_key": "the epoch content tier key",
		"wakeKey":     "the epoch wake tier key",
		"wake_key":    "the epoch wake tier key",
		"snapshot":    "a rendered terminal grid",
		"snapText":    "a rendered terminal grid",
		"keystroke":   "typed input",
		"gateToken":   "the take_control gate token, bound into a signature",
		"privateKey":  "private key material",
		"secret":      "secret material",
		"token":       "a push or auth token",
		"sas":         "the pairing short authentication string",
	}

	var findings []string
	for _, s := range sinks {
		lower := strings.ToLower(s.Call)
		for ident, why := range dangerous {
			if strings.Contains(lower, strings.ToLower(ident)) {
				findings = append(findings, s.File+":"+itoa(s.Line)+" logs "+ident+
					" ("+why+"): "+s.Call)
			}
		}
	}
	sort.Strings(findings)
	if len(findings) > 0 {
		t.Errorf("PB-SEC-3: %d phone-side log call(s) take session content or secret material. "+
			"The Android log buffer is shared, survives the process, is captured verbatim in a "+
			"bug report, and is readable by `adb logcat` from any workstation the handset has "+
			"ever trusted -- so this defeats the at-rest sealing S15 built:\n\t%s",
			len(findings), strings.Join(findings, "\n\t"))
	}
}

func indent(s string) string {
	if s == "" {
		return "\t(empty)\n"
	}
	var b strings.Builder
	for _, line := range strings.Split(strings.TrimRight(s, "\n"), "\n") {
		b.WriteString("\t" + line + "\n")
	}
	return b.String()
}
