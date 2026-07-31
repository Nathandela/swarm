package gate

// FAILING-FIRST (TDD RED, GG-5) for PB-TOK-7, second criterion.
//
// "Every derived colour is produced by a single documented blend function over token inputs; a
//  gate asserts NO KOTLIN OR XML LITERAL EQUALS A DERIVATION'S OUTPUT, and that changing a base
//  token moves the derived value."
//
// The blend function and the "changing a base token moves the derived value" half live in
// internal/design (derive.go, derive_test.go), because that is where the arithmetic is and where
// a perturbed token set can be constructed in memory. THIS file is the other half, and it can
// only exist here: it is a scan of the Android module for the four values the artifact resolves.
//
// WHY THE SCAN IS NECESSARY AND NOT BELT-AND-BRACES. PB-TOK-1's join is between a token NAME and
// a resource name. #6D5220 is not any token's value, so no row would ever have named it, so the
// join is structurally incapable of noticing it typed into a drawable -- the fence and the defect
// pass through each other. That is worse than the original three-copies-of-the-palette problem it
// was written for, because at least those three copies were all of the SAME numbers and a
// reviewer comparing them could see the difference. A derived colour transcribed once looks like
// an ordinary colour to everything that inspects the theme.
//
// AND THE FOUR ARE EXACTLY THE VALUES SOMEONE WILL TRANSCRIBE. They are quoted as resolved hex in
// the requirements, in the design inventory and in the ADR, because that is how a human discusses
// a colour. Reading `#B3F1A10D` in a spec and typing it into a Drawable is not carelessness; it
// is the obvious thing to do, and the only thing that makes it wrong is that the spec's number is
// downstream of a token that can change.

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/Nathandela/swarm/internal/design"
)

// colourLiteral matches every spelling of a colour an Android source can carry: an XML
// `#rrggbb` / `#aarrggbb`, and Kotlin's `0xAARRGGBB`. Both are captured as bare hex digits so the
// comparison below is over VALUES and not over notation -- a transcription that switched
// notation on the way in is still a transcription.
var colourLiteral = regexp.MustCompile(`(?i)(?:#|0x)([0-9a-f]{6}|[0-9a-f]{8})\b`)

type foundLiteral struct {
	Hex  string // upper case, without the # or 0x
	File string
	Line int
}

// scanForColourLiterals walks the Android module's production sources and returns every colour
// literal in them.
//
// SCOPE, stated rather than left to be inferred: app/src/main only -- production Kotlin (comments
// stripped, see kotlinCodeOnly) and every file under res/. Test sources are deliberately out of
// scope, because a test that PINS a derived value against the blend function has to name it, and
// forbidding that would forbid the test that proves the derivation is right. What must never
// happen is the value SHIPPING as a literal, and everything that ships is under src/main.
func scanForColourLiterals(t *testing.T) []foundLiteral {
	t.Helper()
	var out []foundLiteral

	add := func(path, content string) {
		for i, line := range strings.Split(content, "\n") {
			for _, m := range colourLiteral.FindAllStringSubmatch(line, -1) {
				out = append(out, foundLiteral{
					Hex:  strings.ToUpper(m[1]),
					File: mustRel(t, path),
					Line: i + 1,
				})
			}
		}
	}

	for _, path := range kotlinFiles(t, kotlinMainRoot(t)) {
		// Comments are stripped for the same reason s20's reachability checks had to strip
		// them: a fence a comment can trip is a fence the next thorough commenter turns into
		// noise, and a fence a comment can SATISFY is one they turn off.
		add(path, kotlinCodeOnly(readFileOrFail(t, path, "PB-TOK-7")))
	}

	// Only .xml under res/. Every resource that can carry a colour as text is XML; reading a
	// binary asset as a string would put its bytes through a regexp for no benefit and a small
	// chance of a nonsense hit.
	resRoot := filepath.Join(appModule(t), "src", "main", "res")
	resXML := 0
	_ = filepath.WalkDir(resRoot, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".xml") {
			return nil
		}
		resXML++
		add(path, readFileOrFail(t, path, "PB-TOK-7"))
		return nil
	})
	if resXML == 0 {
		t.Fatalf("PB-TOK-7: no XML found under %s; the scan is looking at nothing",
			mustRel(t, resRoot))
	}

	sort.Slice(out, func(i, j int) bool {
		if out[i].File != out[j].File {
			return out[i].File < out[j].File
		}
		return out[i].Line < out[j].Line
	})
	return out
}

