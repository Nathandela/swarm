package verify_test

// PB-DOC-2's SUMMARY half: the numbers the traceability document states about itself.
//
// WHY THIS IS A SEPARATE FILE FROM phaseb_coverage_test.go. That file's subject, stated in
// its own opening line, is "COVERAGE OF IDS, NOT OF PROSE": it asks whether every active
// requirement has a row, and it deliberately reads nothing else. Its comment enumerating
// how it differs from the two checks either side of it would stop being true if a count
// checker moved in beside it. This file has a different subject, needs a different parser
// (column-accurate rows and whitespace-normalised prose, rather than one id regexp over
// two documents), and shares only the requirement id -- so `go test -run TestPBDOC2_`
// still runs both halves together.
//
// WHAT DRIFTED, TWICE IN ONE DAY, WITH NOTHING TO CATCH IT. The document opens with a
// count table and several paragraphs restating the same numbers. Nothing recomputed either
// from the rows underneath:
//
//   - commit effd1ac flipped PB-DS-2 from shipped to NOT MET; the header went on saying
//     Shipped 144 / Evidenced 144 / 16 not met.
//   - a paragraph reading "10 not met + 1 void" was stale for two commits after b669af9
//     moved the bucket to 16.
//   - "109 of 146 requirements are DERIVED" had never accounted for 16 rows added by hand.
//
// All three were caught by people reading carefully, which is not a mechanism. The summary
// is what a reviewer reads FIRST, so a document whose stated coverage silently diverges
// from its own table is worse than one with no summary at all: it spends the reader's
// trust on a number nobody computed.
//
// THE DOCUMENT IS NOT SIMPLY THE GENERATOR'S OUTPUT, which is why recomputing from the
// ROWS is the only correct comparison. scripts/phaseb-traceability.py emits the "not met +
// void" sentence as a HARD-CODED literal, and the paragraph about the 16 hand-added rows
// is not in the generator at all. Re-running the generator and diffing would therefore
// report the document wrong when it is right. The rows are the ground truth a reader
// checks a summary against, so they are what this gate checks it against.

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// traceRowFloor is the "cannot pass by measuring nothing" floor. The document carries 162
// requirement rows today; a run that parsed fewer than this has lost the row pattern, and
// every count below would then be compared against a nearly empty tally and agree with
// nothing.
const traceRowFloor = 100

func traceDoc(t *testing.T) string {
	t.Helper()
	return readDoc(t, "docs/verification/remote-phaseB-traceability.md")
}

// liveTally is the document's rows counted against the real filesystem, which is what
// "Evidenced (measured on disk)" claims to be.
func liveTally(t *testing.T) traceTally {
	t.Helper()
	root := repoRoot(t)
	return tallyTraceRows(traceDoc(t), func(rel string) bool {
		_, err := os.Stat(filepath.Join(root, filepath.FromSlash(rel)))
		return err == nil
	})
}

// TestPBDOC2_EveryRowStatusIsClassified runs before the counts and is the reason they mean
// anything. The tally sorts rows into shipped, NOT MET and remaining; if a row carries a
// status none of those three match, it would vanish from every count while still appearing
// in the table, and the header would be "wrong" in a way that reads as a tally bug.
func TestPBDOC2_EveryRowStatusIsClassified(t *testing.T) {
	tl := liveTally(t)

	if tl.requirements < traceRowFloor {
		t.Fatalf("PB-DOC-2: only %d requirement rows were parsed out of the traceability "+
			"document (floor %d). The row pattern no longer matches its table, so every count "+
			"check below would compare the header against nothing", tl.requirements, traceRowFloor)
	}
	if len(tl.unclassified) > 0 {
		t.Errorf("PB-DOC-2: %d row(s) carry a Status this gate cannot classify as shipped, NOT "+
			"MET or remaining: %v. They are counted in the total and in nothing else, so the "+
			"header's own arithmetic would no longer add up", len(tl.unclassified), tl.unclassified)
	}
	if sum := tl.shipped + tl.notMet + tl.remaining; sum != tl.requirements {
		t.Errorf("PB-DOC-2: the rows partition into shipped=%d + NOT MET=%d + remaining=%d = %d, "+
			"but there are %d rows. The three buckets must cover every row or the header's counts "+
			"describe a subset of its own table",
			tl.shipped, tl.notMet, tl.remaining, sum, tl.requirements)
	}
}

