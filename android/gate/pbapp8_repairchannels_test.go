package gate

// FAILING-FIRST (TDD RED, GG-5) for PB-APP-8 / PB-SYNC-1: the four REPAIR CHANNELS the phone
// asks about are the four the core can answer for.
//
// WHY THIS SEAM NEEDS A CHECK AND THE PAIRING ONE DID TOO. `App.StreamState` does NOT validate
// its argument. It falls through `streamStale` to `core.StreamStale(name)`, which answers for a
// map key that was never set -- so an unknown channel name reads "live", forever, silently. Its
// sibling `App.Resync` validates the same four names and FAILS CLOSED on a fifth, in its own
// words: "a caller that mistyped one of the four would see exactly what a working resync looks
// like". That reasoning applies with more force to the read than to the write, because the read
// is what a screen renders: a typo here does not refuse, it reports a healthy channel over a
// stream nobody is watching.
//
// AND KOTLIN CANNOT SEE THE CONSTANTS. `internal/phonecore.StreamJournal` and friends are Go
// identifiers behind a gomobile boundary that passes the channel as a bare string, so the four
// names must be spelled a second time on the Android side and nothing in either toolchain
// compares the two spellings. That is the same shape as PB-PAIR-5's terminal states, where the
// Go alphabet moved and the screen's did not with every check green -- and
// android/gate/pairingstates_test.go is the remedy this file is modelled on.
//
// IT READS CHECKED-IN SOURCE ONLY: no Android SDK, no JDK, no emulator, no handset.