// derivedValues resolves every derivation over the staged origin and returns the set of hex
// spellings each one is recognisable by.
//
// BOTH SPELLINGS OF AN OPAQUE COLOUR COUNT. #6D5220 and #FF6D5220 are the same colour, and a gate
// that only knew one of them would be defeated by whichever the transcriber happened to write.
func derivedValues(t *testing.T) map[string]design.Derivation {
	t.Helper()
	tokens := loadDesignTokens(t)

	derivations := design.Derivations()
	if len(derivations) == 0 {
		t.Fatal("PB-TOK-7: internal/design declares no derivations, so this scan would look for " +
			"nothing and pass over any transcription at all")
	}

	out := map[string]design.Derivation{}
	canonical := map[string]string{} // resolved value -> the derivation that produced it
	for _, d := range derivations {
		c, err := d.Resolve(tokens.Tokens)
		if err != nil {
			t.Fatalf("PB-TOK-7: resolving %s over %s: %v", d.Name, tokensRelPath, err)
		}
		bare := strings.TrimPrefix(c.Hex(), "#")

		// COLLISION IS CHECKED AT INSERTION, not by inspecting the finished map. A map's keys
		// are unique by construction, so a loop over `out` asking whether two entries share a
		// key can never answer yes -- an earlier draft of this file asserted exactly that and
		// was therefore green no matter what. Two derivations resolving to one value would make
		// a scan hit unattributable, and the second would silently overwrite the first.
		if prev, dup := canonical[bare]; dup {
			t.Errorf("PB-TOK-7: derivations %q and %q both resolve to #%s, so a literal matching "+
				"it could not be attributed to either", prev, d.Name, bare)
		}
		canonical[bare] = d.Name

		out[bare] = d
		if c.A == 0xFF {
			// Hex() renders an opaque colour with six digits; the eight-digit spelling of the
			// same colour is the one a Kotlin `0x...` literal or an explicit-alpha resource
			// would carry, and both must be caught.
			out["FF"+bare] = d
		}
	}
	if len(canonical) != len(derivations) {
		t.Fatalf("PB-TOK-7: %d derivations resolved to %d distinct values",
			len(derivations), len(canonical))
	}
	return out
}

// TestPBTOK7_NoShippedLiteralIsADerivationsOutput is the criterion.
func TestPBTOK7_NoShippedLiteralIsADerivationsOutput(t *testing.T) {
	derived := derivedValues(t)
	literals := scanForColourLiterals(t)

	// NON-VACUITY, first. A scan that found nothing would report "no transcriptions" forever,
	// which is the exact shape of a green fence looking at an empty set. colors.xml alone carries
	// the 16 colours PB-TOK-5 landed, so the floor is real and not a guess.
	if len(literals) < colourTokenCount {
		t.Fatalf("PB-TOK-7: the scan found %d colour literal(s) across the app's production "+
			"sources. colors.xml alone declares %d, so the scan is not reaching the files it "+
			"claims to cover and every assertion below passes over nothing.",
			len(literals), colourTokenCount)
	}

	for _, lit := range literals {
		d, ok := derived[lit.Hex]
		if !ok {
			continue
		}
		t.Errorf("PB-TOK-7: %s:%d carries the literal #%s, which is the output of the derivation "+
			"%q -- color-mix(in srgb, %s %d%%, %s).\n"+
			"\tSite in the design: %s\n"+
			"A derived colour is a FUNCTION of a token, so transcribing its resolved value pins "+
			"today's answer to a question the origin is still allowed to change: edit %s and this "+
			"literal keeps rendering the old design, silently, with every existing fence green. "+
			"PB-TOK-1's join cannot see it either -- #%s is not any token's value, so no row would "+
			"ever have named it.\n"+
			"Compute it instead: internal/design.Derivations() resolves it from the staged tokens.",
			lit.File, lit.Line, lit.Hex, d.Name, d.Base, d.Percent, d.Over, d.Site,
			tokensRelPath, lit.Hex)
	}
}