// TestPBDOC2_TheHeaderCountTableAgreesWithTheRows is the effd1ac defect, gated.
func TestPBDOC2_TheHeaderCountTableAgreesWithTheRows(t *testing.T) {
	doc := traceDoc(t)
	tl := liveTally(t)

	claims, labels := headerCountClaims(doc, tl)
	if len(claims) != traceHeaderRowCount {
		t.Fatalf("PB-DOC-2: the header count table yielded %d of the %d counts this gate knows "+
			"how to recompute. Labels found in the table: %v. A count that disappeared is a count "+
			"nothing checks; either it moved, or the table was edited by hand",
			len(claims), traceHeaderRowCount, labels)
	}
	for _, c := range wrongClaims(claims) {
		t.Errorf("PB-DOC-2: %s says %d; the rows beneath it say %d", c.what, c.stated, c.computed)
	}
}

// TestPBDOC2_TheProseCountsAgreeWithTheRows covers the other two drifts, which were both in
// paragraphs rather than in the table.
func TestPBDOC2_TheProseCountsAgreeWithTheRows(t *testing.T) {
	doc := traceDoc(t)
	tl := liveTally(t)

	claims := proseCountClaims(doc, tl)
	if len(claims) == 0 {
		t.Fatalf("PB-DOC-2: not one numeric claim in the document's summary prose was readable. " +
			"Either every such sentence was deleted, or all of them were reworded past the anchors " +
			"below -- and in the second case this half of the gate is now inert while the document " +
			"goes on stating numbers. Restore a claim or update the anchors")
	}
	for _, c := range wrongClaims(claims) {
		t.Errorf("PB-DOC-2: the summary prose says %s = %d; the rows say %d",
			c.what, c.stated, c.computed)
	}
}

// TestPBDOC2_TheEvidenceBulletCountsAgreeWithTheRows checks the per-slice bullets under
// "Evidence files carrying a dated correction", each of which states a count AND lists the
// ids it counts. Three things must agree there, not two: the number, the list, and the rows
// that actually name that slice.
func TestPBDOC2_TheEvidenceBulletCountsAgreeWithTheRows(t *testing.T) {
	doc := traceDoc(t)
	tl := liveTally(t)

	claims := evidenceBulletClaims(doc, tl)
	if strings.Contains(doc, traceBulletHeading) && len(claims) == 0 {
		t.Fatalf("PB-DOC-2: the %q section is present and not one of its bullets parsed, so its "+
			"counts are checked by nothing", traceBulletHeading)
	}
	for _, c := range wrongClaims(claims) {
		t.Errorf("PB-DOC-2: %s = %d; the rows say %d", c.what, c.stated, c.computed)
	}
}

// ---------------------------------------------------------------------------
// Negative controls. The three checks above have only ever run against a document that
// satisfies them, and a wrongClaims that returned nil unconditionally would look exactly
// the same from here.
// ---------------------------------------------------------------------------

// traceFixture is a whole traceability document in miniature: six header counts, both
// prose claims, one evidence bullet, and three rows that make every count non-trivial --
// two shipped (one of them evidenced by a file the fixture's filesystem says is missing)
// and one NOT MET.
const traceFixture = "# Phase B requirement traceability\n\n" +
	"| | count |\n|---|---|\n" +
	"| Requirements | 3 |\n" +
	"| Shipped (asserted by hand) | 2 |\n" +
	"| Evidenced (measured on disk) | 1 |\n" +
	"| **NOT MET (slice shipped, requirement invalidated later)** | **1** |\n" +
	"| Remaining | 0 |\n" +
	"| **Shipped with NO evidence file** | **1** |\n\n" +
	"so the honest reading of the number above is **0 not met + 1 void**. The row stays.\n\n" +
	"## Evidence files carrying a dated correction, amendment or withdrawal\n\n" +
	"- **S1** — cited for 2 requirements: PB-NET-1, PB-NET-2\n\n" +
	"## Derivation: has anyone ever broken this row's fence?\n\n" +
	"**1 of 3 requirements are DERIVED.** The rest have never had a fence broken on\n" +
	"purpose, which is this project's largest measured blind spot.\n\n" +
	"## Every requirement\n\n" +
	"| Requirement | Slice | Status | Derivation | Evidence |\n|---|---|---|---|---|\n" +
	"| PB-NET-1 | S1 | shipped | **DERIVED** — the guard removed -> the fence fails | `there.md` |\n" +
	"| PB-NET-2 | S1 | shipped | not derived | `gone.md` |\n" +
	"| PB-NET-3 | S2 | **NOT MET** | not derived | the reason it is unmet |\n"

