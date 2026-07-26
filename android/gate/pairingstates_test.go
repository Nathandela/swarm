package gate

// FAILING-FIRST (TDD RED, GG-5) for PB-PAIR-5, and for the FAILURE MODE that let it read
// `shipped` while unmet: an amendment changes what must be true, and nothing re-checks the
// surface it moved the requirement onto.
//
// WHAT HAPPENED. PB-PAIR-5 was amended on 2026-07-25 to RETIRE `already-paired` -- unreachable
// on the phone, because the machine refuses a second pairing before minting any rendezvous --
// and to SUBSTITUTE `different_machine`, the QR belonging to a machine this phone is not pinned
// to. The Go core was changed correctly: mobile/pairing.go declares `different_machine` and
// finish() reaches it. The APP was not. `PairingStep` still declared ALREADY_PAIRED and no
// DIFFERENT_MACHINE, `stepOf` returned null for the state, and the user got the GENERIC
// pairing-failed message -- which is the opaque error the requirement exists to remove, for the
// one state the amendment created. Two further core states, `rate_limited` and `failed`, had
// never had a step either.
//
// WHY THIS FILE IS THE REMEDY AND NOT A THIRD KOTLIN TEST. Every check that existed was on ONE
// side of the seam. The Go tests pass over a state string no screen renders; the Kotlin tests
// pass over an enum constant no core state produces. Neither can see the other, so the two ends
// of PB-PAIR-5's terminal-state alphabet drifted with every check green. This is the only
// check in the tree that reads BOTH sources and compares them, so an amendment that moves the
// Go alphabet fails HERE until the screen follows it.
//
// IT READS CHECKED-IN SOURCE ONLY: no Android SDK, no JDK, no emulator, no handset. Nothing
// here claims anything about PB-E2E-5's deferred set.

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// pairingStateFloor is the "cannot pass by measuring nothing" floor (defect class (i) applied
// to this file). mobile/pairing.go declares thirteen state strings today; a run that found
// materially fewer has stopped parsing the facade and would report perfect parity between two
// empty sets.
const pairingStateFloor = 10

// screenLocalPairingSteps are the PairingStep constants the CORE has no state string for,
// listed here rather than inferred so that adding one is a decision somebody wrote down.
//
// There is exactly one. SCAN is "no attempt in progress": mobile/pairing.go clears the
// persisted record on a completed pairing and never had a string for the absence of an
// attempt, so PairingFlow.restore(null) is what produces it. Every OTHER step must be a step
// the core can actually put the screen into -- a step nothing produces is a dead branch in a
// when(), which is how a screen acquires a message a later reader trusts and no user can ever
// see. ALREADY_PAIRED was exactly that.
var screenLocalPairingSteps = map[string]bool{"SCAN": true}

// ---------------------------------------------------------------------------
// The core's alphabet, read from mobile/pairing.go's SOURCE.
// ---------------------------------------------------------------------------

// corePairingStates is every pairing state string the Go core can report through
// Pairing.State() / App.PairingState().
//
// Derived from source rather than from a checked-in list on purpose: a list would have to be
// edited by the same person adding the state, which is exactly the step this control exists to
// stop depending on.
func corePairingStates(t *testing.T) []string {
	t.Helper()
	path := filepath.Join(repoRoot(t), "mobile", "pairing.go")
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, nil, 0)
	if err != nil {
		t.Fatalf("PB-PAIR-5: parse %s: %v", mustRel(t, path), err)
	}
	var out []string
	for _, d := range f.Decls {
		gen, ok := d.(*ast.GenDecl)
		if !ok || gen.Tok != token.CONST {
			continue
		}
		for _, spec := range gen.Specs {
			vs, ok := spec.(*ast.ValueSpec)
			if !ok || len(vs.Names) != 1 || len(vs.Values) != 1 {
				continue
			}
			if !isPairStateConst(vs.Names[0].Name) {
				continue
			}
			lit, ok := vs.Values[0].(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				continue
			}
			value, err := strconv.Unquote(lit.Value)
			if err != nil {
				continue
			}
			out = append(out, value)
		}
	}
	sort.Strings(out)
	return out
}

