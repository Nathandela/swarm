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
	// Truncated records that wholeCall could NOT close this call's parentheses within its bound,
	// so Call holds a PREFIX. It is not in the artifact row: it is a property of the scan, not of
	// the call site, and it makes the argument check fail rather than read a partial call as whole.
	Truncated bool
}

// logSinkNote is the REVIEWER NOTE for ONE logging call site: what it prints, and why that is
// safe to put in a buffer `adb logcat` can read.
//
// PB-SEC-3's criterion is "evidence artifact required, NOT reviewed", and the note is how the
// artifact stops being a bare list. An inventory row on its own records that a sink exists; it
// does not record that anyone looked at it, so a reader six months out cannot tell a reviewed
// line from one that merely survived a regeneration.
//
// KEYED ON THE CALL, NOT THE FILE, AND NOT THE LINE. (B73(2), residual 4.16.)
//
// It was keyed by FILE, on the stated grounds that a line number churns on any edit above it and
// a note that churns is a note nobody rewrites. The churn half of that is true; the conclusion
// did not follow, and file-keying bought a worse defect: EVERY call in a noted file inherited
// that note, including a new one nobody had reviewed. B72's probe produced the sharpest form --
// a call dumping the epoch content key inventoried under "No token value is in scope on either
// path", a sentence written about two different calls. The artifact attached an exoneration to
// the leak.
//
// The call TEXT is the stable key, and this was measured rather than assumed:
//
//   - An edit ABOVE a call already fails the artifact test today, because the row carries
//     file:LINE. So file-keying never actually spared anyone that churn -- the regeneration and
//     the reviewer diff happen regardless. It only spared them rewriting the note, which
//     call-keying spares them too, since the call text is unchanged.
//   - RE-WRAPPING a call changes nothing at all: wholeCall normalises whitespace, so the call
//     text is MORE stable than the line number already in the artifact.
//   - Changing what a call PASSES does break its note, and that is the point. Editing a log
//     call's arguments is precisely the moment its safety note must be re-justified, and the row
//     changes anyway, so this adds no review event that did not already exist.
//
// A sink with no entry here FAILS THE ARTIFACT TEST, in `-update-logscan` mode too. That is
// deliberate and it is the whole mechanism: regenerating is the moment a human has to justify
// the diff, so a new sink cannot be regenerated into a green without one.
type logSinkNote struct {
	File string // repo-relative, as the inventory records it
	Call string // the normalised call text this note was written about
	Note string
}

// The two arms carry the SAME note because one review covered both and it describes both. They
// are listed separately because coverage is per call: adding a third call here must not inherit
// what was written about these two.
const pushTokensNote = "PB-PUSH-5's graceful-and-loud degradation, both arms: static prose " +
	"plus the caught Throwable. No token value is in scope on either path -- getInstance() " +
	"threw because this build configures no Firebase project, or the asynchronous fetch " +
	"failed and produced none."

var pushTokensPath = filepath.Join("android", "app", "src", "main", "kotlin", "dev", "swarm",
	"phone", "push", "PushTokens.kt")

var logSinkNotes = []logSinkNote{
	{
		File: pushTokensPath,
		Call: `Log.w(TAG, "push token fetch failed; this launch registered no token and the " + ` +
			`"phone will not receive background wakes until the next one", e)`,
		Note: pushTokensNote,
	},
	{
		File: pushTokensPath,
		Call: `Log.w(TAG, "push unavailable: no Firebase project is configured for this build; " + ` +
			`"the phone works without push and will not receive background wakes", e)`,
		Note: pushTokensNote,
	},
}

// noteKey identifies the call a reviewer note covers.
func noteKey(file, call string) string { return file + "\t" + call }

// noteFor returns the reviewer note written about THIS call, or "" if nobody has written one.
func noteFor(file, call string) string {
	for _, n := range logSinkNotes {
		if noteKey(n.File, n.Call) == noteKey(file, call) {
			return n.Note
		}
	}
	return ""
}

// unreviewedSinks reports the sinks no reviewer note covers, in the artifact's own terms. It is a
// function rather than a loop inside the artifact test because the note lookup has TWO consumers
// -- this check and the artifact row -- and a fix applied to one and not the other leaves the
// artifact still carrying a note nobody wrote for the row beside it.
func unreviewedSinks(sinks []logSink) []string {
	var out []string
	for _, s := range sinks {
		if strings.TrimSpace(noteFor(s.File, s.Call)) == "" {
			out = append(out, s.File+":"+itoa(s.Line)+": "+s.Call)
		}
	}
	return out
}