// traceFixtureDisk is the fixture's filesystem: `there.md` exists, `gone.md` does not, so
// "Evidenced" and "Shipped with NO evidence file" are both non-zero and cannot be right by
// accident.
func traceFixtureDisk(rel string) bool { return rel == "there.md" }

func TestPBDOC2_TheCountRulesAcceptADocumentThatAgreesWithItself(t *testing.T) {
	tl := tallyTraceRows(traceFixture, traceFixtureDisk)

	for what, got := range map[string]int{
		"requirements": tl.requirements, "shipped": tl.shipped, "notMet": tl.notMet,
		"remaining": tl.remaining, "derived": tl.derived, "evidenced": tl.evidenced,
		"noEvidence": tl.noEvidence,
	} {
		want := map[string]int{
			"requirements": 3, "shipped": 2, "notMet": 1, "remaining": 0,
			"derived": 1, "evidenced": 1, "noEvidence": 1,
		}[what]
		if got != want {
			t.Errorf("tally.%s = %d, want %d", what, got, want)
		}
	}

	var all []traceClaim
	header, _ := headerCountClaims(traceFixture, tl)
	all = append(all, header...)
	all = append(all, proseCountClaims(traceFixture, tl)...)
	all = append(all, evidenceBulletClaims(traceFixture, tl)...)
	if len(all) < 9 {
		t.Fatalf("the fixture yielded only %d claims (6 header + 2 prose + 1 bullet expected); "+
			"the controls below would then be proving very little", len(all))
	}
	if wrong := wrongClaims(all); len(wrong) != 0 {
		t.Fatalf("a document that agrees with itself reported %d wrong claims: %v", len(wrong), wrong)
	}
}

// TestPBDOC2_AHeaderThatOutlivesAFlippedRowIsReported is the effd1ac incident replayed: one
// row goes from shipped to NOT MET and the header is not touched.
func TestPBDOC2_AHeaderThatOutlivesAFlippedRowIsReported(t *testing.T) {
	doc := strings.Replace(traceFixture,
		"| PB-NET-1 | S1 | shipped |", "| PB-NET-1 | S1 | **NOT MET** |", 1)
	if doc == traceFixture {
		t.Fatal("the fixture no longer contains the row this control flips")
	}
	tl := tallyTraceRows(doc, traceFixtureDisk)

	claims, _ := headerCountClaims(doc, tl)
	wrong := wrongClaims(claims)

	// All three counts the flip moves must be reported, not just the first: the row leaves
	// Shipped, joins NOT MET, and takes its evidence file out of Evidenced with it. A gate
	// that named only one of them would send someone to fix a number and leave two.
	for _, want := range []string{"Shipped", "NOT MET", "Evidenced"} {
		var found bool
		for _, c := range wrong {
			if strings.Contains(c.what, want) {
				found = true
			}
		}
		if !found {
			t.Errorf("flipping one row from shipped to NOT MET did not report the %s count as "+
				"wrong; reported: %v. This is the exact drift the gate was written for", want, wrong)
		}
	}
}

// TestPBDOC2_AStaleProseSentenceIsReported is the "10 not met + 1 void" drift: the bucket
// moves and the paragraph does not.
func TestPBDOC2_AStaleProseSentenceIsReported(t *testing.T) {
	doc := strings.Replace(traceFixture,
		"**0 not met + 1 void**", "**7 not met + 1 void**", 1)
	if doc == traceFixture {
		t.Fatal("the fixture no longer contains the sentence this control staled")
	}
	tl := tallyTraceRows(doc, traceFixtureDisk)

	wrong := wrongClaims(proseCountClaims(doc, tl))
	if len(wrong) != 1 || !strings.Contains(wrong[0].what, "void") {
		t.Fatalf("a paragraph claiming 7 not met + 1 void over a table with 1 NOT MET row "+
			"reported %v; want exactly the not-met-plus-void claim", wrong)
	}
}