// isPairStateConst is the naming rule mobile/pairing.go already follows: a state constant is
// `pair` followed by an UPPER-CASE letter (pairConfirmDestination, pairDifferentMachine).
// `pairingStateFile` and `pairingTTL` continue "pair" with a lower-case letter and are a
// filename and a duration -- neither is a state, and counting either would demand a screen arm
// for the string "pairing-attempt".
func isPairStateConst(name string) bool {
	rest, ok := strings.CutPrefix(name, "pair")
	return ok && rest != "" && rest[0] >= 'A' && rest[0] <= 'Z'
}

// ---------------------------------------------------------------------------
// The screen's alphabet, read from the app's Kotlin SOURCE.
// ---------------------------------------------------------------------------

const (
	pairingUIFile      = "dev/swarm/phone/ui/PairingUi.kt"
	pairingSurfaceFile = "dev/swarm/phone/PairingSurface.kt"
)

var pairingStepEnum = regexp.MustCompile(`(?s)enum\s+class\s+PairingStep\s*(?:\([^)]*\))?\s*\{(.*?)\n\}`)

// declaredPairingSteps is every constant of the screen's PairingStep enum.
func declaredPairingSteps(t *testing.T) []string {
	t.Helper()
	path := filepath.Join(kotlinMainRoot(t), filepath.FromSlash(pairingUIFile))
	src := stripKotlinComments(readFileOrFail(t, path, "PB-PAIR-5"))
	body := pairingStepEnum.FindStringSubmatch(src)
	if body == nil {
		t.Fatalf("PB-PAIR-5: no `enum class PairingStep` in %s.\n"+
			"It is the screen model for the requirement's terminal states; without it the app "+
			"can only branch on prose, which is the opaque error the requirement removes",
			mustRel(t, path))
	}
	var out []string
	for _, m := range enumConstant.FindAllStringSubmatch(body[1], -1) {
		out = append(out, m[1])
	}
	sort.Strings(out)
	return out
}

var (
	pairingCaseLabel  = regexp.MustCompile(`"([a-z_]+)"\s*->`)
	pairingStepInline = regexp.MustCompile(`PairingStep\.([A-Z][A-Z0-9_]*)`)
)

// routedPairingStates is the (state string -> steps) mapping PairingSurface.stepOf performs.
//
// Read off the FUNCTION BODY rather than the file, so a state string mentioned anywhere else
// in the screen -- in a routed message, in a comment that survived stripping -- cannot stand in
// for a routing arm. The `"pairing"` arm yields two steps, which is why the value is a set.
func routedPairingStates(t *testing.T) map[string][]string {
	t.Helper()
	path := filepath.Join(kotlinMainRoot(t), filepath.FromSlash(pairingSurfaceFile))
	src := stripKotlinComments(readFileOrFail(t, path, "PB-PAIR-5"))
	body, ok := kotlinFunBody(src, "stepOf")
	if !ok {
		t.Fatalf("PB-PAIR-5: no `fun stepOf(` in %s.\n"+
			"It is the ONE seam that turns a core pairing state into a step the screen renders. "+
			"Without it nothing maps the requirement's terminal states onto the app at all",
			mustRel(t, path))
	}
	out := map[string][]string{}
	for _, line := range strings.Split(body, "\n") {
		label := pairingCaseLabel.FindStringSubmatch(line)
		if label == nil {
			continue
		}
		var steps []string
		for _, m := range pairingStepInline.FindAllStringSubmatch(line, -1) {
			steps = append(steps, m[1])
		}
		out[label[1]] = steps
	}
	return out
}

// kotlinFunBody returns the brace-balanced body of the named Kotlin function, expression
// bodies included (`fun f(...) = when (x) { ... }`). It starts the scan at the function's
// first `{` AFTER its parameter list, so a default argument containing a brace cannot end it.
func kotlinFunBody(src, name string) (string, bool) {
	decl := regexp.MustCompile(`fun\s+` + regexp.QuoteMeta(name) + `\s*\(`).FindStringIndex(src)
	if decl == nil {
		return "", false
	}
	// Walk out of the parameter list first.
	depth, i := 1, decl[1]
	for ; i < len(src) && depth > 0; i++ {
		switch src[i] {
		case '(':
			depth++
		case ')':
			depth--
		}
	}
	open := strings.IndexByte(src[i:], '{')
	if open < 0 {
		return "", false
	}
	i += open
	depth = 0
	for j := i; j < len(src); j++ {
		switch src[j] {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return src[i : j+1], true
			}
		}
	}
	return "", false
}