// TestPBTOK7_TheLiteralScanCanActuallyFail is the NEGATIVE CONTROL.
//
// Two ways the assertion above can be green while proving nothing, and both look fine in review:
// the scan finds no literals in the files it walks, or the regexp does not recognise the notation
// a transcription would actually be written in. So the recogniser is run over a synthetic source
// containing each of the four values in each spelling somebody would plausibly use, and every one
// must be caught.
func TestPBTOK7_TheLiteralScanCanActuallyFail(t *testing.T) {
	derived := derivedValues(t)
	if len(derived) == 0 {
		t.Fatal("PB-TOK-7: no derived values to look for")
	}

	// Every notation the four values could arrive in, built from the DERIVED values rather than
	// typed, so this control cannot itself become a transcription.
	var probes []string
	for hex := range derived {
		probes = append(probes,
			`<color name="x">#`+hex+`</color>`,
			`val x = 0x`+hex+`.toInt()`,
			`android:tint="#`+strings.ToLower(hex)+`"`,
		)
	}
	sort.Strings(probes)

	for _, probe := range probes {
		m := colourLiteral.FindStringSubmatch(probe)
		if m == nil {
			t.Errorf("the colour-literal recogniser does not match %q, so a transcription written "+
				"that way ships unnoticed", probe)
			continue
		}
		if _, ok := derived[strings.ToUpper(m[1])]; !ok {
			t.Errorf("%q matched but resolved to %q, which is not a derived value; the recogniser "+
				"is capturing the wrong part of the literal", probe, m[1])
		}
	}

	// And it must not match everything. A recogniser that hit on any text would make the real
	// assertion fail constantly rather than never -- a different failure, equally useless, and
	// the one that gets a gate deleted. These must produce NO match at all, which is a stronger
	// statement than "no match that happens to be a derived value".
	for _, notAColour := range []string{
		"val n = 12345",
		"sha256:91be04d2 is a digest prefix",
		"setPadding(24)",
		"",
	} {
		if m := colourLiteral.FindStringSubmatch(notAColour); m != nil {
			t.Errorf("the colour-literal recogniser matched %q in %q, which carries no colour "+
				"literal at all", m[0], notAColour)
		}
	}

	// Distinctness of the four resolved values is asserted inside derivedValues, at insertion,
	// where a collision is observable. It cannot be asserted from here: `derived` is a map, its
	// keys are unique by construction, and a loop over it asking whether two entries collide
	// answers no unconditionally.
}

// TestPBTOK7_TheDerivationsAreReachableFromTheOrigin closes the loop the other way.
//
// The scan above forbids the four values from being TYPED. That is only half a requirement: a
// forbidden value with no supported way to obtain it is a rule people route around. So this
// asserts the derivations resolve from the staged origin -- the same tokens.json the Android
// module stages as a test resource -- and that each one's inputs are tokens PB-TOK-5 actually
// landed as colour resources, so a consumer computing the blend on the platform has the operands.
func TestPBTOK7_TheDerivationsAreReachableFromTheOrigin(t *testing.T) {
	tokens := loadDesignTokens(t)
	rows := loadTokenMap(t)

	mapped := map[string]string{}
	for _, r := range rows {
		mapped[r.Token] = r.Resource
	}

	derivations := design.Derivations()
	if len(derivations) == 0 {
		t.Fatal("PB-TOK-7: no derivations declared")
	}
	for _, d := range derivations {
		if _, err := d.Resolve(tokens.Tokens); err != nil {
			t.Errorf("PB-TOK-7: %s does not resolve over the staged origin: %v", d.Name, err)
			continue
		}
		operands := []string{d.Base}
		if d.Over != design.Transparent {
			operands = append(operands, d.Over)
		}
		for _, tok := range operands {
			if _, ok := mapped[tok]; !ok {
				t.Errorf("PB-TOK-7: derivation %q needs token %s, which has no row in %s and so "+
					"no <color> the app can read. Forbidding the transcription without landing "+
					"the operands leaves no supported way to obtain the colour, which is how a "+
					"rule becomes something to route around.", d.Name, tok, tokenMapFile)
			}
		}
	}
}
