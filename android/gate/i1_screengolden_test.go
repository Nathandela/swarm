package gate

// FAILING-FIRST (TDD RED, GG-5) for the JOIN slice I1's screen recording rests on: the fields the
// Robolectric suite renders are the fields the PRODUCTION bridge reads off the facade.
//
// WHY A THIRD FILE FOR THIS. `internal/skeleton/interaction_screen_golden_test.go` records what the
// real stack delivers; `TranscriptScreenGoldenTest` renders that recording. Both are necessary and
// together they still leave one hole, and it is the hole this package exists for: the Robolectric
// suite cannot construct `swarmmobile.App` (a gomobile class over Android-ABI .so files), so it
// builds its `InteractionItem`s from the golden BY HAND. That hand mapping is a second spelling of
// `FacadeBridge.transcript`, and nothing in either toolchain compares the two -- so a getter
// dropped from the bridge would leave the screen suite green over a field the app no longer reads.
//
// That is exactly PB-APP-8's repair channels and PB-PAIR-5's terminal states again: a value spelled
// on both sides of a boundary neither compiler crosses, where "the Go alphabet moved and the
// screen's did not with every check green". android/gate/pbapp8_repairchannels_test.go is the file
// this one is modelled on, and the remedy is the same -- set-compare the two spellings, read from
// SOURCE rather than from a list somebody would have to remember to edit, and fail in either
// direction.
//
// IT READS CHECKED-IN SOURCE ONLY: no Android SDK, no JDK, no emulator, no handset.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

const (
	i1FacadeBridgeFile = "android/app/src/main/kotlin/dev/swarm/phone/ui/FacadeBridge.kt"
	i1ScreenGoldenTest = "android/app/src/test/kotlin/dev/swarm/phone/ui/screens/TranscriptScreenGoldenTest.kt"
	i1ScreenGoldenFile = "internal/skeleton/testdata/i1-transcript-screen.golden.json"
)

// i1BoundFieldFloor is the "cannot pass by measuring nothing" floor. `TranscriptItem` binds ten
// fields the app could read and the bridge reads ten of them; a run that found one or two has
// stopped parsing one of the sources and would report perfect parity between two short lists.
const i1BoundFieldFloor = 8

// i1TranscriptGetters is every `item.getX()` the production bridge calls inside its `transcript`
// mapping, as the bound FIELD names (getSessionID -> SessionID).
//
// Scoped to that one function rather than to the file: `FacadeBridge` maps the roster, the journal
// and the push settings off other bound types with getters of their own, and a file-wide scan would
// fold `entry.getCursor()` from the journal in beside the transcript's.
func i1TranscriptGetters(t *testing.T) []string {
	t.Helper()
	src := kotlinCodeOnly(readRepoFileOrFail(t, i1FacadeBridgeFile))
	body := i1FunctionBody(t, src, "fun transcript(", i1FacadeBridgeFile)
	return i1SortedUnique(i1GetterPattern.FindAllStringSubmatch(body, -1))
}

// i1GoldenTestKeys is every golden key the Robolectric suite's own `itemFrom` mapping reads.
func i1GoldenTestKeys(t *testing.T) []string {
	t.Helper()
	src := kotlinCodeOnly(readRepoFileOrFail(t, i1ScreenGoldenTest))
	body := i1FunctionBody(t, src, "private fun itemFrom(", i1ScreenGoldenTest)
	return i1SortedUnique(i1JSONKeyPattern.FindAllStringSubmatch(body, -1))
}

var (
	// `item.getSessionID()` -- the receiver is named so a bare `getX()` on something else cannot
	// be counted.
	i1GetterPattern = regexp.MustCompile(`item\.get([A-Za-z0-9]+)\(\)`)
	// `o.getString("SessionID")`, `o.getLong("Cursor")`, `o.getBoolean("Resolved")`.
	i1JSONKeyPattern = regexp.MustCompile(`o\.get(?:String|Long|Int|Boolean)\("([A-Za-z0-9]+)"\)`)
)

// i1FunctionBody returns the source between the brace-balanced body of the declaration starting at
// marker. It is deliberately crude -- brace counting over comment-stripped source -- because the
// alternative is a Kotlin parser, and what is needed is "which lines belong to this function".
func i1FunctionBody(t *testing.T, src, marker, where string) string {
	t.Helper()
	start := strings.Index(src, marker)
	if start < 0 {
		t.Fatalf("slice I1: %s no longer declares %q. The two spellings this gate compares are "+
			"joined by that name; renaming it silently turns the comparison off", where, marker)
	}
	open := strings.Index(src[start:], "(")
	if open < 0 {
		t.Fatalf("slice I1: %s: %q has no parameter list", where, marker)
	}
	// Walk from the parameter list to the end of the declaration, tracking both bracket kinds:
	// the mapping is an expression body (`= InteractionItem(...)`) in one file and a block in the
	// other, so stopping at the first `}` or the first `)` would truncate one of them.
	depth := 0
	i := start + open
	for ; i < len(src); i++ {
		switch src[i] {
		case '(', '{':
			depth++
		case ')', '}':
			depth--
			if depth == 0 {
				// One balanced group closed. For an expression body the mapping is in the NEXT
				// group, so keep going until a group closes at the end of a statement.
				if j := i + 1; j < len(src) && (src[j] == '\n' || src[j] == '\r') {
					return src[start : i+1]
				}
			}
		}
	}
	return src[start:]
}

