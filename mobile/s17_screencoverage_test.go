package swarmmobile_test

// FAILING-FIRST (TDD RED, GG-5) source-level guards for slice S17: the push receiver's place
// on the bound surface (PB-BIND-3) and the shape of what it hands the lock screen (PB-PUSH-4).
//
// WHY A SEPARATE FILE, for the reason S16's equivalent states: coverage_test.go's
// requiredScreenElements is S8's transcription of PB-BIND-3, and editing a shipped slice's
// enumeration from inside another slice puts this slice's RED inside S8's test names, where an
// auditor reading the S8 evidence finds failures the S8 evidence never mentions. The element
// below is S17's, it points at the SAME checked-in table, and it is enforced the way S8 does it.
//
// These guards do not import the runtime; they read the facade's source and the checked-in
// table. Nothing here claims anything about a handset -- PB-E2E-5 stays deferred.

import (
	"go/ast"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// s17ScreenElements are the elements S17 adds. Hard-coded HERE, not read from the TSV, for the
// reason S8 states: deleting a row from the table must not make the requirement vanish.
var s17ScreenElements = map[string]string{
	// PB-PUSH-4's own screen element. A push arrives, the app must render something, and the
	// decision about WHAT is not the app's to make -- only the Go core holds the wake key that
	// says the wake is genuine and the replay coordinate that says it is new.
	"push.notification": "PB-PUSH-4,PB-PUSH-3",
}

func TestS17_TheWakeReceiverIsTracedToAFacadeMethod(t *testing.T) {
	src := loadFacade(t)
	rows := s17CoverageRows(t, src.Dir)

	have := map[string]bool{}
	for _, s := range exportedSurface(src) {
		switch s.Kind {
		case "func":
			have[s.Name] = true
		case "method", "field":
			have[s.Owner+"."+s.Name] = true
		}
	}

	elements := make([]string, 0, len(s17ScreenElements))
	for el := range s17ScreenElements {
		elements = append(elements, el)
	}
	sort.Strings(elements)

	for _, el := range elements {
		methods, ok := rows[el]
		if !ok {
			t.Errorf("PB-BIND-3 COVERAGE FAILURE: screen element %q (%s) has no row in "+
				"screen_coverage.tsv. A push receiver that no row traces is a verb the Android "+
				"side can call that no screen asked for -- and PB-PUSH-9's own warning is that a "+
				"facade method can exist while no Android code ever calls it",
				el, s17ScreenElements[el])
			continue
		}
		if len(methods) == 0 {
			t.Errorf("PB-BIND-3 COVERAGE FAILURE: screen element %q has a row and no facade "+
				"method (%s)", el, s17ScreenElements[el])
			continue
		}
		for _, m := range methods {
			if !have[m] {
				t.Errorf("PB-BIND-3: screen_coverage.tsv maps %q to %q, which the facade does not "+
					"export", el, m)
			}
		}
	}
}

// s17CoverageRows parses the checked-in table into element -> methods.
func s17CoverageRows(t *testing.T, dir string) map[string][]string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(dir, "screen_coverage.tsv"))
	if err != nil {
		t.Fatalf("PB-BIND-3 requires a checked-in traceability table: %v", err)
	}
	out := map[string][]string{}
	for _, line := range strings.Split(string(raw), "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		cols := strings.Split(line, "\t")
		var methods []string
		if len(cols) > 1 {
			for _, m := range strings.Split(cols[1], ",") {
				if m = strings.TrimSpace(m); m != "" {
					methods = append(methods, m)
				}
			}
		}
		out[strings.TrimSpace(cols[0])] = methods
	}
	if len(out) == 0 {
		t.Fatalf("screen_coverage.tsv has no rows; the guard would be vacuous")
	}
	return out
}

// ---------------------------------------------------------------------------
// What the wake receiver is allowed to hand the lock screen.
// ---------------------------------------------------------------------------

// s17WakeAlertAllowedFields is the CLOSED set of fields the push alert may carry.
//
// It is closed rather than a blocklist because the failure this guards against is a field
// somebody adds in good faith: SessionID "so the user knows which one", Count "so we can say
// how many", Group "so the icon can differ". Each of those is a value the machine chose,
// rendered on the lock screen of a device that may be in someone else's hands, and each would
// need the CONTENT tier to populate -- which on a wake is locked. A blocklist has to guess the
// name; an allowlist does not.
var s17WakeAlertAllowedFields = map[string]string{
	"Text":         "the constant, content-free string the app renders",
	"ContentReady": "whether the content tier is open, which is a fact about custody and not about any session",
}

