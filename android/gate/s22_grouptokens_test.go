package gate

// FAILING-FIRST (TDD RED, GG-5) for PB-TOK-8.
//
// "The four session Groups are bound to tokens, machine-readably -- including ReadyForReview,
//  which Substrate never coloured. Decision: the four Groups take --p-att / --p-work / --p-ok /
//  --p-ink3. The Group->token mapping is a checked-in table joined bidirectionally to both
//  status.Group and the theme, in the style of design-tokens.tsv. A Group with no token, or a
//  token bound to two Groups, fails. Recorded in the ADR."
//
// THE STATE OF THE WORLD, verified before these assertions were written: internal/status declares
// four Group constants and nothing anywhere associates any of them with a colour. `grep -rn
// GroupReadyForReview` finds the derivation, the protocol and the TUI; it finds no token, no
// resource and no table. The Android app has no status colour at all.
//
// WHY THIS IS THE LARGEST HOLE AND NOT A DETAIL. ReadyForReview is a SERVER-DERIVED first-class
// Group -- internal/status.Derive returns it, the phone renders it verbatim and never re-derives
// it -- and the Substrate skin gives it no token. The retired mock painted it #bf5af2; the
// directions artifact's own rationale retires purple; and the artifact's demo phone quietly
// renders only "Needs you / Working / Done", so the one screen that would have exposed the gap
// omits the section instead. An implementer reaching that row today has to invent a colour, and
// whatever they invent enters the product without passing through the origin -- which is
// PB-TOK-1's defect arriving from a direction PB-TOK-1 cannot see.
//
// WHY A TABLE AND NOT A `when (group)` IN KOTLIN. A Kotlin expression binds the Groups for
// Kotlin. The Group is derived in Go, crosses the wire in Go's spelling, and is rendered on two
// clients; a binding stated in one client's source is a binding the other client can contradict
// silently. The table is the artifact both sides read, and it is joined HERE against the real
// constants -- parsed out of internal/status, not transcribed -- so a fifth Group cannot be added
// without this failing.

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// groupTokenMapFile is PB-TOK-8's checked-in table, beside design-tokens.tsv and for the same
// reason: one artifact a Go gate and a Kotlin renderer both read.
const groupTokenMapFile = "group-tokens.tsv"

// adrRelPath carries the decision these bindings execute.
const adrRelPath = "docs/adr/ADR-007-remote-access.md"

type groupTokenRow struct {
	Const string // the Go constant name, e.g. GroupNeedsInput
	Value string // its wire value, e.g. needs_input
	Token string // the design token it binds, e.g. --p-att
	Line  int
}

func loadGroupTokenMap(t *testing.T) []groupTokenRow {
	t.Helper()
	path := filepath.Join(androidRoot(t), groupTokenMapFile)
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("PB-TOK-8 requires a checked-in Group->token table at %s: %v\n"+
			"Columns (tab separated): group_const<TAB>group_value<TAB>token<TAB>note.\n"+
			"`group_const` is a Group constant in internal/status (e.g. GroupNeedsInput), "+
			"`group_value` is its string value (e.g. needs_input), and `token` is a key of %s's "+
			"\"tokens\" object. Both Group columns are checked against the REAL constants, parsed "+
			"from internal/status rather than transcribed.",
			mustRel(t, path), err, tokensRelPath)
	}
	var rows []groupTokenRow
	for i, line := range strings.Split(string(raw), "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		cols := strings.Split(line, "\t")
		if len(cols) < 3 {
			t.Errorf("%s:%d: needs at least group_const, group_value and token", groupTokenMapFile, i+1)
			continue
		}
		rows = append(rows, groupTokenRow{
			Const: strings.TrimSpace(cols[0]),
			Value: strings.TrimSpace(cols[1]),
			Token: strings.TrimSpace(cols[2]),
			Line:  i + 1,
		})
	}
	if len(rows) == 0 {
		t.Fatalf("%s has no rows; the binding would be vacuous", mustRel(t, path))
	}
	return rows
}

