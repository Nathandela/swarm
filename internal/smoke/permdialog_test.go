//go:build realcli

// =============================================================================
//  PERMISSION-DIALOG CHARACTERIZATION  (Mirror M1.1, bead agents-tracker-dwwv.2.1)
// =============================================================================
//
// WHAT THIS DOES
//   Drives the REAL `claude` CLI into its tool-approval dialog (Bash and Edit
//   variants), dumps the rendered grid at every decision point, and characterizes
//   the ACCEPTED KEYS empirically: what a bare digit does, whether Enter is needed,
//   what selects deny. M1.2 injects those keys into the PTY on a phone approval, so
//   they are a RECORDED fixture per CLI version, never an assumption.
//
// !!! BILLABLE -- GATED -- NEVER IN CI -- NEVER AUTOMATIC !!!
//   Two gates, both required: the `//go:build realcli` tag and SWARM_REALCLI=1.
//
// EXACT HUMAN COMMAND (run only when you intend to spend money):
//
//     SWARM_REALCLI=1 go test -tags realcli -run TestRealCLIPermissionDialog \
//       -timeout 20m -v ./internal/smoke
//
//   Env (all have defaults):
//     SWARM_PERMDIALOG_DUMP    dump root (default /tmp/swarm-permdialog)
//     SWARM_PERMDIALOG_MODEL   model alias for the capture runs (default sonnet)
//
//   The runs happen in a scratch directory under the dump root, OUTSIDE this repo,
//   and the child environment has every SWARM_* variable stripped (dialogEnv), so
//   no swarm hook can fire during a capture.
//
//   VERIFY compile-only (NEVER run): go build -tags realcli ./... ;
//                                    go vet -tags realcli ./...
// =============================================================================

package smoke

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

// Grid markers the scenarios wait on. Each is a literal a real claude screen
// prints; they are the harness's clock, so a capture never guesses at timing.
const (
	// Both variants open their question row with this stem and diverge after it:
	// Bash asks "Do you want to proceed?", Edit asks "Do you want to make this edit
	// to <file>?" (observed, claude 2.1.231).
	markerApproval = "Do you want to "
	markerTrust    = "trust this folder"
	markerBusy     = "esc to interrupt"
)

// Wall-clock bounds. A dialog is human-timescale; these are generous enough for a
// cold model start and tight enough that a wedged run dies rather than bills.
const (
	waitDialog = 90 * time.Second
	waitIdle   = 90 * time.Second
	settle     = 2500 * time.Millisecond
)

// optionLineRE matches one numbered option row of a claude dialog, capturing the
// selection marker (U+276F when preselected), the digit, and the label verbatim.
var optionLineRE = regexp.MustCompile(`(?m)^\s*(❯?)\s*(\d)\.\s+(.*?)\s*$`)

// dialogOptions returns the dialog's numbered option rows exactly as rendered.
func dialogOptions(text string) []string {
	var out []string
	for _, m := range optionLineRE.FindAllStringSubmatch(text, -1) {
		marker := " "
		if m[1] != "" {
			marker = "❯"
		}
		out = append(out, marker+" "+m[2]+". "+m[3])
	}
	return out
}

// preselected returns the digit of the option carrying the selection marker.
func preselected(text string) string {
	for _, m := range optionLineRE.FindAllStringSubmatch(text, -1) {
		if m[1] != "" {
			return m[2]
		}
	}
	return ""
}

// denyDigit returns the digit of the option whose label begins with "No" -- the
// dialog's own refusal row. It is READ FROM THE SCREEN rather than assumed, so a
// variant that numbers its options differently is characterized, not mis-keyed.
func denyDigit(text string) (string, bool) {
	for _, m := range optionLineRE.FindAllStringSubmatch(text, -1) {
		if strings.HasPrefix(m[3], "No") {
			return m[2], true
		}
	}
	return "", false
}

// dumpRoot is the directory every capture writes under.
func dumpRoot() string {
	if v := os.Getenv("SWARM_PERMDIALOG_DUMP"); v != "" {
		return v
	}
	return filepath.Join(os.TempDir(), "swarm-permdialog")
}

// captureModel is the model alias the capture runs use (a cheap one by default:
// the runs only have to call one tool).
func captureModel() string {
	if v := os.Getenv("SWARM_PERMDIALOG_MODEL"); v != "" {
		return v
	}
	return "sonnet"
}

// TestRealCLIPermissionDialog is the single human-run entrypoint for M1.1.
func TestRealCLIPermissionDialog(t *testing.T) {
	requireOptIn(t)
	ver, err := claudeVersion()
	if err != nil {
		t.Skipf("claude not runnable (%v); nothing to characterize", err)
	}
	t.Logf("claude --version: %s", ver)
	if err := os.WriteFile(filepath.Join(dumpRoot(), "version.txt"), []byte(ver+"\n"), 0o644); err != nil {
		if mkErr := os.MkdirAll(dumpRoot(), 0o755); mkErr == nil {
			_ = os.WriteFile(filepath.Join(dumpRoot(), "version.txt"), []byte(ver+"\n"), 0o644)
		}
	}
	t.Run("bash", testBashDialog)
	t.Run("edit", testEditDialog)
}