// PASSES TODAY, AND THAT IS NOT COVERAGE -- labelled so no evidence line counts it as earned.
// The RED scaffolding declares exactly the two allowed fields, so this is green from the first
// run. It is written now, with the tests it sits beside, because the field this guards against
// is added during IMPLEMENTATION ("the user should know which session"), and a fence written
// after that is a fence written to fit.
//
// TestS17_TheWakeAlertCarriesNothingTheMachineChose reads the declared struct rather than an
// instance, so it fails when the field is ADDED rather than when a test happens to populate it.
func TestS17_TheWakeAlertCarriesNothingTheMachineChose(t *testing.T) {
	src := loadFacade(t)

	found := false
	for _, s := range exportedSurface(src) {
		if s.Kind != "field" || s.Owner != "WakeAlert" {
			continue
		}
		found = true
		if _, ok := s17WakeAlertAllowedFields[s.Name]; !ok {
			t.Errorf("PB-PUSH-4/PB-PUSH-3: WakeAlert declares %s, which is not in the closed set "+
				"%v.\nEvery field on this type is rendered while the device may still be locked. "+
				"There is nothing in the payload to populate a new one from -- the wake is a "+
				"constant 78 bytes over an EMPTY plaintext (ADR-007 B20) -- so a field added here "+
				"can only be filled by going and reading content, which is the defect PB-PUSH-4 "+
				"exists to stop.", s.Line(), s17AllowedNames())
		}
	}
	if !found {
		t.Errorf("PB-PUSH-4: the facade declares no WakeAlert fields, so this fence measures "+
			"nothing. The push receiver has to hand the app SOMETHING or the requirement fails "+
			"in the other direction: a push arrives and nothing is rendered.\nallowed: %v",
			s17AllowedNames())
	}
}

func s17AllowedNames() []string {
	var out []string
	for n := range s17WakeAlertAllowedFields {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// PASSES TODAY AGAINST A STUB, AND THAT IS NOT COVERAGE -- labelled so no evidence line counts
// it as earned. HandlePushWake's RED body is a refusal, so it calls nothing and the assertion
// is vacuously satisfied until the method is implemented. It earns its green at the moment the
// implementer writes the body, which is exactly when the temptation it guards against arrives.
//
// TestS17_TheWakeReceiverNeverOpensAMailboxFrame is the source half of PB-KEY-2's tier split
// on this path.
//
// The facade already holds the machinery to open session content: viewFrame calls
// crypto.OpenMailbox, and accept() uses it on every inbound frame. A wake handler that reached
// for it -- to "enrich" the notification, or simply because the helper was there -- would
// decrypt session content on a path that runs with no user present. The Go conformance test
// measures the same property at the custody seam by counting content-KEK unwraps; this one
// names the specific call, so the failure says which line rather than which count.
func TestS17_TheWakeReceiverNeverOpensAMailboxFrame(t *testing.T) {
	src := loadFacade(t)

	var body *ast.FuncDecl
	for _, f := range src.Files {
		for _, d := range f.Decls {
			fn, ok := d.(*ast.FuncDecl)
			if !ok || fn.Name.Name != "HandlePushWake" || fn.Recv == nil {
				continue
			}
			body = fn
		}
	}
	if body == nil || body.Body == nil {
		t.Fatalf("PB-PUSH-4: the facade declares no App.HandlePushWake, so there is no push " +
			"receiver for the Android FirebaseMessagingService to call. PB-PUSH-9's warning is " +
			"that a facade method can exist with no caller; this is the case one step earlier, " +
			"where there is no method either")
	}

	forbidden := map[string]string{
		"OpenMailbox":       "opens session content under the CONTENT key",
		"viewFrame":         "decodes an inbound content frame's read model",
		"AcceptCommit":      "runs the receive transaction, which needs the content key",
		"InstallContentKey": "installs the content key from the wake path",
	}
	var found []string
	ast.Inspect(body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		name := ""
		switch fn := call.Fun.(type) {
		case *ast.Ident:
			name = fn.Name
		case *ast.SelectorExpr:
			name = fn.Sel.Name
		}
		if why, bad := forbidden[name]; bad {
			found = append(found, name+" -- "+why)
		}
		return true
	})
	sort.Strings(found)
	if len(found) > 0 {
		t.Errorf("PB-KEY-2/PB-PUSH-4: App.HandlePushWake calls %d content-tier helper(s):\n\t%s\n"+
			"A wake arrives with no user present, so the content tier is locked by definition. "+
			"The wake is opened with the WAKE key and nothing else.",
			len(found), strings.Join(found, "\n\t"))
	}
}
