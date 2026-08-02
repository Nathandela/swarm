package gate

// FAILING-FIRST (TDD RED, GG-5) for agents-tracker-64rf: "Pairing has no findable entry point:
// it is buried below the inbox list."
//
// WHAT HAPPENED. On the first handset install the owner could not find where to start pairing.
// The pairing block -- `PairingSurface.root`, whose one button reads "Scan the code on your
// machine" -- is a child of `PhoneSurface.unrecomposedControls`, and that column is hosted BELOW
// the session list on the Inbox destination. A fresh install is unpaired, so the inbox above it is
// empty, and the one action the phone can usefully take is a control at the bottom of a scroll
// under a list of nothing, on one of four tabs, with no affordance anywhere saying that pairing is
// what an unpaired phone does first.
//
// THE DESIGN THIS FENCES (owner's, recorded on agents-tracker-64rf, 2026-08-02): an UNPAIRED phone
// shows ONE screen -- `--p-bg` ground, one hero CTA offering to pair, nothing else. No tab bar, no
// inbox, no empty triage sections. Tapping the CTA runs the existing `PairingSurface` flow
// full-screen in the same host. Once PAIRED the normal four-tab scaffold appears and the pairing
// entry point lives on the SETTINGS destination.
//
// WHY IT IS A GO GATE AND NOT ONLY A KOTLIN TEST. The Kotlin suite can assert that an unpaired
// state renders a pairing host and no tabs -- and it does, and that is the right test for the
// DECISION. What it cannot durably assert is the containment fact underneath it: that the pairing
// panel is not a child of something the tab scaffold hosts. That is a statement about the view
// GRAPH the source declares, and the cheapest way to reintroduce this defect is to add the panel
// back to a column somewhere below a list, which type-checks, renders, and passes every state test
// that only looks at what an unpaired phone shows.
//
// WHAT THIS FILE DELIBERATELY DOES NOT FENCE is recorded at the bottom, next to the reasons. A
// gate that pretends to check something it cannot see is worse than no gate.
//
// NOTHING HERE WALKS THE REPOSITORY ROOT. Every scan starts at the app module
// (android/app/src/main/kotlin), so it cannot descend into `.claude/worktrees/`, which holds other
// agents' full checkouts and has already made four gates in this repository report findings about
// somebody else's private copy as findings about this tree.