// prompts driving each variant into its dialog. Each turn names a DIFFERENT
// command/edit so no per-session allowlisting from an earlier answer can suppress
// a later dialog.
// The command must have a SIDE EFFECT outside the workspace: claude 2.1.231 runs
// read-only commands (echo, ls) in its own sandbox with NO approval dialog at all,
// even under --permission-mode manual (observed run 2, docs/verification/mirror-m1.md).
// A write outside the workspace is what reaches the approval path.
var bashPrompts = []string{
	"Use the Bash tool to run exactly this command and nothing else: touch /tmp/swarm-m1-one.marker",
	"Use the Bash tool to run exactly this command and nothing else: touch /tmp/swarm-m1-two.marker",
	"Use the Bash tool to run exactly this command and nothing else: touch /tmp/swarm-m1-three.marker",
	"Use the Bash tool to run exactly this command and nothing else: touch /tmp/swarm-m1-four.marker",
}

// Each edit turn targets a DIFFERENT line of the seed file, so a denied turn does
// not invalidate the next one -- and the file itself is the ground truth for which
// key allowed and which denied.
var editPrompts = []string{
	"Use the Edit tool (never Write, never Bash) to replace the word one with ONE in note.txt.",
	"Use the Edit tool (never Write, never Bash) to replace the word two with TWO in note.txt.",
	"Use the Edit tool (never Write, never Bash) to replace the word three with THREE in note.txt.",
	"Use the Edit tool (never Write, never Bash) to replace the word four with FOUR in note.txt.",
}

// testBashDialog characterizes the Bash-approval variant.
func testBashDialog(t *testing.T) {
	dir := filepath.Join(dumpRoot(), "bash")
	scratch := filepath.Join(dir, "scratch")
	if err := os.MkdirAll(scratch, 0o755); err != nil {
		t.Fatalf("scratch: %v", err)
	}
	argv := []string{"claude", "--permission-mode", "manual", "--strict-mcp-config", "--model", captureModel(), bashPrompts[0]}
	runDialogScenario(t, "bash", dir, scratch, argv, bashPrompts[1:])
}

// testEditDialog characterizes the Edit-approval variant. Read is pre-allowed so
// the run reaches the EDIT dialog rather than spending a turn on a read approval.
func testEditDialog(t *testing.T) {
	dir := filepath.Join(dumpRoot(), "edit")
	scratch := filepath.Join(dir, "scratch")
	if err := os.MkdirAll(scratch, 0o755); err != nil {
		t.Fatalf("scratch: %v", err)
	}
	if err := os.WriteFile(filepath.Join(scratch, "note.txt"), []byte("one\ntwo\nthree\nfour\n"), 0o644); err != nil {
		t.Fatalf("seed note.txt: %v", err)
	}
	argv := []string{"claude", "--permission-mode", "manual", "--strict-mcp-config", "--allowedTools=Read", "--model", captureModel(), editPrompts[0]}
	runDialogScenario(t, "edit", dir, scratch, argv, editPrompts[1:])
}