// ---------------------------------------------------------------------------
// The two directions.
// ---------------------------------------------------------------------------

// TestPairingStates_EveryCoreStateReachesAStepTheScreenDeclares is the direction PB-PAIR-5's
// amendment broke.
//
// AT THE COMMIT THAT INTRODUCED THIS FILE it failed on three states -- different_machine (the
// state the amendment SUBSTITUTED for the retired already-paired), rate_limited and failed.
// stepOf returned null for each, so the screen showed the generic PAIRING_FAILED message: one
// wording for three conditions whose next steps differ completely -- pair with the machine you
// are pinned to, wait and retry, and try again respectively.
func TestPairingStates_EveryCoreStateReachesAStepTheScreenDeclares(t *testing.T) {
	states := corePairingStates(t)
	if len(states) < pairingStateFloor {
		t.Fatalf("the scan found %d pairing states in mobile/pairing.go, want at least %d.\n"+
			"The core has not lost half its state machine; this file has stopped reading it, and "+
			"a parity check over nothing passes vacuously.\nfound: %s",
			len(states), pairingStateFloor, strings.Join(states, ", "))
	}
	routed := routedPairingStates(t)
	declared := map[string]bool{}
	for _, s := range declaredPairingSteps(t) {
		declared[s] = true
	}

	for _, state := range states {
		steps, ok := routed[state]
		if !ok {
			t.Errorf("PB-PAIR-5: the core reports pairing state %q and PairingSurface.stepOf has "+
				"no arm for it, so it answers null and the screen shows the GENERIC "+
				"pairing-failed message.\n"+
				"That is the opaque error the requirement exists to remove: its criterion is "+
				"\"each is user-legible, not an opaque error\", and a state that shares one "+
				"wording with every other failure is not legible. Add a PairingStep for it, route "+
				"it here, and give it a message in PairingFlow.messageFor.", state)
			continue
		}
		if len(steps) == 0 {
			t.Errorf("PB-PAIR-5: stepOf's arm for %q names no PairingStep", state)
		}
		for _, step := range steps {
			if !declared[step] {
				t.Errorf("PB-PAIR-5: stepOf routes %q to PairingStep.%s, which %s does not declare",
					state, step, pairingUIFile)
			}
		}
	}
}

// TestPairingStates_EveryDeclaredStepIsOneTheCoreCanProduce is the direction that catches a
// RETIRED state outliving its retirement.
//
// AT THE COMMIT THAT INTRODUCED THIS FILE it failed on ALREADY_PAIRED. The 2026-07-25 amendment
// retired it -- the machine fail-fasts a second pairing BEFORE minting any rendezvous id,
// secret or QR, so the phone has nothing to scan and can never enter the state -- and the enum
// kept declaring it, with a full paragraph of user-facing prose telling a user to go and revoke
// a device, for a condition no handset can reach.
//
// A step nothing produces is worse than a missing one: it reads as coverage. A reader counting
// terminal states finds the number the requirement asks for.
func TestPairingStates_EveryDeclaredStepIsOneTheCoreCanProduce(t *testing.T) {
	produced := map[string]bool{}
	for _, steps := range routedPairingStates(t) {
		for _, s := range steps {
			produced[s] = true
		}
	}
	for _, step := range declaredPairingSteps(t) {
		if produced[step] || screenLocalPairingSteps[step] {
			continue
		}
		t.Errorf("PB-PAIR-5: %s declares PairingStep.%s and PairingSurface.stepOf never returns "+
			"it, so no core state can put the screen there.\n"+
			"Either it is a state the core lost -- in which case the screen model and its "+
			"user-facing message must follow -- or it is genuinely screen-local, in which case "+
			"add it to screenLocalPairingSteps in this file with the reason. A dead branch in a "+
			"when() is a message a reader counts as shipped and no user can ever see.",
			pairingUIFile, step)
	}
}