// declaredGroups parses internal/status and returns every Group constant, name -> value.
//
// IT PARSES RATHER THAN TRANSCRIBES, which is the only version of this that satisfies "a new
// Group added to internal/status with no row fails". A list of the four constants written out
// here would be a fifth copy of the status model and would be exactly as green on the day a
// fifth Group landed as it is today.
func declaredGroups(t *testing.T) map[string]string {
	t.Helper()
	dir := filepath.Join(repoRoot(t), "internal", "status")
	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, dir, func(fi os.FileInfo) bool {
		return !strings.HasSuffix(fi.Name(), "_test.go")
	}, 0)
	if err != nil {
		t.Fatalf("PB-TOK-8: parsing internal/status: %v", err)
	}

	out := map[string]string{}
	for _, pkg := range pkgs {
		for _, file := range pkg.Files {
			for _, decl := range file.Decls {
				gd, ok := decl.(*ast.GenDecl)
				if !ok || gd.Tok != token.CONST {
					continue
				}
				for _, spec := range gd.Specs {
					vs, ok := spec.(*ast.ValueSpec)
					if !ok {
						continue
					}
					ident, ok := vs.Type.(*ast.Ident)
					if !ok || ident.Name != "Group" {
						continue
					}
					for i, name := range vs.Names {
						if i >= len(vs.Values) {
							continue
						}
						lit, ok := vs.Values[i].(*ast.BasicLit)
						if !ok || lit.Kind != token.STRING {
							t.Errorf("PB-TOK-8: Group constant %s is not a string literal, so this "+
								"gate cannot read its wire value", name.Name)
							continue
						}
						v, err := strconv.Unquote(lit.Value)
						if err != nil {
							t.Errorf("PB-TOK-8: Group constant %s: %v", name.Name, err)
							continue
						}
						out[name.Name] = v
					}
				}
			}
		}
	}
	if len(out) == 0 {
		t.Fatal("PB-TOK-8: parsed no Group constants out of internal/status. Every assertion " +
			"below would pass over an empty set, which is the exact shape of a fence that is " +
			"green because it is looking at nothing.")
	}
	return out
}

// TestPBTOK8_EveryStatusGroupIsBoundToExactlyOneToken is the requirement.
func TestPBTOK8_EveryStatusGroupIsBoundToExactlyOneToken(t *testing.T) {
	groups := declaredGroups(t)
	rows := loadGroupTokenMap(t)
	tokens := loadDesignTokens(t)

	boundGroups := map[string][]int{}
	boundTokens := map[string][]string{}

	for _, r := range rows {
		value, ok := groups[r.Const]
		if !ok {
			names := make([]string, 0, len(groups))
			for n := range groups {
				names = append(names, n)
			}
			sort.Strings(names)
			t.Errorf("%s:%d: %q is not a Group constant in internal/status. Declared: %s",
				groupTokenMapFile, r.Line, r.Const, strings.Join(names, ", "))
			continue
		}
		if value != r.Value {
			t.Errorf("%s:%d: the table says %s is %q and internal/status says %q. The wire value "+
				"is what crosses to the phone, so a table holding a stale one binds a colour to a "+
				"Group the daemon never sends.", groupTokenMapFile, r.Line, r.Const, r.Value, value)
		}
		boundGroups[r.Const] = append(boundGroups[r.Const], r.Line)

		if _, ok := tokens.Tokens[r.Token]; !ok {
			t.Errorf("%s:%d: binds %s to %q, which %s does not declare",
				groupTokenMapFile, r.Line, r.Const, r.Token, tokensRelPath)
			continue
		}
		if kind := tokens.Kinds[r.Token]; kind != "color" {
			t.Errorf("%s:%d: binds %s to %s, which the origin types %q. A Group is rendered as a "+
				"colour -- a dot fill, a row rail, a section label -- so its token must be one.",
				groupTokenMapFile, r.Line, r.Const, r.Token, kind)
			continue
		}
		boundTokens[r.Token] = append(boundTokens[r.Token], r.Const)
	}

	// A Group with no token fails. This is the direction that catches a FIFTH Group: the
	// constants are parsed, so adding one to internal/status makes this list non-empty
	// immediately, before anyone has to remember that a table exists.
	var unbound []string
	for name, value := range groups {
		if len(boundGroups[name]) == 0 {
			unbound = append(unbound, name+" ("+value+")")
		}
	}
	sort.Strings(unbound)
	if len(unbound) > 0 {
		t.Errorf("PB-TOK-8: %d status.Group constant(s) have no row in %s:\n\t%s\n"+
			"A Group the phone renders with no token is a Group whose colour an implementer has "+
			"to invent at the point of use -- which is how ReadyForReview would have acquired the "+
			"retired mock's purple.", len(unbound), groupTokenMapFile, strings.Join(unbound, "\n\t"))
	}

	// One row per Group, both ways.
	for name, lines := range boundGroups {
		if len(lines) > 1 {
			t.Errorf("PB-TOK-8: %s is bound on %d rows (lines %v); a Group with two colours has no "+
				"colour", name, len(lines), lines)
		}
	}

	// A token bound to two Groups fails. Four Groups sharing three hues means two states that
	// look identical on a triage surface whose entire job is telling them apart.
	for tok, consts := range boundTokens {
		if len(consts) > 1 {
			sort.Strings(consts)
			t.Errorf("PB-TOK-8: token %s is bound to %d Groups (%s). ADR-007 B134 decision 1 "+
				"chose a rebinding over a 32nd token specifically because it gives all four "+
				"Groups DISTINCT hues at zero token cost; sharing one throws that away and makes "+
				"two states indistinguishable on the surface built to distinguish them.",
				tok, len(consts), strings.Join(consts, ", "))
		}
	}
}