import (
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

// repairChannelCount is the "cannot pass by measuring nothing" floor. PB-SYNC-1 declares four
// channels and `App.Resync`'s own refusal message names all four; a run that found fewer has
// stopped parsing one of the two sources and would report perfect parity between two short
// lists, or between two empty ones.
const repairChannelCount = 4

const phonecoreStreamFile = "internal/phonecore/snapshot.go"

// coreRepairChannels is every repair-channel string the phone core marks staleness for.
//
// Read from SOURCE rather than from a checked-in list, for corePairingStates' reason: a list
// would have to be edited by the same person adding the channel, which is exactly the step this
// control exists to stop depending on.
func coreRepairChannels(t *testing.T) []string {
	t.Helper()
	path := filepath.Join(repoRoot(t), filepath.FromSlash(phonecoreStreamFile))
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, nil, 0)
	if err != nil {
		t.Fatalf("PB-SYNC-1: parse %s: %v", mustRel(t, path), err)
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
			if !isRepairChannelConst(vs.Names[0].Name) {
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

// isRepairChannelConst is the naming rule internal/phonecore already follows: a channel
// constant is `Stream` followed by an UPPER-CASE letter. It is exported-name-shaped on purpose
// -- the unexported `kind*` discriminators in the same file are FRAME FAMILIES demuxed off the
// mailbox, which is a different alphabet with five members, and counting them would demand a
// row on this screen for `reconcile`.
func isRepairChannelConst(name string) bool {
	rest, ok := strings.CutPrefix(name, "Stream")
	return ok && rest != "" && rest[0] >= 'A' && rest[0] <= 'Z'
}

var repairChannelList = regexp.MustCompile(`(?s)REPAIR_CHANNELS\s*:\s*List<String>\s*=\s*\n?\s*listOf\(([^)]*)\)`)

// declaredRepairChannels is the channel alphabet the ADAPTER spends, read off FacadeBridge.
//
// IT IS READ FROM THE ONE DECLARATION AND NOT FROM THE FILE. A screen or a surface that typed a
// channel name of its own would be a second alphabet with no check over it, which is what
// TestPBAPP8_NoChannelNameIsTypedOutsideTheAdapter refuses.
func declaredRepairChannels(t *testing.T) []string {
	t.Helper()
	path := facadeBridgePath(t)
	src := stripKotlinComments(readFileOrFail(t, path, "PB-APP-8"))
	body := repairChannelList.FindStringSubmatch(src)
	if body == nil {
		t.Fatalf("PB-APP-8 / PB-SYNC-1: no `REPAIR_CHANNELS: List<String> = listOf(...)` in %s.\n"+
			"The four channels PB-SYNC-1 repairs cross the gomobile boundary as bare strings, so "+
			"the app has to spell them somewhere. Spelled at a call site they are unfenced: "+
			"`App.StreamState` does not validate its argument and answers \"live\" for a name it "+
			"has never seen, so a typo renders a healthy channel over a stream nobody is watching.",
			mustRel(t, path))
	}
	var out []string
	for _, m := range s24QuotedString.FindAllStringSubmatch(body[1], -1) {
		out = append(out, m[1])
	}
	sort.Strings(out)
	return out
}

// TestPBAPP8_TheChannelsTheScreenAsksAboutAreTheChannelsTheCoreRepairs is the set comparison.
func TestPBAPP8_TheChannelsTheScreenAsksAboutAreTheChannelsTheCoreRepairs(t *testing.T) {
	core := coreRepairChannels(t)
	if len(core) != repairChannelCount {
		t.Fatalf("PB-SYNC-1: %s declares %d repair channels (%v), want %d. The scan has stopped "+
			"reading the core, and a parity claim over a short list is a parity claim over nothing",
			mustRel(t, filepath.Join(repoRoot(t), filepath.FromSlash(phonecoreStreamFile))),
			len(core), core, repairChannelCount)
	}

	app := declaredRepairChannels(t)
	inCore := map[string]bool{}
	for _, c := range core {
		inCore[c] = true
	}
	inApp := map[string]bool{}
	for _, c := range app {
		inApp[c] = true
	}

	for _, c := range app {
		if !inCore[c] {
			t.Errorf("PB-APP-8: the app asks `App.StreamState(%q)` and the core repairs no such "+
				"channel.\nStreamState does not validate its argument -- it reads a map key that "+
				"was never set and answers \"live\" -- so this channel renders healthy on every "+
				"draw and can never render anything else.\nThe core's four: %s",
				c, strings.Join(core, ", "))
		}
	}
	for _, c := range core {
		if !inApp[c] {
			t.Errorf("PB-SYNC-1: the core marks and repairs the %q channel and the app never asks "+
				"about it.\nPB-APP-8's whole discipline is that staleness belongs to ONE stream "+
				"and never to the handset, so a channel nobody asks about is a hole the user is "+
				"not told of while the three beside it say they are fine.\nThe app's: %s",
				c, strings.Join(app, ", "))
		}
	}
}

// TestPBAPP8_NoChannelNameIsTypedOutsideTheAdapter is the half that keeps the check meaningful.
//
// A set comparison over ONE declaration says nothing about a call site that spelled its own
// channel, and that call site is the likely one: `streamView("journal")` reads perfectly well
// and is a second alphabet with no fence over it. FacadeBridge is where the screen models meet
// the bound facade by construction (its own KDoc), so it is the only file allowed to name one.
func TestPBAPP8_NoChannelNameIsTypedOutsideTheAdapter(t *testing.T) {
	core := coreRepairChannels(t)
	if len(core) != repairChannelCount {
		t.Fatalf("PB-SYNC-1: the core scan found %d channels, want %d; see the test above",
			len(core), repairChannelCount)
	}
	bridge := facadeBridgeFile

	for name, src := range s24ProductionKotlin(t) {
		if name == bridge {
			continue
		}
		code := stripKotlinComments(src)
		for _, c := range core {
			if !strings.Contains(code, `"`+c+`"`) {
				continue
			}
			t.Errorf("%s spells the repair channel %q.\nThe four channel names belong to "+
				"`FacadeBridge.REPAIR_CHANNELS` and nowhere else: spelled here they are outside "+
				"the one comparison that joins them to internal/phonecore, and `App.StreamState` "+
				"answers \"live\" for a name it does not know rather than refusing it.",
				name, c)
		}
	}
}