// TestPBDOC2_AStaleDerivedCountIsReported is the "109 of 146" drift: rows are added and the
// denominator is not recounted.
func TestPBDOC2_AStaleDerivedCountIsReported(t *testing.T) {
	doc := strings.Replace(traceFixture,
		"**1 of 3 requirements are DERIVED.**", "**1 of 2 requirements are DERIVED.**", 1)
	if doc == traceFixture {
		t.Fatal("the fixture no longer contains the sentence this control staled")
	}
	tl := tallyTraceRows(doc, traceFixtureDisk)

	wrong := wrongClaims(proseCountClaims(doc, tl))
	if len(wrong) != 1 || wrong[0].stated != 2 || wrong[0].computed != 3 {
		t.Fatalf("a document claiming DERIVED out of 2 over a 3-row table reported %v; want the "+
			"denominator reported as stated 2 / computed 3", wrong)
	}
}

// TestPBDOC2_ABulletThatMiscountsItsOwnListIsReported covers the third shape: the bullet's
// number, its id list and the rows must all agree.
func TestPBDOC2_ABulletThatMiscountsItsOwnListIsReported(t *testing.T) {
	doc := strings.Replace(traceFixture,
		"cited for 2 requirements: PB-NET-1, PB-NET-2",
		"cited for 5 requirements: PB-NET-1, PB-NET-2", 1)
	if doc == traceFixture {
		t.Fatal("the fixture no longer contains the bullet this control miscounts")
	}
	tl := tallyTraceRows(doc, traceFixtureDisk)

	if wrong := wrongClaims(evidenceBulletClaims(doc, tl)); len(wrong) == 0 {
		t.Fatal("a bullet claiming 5 requirements while listing 2 and owning 2 was reported as " +
			"agreeing with the rows")
	}
}

// TestPBDOC2_AnUnknownStatusIsNotSilentlyDropped guards the tally's own blind spot. A row
// whose Status is neither shipped, NOT MET nor pending must be reported, not ignored --
// otherwise a new status word would shrink every count at once and the header would look
// wrong while the rows looked fine.
func TestPBDOC2_AnUnknownStatusIsNotSilentlyDropped(t *testing.T) {
	doc := strings.Replace(traceFixture,
		"| PB-NET-2 | S1 | shipped |", "| PB-NET-2 | S1 | half-shipped |", 1)
	tl := tallyTraceRows(doc, traceFixtureDisk)

	if len(tl.unclassified) != 1 || !strings.Contains(tl.unclassified[0], "half-shipped") {
		t.Fatalf("a row with an unrecognised Status was classified as %v; want it reported so it "+
			"cannot vanish from every count at once", tl.unclassified)
	}
	if tl.requirements != 3 {
		t.Errorf("requirements = %d, want 3: an unclassifiable row is still a row", tl.requirements)
	}
}

// TestPBDOC2_APipeInsideACodeSpanDoesNotCorruptARow is a positive control over the parser's
// one real hazard, and it is not hypothetical: thirteen rows in the live document carry a
// pipe inside a backticked code span, PB-DS-6's `git show ... | grep PhoneScaffoldView`
// among them. A parser that split those rows on every pipe would read the wrong cell as
// Evidence and quietly undercount "Evidenced (measured on disk)" by thirteen.
func TestPBDOC2_APipeInsideACodeSpanDoesNotCorruptARow(t *testing.T) {
	doc := strings.Replace(traceFixture,
		"| PB-NET-1 | S1 | shipped | **DERIVED** — the guard removed -> the fence fails | `there.md` |",
		"| PB-NET-1 | S1 | shipped | **DERIVED** — `git show HEAD:f | grep x` returns nothing | `there.md` |",
		1)
	if doc == traceFixture {
		t.Fatal("the fixture no longer contains the row this control rewrites")
	}
	tl := tallyTraceRows(doc, traceFixtureDisk)

	if tl.shipped != 2 || tl.derived != 1 || tl.evidenced != 1 {
		t.Fatalf("a row carrying a pipe inside a code span tallied shipped=%d derived=%d "+
			"evidenced=%d; want 2/1/1. The live document has thirteen such rows",
			tl.shipped, tl.derived, tl.evidenced)
	}
}

// ---------------------------------------------------------------------------
// The rows, counted.
// ---------------------------------------------------------------------------