// runDialogScenario is the shared four-turn characterization: the same dialog is
// answered four ways -- bare digit, Esc, bare Enter, deny digit -- with the grid
// dumped before and after each, and every observation written to the run log.
func runDialogScenario(t *testing.T, variant, dumpDir, scratch string, argv, follow []string) {
	t.Helper()
	s, err := startDialogSession(argv, scratch, dialogEnv(), 100, 30, dumpDir)
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	defer s.close()
	s.note("argv: %v", argv)
	s.note("cwd: %s", scratch)

	// A first run in a new directory asks for trust before anything else. Wait for
	// whichever screen arrives first rather than sleeping a guessed interval.
	first, err := s.waitForAny([]string{markerTrust, markerApproval, markerBusy}, 60*time.Second)
	if err != nil {
		_, _ = s.record("00-startup-TIMEOUT")
		t.Fatalf("no recognizable startup screen: %v", err)
	}
	s.note("startup screen: %q", first)
	if first == markerTrust {
		time.Sleep(500 * time.Millisecond)
		if _, err := s.record("00-trust-dialog"); err != nil {
			t.Fatalf("record trust: %v", err)
		}
		s.note("trust dialog seen; answering 1")
		if err := s.send("1"); err != nil {
			t.Fatalf("send trust: %v", err)
		}
		if err := s.waitAbsent(markerTrust, 30*time.Second); err != nil {
			t.Fatalf("trust dialog did not clear: %v", err)
		}
	}

	// Turn 1 -- bare digit on the preselected allow option.
	dialog := awaitDialog(t, s, "10-"+variant+"-dialog")
	allow := preselected(dialog)
	if allow == "" {
		allow = "1"
	}
	s.note("turn1: sending bare %q (no CR)", allow)
	sendKeys(t, s, allow)
	time.Sleep(settle)
	after, _ := s.record("11-" + variant + "-after-bare-digit")
	s.note("turn1 after bare %q: dialog present=%v", allow, strings.Contains(after, markerApproval))

	// The idle composer that follows an answered dialog is the recognizer's
	// primary NEGATIVE fixture; capture it here rather than staging one.
	if err := s.waitAbsent(markerBusy, waitIdle); err != nil {
		s.note("turn1: never went idle: %v", err)
	}
	time.Sleep(settle)
	if _, err := s.record("12-" + variant + "-idle-composer"); err != nil {
		t.Fatalf("record idle: %v", err)
	}

	// Turn 2 -- Esc.
	submitPrompt(t, s, follow[0])
	time.Sleep(settle)
	if _, err := s.record("13-" + variant + "-midoutput"); err != nil {
		t.Fatalf("record midoutput: %v", err)
	}
	awaitDialog(t, s, "20-"+variant+"-dialog-2")
	s.note("turn2: sending ESC (0x1b)")
	sendKeys(t, s, "\x1b")
	time.Sleep(settle)
	afterEsc, _ := s.record("21-" + variant + "-after-esc")
	s.note("turn2 after ESC: dialog present=%v", strings.Contains(afterEsc, markerApproval))
	if err := s.waitAbsent(markerBusy, waitIdle); err != nil {
		s.note("turn2: never went idle: %v", err)
	}

	// Turn 3 -- bare Enter on the preselected option.
	submitPrompt(t, s, follow[1])
	awaitDialog(t, s, "30-"+variant+"-dialog-3")
	s.note("turn3: sending bare CR")
	sendKeys(t, s, "\r")
	time.Sleep(settle)
	afterCR, _ := s.record("31-" + variant + "-after-enter")
	s.note("turn3 after CR: dialog present=%v", strings.Contains(afterCR, markerApproval))
	if err := s.waitAbsent(markerBusy, waitIdle); err != nil {
		s.note("turn3: never went idle: %v", err)
	}

	// Turn 4 -- the deny DIGIT, read off the screen rather than assumed.
	submitPrompt(t, s, follow[2])
	dialog4 := awaitDialog(t, s, "40-"+variant+"-dialog-4")
	deny, ok := denyDigit(dialog4)
	if !ok {
		s.note("turn4: no option row starts with \"No\"; deny digit unknown, skipping")
		return
	}
	s.note("turn4: sending bare deny digit %q", deny)
	sendKeys(t, s, deny)
	time.Sleep(settle)
	afterDeny, _ := s.record("41-" + variant + "-after-deny-digit")
	s.note("turn4 after %q: dialog present=%v", deny, strings.Contains(afterDeny, markerApproval))
	if err := s.waitAbsent(markerBusy, waitIdle); err != nil {
		s.note("turn4: never went idle: %v", err)
	}
	time.Sleep(settle)
	if _, err := s.record("50-" + variant + "-final"); err != nil {
		t.Fatalf("record final: %v", err)
	}
	for _, line := range s.observations() {
		t.Log(line)
	}
}

// awaitDialog waits for the approval dialog, dumps it under label, and logs its
// option rows verbatim. A dialog that never appears fails the run loudly.
func awaitDialog(t *testing.T, s *dialogSession, label string) string {
	t.Helper()
	if err := s.waitFor(markerApproval, waitDialog); err != nil {
		_, _ = s.record(label + "-TIMEOUT")
		for _, line := range s.observations() {
			t.Log(line)
		}
		t.Fatalf("%s: %v", label, err)
	}
	// One settle so a half-drawn dialog is not what gets recorded.
	time.Sleep(700 * time.Millisecond)
	text, err := s.record(label)
	if err != nil {
		t.Fatalf("record %s: %v", label, err)
	}
	s.note("%s options: %v (preselected=%q)", label, dialogOptions(text), preselected(text))
	return text
}

// sendKeys writes keystrokes to the live CLI, failing the run if the PTY is gone.
func sendKeys(t *testing.T, s *dialogSession, data string) {
	t.Helper()
	if err := s.send(data); err != nil {
		t.Fatalf("send %q: %v", data, err)
	}
}

// submitPrompt types a follow-up prompt into the composer and submits it in a
// SEPARATE frame, so the CLI's paste heuristic does not swallow the newline
// (internal/submitframe's rule, applied to the harness).
func submitPrompt(t *testing.T, s *dialogSession, prompt string) {
	t.Helper()
	if err := s.waitAbsent(markerApproval, 30*time.Second); err != nil {
		s.note("submitPrompt: a dialog is still up: %v", err)
	}
	sendKeys(t, s, prompt)
	time.Sleep(400 * time.Millisecond)
	sendKeys(t, s, "\r")
	s.note("submitted: %s", prompt)
}