// TestPBTOK8_EveryBoundTokenReachesTheTheme is the second half of "joined bidirectionally to both
// status.Group AND the theme".
//
// The binding is only real if the token it names is one the app can actually paint with. A Group
// bound to a token that has no <color> resource is bound to nothing, and the failure would
// surface as an unstyled dot on a handset rather than as a red test.
func TestPBTOK8_EveryBoundTokenReachesTheTheme(t *testing.T) {
	rows := loadGroupTokenMap(t)
	colourRows := loadTokenMap(t)
	colors := androidColors(t)

	resourceOf := map[string]string{}
	for _, r := range colourRows {
		resourceOf[r.Token] = r.Resource
	}

	seen := 0
	for _, r := range rows {
		resource, ok := resourceOf[r.Token]
		if !ok {
			t.Errorf("PB-TOK-8: %s binds %s to %s, which has no row in %s and therefore no Android "+
				"colour resource. PB-TOK-5 exists so that every colour token reaches the app; a "+
				"Group bound to one that does not is bound to nothing.",
				groupTokenMapFile, r.Const, r.Token, tokenMapFile)
			continue
		}
		if _, ok := colors[resource]; !ok {
			t.Errorf("PB-TOK-8: %s binds %s to %s -> <color name=%q>, which colors.xml does not "+
				"declare", groupTokenMapFile, r.Const, r.Token, resource)
			continue
		}
		seen++
	}
	if seen == 0 {
		t.Fatal("PB-TOK-8: no binding resolved all the way to a colour resource, so this test " +
			"asserted nothing")
	}
}