import (
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// The subject: the surface that decides what the window holds.
// ---------------------------------------------------------------------------

const pairingEntrySurfaceFile = "dev/swarm/phone/PhoneSurface.kt"

// pairingEntryInboxSources are the three files the triage inbox is made of. They are named
// individually rather than found by prefix so that a missing one is a LOUD failure: a fence whose
// subject silently disappeared is a fence that reports clean forever.
var pairingEntryInboxSources = []string{
	"dev/swarm/phone/ui/screens/TriageInboxView.kt",
	"dev/swarm/phone/ui/screens/TriageInboxScreen.kt",
	"dev/swarm/phone/ui/TriageInbox.kt",
}

// pairingEntryCode reads one production Kotlin source as REFERENCES and nothing else: comments out
// (kotlinCodeOnly's recorded reason -- a fence a comment can satisfy is one the next thorough
// comment turns off), and string literals out too.
//
// THE STRINGS GO FOR THE SAME REASON THE COMMENTS DO, in the other direction. Every screen in this
// app carries its own copy, and copy talks about the product: `MachinesPanel` already writes "been
// repaired", `LinkPanel` writes "repair channel", and a settings row that offers to pair will say
// so in a sentence. This fence is about which VIEW is a child of which container, so a word in a
// user-visible string is not evidence and must not be able to fail it -- nor, by sitting there, to
// make a green run look earned.
//
// The stated limit: a string TEMPLATE (`"${pairing.name}"`) is a real reference and is stripped
// with its string. No composition in this surface is written that way, and a fence that parsed
// templates would be parsing Kotlin.
func pairingEntryCode(t *testing.T, rel string) string {
	t.Helper()
	path := filepath.Join(kotlinMainRoot(t), filepath.FromSlash(rel))
	return kotlinWithoutStringLiterals(kotlinCodeOnly(readFileOrFail(t, path, "agents-tracker-64rf")))
}

// kotlinWithoutStringLiterals replaces every string literal with an empty one, leaving the
// expression's shape (and its line numbering) intact.
func kotlinWithoutStringLiterals(code string) string {
	var out strings.Builder
	out.Grow(len(code))
	for i := 0; i < len(code); {
		switch {
		case strings.HasPrefix(code[i:], `"""`):
			end := strings.Index(code[i+3:], `"""`)
			if end < 0 {
				// Unterminated: the rest of the file is inside it.
				return out.String()
			}
			out.WriteString(`""`)
			// Newlines survive so reported positions and the shape of the file do.
			out.WriteString(strings.Repeat("\n", strings.Count(code[i:i+3+end], "\n")))
			i += 3 + end + 3
		case code[i] == '"':
			j := i + 1
			for j < len(code) {
				if code[j] == '\\' {
					j += 2
					continue
				}
				if code[j] == '"' {
					j++
					break
				}
				if code[j] == '\n' {
					break
				}
				j++
			}
			out.WriteString(`""`)
			i = j
		default:
			out.WriteByte(code[i])
			i++
		}
	}
	return out.String()
}

// ---------------------------------------------------------------------------
// Reading the source: declarations, calls, and one hop behind a name.
// ---------------------------------------------------------------------------

// pairingReference is the pairing panel NAMED. Every pairing symbol this app has spells it as a
// whole word at the start of a token -- `pairing.root`, `PairingSurface`, `pairingPanelView`,
// `PairingPanel` -- and the word boundary is what keeps "repairing" and "repaired", which the link
// and machines screens both use, from firing it.
//
// ITS LIMIT IS STATED RATHER THAN HIDDEN: a host named without the word (`onPair`, `linkHost`) is
// invisible to it. That is why the checks below read the container GRAPH as well as the text -- a
// view whose declaration holds `pairing.root` is caught whatever the identifier holding it is
// called.
var pairingReference = regexp.MustCompile(`(?i)\bpairing`)

var kotlinIdentifier = regexp.MustCompile(`[A-Za-z_][A-Za-z0-9_]*`)

var kotlinFunctionDeclaration = regexp.MustCompile(`\bfun\s+([A-Za-z_][A-Za-z0-9_]*)\s*\(`)

// kotlinInitialiser returns the text a file-local `val`/`var` is INITIALISED to -- everything to
// the right of the `=`, including the `.apply { ... }` block that builds the view.
//
// IT IS THE ONE HOP THIS FILE FOLLOWS, and it is the hop the defect is hiding behind: the inbox is
// not handed the pairing panel, it is handed `unrecomposedControls`, and the panel is a child of
// that. A check that only read the call site would report the composition clean.
//
// ONE HOP AND NO MORE. A declaration in another file, or a view assembled at runtime through a
// path this file cannot see, is not followed: that needs a type checker, and a heuristic that
// guessed would fail in both directions. s24_screens_test.go's constant table draws the same line
// for the same reason.
//
// @return the initialiser text, and false when the name has no such declaration (a parameter, a
//
//	function, an import, or a name from another file).
func kotlinInitialiser(code, name string) (string, bool) {
	decl := regexp.MustCompile(`(?m)^[ \t]*(?:(?:private|internal|public|protected|lateinit|override)[ \t]+)*va[lr][ \t]+` +
		regexp.QuoteMeta(name) + `\b[^\n=]*=`)
	loc := decl.FindStringIndex(code)
	if loc == nil {
		return "", false
	}
	start := loc[1]
	depth := 0
	for i := start; i < len(code); i++ {
		switch code[i] {
		case '(', '{', '[':
			depth++
		case ')', '}', ']':
			depth--
		case '\n':
			if depth > 0 {
				continue
			}
			// At depth zero a newline ends the declaration UNLESS the next line continues the
			// expression, which in this module means a chained call: `LinearLayout(context)` on one
			// line and `.apply { ... }` on the next.
			if kotlinLineContinues(code[i+1:]) {
				continue
			}
			return code[start:i], true
		}
	}
	return code[start:], true
}

// kotlinLineContinues reports whether the next line carries on the previous expression.
func kotlinLineContinues(rest string) bool {
	trimmed := strings.TrimLeft(rest, " \t")
	return strings.HasPrefix(trimmed, ".") || strings.HasPrefix(trimmed, "?:")
}

// kotlinCallSites returns the index of the opening parenthesis of every CALL of name, and never of
// its declaration. The difference is the whole point of the scans below: `fun hostContent(view:
// View?)` is not a place a view is installed.
func kotlinCallSites(code, name string) []int {
	re := regexp.MustCompile(`(\bfun\s+)?\b` + regexp.QuoteMeta(name) + `\s*\(`)
	var out []int
	for _, m := range re.FindAllStringSubmatchIndex(code, -1) {
		if m[2] >= 0 {
			continue
		}
		out = append(out, m[1]-1)
	}
	return out
}

// kotlinEnclosingFunction names the function an offset falls inside, read as the nearest PRECEDING
// `fun name(`.
//
// It is an approximation and the approximation is safe here: it is used to find which function
// installs a view into a named container, and the failure mode -- naming an outer function when
// the call is inside a nested one -- widens the set of call sites the fence then reads. A wider
// set produces more text to search, never less.
func kotlinEnclosingFunction(code string, at int) (string, bool) {
	var name string
	for _, m := range kotlinFunctionDeclaration.FindAllStringSubmatchIndex(code[:at], -1) {
		name = code[m[2]:m[3]]
	}
	return name, name != ""
}

// pairingEntryCallText is the whole argument list of the call whose `(` is at open.
func pairingEntryCallText(code string, open int) (string, bool) {
	args := s23CallArguments(code, open)
	if args == nil {
		return "", false
	}
	return strings.Join(args, "\n"), true
}

// pairingEntryFaults reports every way one argument list reaches the pairing panel: named in the
// text itself, or one hop behind an identifier declared in the same file.
//
// @param code the whole source, for resolving the hop.
// @param where the composition being described, for the message.
// @param text the argument list.
func pairingEntryFaults(code, where, text string) []string {
	var faults []string
	if pairingReference.MatchString(text) {
		faults = append(faults, where+" names the pairing panel directly (`"+
			pairingEntryEvidence(text)+"`)")
	}
	seen := map[string]bool{}
	for _, id := range kotlinIdentifier.FindAllString(text, -1) {
		if seen[id] {
			continue
		}
		seen[id] = true
		body, ok := kotlinInitialiser(code, id)
		if !ok || !pairingReference.MatchString(body) {
			continue
		}
		faults = append(faults, where+" is handed `"+id+"`, and `"+id+
			"` is declared holding the pairing panel (`"+pairingEntryEvidence(body)+"`)")
	}
	sort.Strings(faults)
	return faults
}

// pairingEntryEvidence is the first line naming the panel, so a failure quotes the source rather
// than asserting about it.
func pairingEntryEvidence(text string) string {
	for _, line := range strings.Split(text, "\n") {
		if pairingReference.MatchString(line) {
			return strings.TrimSpace(line)
		}
	}
	return ""
}

// ---------------------------------------------------------------------------
// The scaffold's content is never the pairing panel.
// ---------------------------------------------------------------------------

// pairingEntryScaffoldContent is `content = <name>` in the surface's own call to
// `phoneScaffoldView`. The container is DERIVED rather than named here, so this gate holds no
// second copy of a private field's name: whatever the surface hands the scaffold is what gets
// fenced, and a rename fails loudly at the read below instead of quietly fencing nothing.
var pairingEntryScaffoldContent = regexp.MustCompile(`\bcontent\s*=\s*([A-Za-z_][A-Za-z0-9_]*)`)

func pairingEntryContentHost(t *testing.T, code string) string {
	t.Helper()
	sites := kotlinCallSites(code, "phoneScaffoldView")
	if len(sites) == 0 {
		t.Fatalf("agents-tracker-64rf: %s never calls phoneScaffoldView, so this gate cannot find "+
			"the tab scaffold and has nothing to fence. If the scaffold moved, re-point this check "+
			"at its new host rather than deleting it: the defect it guards is the pairing panel "+
			"being a child of whatever the tabs are wrapped around.", pairingEntrySurfaceFile)
	}
	for _, open := range sites {
		text, ok := pairingEntryCallText(code, open)
		if !ok {
			continue
		}
		if m := pairingEntryScaffoldContent.FindStringSubmatch(text); m != nil {
			return m[1]
		}
	}
	t.Fatalf("agents-tracker-64rf: %s calls phoneScaffoldView and this gate cannot read which view "+
		"it passes as `content = ...`, so it cannot tell what the tab scaffold holds. A positional "+
		"argument would do this; name it, or re-point this check.", pairingEntrySurfaceFile)
	return ""
}

// pairingEntryInstallSites returns every place a view is put INTO the scaffold's content host: the
// direct `host.addView(` calls, and the calls to whatever function wraps them.
//
// THE WRAPPER IS DERIVED AND NOT NAMED. `PhoneSurface.hostContent` is the one that exists today,
// and naming it here would make this gate silently vacuous the day it is renamed. What is asked
// instead is the structural question -- which function's body reaches `contentHost.addView` -- so
// the fence follows the code.
func pairingEntryInstallSites(t *testing.T, code, host string) []int {
	t.Helper()
	direct := kotlinCallSites(code, host+".addView")
	if len(direct) == 0 {
		t.Fatalf("agents-tracker-64rf: nothing in %s adds a view to `%s`, the container the tab "+
			"scaffold is given as its content. Either the surface installs its destinations some "+
			"other way now, in which case this gate must be re-pointed at it, or the scaffold shows "+
			"nothing at all.", pairingEntrySurfaceFile, host)
	}
	sites := append([]int{}, direct...)
	wrappers := map[string]bool{}
	for _, at := range direct {
		if name, ok := kotlinEnclosingFunction(code, at); ok {
			wrappers[name] = true
		}
	}
	for name := range wrappers {
		sites = append(sites, kotlinCallSites(code, name)...)
	}
	sort.Ints(sites)
	return sites
}

// TestPairingEntry_TheTabScaffoldsContentIsNeverThePairingPanel is the fence on the defect the
// field report found.
//
// It reads every view the surface installs as the tab scaffold's content -- on any destination,
// not only the inbox -- and follows one hop behind each name, which is where the panel is actually
// hiding: `hostContent(unrecomposedControls)`, and `unrecomposedControls` is a column with
// `pairing.root` in it.
//
// WHY THE FENCE IS "NOT INSIDE THE SCAFFOLD" AND NOT "NOT INSIDE THE INBOX". Moving the block from
// the inbox to a different tab would satisfy the narrower rule and leave the bug exactly where it
// was: an unpaired phone would still be shown four tabs and be expected to guess which one leads
// to the only thing it can do. The design's claim is that pairing is a SCREEN -- the whole window,
// no bar -- so the containment fact to hold forever is that the panel is not a child of the thing
// the tabs are wrapped around.
func TestPairingEntry_TheTabScaffoldsContentIsNeverThePairingPanel(t *testing.T) {
	code := pairingEntryCode(t, pairingEntrySurfaceFile)
	host := pairingEntryContentHost(t, code)

	var faults []string
	for _, open := range pairingEntryInstallSites(t, code, host) {
		text, ok := pairingEntryCallText(code, open)
		if !ok {
			faults = append(faults, "unbalanced parentheses at a call installing content into `"+
				host+"`, so the gate cannot read its argument and would report it clean")
			continue
		}
		faults = append(faults, pairingEntryFaults(code, "the tab scaffold's content", text)...)
	}
	sort.Strings(faults)
	faults = pairingEntryUnique(faults)
	if len(faults) > 0 {
		t.Errorf("agents-tracker-64rf: the pairing panel is inside the tab scaffold:\n  %s\n\n"+
			"An unpaired phone has exactly ONE useful action, and the owner could not find it on a "+
			"real handset: the block sits below the session list, on one of four tabs, under an "+
			"inbox that is empty precisely because nothing is paired yet. The recorded design is "+
			"that an unpaired phone is shown ONE screen -- `--p-bg` ground, one hero CTA, no tab "+
			"bar, no inbox -- and that the panel therefore is not a child of anything the scaffold "+
			"hosts.", strings.Join(faults, "\n  "))
	}
}

func pairingEntryUnique(in []string) []string {
	var out []string
	seen := map[string]bool{}
	for _, s := range in {
		if seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	return out
}

// ---------------------------------------------------------------------------
// The inbox composition never receives the pairing panel.
// ---------------------------------------------------------------------------

// TestPairingEntry_TheInboxIsNeverHandedThePairingPanel fences the seam the panel actually
// travelled through, which is not a container but a PARAMETER.
//
// `triageInboxView(..., below = <view>)` takes an opaque `View` and puts it under the sections.
// That parameter is why the inbox's own source can be searched for "pairing" forever and stay
// clean while the panel renders inside it on every draw -- the inbox never names the thing it is
// hosting. So the fence is on the CALL SITES, one hop behind each name, plus the inbox's own three
// sources for the direct form.
//
// Both directions are needed and neither is redundant: the call-site half is what is red today,
// and the source half is what stops the panel being composed into the screen itself later, where
// no argument would reveal it.
func TestPairingEntry_TheInboxIsNeverHandedThePairingPanel(t *testing.T) {
	var faults []string
	callSites := 0
	for _, path := range kotlinFiles(t, kotlinMainRoot(t)) {
		code := kotlinWithoutStringLiterals(kotlinCodeOnly(readFileOrFail(t, path, "agents-tracker-64rf")))
		for _, open := range kotlinCallSites(code, "triageInboxView") {
			callSites++
			text, ok := pairingEntryCallText(code, open)
			if !ok {
				faults = append(faults, mustRel(t, path)+": unbalanced parentheses after "+
					"`triageInboxView(`, so the gate cannot read its arguments")
				continue
			}
			faults = append(faults, pairingEntryFaults(code, mustRel(t, path)+
				": the triage inbox's composition", text)...)
		}
	}
	if callSites == 0 {
		t.Fatalf("agents-tracker-64rf: no production Kotlin composes `triageInboxView`, so this " +
			"fence has no subject and would pass over anything. The inbox is PB-DS-9's first screen; " +
			"if it is composed under another name, re-point this check at it.")
	}

	for _, rel := range pairingEntryInboxSources {
		code := pairingEntryCode(t, rel)
		if pairingReference.MatchString(code) {
			faults = append(faults, rel+" names the pairing panel itself (`"+
				pairingEntryEvidence(code)+"`)")
		}
	}

	sort.Strings(faults)
	if len(faults) > 0 {
		t.Errorf("agents-tracker-64rf: the triage inbox hosts the pairing panel:\n  %s\n\n"+
			"The inbox is where the panel was found buried, and the parameter it arrived through "+
			"(`below`) takes an opaque View -- so the inbox renders it without ever naming it, and "+
			"a search of the screen's own source reports nothing. The pairing entry point belongs "+
			"to the unpaired phone's own screen, and afterwards to Settings; the Inbox is the one "+
			"destination it must never be under.", strings.Join(faults, "\n  "))
	}
}

// ---------------------------------------------------------------------------
// Negative controls. Each one feeds a perturbed source to the SAME function the assertions call.
// ---------------------------------------------------------------------------

// TestPairingEntry_TheDeclarationReaderReadsAViewAndStopsAtIt is the control on the one hop, which
// is the part of this file that can fail silently.
//
// A reader that stopped at the first newline would return `LinearLayout(activity).apply {` and see
// no children at all -- and every check above would report a clean composition over a column with
// the pairing panel in it. A reader that never stopped would swallow the rest of the file and
// report every container as holding everything. Both directions are exercised.
func TestPairingEntry_TheDeclarationReaderReadsAViewAndStopsAtIt(t *testing.T) {
	const src = `class Surface {
    private val pairing = PairingSurface(activity, runtime)

    private val column = LinearLayout(activity).apply {
        orientation = LinearLayout.VERTICAL
        for (child in listOf(
            status, notice, pairing.root, peekHost,
        )) {
            addView(child)
        }
    }

    private val settingsHost = FrameLayout(activity).apply {
        layoutParams = ViewGroup.LayoutParams(MATCH, WRAP)
    }
}`

	body, ok := kotlinInitialiser(src, "column")
	if !ok {
		t.Fatal("the declaration reader cannot find a well-formed `private val column = ...`")
	}
	if !strings.Contains(body, "pairing.root") {
		t.Errorf("the declaration reader stops before the children, so a column holding the "+
			"pairing panel reads as empty and every check in this file would pass over it:\n%s", body)
	}
	if strings.Contains(body, "settingsHost") {
		t.Errorf("the declaration reader runs past the end of the declaration, so every container "+
			"in a file reads as holding everything the file mentions:\n%s", body)
	}

	// A chained continuation must not end it either: `LinearLayout(context)` on one line and
	// `.apply { ... }` on the next is the same declaration.
	const chained = `    private val column = LinearLayout(activity)
        .apply { addView(pairing.root) }
    private val other = View(activity)`
	body, ok = kotlinInitialiser(chained, "column")
	if !ok || !strings.Contains(body, "pairing.root") {
		t.Errorf("a declaration whose builder is on the next line reads as empty: %q", body)
	}
	if strings.Contains(body, "other") {
		t.Errorf("the reader ran past the chained declaration: %q", body)
	}

	if _, ok := kotlinInitialiser(src, "activity"); ok {
		t.Error("a constructor parameter reads as a declaration with an initialiser, so identifiers " +
			"that stand for nothing in this file would be resolved to arbitrary text")
	}

	// A call is not a declaration, and the distinction is what keeps `fun hostContent(...)` from
	// being read as a place a view is installed.
	const calls = `    private fun hostContent(view: View?) { contentHost.addView(view) }
    private fun draw() { hostContent(column) }`
	if got := kotlinCallSites(calls, "hostContent"); len(got) != 1 {
		t.Errorf("the call-site reader found %d calls of hostContent where there is exactly one; "+
			"counting the declaration would make the fence read a parameter list as a composition",
			len(got))
	}
}

// TestPairingEntry_ThePairingScanDiscriminates drives the assertions' own fault function against
// perturbed sources, in both directions.
//
// The rows that MUST fault are the three shapes the panel can arrive in: named at the call site,
// one hop behind a container, and behind a host whose name merely starts with the word. The rows
// that must NOT fault are the ones a working app is full of -- another destination's panel, and
// copy that talks about repairing a channel or about pairing itself, which is text a user reads
// and not a view anybody composed.
func TestPairingEntry_ThePairingScanDiscriminates(t *testing.T) {
	const code = `class Surface {
    private val pairing = PairingSurface(activity, runtime)
    private val settings = SettingsSurface(activity, runtime)
    private val column = LinearLayout(activity).apply {
        addView(pairing.root)
    }
    private val clean = LinearLayout(activity).apply {
        addView(status)
    }
}`

	faulty := []struct {
		what string
		text string
	}{
		{"the panel named at the call site", "pairing.root"},
		{"the panel one hop behind a container", "column"},
		{"a pairing host whose name carries the word", "pairingHostView(activity)"},
		{"the surface constructed inline", "PairingSurface(activity, runtime).root"},
	}
	for _, c := range faulty {
		if got := pairingEntryFaults(code, "a composition", c.text); len(got) == 0 {
			t.Errorf("the scan is blind to %s: `%s` produced no fault, so every clean run of the "+
				"assertions above is about nothing", c.what, c.text)
		}
	}

	allowed := []struct {
		what string
		text string
	}{
		{"another destination's panel", "settings.root"},
		{"a container with no pairing in it", "clean"},
		{"a repaired channel", "linkPanelView(activity, panel, below = unavailable)"},
		{"a screen composed from the kit", "activityPanelView(activity, panel)"},
	}
	for _, c := range allowed {
		if got := pairingEntryFaults(code, "a composition", c.text); len(got) > 0 {
			t.Errorf("the scan rejects %s, which is not the defect: `%s`\n%s",
				c.what, c.text, strings.Join(got, "\n"))
		}
	}

	// Copy is not composition. A screen that SAYS the word in a sentence a user reads must not
	// fail this fence -- and a screen that HOLDS the panel must still fail it after the strings
	// are gone, which is the direction a too-eager stripper would break.
	const copyOnly = `val notice = label("Pairing lives in Settings once this phone is paired.")`
	if got := pairingEntryFaults(code, "a composition",
		kotlinWithoutStringLiterals(copyOnly)); len(got) > 0 {
		t.Errorf("a sentence about pairing reads as a composition holding the panel:\n%s",
			strings.Join(got, "\n"))
	}
	const both = `val row = label("Pair this phone") .also { it.addView(pairing.root) }`
	if got := pairingEntryFaults(code, "a composition",
		kotlinWithoutStringLiterals(both)); len(got) == 0 {
		t.Error("stripping the strings also removed the reference beside them, so a composition " +
			"that holds the panel reads as clean whenever it carries copy")
	}

	// And the stripper itself: literals out, code intact, lines preserved.
	stripped := kotlinWithoutStringLiterals("val a = \"pairing\"\nval b = pairing.root\n")
	if strings.Contains(stripped, `"pairing"`) || !strings.Contains(stripped, "b = pairing.root") {
		t.Errorf("kotlinWithoutStringLiterals does not remove literals and leave code: %q", stripped)
	}
	if strings.Count(stripped, "\n") != 2 {
		t.Errorf("kotlinWithoutStringLiterals changed the line count: %q", stripped)
	}
}

// ---------------------------------------------------------------------------
// What is NOT fenced here, and why. Recorded so the absence reads as a decision rather than an
// oversight, and so the next reader does not take a green run for more than it is.
//
//   - "The pairing-only screen builds no tab bar." There is no sound textual subject for it yet:
//     the screen does not exist, and locating it by a file name this gate invented would fence a
//     naming convention rather than an invariant -- the design permits the CTA to be built in the
//     surface itself. What IS fenced is the containment fact that makes the tab bar impossible on
//     it: the panel is not a child of the scaffold's content (first test above), and the scaffold
//     is the only thing in this app that composes `tabBar`.
//
//   - "Settings is the ONLY destination that names the pairing entry point." The entry point's
//     spelling does not exist yet -- it is a row with copy nobody has written -- so any regexp for
//     it would be this gate inventing the product's words and then checking that the product used
//     them. The half of the claim that has a subject today ("never the Inbox") is the second test.
//
//   - "The pairing-only screen offers exactly ONE control." Counting controls needs a file to
//     count them in, and the flow BEHIND the CTA legitimately has several (scan, manual entry, and
//     PB-SAS-3's two answer buttons, which ADR-007 B133 made the only human-in-the-loop security
//     step in the product). A count that could not tell the entry screen from the flow would
//     either be vacuous or would pressure someone into deleting a security control to make a gate
//     green. It belongs in the Kotlin suite, against the composed view.
//
// ---------------------------------------------------------------------------