func i1SortedUnique(matches [][]string) []string {
	seen := map[string]bool{}
	var out []string
	for _, m := range matches {
		if len(m) < 2 || seen[m[1]] {
			continue
		}
		seen[m[1]] = true
		out = append(out, m[1])
	}
	sort.Strings(out)
	return out
}

func readRepoFileOrFail(t *testing.T, rel string) string {
	t.Helper()
	path := filepath.Join(repoRoot(t), filepath.FromSlash(rel))
	return readFileOrFail(t, path, "slice I1")
}

// TestI1_TheScreenSuiteReadsTheFieldsTheBridgeReads.
func TestI1_TheScreenSuiteReadsTheFieldsTheBridgeReads(t *testing.T) {
	bridge := i1TranscriptGetters(t)
	suite := i1GoldenTestKeys(t)

	if len(bridge) < i1BoundFieldFloor {
		t.Fatalf("slice I1: only %d bound getter(s) found in %s's transcript mapping (%v). The scan "+
			"has stopped seeing the mapping, and a comparison between two short lists reports parity "+
			"it did not measure", len(bridge), i1FacadeBridgeFile, bridge)
	}

	missing := i1Diff(bridge, suite)
	extra := i1Diff(suite, bridge)
	if len(missing) > 0 {
		t.Errorf("slice I1: %s reads %v off the facade and the recorded-bytes suite renders none of "+
			"them. A field the app reads in production and the golden suite does not is a field whose "+
			"rendering is asserted nowhere, which is what the recording was written to end.\n"+
			"  bridge: %v\n  suite:  %v", i1FacadeBridgeFile, missing, bridge, suite)
	}
	if len(extra) > 0 {
		t.Errorf("slice I1: the recorded-bytes suite renders %v, which %s does not read off the "+
			"facade. Either the bridge stopped reading a field the screen still draws -- so the app "+
			"draws nothing there while the suite stays green -- or the suite is asserting over a "+
			"field production never receives.\n  bridge: %v\n  suite:  %v",
			extra, i1FacadeBridgeFile, bridge, suite)
	}
}

// TestI1_TheRecordingCarriesEveryFieldBothSidesRead. The golden is the SUPERSET: it is
// `json.Marshal` of the whole bound struct, so it also carries `TurnID`, `TSUnixMs` and `Detail`,
// which the app deliberately does not read (no ruling exists for what a transcript timestamp reads
// as -- `InteractionItem`'s own KDoc records the refusal). What must hold is that nothing either
// side reads is ABSENT from the recording.
func TestI1_TheRecordingCarriesEveryFieldBothSidesRead(t *testing.T) {
	path := filepath.Join(repoRoot(t), filepath.FromSlash(i1ScreenGoldenFile))
	blob, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("slice I1: cannot read the pinned crossing %s: %v. It is written by "+
			"internal/skeleton/interaction_screen_golden_test.go -update-screen-golden and staged "+
			"onto the Robolectric classpath by android/app/build.gradle.kts", i1ScreenGoldenFile, err)
	}
	var golden struct {
		Corpus  string `json:"corpus"`
		Pending struct {
			Items     []map[string]json.RawMessage `json:"items"`
			Approvals []map[string]json.RawMessage `json:"approvals"`
		} `json:"pending"`
	}
	if err := json.Unmarshal(blob, &golden); err != nil {
		t.Fatalf("slice I1: %s does not parse: %v", i1ScreenGoldenFile, err)
	}
	if golden.Corpus == "" {
		t.Error("slice I1: the recording does not name the corpus it descends from, so a reader " +
			"cannot find the bytes it was taken from")
	}
	if len(golden.Pending.Items) == 0 || len(golden.Pending.Approvals) == 0 {
		t.Fatalf("slice I1: the recording holds %d item(s) and %d pending approval(s); a run with "+
			"either empty renders an empty conversation and asserts nothing",
			len(golden.Pending.Items), len(golden.Pending.Approvals))
	}

	present := golden.Pending.Items[0]
	for _, field := range i1TranscriptGetters(t) {
		if _, ok := present[field]; !ok {
			t.Errorf("slice I1: the recording carries no %q, but %s reads it off every item. The "+
				"golden is json.Marshal of the bound struct, so an absent key means the FACADE stopped "+
				"exporting the field the app still asks for", field, i1FacadeBridgeFile)
		}
	}
}

func i1Diff(from, minus []string) []string {
	in := map[string]bool{}
	for _, s := range minus {
		in[s] = true
	}
	var out []string
	for _, s := range from {
		if !in[s] {
			out = append(out, s)
		}
	}
	return out
}