// TestPBTOK8_TheRebindingRecordedInTheADRIsTheOneInTheTable is "Recorded in the ADR", executed.
//
// The criterion asks for the decision to be recorded, and a decision recorded in one place and
// executed from another drifts the first time someone edits only one of them. So the table's
// tokens are joined against the ADR entry that chose them.
//
// The two DIRECTIONAL assertions are the ones that matter and they are not a second copy of the
// table -- they are the decision itself, which the table alone cannot defend. B134 moves green
// from Completed to ReadyForReview and gives Completed the recessive grey, against both the
// artifact's demo (which labels the green dot "Done") and the intuition of everyone who reads
// "ok" as "finished". Reverting that is a one-word edit that looks like a bug fix, and only a
// fence that states the direction can tell the difference.
func TestPBTOK8_TheRebindingRecordedInTheADRIsTheOneInTheTable(t *testing.T) {
	rows := loadGroupTokenMap(t)

	adr := readFileOrFail(t, filepath.Join(repoRoot(t), filepath.FromSlash(adrRelPath)), "PB-TOK-8")
	const marker = "## B134."
	start := strings.Index(adr, marker)
	if start < 0 {
		t.Fatalf("PB-TOK-8: %s has no %q entry, and it is where this binding was decided",
			adrRelPath, strings.TrimSpace(marker))
	}
	section := adr[start:]
	if next := strings.Index(section[len(marker):], "\n## "); next >= 0 {
		section = section[:len(marker)+next]
	}
	if len(section) < 200 {
		t.Fatalf("PB-TOK-8: the B134 section extracted from %s is %d bytes; the scan below would "+
			"pass or fail for the wrong reason", adrRelPath, len(section))
	}

	bindingOf := map[string]string{}
	for _, r := range rows {
		bindingOf[r.Const] = r.Token
		if !strings.Contains(section, r.Token) {
			t.Errorf("PB-TOK-8: %s binds %s to %s, and ADR-007 B134 does not mention %s. The "+
				"criterion asks for this decision to be recorded; a table edited without the ADR "+
				"is a decision changed without one.", groupTokenMapFile, r.Const, r.Token, r.Token)
		}
	}

	// The rebinding B134 argues for, in both directions.
	if got := bindingOf["GroupReadyForReview"]; got != "--p-ok" {
		t.Errorf("PB-TOK-8: ReadyForReview is bound to %q; ADR-007 B134 decision 1 binds it to "+
			"--p-ok. Substrate's demo labelled the green dot \"Done\", and moving green to review "+
			"is the deliberate part of the decision -- it is what swarm's own TUI identity already "+
			"does and what a triage surface needs.", got)
	}
	if got := bindingOf["GroupCompleted"]; got != "--p-ink3" {
		t.Errorf("PB-TOK-8: Completed is bound to %q; ADR-007 B134 decision 1 binds it to --p-ink3, "+
			"the recessive grey. Finished work should recede rather than hold the most saturated "+
			"colour on screen. Binding it to the green is the intuitive edit and the one the "+
			"decision was made against.", got)
	}
}

// TestPBTOK8_TheGroupParserCanActuallyFail is the NEGATIVE CONTROL.
//
// Every assertion above is built on declaredGroups, and a parser that returned nothing would make
// "a Group with no token fails" vacuous while looking green -- the failure mode this repository
// has had to reject before. So the parser is checked against what internal/status actually
// declares: it must find the Group constants, it must NOT sweep up the other three constant
// blocks in the same file (Process, Turn, Interaction are also string-typed constants in a
// const block, which is exactly what a parser filtering on shape rather than on type would
// collect), and it must recover the wire values rather than the identifier names.
func TestPBTOK8_TheGroupParserCanActuallyFail(t *testing.T) {
	groups := declaredGroups(t)

	if len(groups) < 4 {
		t.Fatalf("PB-TOK-8: the parser found %d Group constants; internal/status declares at "+
			"least the four the status model is built on", len(groups))
	}
	for _, other := range []string{"ProcessRunning", "TurnActive", "InteractionNone"} {
		if _, ok := groups[other]; ok {
			t.Errorf("PB-TOK-8: the parser collected %s, which is not a Group. It is filtering on "+
				"the shape of a const block rather than on the declared type, so it would bind "+
				"colours to constants that are not display states.", other)
		}
	}
	for name, value := range groups {
		if value == name {
			t.Errorf("PB-TOK-8: Group %s parsed to the value %q; the parser is returning identifier "+
				"names instead of the string literals that cross the wire", name, value)
		}
		if !strings.HasPrefix(name, "Group") {
			t.Errorf("PB-TOK-8: the parser collected %q as a Group constant", name)
		}
	}
	// And the values must be distinct, or "a token bound to two Groups" could never be detected
	// because two Groups would already be one.
	byValue := map[string]string{}
	for name, value := range groups {
		if prev, dup := byValue[value]; dup {
			t.Errorf("PB-TOK-8: %s and %s both parsed to %q", prev, name, value)
		}
		byValue[value] = name
	}
}