// traceRowsHeading separates the document's summary -- header table and prose -- from the
// per-requirement table it summarises. Everything above it is a claim; everything below it
// is the evidence for that claim.
const traceRowsHeading = "## Every requirement"

// traceHeaderRowCount is how many of the header table's counts this gate recomputes. All
// six, which is the point: a table where five are checked and one is not is a table with a
// number nobody computed, which is the defect.
const traceHeaderRowCount = 6

const traceBulletHeading = "## Evidence files carrying a dated correction"

// traceReqRow captures one row of the per-requirement table.
//
// THE FOURTH GROUP IS DELIBERATELY GREEDY AND UNSPLIT. Requirement, Slice and Status are
// pipe-free by construction, so the first three cells can be taken exactly. The remainder
// -- Derivation and Evidence -- CANNOT be split on every pipe, because twelve rows in the
// live document carry an EXTRA CELL: the sixteen design-system and token rows were added by
// hand rather than by the generator, and twelve of them put their verification narrative in
// a sixth cell between Derivation and Evidence, where the header declares five columns
// (agents-tracker-brc). Two more rows carried a pipe that was never a cell boundary at all
// -- PB-DS-6 quoting `git show ... | grep PhoneScaffoldView` inside a code span, PB-DS-2
// quoting another document's table inside a sentence -- and those are now escaped as \|.
// Reading any of these rows cell-by-cell would take a fragment of the narrative as the
// Evidence path and undercount "Evidenced (measured on disk)" with no visible symptom.
// Evidence is therefore taken as the text after the LAST pipe, which is where the generator
// puts it, and Derivation is recognised by its leading marker rather than by position.
var traceReqRow = regexp.MustCompile(`^\|\s*(PB-[A-Z0-9]+-\d+)\s*\|([^|]*)\|([^|]*)\|(.*)\|\s*$`)

// traceEvidencePath matches an Evidence cell that names a file, as opposed to one holding
// the prose reason a NOT MET row carries instead, or the em dash a pending row carries.
var traceEvidencePath = regexp.MustCompile("^`([^`]+)`$")

type traceTally struct {
	requirements int
	shipped      int
	notMet       int
	remaining    int
	derived      int
	evidenced    int
	noEvidence   int
	perSlice     map[string][]string
	// unclassified holds rows whose Status matched none of the three buckets, so that a
	// new status word is reported rather than silently dropped from every count at once.
	unclassified []string
}

// tallyTraceRows is the whole ground truth: what the rows say, counted the way the header
// claims to count them. It is a pure function of the document text and a filesystem
// predicate, so the controls can drive it over a document constructed to break it.
func tallyTraceRows(doc string, onDisk func(rel string) bool) traceTally {
	tl := traceTally{perSlice: map[string][]string{}}
	for _, line := range strings.Split(doc, "\n") {
		m := traceReqRow.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		id, slice, status, rest := m[1], strings.TrimSpace(m[2]), strings.TrimSpace(m[3]), m[4]
		tl.requirements++
		tl.perSlice[slice] = append(tl.perSlice[slice], id)

		if strings.HasPrefix(strings.TrimSpace(rest), "**DERIVED**") {
			tl.derived++
		}

		switch {
		case strings.Contains(status, "NOT MET"):
			tl.notMet++
		case status == "shipped":
			tl.shipped++
			// Evidenced is MEASURED, per the header's own wording, so the cited path is
			// resolved against the filesystem rather than merely being present.
			evidence := strings.TrimSpace(rest)
			if i := strings.LastIndex(evidence, "|"); i >= 0 {
				evidence = strings.TrimSpace(evidence[i+1:])
			}
			if p := traceEvidencePath.FindStringSubmatch(evidence); p != nil && onDisk(p[1]) {
				tl.evidenced++
			} else {
				tl.noEvidence++
			}
		case status == "pending":
			tl.remaining++
		default:
			tl.unclassified = append(tl.unclassified, fmt.Sprintf("%s (Status %q)", id, status))
		}
	}
	for _, ids := range tl.perSlice {
		sort.Strings(ids)
	}
	return tl
}

// ---------------------------------------------------------------------------
// The claims the document makes about those rows.
// ---------------------------------------------------------------------------

// traceClaim is one number the document states, beside the number the rows produce. Every
// check below reduces to comparing these two, so every failure reads the same way: here is
// what the document says, here is what its own table says.
type traceClaim struct {
	what     string
	stated   int
	computed int
}