func (s logSink) row() string {
	return fmt.Sprintf("%s\t%s:%d\t%s\t%s", s.Area, s.File, s.Line, s.Call, noteFor(s.File, s.Call))
}

func scanLogSinks(t *testing.T) []logSink {
	t.Helper()
	return scanLogSinksIn(t, phoneSideRoots(t))
}

// scanLogSinksIn is the scan over an arbitrary set of roots. The roots are a parameter so the
// discrimination the argument check performs can be exercised on SYNTHETIC sources
// (pbsec3_logdiscrimination_test.go) rather than only on whatever the repository happens to
// contain today: a guard whose only inputs are the call sites that already exist can be proved
// to accept them and never proved to reject anything.
func scanLogSinksIn(t *testing.T, roots map[string]string) []logSink {
	t.Helper()
	var out []logSink
	for area, dir := range roots {
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
			// RAW SOURCE, COMMENTS INCLUDED. See the note on wholeCall's neighbour below.
			lines := strings.Split(string(raw), "\n")
			for i, line := range lines {
				for _, p := range logSinkPatterns {
					if p.Lang != lang || !p.Re.MatchString(line) {
						continue
					}
					// THE WHOLE CALL, not the matched line. This codebase wraps long
					// argument lists, so a line-scoped capture sees `Log.w(TAG,` and
					// nothing else -- and the argument-inspection test below would then be
					// blind to every logged identifier that happened to sit on the
					// continuation line. That is a guard defeated by pressing Enter.
					call, complete := wholeCall(lines, i)
					out = append(out, logSink{
						Area:      area,
						File:      mustRel(t, path),
						Line:      i + 1,
						Call:      call,
						Truncated: !complete,
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

// wholeCallMaxLines bounds the join. An unbounded walk would swallow the rest of the file on a
// source whose parentheses never balance -- which is not hypothetical, since the depth count is
// not literal-aware and a `:(` in a message is enough to unbalance it. The bound is generous
// because EXCEEDING IT NOW FAILS THE GATE (see wholeCall), so it must clear real wrapped calls
// comfortably; the longest in this tree is three lines.
const wholeCallMaxLines = 40

// wholeCall joins the matched line with its continuations until the call's parentheses balance,
// so the inventory records every argument rather than only those that fit on one line. It reports
// whether it actually CLOSED the call.
//
// THE SECOND RETURN VALUE IS THE SECURITY-RELEVANT PART, and B72 added it after a probe.
//
// The bound was always correct; returning the prefix as though it were the whole call was not.
// A wrapped call whose sensitive argument sat past the bound was handed to the argument check
// already truncated, and the check read what it was given and reported clean. That is the same
// fail-open shape as the comment strip removed below -- a lossy producer feeding a correct
// consumer -- and it is why the caller now marks such a sink and the argument check refuses it.
// An un-examinable call must fail, not pass quietly.
func wholeCall(lines []string, start int) (string, bool) {
	depth := 0
	complete := false
	var parts []string
	for i := start; i < len(lines) && i < start+wholeCallMaxLines; i++ {
		parts = append(parts, strings.TrimSpace(lines[i]))
		for _, c := range lines[i] {
			switch c {
			case '(':
				depth++
			case ')':
				depth--
			}
		}
		if depth <= 0 {
			complete = true
			break
		}
	}
	return strings.Join(strings.Fields(strings.Join(parts, " ")), " "), complete
}

// ---------------------------------------------------------------------------
// WHY THE SCAN READS RAW SOURCE, COMMENTS INCLUDED. (B72; ADR-007 B66 is the precedent.)
// ---------------------------------------------------------------------------
//
// This scan used to strip `//` comments first, for two reasonable-sounding reasons: a
// commented-out log call should not be inventoried, and a comment containing `log.Printf` should
// not become a finding. The stripper was not string-literal-aware, so the `//` in an ordinary URL
// blanked the rest of the line -- and under PB-SEC-3's assertion that is FAIL-OPEN. Measured, by
// planting each shape in real production Kotlin and running this gate:
//
//	val doc = "https://swarm.dev/logging"; Log.w(TAG, plaintext + doc)
//	    -> the line became `val doc = "https:` before any pattern applied. Not merely
//	       un-flagged: never inventoried, so both PB-SEC-3 tests reported clean.
//
//	Log.w(TAG, "see https://swarm.dev/logging: $plaintext")
//	    -> the pattern matched before the truncation point, so the row WAS inventoried and
//	       looked reviewed, while the argument check was handed `Log.w(TAG, "see https:` and
//	       never saw the plaintext. The bookkeeping half kept counting a row the content half
//	       could no longer read. This one needs no adversary -- a doc link in a log message is
//	       the whole of it -- and in a file that already carried a reviewer note the only
//	       symptom was ROW CHURN, which `-update-logscan` is documented to absorb.
//
// So the strip is gone, and with it the entire "is this text code or prose" question, which is
// the question a scanner can be made to answer wrongly. THE PRICE, paid deliberately: a
// commented-out call and a comment naming a log API each become an inventory ROW. That is the
// cheap direction -- an extra row lands in front of the reviewer-note step, which is a human
// reading a diff, whereas a missed row is a leak with nothing downstream to catch it.
//
// Do not reinstate the strip to quieten the inventory. Write the note.

// ---------------------------------------------------------------------------
// What a call site LOGS, as against what it MENTIONS.
// ---------------------------------------------------------------------------

// dangerousLogIdentifiers are the identifiers that carry decrypted session content, typed
// input, or key material in this codebase, mapped to what each one holds.
var dangerousLogIdentifiers = map[string]string{
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

// loggedData reduces one call site to the text whose VALUE can reach the log buffer: every
// expression the call passes, plus every Kotlin string template interpolated into its literals.
// Literal prose is dropped.
//
// THE DISCRIMINATION THIS DRAWS, and why the guard was worthless without it. The check used to
// substring-match the identifier list against the WHOLE call source, string literals included.
// That reads what the author WROTE rather than what the device HOLDS, and it is wrong in both
// directions at once:
//
//   - It flagged `Log.w(TAG, "push token fetch failed; ...", e)` -- static prose, a Throwable,
//     on a path taken precisely BECAUSE the fetch failed and no token exists -- solely for
//     containing the word "token". A guard that fires on the word for the thing rather than the
//     thing is a guard someone silences.
//   - It could not see past a literal at all, so it caught only the leaks polite enough to name
//     themselves in prose beside the value.
//
// So: what is interpolated or passed as an argument is DATA; what is literal prose is not.
// `"...token..."` as text is clean, while `"x: $token"`, `"${resp.token}"` and a bare
// `Log.d(TAG, tokenVar)` all remain findings.
//
// LANGUAGE-AGNOSTIC ON PURPOSE, though templates are a Kotlin mechanism. A Go literal holding a
// bare `$` would have the text after it read as data, which can only ever ADD a finding a
// reviewer then dismisses -- the safe direction for this guard, and cheaper than threading the
// source language through the inventory to buy nothing.
func loggedData(call string) string {
	var data strings.Builder
	var quote byte // 0 outside any literal
	for i := 0; i < len(call); i++ {
		c := call[i]
		if quote == 0 {
			if c == '"' || c == '\'' || c == '`' {
				quote = c
				continue
			}
			data.WriteByte(c)
			continue
		}
		switch {
		case c == '\\' && quote != '`':
			i++ // an escape neither closes a literal nor opens a template
		case c == quote:
			quote = 0
		case c == '$' && quote == '"':
			i = writeTemplateExpr(call, i, &data)
		}
	}
	return data.String()
}

// writeTemplateExpr copies one Kotlin string template's EXPRESSION into data and returns the
// index of its last byte.
//
// `${expr}` yields expr. `$ident` yields the identifier and STOPS THERE, because a dotted tail
// after a braceless template is literal text in Kotlin -- `"$a.b"` prints a.toString() followed
// by the characters ".b" -- and swallowing it would reintroduce the very prose-as-data confusion
// this function exists to remove.
func writeTemplateExpr(s string, i int, data *strings.Builder) int {
	j := i + 1
	if j < len(s) && s[j] == '{' {
		depth := 0
		for ; j < len(s); j++ {
			switch s[j] {
			case '{':
				depth++
			case '}':
				if depth--; depth == 0 {
					data.WriteByte(' ')
					return j
				}
			default:
				data.WriteByte(s[j])
			}
		}
		return j
	}
	for ; j < len(s) && isIdentByte(s[j]); j++ {
		data.WriteByte(s[j])
	}
	data.WriteByte(' ')
	return j - 1
}

func isIdentByte(c byte) bool {
	return c == '_' || (c >= '0' && c <= '9') || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
}

// dangerousLogFindings reports which dangerous identifiers a call site actually puts in the log
// buffer, in a stable order.
func dangerousLogFindings(call string) []string {
	data := strings.ToLower(loggedData(call))
	var out []string
	for ident := range dangerousLogIdentifiers {
		if strings.Contains(data, strings.ToLower(ident)) {
			out = append(out, ident)
		}
	}
	sort.Strings(out)
	return out
}

// ---------------------------------------------------------------------------
// PB-SEC-3: the artifact.
// ---------------------------------------------------------------------------

// TestPBSEC3_TheLogSinkInventoryIsAnArtifactAndIsCurrent.
//
// The golden is the evidence the criterion demands. A diff means one of two things and both
// need a human: a new logging call site appeared on the phone-side path, or an existing one
// moved. Neither is forbidden -- PushTokens.kt logs two legitimate, content-free lines saying
// push is unavailable or its token fetch failed -- but neither may land unseen.
//
// Each row carries its file's REVIEWER NOTE, and a file with no note fails BEFORE the golden is
// consulted and before -update-logscan can rewrite it. Without that, "not reviewed" is exactly
// what the artifact would become: a new sink would regenerate into a passing file with nobody
// having said what it prints.
func TestPBSEC3_TheLogSinkInventoryIsAnArtifactAndIsCurrent(t *testing.T) {
	sinks := scanLogSinks(t)

	unreviewed := unreviewedSinks(sinks)
	if len(unreviewed) > 0 {
		t.Fatalf("PB-SEC-3: %d phone-side log call site(s) that NO REVIEWER NOTE COVERS. The "+
			"criterion is \"evidence artifact required, NOT 'reviewed'\", and a row that records "+
			"only that a sink EXISTS does not record that anyone looked at it. Add a logSinkNote "+
			"whose Call is the call text below, saying what THIS call prints and why it is safe "+
			"in a buffer `adb logcat` can read, THEN regenerate.\n\nNotes are keyed per CALL, not "+
			"per file (B73(2)): a sibling call in the same file having a note does not review "+
			"this one, and copying that note across without reading this call is precisely the "+
			"false assurance the per-call keying exists to prevent:\n\t%s",
			len(unreviewed), strings.Join(unreviewed, "\n\t"))
	}

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
			"# Columns: area <TAB> file:line <TAB> the call <TAB> the reviewer's note on the file\n" +
			"# The note is not decoration: a sink in a file with no note fails the artifact test,\n" +
			"# in -update mode too, so a new call site cannot be regenerated into a green.\n"
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

	// A call the join could not close is a call this test CANNOT examine: what follows below
	// would read a prefix and find nothing, which is the fail-open B72 removed one function
	// over. Refuse it instead of reporting clean on it.
	var unexaminable []string
	for _, s := range sinks {
		if s.Truncated {
			unexaminable = append(unexaminable, s.File+":"+itoa(s.Line)+": "+s.Call)
		}
	}
	if len(unexaminable) > 0 {
		t.Errorf("PB-SEC-3: %d phone-side log call(s) could not be read in full -- wholeCall did "+
			"not close the parentheses within %d lines, so the argument check below would be "+
			"inspecting a PREFIX and would report clean whatever the later arguments hold. Either "+
			"the call wraps past the bound, or a parenthesis inside a string literal unbalanced "+
			"the count. Rewrite the call so it closes, or fix the join -- do NOT raise the bound "+
			"to make this quiet:\n\t%s",
			len(unexaminable), wholeCallMaxLines, strings.Join(unexaminable, "\n\t"))
	}

	var findings []string
	for _, s := range sinks {
		for _, ident := range dangerousLogFindings(s.Call) {
			findings = append(findings, s.File+":"+itoa(s.Line)+" logs "+ident+
				" ("+dangerousLogIdentifiers[ident]+"): "+s.Call)
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