// TestPairingStates_TheParserSeesBothSidesOfTheSeam is defect class (i) turned on this file.
//
// Both assertions above are of the form "nothing was found wrong". A Kotlin reader that
// returned an empty routing map produces exactly that answer for the second test while making
// the first fail on EVERY state -- whose tempting repair is to go and edit the screen. This
// fails as itself first.
func TestPairingStates_TheParserSeesBothSidesOfTheSeam(t *testing.T) {
	if routed := routedPairingStates(t); len(routed) < pairingStateFloor {
		t.Fatalf("stepOf's routing table read as %d arms, want at least %d.\n"+
			"The screen has not lost its state routing; this file has stopped reading "+
			"%s.\nfound: %v", len(routed), pairingStateFloor, pairingSurfaceFile, routed)
	}
	if steps := declaredPairingSteps(t); len(steps) < pairingStateFloor {
		t.Fatalf("PairingStep read as %d constants, want at least %d.\nfound: %s",
			len(steps), pairingStateFloor, strings.Join(steps, ", "))
	}
}

// TestPairingStates_TheComparisonCanFail drives both decisions against SYNTHETIC input.
//
// Against the production tree (by the time this lands) a check that understands nothing and a
// codebase that does nothing wrong are indistinguishable -- which is exactly how PB-PAIR-5
// stayed green through its own amendment. These are the mutations.
func TestPairingStates_TheComparisonCanFail(t *testing.T) {
	const surface = `
    private fun stepOf(state: String, sasKnown: Boolean): PairingStep? = when (state) {
        "confirm_destination" -> PairingStep.CONFIRM_DESTINATION
        "pairing" -> if (sasKnown) PairingStep.COMPARING_CODES else PairingStep.HANDSHAKING
        "paired" -> PairingStep.PAIRED
        else -> null
    }

    private fun routedStateMessage(state: String): String =
        ErrorRouter.route(if (state == "different_machine") X else Y).message
`
	routed := map[string][]string{}
	body, ok := kotlinFunBody(surface, "stepOf")
	if !ok {
		t.Fatalf("the body reader found no stepOf in the synthetic surface")
	}
	for _, line := range strings.Split(body, "\n") {
		if label := pairingCaseLabel.FindStringSubmatch(line); label != nil {
			var steps []string
			for _, m := range pairingStepInline.FindAllStringSubmatch(line, -1) {
				steps = append(steps, m[1])
			}
			routed[label[1]] = steps
		}
	}

	t.Run("a state mentioned outside stepOf is not routed", func(t *testing.T) {
		if _, ok := routed["different_machine"]; ok {
			t.Fatalf("the reader counted a state named in routedStateMessage as ROUTED.\n"+
				"That is the exact false green PB-PAIR-5 shipped with: the screen mentioned the "+
				"state only to hand it to the generic error router.\nrouted: %v", routed)
		}
	})

	t.Run("an arm with two steps yields both", func(t *testing.T) {
		if got := routed["pairing"]; len(got) != 2 {
			t.Fatalf("the reader saw %v for the two-step `pairing` arm, want both steps.\n"+
				"A reader that sees one would report the other as a step nothing produces", got)
		}
	})

	t.Run("a retired step with no producing arm is reported", func(t *testing.T) {
		produced := map[string]bool{}
		for _, steps := range routed {
			for _, s := range steps {
				produced[s] = true
			}
		}
		var dead []string
		for _, step := range []string{"CONFIRM_DESTINATION", "PAIRED", "SCAN", "ALREADY_PAIRED"} {
			if !produced[step] && !screenLocalPairingSteps[step] {
				dead = append(dead, step)
			}
		}
		if fmt.Sprint(dead) != "[ALREADY_PAIRED]" {
			t.Fatalf("the check reported %v, want exactly [ALREADY_PAIRED].\n"+
				"SCAN is screen-local and must NOT be reported; a retired terminal state must be", dead)
		}
	})
}