func (c traceClaim) String() string {
	return fmt.Sprintf("%s: stated %d, rows say %d", c.what, c.stated, c.computed)
}

func wrongClaims(in []traceClaim) []traceClaim {
	var out []traceClaim
	for _, c := range in {
		if c.stated != c.computed {
			out = append(out, c)
		}
	}
	return out
}

// traceSummary is the region above the per-requirement table: the only place a claim ABOUT
// the rows can live. Scoping to it keeps the prose patterns from matching a number that
// happens to appear inside a row's own Derivation text.
func traceSummary(doc string) string {
	if i := strings.Index(doc, traceRowsHeading); i >= 0 {
		return doc[:i]
	}
	return doc
}

// traceCountRow matches a two-column row of the header count table.
var traceCountRow = regexp.MustCompile(`^\|([^|]*)\|([^|]*)\|\s*$`)

// headerCountRule maps a label to the tally field it must equal. ORDER IS LOAD-BEARING and
// the labels overlap on purpose: "NOT MET (slice shipped, requirement invalidated later)"
// contains both "shipped" and "requirement", and "Shipped with NO evidence file" contains
// "shipped", so the specific rules must be tried before the general ones.
//
// Matching a SUBSTRING rather than the whole label is what keeps this from being brittle:
// the parentheticals are prose and can be rewritten without breaking the gate, while the
// term of art each row is named for cannot be dropped without changing what the row means.
type headerCountRule struct {
	needle string
	what   string
	value  func(traceTally) int
}

var headerCountRules = []headerCountRule{
	{"no evidence", "Shipped with NO evidence file", func(t traceTally) int { return t.noEvidence }},
	{"not met", "NOT MET", func(t traceTally) int { return t.notMet }},
	{"evidenced", "Evidenced", func(t traceTally) int { return t.evidenced }},
	{"remaining", "Remaining", func(t traceTally) int { return t.remaining }},
	{"shipped", "Shipped", func(t traceTally) int { return t.shipped }},
	{"requirement", "Requirements", func(t traceTally) int { return t.requirements }},
}

// headerCountClaims reads the header count table. It also returns every label it saw, so a
// count that has gone missing can be reported with the table's actual contents rather than
// with "expected 6, got 5".
func headerCountClaims(doc string, tl traceTally) (claims []traceClaim, labels []string) {
	for _, line := range strings.Split(traceSummary(doc), "\n") {
		m := traceCountRow.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		n, err := strconv.Atoi(strings.Trim(strings.TrimSpace(m[2]), "*"))
		if err != nil {
			continue // the table's own header and separator rows
		}
		label := strings.ToLower(strings.Trim(strings.TrimSpace(m[1]), "*"))
		labels = append(labels, label)
		for _, rule := range headerCountRules {
			if strings.Contains(label, rule.needle) {
				claims = append(claims, traceClaim{
					what: "the header's " + rule.what, stated: n, computed: rule.value(tl),
				})
				break
			}
		}
	}
	return claims, labels
}

// ---------------------------------------------------------------------------
// Prose, read without pinning anyone's sentences.
//
// HOW THIS AVOIDS BEING BRITTLE, because a gate that fails when someone rephrases a
// sentence is a gate that gets deleted rather than fixed. Three choices:
//
//  1. The text is WHITESPACE-NORMALISED first. The document is hard-wrapped, so
//     "the denominator\ngrows to 162" is one claim split across two lines; matching the raw
//     text would miss it and the check would silently pass.
//  2. Each pattern is anchored on the TERM OF ART the claim cannot be stated without --
//     "void", "requirements are DERIVED", "denominator" -- not on the sentence around it.
//     The words between the anchors are free to change.
//  3. A claim that no longer matches ANY pattern is not a failure. A sentence that has been
//     deleted states no number and cannot be wrong. The floor is that at least ONE claim
//     must still be readable, which is what stops the whole half going quietly inert.
//
// WHAT THAT DELIBERATELY DOES NOT CATCH, stated rather than left to be discovered: a claim
// reworded past its anchor -- "sixteen unmet alongside one void" -- goes unchecked. The
// exchange is that rewording produces silence rather than a false failure, and every number
// still phrased around its anchor is still checked.
// ---------------------------------------------------------------------------

// proseClaimPattern is one numeric claim shape. sum says whether the captured numbers are
// two halves of one total (16 not met + 1 void) or two independent quantities (109 of 162).
type proseClaimPattern struct {
	re    *regexp.Regexp
	what  string
	sum   bool
	value func(traceTally) int
	// second, when set, checks the pattern's second capture as its own claim.
	second     string
	secondFrom func(traceTally) int
}

var proseClaimPatterns = []proseClaimPattern{
	{
		re:    regexp.MustCompile(`(\d+)\s*not met\s*\+\s*(\d+)\s*void`),
		what:  "the not-met-plus-void reading of the bucket",
		sum:   true,
		value: func(t traceTally) int { return t.notMet },
	},
	{
		re:         regexp.MustCompile(`(\d+)\s+of\s+(\d+)\s+requirements are DERIVED`),
		what:       "the DERIVED count",
		value:      func(t traceTally) int { return t.derived },
		second:     "the DERIVED count's denominator",
		secondFrom: func(t traceTally) int { return t.requirements },
	},
	{
		// "applies to these 16 exactly as to the other 146" -- the two halves of the whole.
		// The gap is bounded and lazy so the clause between them can be rewritten.
		re:    regexp.MustCompile(`these (\d+).{0,80}?the other (\d+)`),
		what:  "the hand-added-plus-generated split of the requirements",
		sum:   true,
		value: func(t traceTally) int { return t.requirements },
	},
	{
		re:    regexp.MustCompile(`denominator\D{0,40}?(\d+)`),
		what:  "the denominator the derivation paragraph names",
		value: func(t traceTally) int { return t.requirements },
	},
}

func proseCountClaims(doc string, tl traceTally) []traceClaim {
	text := strings.Join(strings.Fields(traceSummary(doc)), " ")
	var out []traceClaim
	for _, p := range proseClaimPatterns {
		for _, m := range p.re.FindAllStringSubmatch(text, -1) {
			first, _ := strconv.Atoi(m[1])
			if p.sum {
				second, _ := strconv.Atoi(m[2])
				out = append(out, traceClaim{
					what:     fmt.Sprintf("%s (%s + %s)", p.what, m[1], m[2]),
					stated:   first + second,
					computed: p.value(tl),
				})
				continue
			}
			out = append(out, traceClaim{what: p.what, stated: first, computed: p.value(tl)})
			if p.second != "" {
				n, _ := strconv.Atoi(m[2])
				out = append(out, traceClaim{what: p.second, stated: n, computed: p.secondFrom(tl)})
			}
		}
	}
	return out
}

// ---------------------------------------------------------------------------
// The per-slice bullets, which state a count AND list what they counted.
// ---------------------------------------------------------------------------

// traceBullet matches both bullet shapes the generator emits: the superseded-evidence list
// ("cited for N requirements: ...") and the no-evidence list ("N requirements: ...").
var traceBullet = regexp.MustCompile(`^-\s+\*\*([A-Za-z0-9]+)\*\*\s*—\s*(?:cited for\s+)?(\d+)\s+requirements?:\s*(.+)$`)

// evidenceBulletClaims checks each bullet's number against its own id list AND against the
// rows that name that slice. Both are needed: a bullet can be internally consistent and
// still describe a slice the table has since moved rows away from.
//
// It compares COUNTS, not sets. A bullet that swapped one id for another of a slice with
// the same size would pass here -- that direction is the sibling coverage gate's subject,
// which fails on any id in the document that the spec does not define.
func evidenceBulletClaims(doc string, tl traceTally) []traceClaim {
	var out []traceClaim
	for _, line := range strings.Split(doc, "\n") {
		m := traceBullet.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		slice, listed := m[1], 0
		stated, _ := strconv.Atoi(m[2])
		for _, id := range strings.Split(m[3], ",") {
			if strings.TrimSpace(id) != "" {
				listed++
			}
		}
		out = append(out,
			traceClaim{
				what:     fmt.Sprintf("the %s bullet's count against the ids it lists", slice),
				stated:   stated,
				computed: listed,
			},
			traceClaim{
				what:     fmt.Sprintf("the %s bullet's count against the rows naming %s", slice, slice),
				stated:   stated,
				computed: len(tl.perSlice[slice]),
			},
		)
	}
	return out
}
