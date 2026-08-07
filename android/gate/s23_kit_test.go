package gate

// FAILING-FIRST (TDD RED, GG-5) for PB-DS-6 and PB-DS-7, plus the one PB-DS-5 fence ADR-007 B134
// assigns to the kit rather than to the resources.
//
//	PB-DS-6 "Every visual element is one factory in a single package, styled entirely from the
//	         theme ... Kit coverage is joined to the component inventory bidirectionally."
//	PB-DS-7 "One table, one row per component, each cell either a token, a documented derivation,
//	         or a named exception with its reason ... No cell is a bare hex."
//
// WHAT THIS FILE CAN CHECK AND WHAT IT CANNOT, stated first because the split is the same one
// PB-TOK-1 arrived at and it is load-bearing here too. This gate compares FILES: the kit's
// sources against the design source, the spacing ledger, the checked-in Group join and
// internal/design's derivation table. It cannot say what a component RESOLVES on a running
// resource table, and a value that is right in Kotlin can still be wrong once appcompat,
// camera-view and firebase have merged their resources over the app's. That half is
// PB-DS-10's, and it lives in app/src/test/.../ui/kit/.
//
// SCOPE. S23 builds the INBOX component set: the foundation plus the nine components the triage
// screen needs. The rest of PB-DS-7's 38 are not here and this gate does not pretend they are --
// s23Inbox below is the claim, one row per factory, and both directions are asserted so a
// factory cannot be added without a row and a row cannot survive its factory being deleted.
//
// ui/kit/Motion.kt IS DELIBERATELY OUT OF THE REVERSE DIRECTION. PB-DS-8 owns it, it is being
// written concurrently, and a gate that required every public function in the package to be one
// of this slice's components would fail the moment an animator landed -- turning a fence into a
// coordination problem between two agents. The reverse check is therefore scoped to the files
// s23Inbox names, which is the set this slice is responsible for.

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/Nathandela/swarm/internal/design"
)

const s23KitPackageDir = "dev/swarm/phone/ui/kit"

// s23ComponentsDoc is PB-DS-7's reviewable table -- the derivation for every component Substrate
// never specified. A kit component with no Substrate CSS rule cites a row in it, and the
// citation is checked, because "derived" with nowhere to look is indistinguishable from invented.
const s23ComponentsDoc = "docs/design/substrate-components.md"

// ---------------------------------------------------------------------------
// The claim: the Inbox component set.
// ---------------------------------------------------------------------------

// s23Component is one factory, and the design authority it answers to.
//
// Origin is a selector in the design source's SHARED structural block -- the Substrate-specified
// components, which need no derivation because the artifact draws them. Derived is a row in
// s23ComponentsDoc, for the parts Substrate never specified. A component may carry BOTH: the
// status dot is drawn by `.pdot` and its four-Group binding is B134's, which only the derivation
// table records.
type s23Component struct {
	Factory string
	File    string
	Origin  string
	Derived string
	Why     string
}

// s23Inbox is the S23 scope. Triage inbox first, because it is the root screen, it exercises the
// most components, and it is where the four-Group identity shows (PB-DS-9's own ordering).
var s23Inbox = []s23Component{
	{
		Factory: "textField",
		File:    "TextField.kt",
		Derived: "#9 Composer",
		Why: "the pairing code field and the launch form's three. Substrate draws no composer " +
			"and no form, so row 9 is the whole specification. It shares wellSurface with the " +
			"mono well because row 9 is explicit that the field INVERTS the mock's lighter fill " +
			"-- --p-well is the token for recessed input. Its placeholder is --p-ink2 and not " +
			"--p-ink3: on this surface the tertiary ink is 3.50:1, under the text floor, and a " +
			"hint IS the field's label on a surface with no XML layouts to carry one.",
	},
	{
		Factory: "monoWell",
		File:    "MonoWell.kt",
		Origin:  ".sheet2 .cmd",
		Why: "every mono block in the app is ONE component, which is row 18's own instruction -- " +
			"the pairing command line and the terminal peek are the same well with one ink " +
			"between them. The terminal variant is where tokens.json's terminal_peek.fg pin " +
			"finally reaches a pixel: PB-TOK-3 has enforced it against the JSON since S22 and no " +
			"Android code ever read it, so the phosphor green the skin is named for has never " +
			"rendered on a handset.",
	},
	{
		Factory: "pairingStep",
		File:    "PairingStepRow.kt",
		Derived: "#18 Pairing scaffold",
		Why: "one numbered step of the pairing instructions, and the component the screen the " +
			"owner opened had no way to build. agents-tracker-qx9m: they installed the " +
			"internal-testing build, found the pairing screen, and it gave them a bare text " +
			"field with no camera and no instructions -- a person was expected to already know " +
			"that a computer has to run `swarm remote pair` first. It is a KIT component and not " +
			"three views in the screen because s24_screens_test.go fences the screens package " +
			"against R.dimen., setTextAppearance and setTextColor, so a list built there would " +
			"have no gutter, no type and no ink AND the fence would pass -- the fence is what " +
			"stops a screen choosing, so the choosing has to happen here. Row 18 specifies the " +
			"body copy it is made of and the two steps it spends; what row 18 does not enumerate " +
			"is the ORDINAL, because the artifact draws one pairing step at a time and never " +
			"numbers them, so the ordinal takes the body's own style rather than a second one " +
			"derived for it.",
	},
	{
		Factory: "settingsRow",
		File:    "SettingsRow.kt",
		Derived: "#15 Settings row",
		Why: "the Settings screen's only structural element. Substrate's directions page has no " +
			"settings screen, so `.setrow` is the retired mock's class and row 15 is the whole " +
			"specification. Its surface is cardSurface rather than a second derivation of the " +
			"same four values -- §2's reuse rule, which is why the remaining components are " +
			"tractable at all.",
	},
	{
		Factory: "statusLabel",
		File:    "SettingsRow.kt",
		Derived: "#15 Settings row",
		Why: "row 15's OTHER trailing form: a state the row reports rather than a control that " +
			"changes it. Separate from settingsRow because the row would otherwise take two " +
			"mutually exclusive arguments and a caller could pass both. It is `--p-hero` and not " +
			"`--p-ok`: \"active\" is a liveness statement, and after B134 --p-ok carries " +
			"ReadyForReview.",
	},
	{
		Factory: "toggle",
		File:    "Toggle.kt",
		Derived: "#4 Toggle",
		Why: "row 15's OTHER trailing control, and the component Substrate never drew: the shared " +
			"block declares no `.toggle` rule at all, so row 4 is the whole specification -- two " +
			"track colours, a 24 dp thumb, an 18 dp travel, and the pill exception. Its off track " +
			"is the FIFTH entry in internal/design's derivation table and the first one whose " +
			"authority is this document rather than a color-mix the artifact itself writes, which " +
			"is why the share is consumed from that table and not re-derived here.",
	},
	{
		Factory: "ctaButton",
		File:    "CtaButton.kt",
		Origin:  ".acts2 button",
		Derived: "§4 In-card CTA pair",
		Why: "the approval sheet's three actions. All four rules are Substrate's OWN -- `.acts2 " +
			"button` carries the shape, the padding and the type, and `.a2-ok`, `.a2-no`, " +
			"`.a2-more` carry the three fills and inks -- so this is an origin, and §3 has no " +
			"numbered row for it precisely because the artifact draws it. The `§4 In-card CTA " +
			"pair` citation is for the one thing the artifact does NOT say: that the approve " +
			"variant drops its `--p-cta-fx` bloom inside a card, because the card clips it.",
	},
	{
		Factory: "navHeaderDrill",
		File:    "NavHeaderDrill.kt",
		Derived: "§4 Drill-down nav header",
		Why: "the header a screen BELOW a root screen carries, which is a different component " +
			"from `.pnav` rather than a variant of it: the artifact draws the root header and " +
			"nothing else, so §4 is the whole specification -- a chevron glyph and its label in " +
			"place of the display title, `Title.Sheet` in place of `Display.NavTitle`, and a " +
			"padding that is three different steps rather than `.pnav`'s two. It is a separate " +
			"file from NavHeader.kt for the reason liveCounter is a separate factory: sharing one " +
			"function between them would make the root header's 27 sp title and this one's 15.5 sp " +
			"a boolean, and a boolean is what a screen would then be choosing type with.",
	},
	{
		Factory: "activityRow",
		File:    "ActivityRow.kt",
		Derived: "#14 Activity row",
		Why: "the activity feed's only structural element, and the machine pane's audit log -- one " +
			"factory for both, which is why it takes a body and an optional emphasis rather than a " +
			"JournalRow. Substrate's demo phone renders the inbox and nothing else, so `.arow` is " +
			"the retired mock's class and row 14 is the whole specification. Its surface and its " +
			"padding are the session row's four values, spent through cardSurface -- §2's reuse " +
			"rule, and the reason the mock's radius 12 and 11/13 padding are not here. Row 14's " +
			"one correction to the mock is that the timestamp column is WRAP-CONTENT and not a " +
			"fixed 52 dp, because a fixed column clips at the 1.3x font scale PB-DS-12 requires. " +
			"That column is also what lets the timestamp be absent at no cost, which it has to be: " +
			"swarmmobile.JournalEntry carries no time at all.",
	},
	{
		Factory: "toast",
		File:    "Toast.kt",
		Derived: "#1 Toast",
		Why: "the one sentence the app says about something that has just happened, and the " +
			"component whose absence left every press answer in a persistent text line -- two of " +
			"which are children of PhoneSurface's unrecomposed column, visible only at the " +
			"bottom of the Inbox tab. Substrate draws no `.toast` rule at all (it is the retired " +
			"mock's class), so row 1 is the whole specification: `--p-elev` OPAQUE rather than " +
			"the card fill every other block in this kit takes, because a toast floats over the " +
			"screen and one ladder step lighter is the only elevation a skin with no drop " +
			"shadows has. Its mono suffix is a SPAN and not a second view, which is the mock's " +
			"own template (`msg + \" \" + <span class=\"m\">mono</span>`).",
	},
	{
		Factory: "readOnlyNote",
		File:    "ReadOnlyNote.kt",
		Derived: "#22 Read-only note",
		Why: "the sentence under the terminal peek that says the snapshot cannot be typed into. " +
			"Substrate draws no `.ro-note` rule -- it is the retired mock's class -- so row 22 is " +
			"the whole specification. Its `[Take control]` is deliberately NOT part of this " +
			"factory: row 22 turns that inline span into a standalone tertiary button, which is " +
			"`ctaButton(kind = MORE)` unchanged, and building a second one inside this note would " +
			"be the copy of `.a2-more` the reuse rule exists to prevent.",
	},
	{
		Factory: "emptyState",
		File:    "EmptyState.kt",
		Derived: "#8 Empty state",
		Why: "PB-DS-9's most-argued clause. An empty section is still a section and says so -- " +
			"dropping it is the obvious implementation and it is wrong for a triage surface, " +
			"because the sections then move under the user and \"nothing is waiting on me\" " +
			"becomes indistinguishable from \"that section scrolled away\". A heading over " +
			"nothing is the same defect wearing a heading. Substrate never drew this block; the " +
			"derivation table specifies it and row 8 says so.",
	},
	{
		Factory: "machineRow",
		File:    "MachineRow.kt",
		Derived: "#11 Machine row",
		Why: "the machines screen's only row, and a COMPONENT rather than a reuse. Its card is " +
			"`cardSurface` -- §2's reuse rule, the same call the settings row and the activity row " +
			"make -- but its SHAPE is its own: a leading mark, a name, a trailing mono identifier " +
			"on one line, and a meta line beneath, at `space_12` x `space_14` rather than " +
			"`.prow`'s `space_10` x `space_12`. `sessionRow` is the near miss and it is a miss in " +
			"the one place that matters: it builds its leading mark by calling statusDot with a " +
			"`status.Group`, and a machine's reachability is not one. A reuse justified by " +
			"identical pixels is not a reuse justified by a compatible seam; this row passed the " +
			"first test and failed the second.",
	},
	{
		Factory: "killSwitchPanel",
		File:    "KillSwitchPanel.kt",
		Derived: "#12 Kill-switch panel",
		Why: "the charged container that reports the daemon-side switch. It is the one component " +
			"in the kit with a BORDER AND NO FILL -- the ground shows through, as the mock drew " +
			"it -- and its border is `.prow.attention`'s recipe with `--p-err` substituted for " +
			"`--p-att`, which is row 12's own sentence and is why it spends the SAME 36% share " +
			"internal/design declares for the attention row rather than a sixth derivation. " +
			"Row 12's trailing control is deleted by its own 2026-08-01 amendment: " +
			"`App.KillSwitchEngaged` is read only by design, `handleRemoteSetControl` refuses the " +
			"remote tier before the backend is consulted, and a toggle here is a control that " +
			"cannot act. So this factory HAS no trailing parameter -- the absence is in the " +
			"signature, where a later caller cannot supply one by accident.",
	},
	{
		Factory: "denyChip",
		File:    "DenyChip.kt",
		Derived: "#13 Paired-device row",
		Why: "Revoke, which row 13 specifies as \"the `.a2-no` treatment at chip metrics\": the " +
			"deny button's fill and ink at the scope chip's radius, padding and label style. " +
			"NEITHER EXISTING FACTORY RENDERS IT. `ctaButton(DENY)` has the colours and the wrong " +
			"metrics -- `--p-btn-r` 9, `space_12`, `Label.Button` against row 13's `--p-chip-r` 8, " +
			"`space_8` x `space_10`, `Label.Chip` -- and `filterChip` has the metrics and hardcodes " +
			"its own two fills. This is not a second denial idiom, which §2 forbids: it is the ONE " +
			"tinted-fill denial at a second size, spending Kit.denyFill and pillSurface rather " +
			"than restating either.",
	},
	{
		Factory: "scanReticle",
		File:    "ScanReticle.kt",
		Derived: "§4 Scanner reticle",
		Why: "the framing square over the QR preview -- the thing that tells a person where to " +
			"point the phone, which the pairing screen has never had. It is a DRAWABLE and not a " +
			"view, and that is the whole of its safety argument rather than a style: a view laid " +
			"over a live camera preview is a thing that can be made clickable, and PB-SEC-12 " +
			"clause 1 exists because a clickable thing over content the user is reading is the " +
			"tapjacking surface. A foreground drawable cannot take a touch at all, and it cannot " +
			"outlive the preview it is the foreground OF -- `QrScanner.stop()` sets that view " +
			"GONE, so the reticle goes with it rather than hanging green over a dead camera. " +
			"Substrate draws no scanner, and row 6's `.qr` is the code TILE rather than the " +
			"viewfinder pointed at one, so §4 is the whole specification -- which is also where " +
			"its three numbers come from, all cited by field.",
	},
	{
		Factory: "focusRing",
		File:    "FocusRing.kt",
		Derived: "#23 Focus ring",
		Why: "PB-DS-12's other unenforced clause, and the one component in this kit that is not a " +
			"thing on screen but a treatment applied to one. It had no product origin at all -- " +
			"PB-DS-7 flagged it as a standing gap, because the only ring anyone had drawn was the " +
			"documentation page's own chrome accent, which is a fact about a documentation page. " +
			"ADR-009 D3 closes the gap: 2 dp `--p-hero`. Obsidian's accent means YOU -- needs-you, " +
			"CTA, focus, live, brand -- so focus is the fifth thing the one accent says rather " +
			"than a sixth meaning bolted onto a fill; §1.1's amendment records that Substrate's " +
			"rejection of hero rested on hero meaning SELECTED, which is the premise that went. " +
			"Still rejected: the neutral pair `--p-ink` and `--p-ink2` (over a warm ladder whose " +
			"hairline is `--p-hair`, a ring in either reads as a heavier border) and the three " +
			"status tokens that are not the accent (they mean state). " +
			"It is a FOREGROUND because every focusable in this kit has already spent its " +
			"background on a surface, and a ring merged into each of those would be five copies " +
			"of one rule.",
	},
	{
		Factory: "grainOverlay",
		File:    "Grain.kt",
		Derived: "#21 Grain overlay",
		Why: "PB-DS-5's other unmet count, and the one component in this kit whose design source " +
			"is a FILE. `feTurbulence` output is implementation-defined, so row 21 makes the noise " +
			"an asset rather than a colour -- pre-rendered once at 140x140 and checked in -- and " +
			"the requirement recorded \"no grain raster exists\" for two months while every gate " +
			"was green, because nothing anywhere asked. It is a component and not a treatment for " +
			"`focusRing`'s reason inverted: the ring is applied TO something, and this is a " +
			"surface of its own that happens to lie over everything. It is a foreground at its " +
			"call site because row 21 says non-interactive and a foreground cannot take a touch, " +
			"which is `scanReticle`'s argument over a camera preview.",
	},
	{
		Factory: "presenceDot",
		File:    "PresenceDot.kt",
		Derived: "#11 Machine row",
		Why: "the machine row's 7 dp mark: row 11's `online --p-ok, offline --p-ink3`, flat in " +
			"both states because a reachable machine is not a running agent. It is a SEPARATE " +
			"FACTORY FROM statusDot rather than a fifth key in the Group table, and the reason is " +
			"that the cheap implementation renders correctly. Those two tokens are the ones " +
			"`ready_for_review` and `completed` already carry, so " +
			"`statusDot(context, if (online) \"ready_for_review\" else \"completed\")` paints this " +
			"mark pixel-for-pixel and is the phone INVENTING a status.Group for a machine. " +
			"TestPBDS7_TheStatusDotBindingIsTheCheckedInMapping refuses any bound name absent from " +
			"android/group-tokens.tsv precisely so that cannot be done quietly: the Group is " +
			"derived once on the server and rendered verbatim (PB-TOK-8), and machine presence is " +
			"not one -- it is `App.Presence`, the RELAY's opinion, and it has three values where a " +
			"Group has four. So the drawable, the 7 dp and the flat treatment are shared with " +
			"statusDot and the BINDING is not: this one takes a boolean, and the Group fence stays " +
			"exactly as strict as it was.",
	},
	{
		Factory: "statusDot",
		File:    "StatusDot.kt",
		Origin:  ".pdot",
		Derived: "§4 Status dots, B134 mapping",
		Why: "the 7dp mark and its glow are Substrate's; WHICH Group takes which colour, and " +
			"which two of the four glow at all, is B134's rebinding and exists nowhere else",
	},
	{
		Factory: "sessionRow",
		File:    "SessionRow.kt",
		Origin:  ".prow",
		Why:     "the triage row, with the .prow.attention variant its rail and warmed border",
	},
	{
		Factory: "sessionList",
		File:    "SessionRow.kt",
		Origin:  ".prows",
		Why: "the rows' container carries the 12dp side padding and the 7dp gap. Without it a " +
			"screen types both, which is the PB-DS-6 violation the kit exists to prevent",
	},
	{
		Factory: "workingBar",
		File:    "WorkingBar.kt",
		Origin:  ".workbar",
		Why:     "Substrate's Working affordance is this static gradient plus the dot glow, no pulse",
	},
	{
		Factory: "filterChip",
		File:    "FilterChip.kt",
		Origin:  ".chip",
		Why:     "scope bar chip; .chip.on is the selected variant and .chip .pd the presence dot",
	},
	{
		Factory: "chipRow",
		File:    "FilterChip.kt",
		Origin:  ".chips",
		Why:     "same reason as sessionList: the gap and the side padding belong to the container",
	},
	{
		Factory: "sectionLabel",
		File:    "SectionLabel.kt",
		Origin:  ".plabel",
		Why:     "the Group heading. Uppercase is the component's, per text-transform",
	},
	{
		Factory: "navHeader",
		File:    "NavHeader.kt",
		Origin:  ".pnav",
		Why:     "the root-screen header: big title on the left, live counter pushed right",
	},
	{
		Factory: "liveCounter",
		File:    "NavHeader.kt",
		Origin:  ".pnav .live",
		Why:     "a separate factory because §1.4 ships it beside the badge, counting a different thing",
	},
	{
		Factory: "tabBar",
		File:    "TabBar.kt",
		Origin:  ".ptabs",
		Why:     "the bar, its top rule, its translucency and its four items",
	},
	{
		Factory: "badge",
		File:    "Badge.kt",
		Derived: "#3 Badge",
		Why: "Substrate's artifact has no badge at all -- it uses the live counter instead. " +
			"§1.4 ships both and recolours this one from the mock's retired red to --p-att",
	},
}

// ---------------------------------------------------------------------------
// Reading the kit.
// ---------------------------------------------------------------------------

func s23KitRoot(t *testing.T) string {
	return filepath.Join(kotlinMainRoot(t), filepath.FromSlash(s23KitPackageDir))
}

// s23KitSources returns the kit's files, base name -> RAW source (comments intact).
//
// Raw, because two of the checks below read the machine-parsed `origin:` annotations, which are
// comments. The checks that must not be satisfiable BY a comment -- the elevation fence and the
// colour-literal fence -- strip them with kotlinCodeOnly at their own call site, for the reason
// that helper records.
func s23KitSources(t *testing.T) map[string]string {
	t.Helper()
	root := s23KitRoot(t)
	if !exists(root) {
		t.Fatalf("PB-DS-6: %s does not exist. The requirement's first sentence is that every "+
			"visual element is one factory in a SINGLE PACKAGE styled entirely from the theme; "+
			"today the app's three surface files each build their own views inline, which is how "+
			"24 derived component specs become copy-paste and drift on first edit.",
			mustRel(t, root))
	}
	out := map[string]string{}
	for _, path := range kotlinFiles(t, root) {
		// DIRECT CHILDREN ONLY, and the omission is load-bearing rather than tidy. kotlinFiles
		// RECURSES, and this map is keyed by BASENAME -- so a file at ui/kit/zz/Badge.kt used to
		// overwrite the real ui/kit/Badge.kt in this map and take its place in every scan. That is
		// not a missed file, it is an EVICTED one: a clean copy of Badge.kt in a subdirectory made
		// the checked-in Badge.kt invisible to the whole gate, and `minWidth = 21` in the real file
		// then passed the complete lane. Measured, not reasoned about.
		//
		// The kit is ONE Kotlin package, so a subdirectory is a different package and not "the
		// kit" at all. Files below the root are collected by s23NestedKitFiles and refused by
		// TestPBDS6_EveryKitFileIsClaimedByAFence rather than being silently read in here.
		if filepath.Dir(path) != root {
			continue
		}
		out[filepath.Base(path)] = readFileOrFail(t, path, "PB-DS-6")
	}
	if len(out) == 0 {
		t.Fatalf("PB-DS-6: %s contains no Kotlin; every assertion below would iterate zero times",
			mustRel(t, root))
	}
	return out
}

// s23NestedKitFiles returns every .kt file BELOW the kit root, as root-relative slash paths.
//
// These are the files s23KitSources refuses to read, for the reason it gives. They are a fault
// rather than a silence: see TestPBDS6_EveryKitFileIsClaimedByAFence.
func s23NestedKitFiles(t *testing.T) []string {
	t.Helper()
	root := s23KitRoot(t)
	var out []string
	for _, path := range kotlinFiles(t, root) {
		if filepath.Dir(path) == root {
			continue
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			rel = path
		}
		out = append(out, filepath.ToSlash(rel))
	}
	sort.Strings(out)
	return out
}

// s23OwnedFiles is the set of kit sources this slice fences: the foundation plus the files
// s23Inbox names. Motion.kt is PB-DS-8's, for the reason the package comment gives.
func s23OwnedFiles() map[string]bool {
	owned := map[string]bool{"Kit.kt": true, "ColorMix.kt": true, "Surfaces.kt": true}
	for _, c := range s23Inbox {
		owned[c.File] = true
	}
	return owned
}

// s23MotionFile is the one kit source this slice does NOT own. PB-DS-8 fences it, in
// s23_motion_test.go, and the package comment says why the split exists.
const s23MotionFile = "Motion.kt"

// s23ClaimedFiles is every file in the kit package that some fence reads, and which fence reads it.
//
// "THE KIT" MEANT ELEVEN OF TWELVE FILES, and until this existed the difference was a hole wide
// enough to drive the whole slice through. Every scan in this file iterates s23OwnedFiles, which is
// a FIXED LIST OF NAMES; the motion fence iterates the production tree but judges only animation
// vocabulary. So a TWELFTH file dropped into ui/kit/ was read, for accounting purposes, by nothing.
// The fourth review wrote one:
//
//	internal const val PROBE_PAD_PX = 37
//	internal const val PROBE_HEX = 0x1F4
//	internal fun probeBlink(view: View, handler: Handler) {
//	    view.layoutParams = LinearLayout.LayoutParams(PROBE_PAD_PX, PROBE_HEX)
//	    handler.postDelayed({ probeBlink(view, handler) }, 16L)
//	}
//
// A raw pixel count in a layout param, a hex literal, and a decorative animation loop -- the three
// things PB-DS-6, PB-DS-7 and PB-DS-8 respectively exist to forbid, in one file, and all twenty-one
// assertions in the android/gate lane reported ok. The fence's SUBJECT was enumerated, so it could
// not see what had been added to the package it was fencing.
//
// The repair is that the enumeration is now itself checked against the directory, in both
// directions. A new file must be claimed before it can be added, which makes entering the kit a
// reviewed act rather than a silent one.
func s23ClaimedFiles() map[string]string {
	claimed := map[string]string{}
	for file := range s23OwnedFiles() {
		claimed[file] = "PB-DS-6/PB-DS-7, by s23OwnedFiles in this file"
	}
	claimed[s23MotionFile] = "PB-DS-8, by s23_motion_test.go's exemption"
	return claimed
}

// s23TopLevelFun matches a top-level factory declaration. Indented `fun` is a method and is not
// part of the kit's surface.
var s23TopLevelFun = regexp.MustCompile(`(?m)^(?:internal\s+)?fun\s+([A-Za-z][A-Za-z0-9]*)\s*\(`)

// s23AnnotationLine reads one machine-read annotation out of a comment, whatever comment shape
// it is written in: `origin: .prow`, ` * origin: .prow`, `// origin: .prow`.
var s23AnnotationLine = regexp.MustCompile(`^(?:\s|\*|/)*(origin|derived):\s*(.+?)\s*(?:\*/)?\s*$`)

// s23Annotations returns every annotation in a source, kind -> values.
func s23Annotations(src string) map[string][]string {
	out := map[string][]string{}
	for _, line := range strings.Split(src, "\n") {
		if m := s23AnnotationLine.FindStringSubmatch(line); m != nil {
			out[m[1]] = append(out[m[1]], m[2])
		}
	}
	return out
}

// ---------------------------------------------------------------------------
// PB-DS-6: the kit is the component inventory, in both directions.
// ---------------------------------------------------------------------------

func TestPBDS6_EveryInboxComponentIsAKitFactory(t *testing.T) {
	sources := s23KitSources(t)
	css := s22bSharedCSS(t)

	for _, c := range s23Inbox {
		src, ok := sources[c.File]
		if !ok {
			t.Errorf("PB-DS-6: the kit has no %s, which is where %s() lives.\n\t%s",
				c.File, c.Factory, c.Why)
			continue
		}
		if !s23DeclaresFun(src, c.Factory) {
			t.Errorf("PB-DS-6: %s declares no top-level `fun %s(`. The requirement is one factory "+
				"per visual element; a component that exists only as inline view-building inside a "+
				"screen is the copy-paste this requirement names.\n\t%s", c.File, c.Factory, c.Why)
			continue
		}

		annotations := s23Annotations(src)
		if c.Origin != "" {
			if _, declared := css[c.Origin]; !declared {
				t.Errorf("PB-DS-6: %s cites `%s` as its design origin, and the shared Substrate "+
					"block declares no such rule. An origin nothing can be read from is a "+
					"component whose values came from somewhere else.", c.File, c.Origin)
			}
			if !s23Contains(annotations["origin"], c.Origin) {
				t.Errorf("PB-DS-6: %s carries no `origin: %s` annotation. The annotation is the "+
					"join -- it is what lets this gate and the Robolectric suite compute every "+
					"expected value from the DESIGN rather than from the implementation they are "+
					"checking, which is the arrangement type.xml's `<!-- origin: -->` comments "+
					"already established.", c.File, c.Origin)
			}
		}
		if c.Derived != "" {
			want := s23ComponentsDoc + " " + c.Derived
			if !s23Contains(annotations["derived"], want) {
				t.Errorf("PB-DS-6: %s carries no `derived: %s` annotation. A component Substrate "+
					"never specified must cite the row that specifies it; a derivation with "+
					"nowhere to look is indistinguishable from an invention.", c.File, want)
			}
		}
		if c.Origin == "" && c.Derived == "" {
			t.Errorf("PB-DS-6: the s23Inbox row for %s names neither an origin nor a derivation, "+
				"so nothing in this gate constrains what it paints", c.Factory)
		}
	}
}

// TestPBDS6_EveryKitFactoryIsAnInboxComponent is the reverse direction, and it is the one that
// makes "bidirectional" mean something: a factory nobody declared is a component that entered
// the kit without passing through the inventory, which is precisely how "single origin" decayed
// into "origin plus a few extras" the first time (PB-TOK-5).
//
// Scoped to the files s23Inbox names -- see the package comment on Motion.kt.
func TestPBDS6_EveryKitFactoryIsAnInboxComponent(t *testing.T) {
	sources := s23KitSources(t)

	declared := map[string]bool{}
	owned := map[string]bool{}
	for _, c := range s23Inbox {
		declared[c.Factory] = true
		owned[c.File] = true
	}

	found := 0
	for file, src := range sources {
		if !owned[file] {
			continue
		}
		for _, m := range s23TopLevelFun.FindAllStringSubmatch(kotlinCodeOnly(src), -1) {
			found++
			if !declared[m[1]] {
				t.Errorf("PB-DS-6: %s declares the factory %s(), which no s23Inbox row names. "+
					"Either it is a component and the inventory must say so -- with its design "+
					"origin, which is what makes it checkable -- or it is a helper and belongs "+
					"behind `private`.", file, m[1])
			}
		}
	}
	if found == 0 {
		t.Fatalf("PB-DS-6: no top-level factories found in the files s23Inbox names; this " +
			"direction passed over an empty set and says nothing")
	}
}

// TestPBDS6_EveryKitFileIsClaimedByAFence makes "the kit" mean the package rather than a list.
//
// See s23ClaimedFiles for the injection that made this necessary and what it walked past. The two
// directions are not the same assertion:
//
//   - FORWARD, a file on disk that no fence claims. That is the hole: everything this gate says
//     about colours, dimensions, metrics and literals is scoped to s23OwnedFiles, so an unclaimed
//     file is one the accounting has never looked at. It is checked FIRST because every other
//     assertion in this file is conditional on it.
//   - REVERSE, a claim naming a file that is not there. Without it the list rots quietly: a
//     component deleted in a refactor leaves its name in s23OwnedFiles, the scans skip a file that
//     does not exist, and the forward direction still passes because the directory is a subset.
func TestPBDS6_EveryKitFileIsClaimedByAFence(t *testing.T) {
	sources := s23KitSources(t)
	claimed := s23ClaimedFiles()

	for _, file := range s23SortedKeys(sources) {
		if _, ok := claimed[file]; ok {
			continue
		}
		t.Errorf("PB-DS-6: %s/%s is in the kit package and no fence claims it. Every scan in this "+
			"gate -- the colour fence, the raw-dimension fence, the metric join, the literal "+
			"accounting -- iterates s23OwnedFiles, so a file nothing claims is a file where a raw "+
			"pixel count, a hex literal and a decorative animation loop all pass unread; that is "+
			"the injection s23ClaimedFiles quotes, and it defeated this entire lane. Add the file "+
			"to s23Inbox with its design origin if it is a component, to s23OwnedFiles if it is "+
			"foundation, or fence it from another slice and claim it here.", s23KitPackageDir, file)
	}

	for _, file := range s23SortedKeys(claimed) {
		if _, ok := sources[file]; ok {
			continue
		}
		t.Errorf("PB-DS-6: %s claims %s/%s and no such file exists. A claim over nothing makes the "+
			"forward direction above pass by having less to check: the scans skip the missing name, "+
			"report no fault, and the inventory goes on describing a kit that is not there.",
			claimed[file], s23KitPackageDir, file)
	}

	// AND NOTHING MAY SIT BELOW THE ROOT. Neither direction above can see a nested file: the
	// forward one iterates a map keyed by basename, and a nested file whose basename is already
	// claimed does not add a key -- it OVERWRITES one. `ui/kit/zz/Badge.kt` holding a clean copy
	// evicted the real Badge.kt from every scan in this gate, and `minWidth = 21` in the
	// checked-in file then passed the complete lane. So the eviction is refused at the source
	// (s23KitSources reads direct children only) and the nested file is a fault here.
	for _, rel := range s23NestedKitFiles(t) {
		t.Errorf("PB-DS-6: %s/%s sits below the kit root. The kit is ONE Kotlin package, so a "+
			"subdirectory is a different package that no fence in this gate reads -- and because "+
			"the kit is scanned by BASENAME, a nested file named after a real one used to take its "+
			"place in every scan rather than merely being skipped. Move it up into the package and "+
			"claim it, or fence its package from its own slice.", s23KitPackageDir, rel)
	}
}

// TestPBDS7_EveryDerivationCitationResolvesToARow follows the `derived:` annotations into
// PB-DS-7's table and requires the row to be there.
func TestPBDS7_EveryDerivationCitationResolvesToARow(t *testing.T) {
	doc := readFileOrFail(t, filepath.Join(repoRoot(t), filepath.FromSlash(s23ComponentsDoc)), "PB-DS-7")
	sources := s23KitSources(t)

	cited := 0
	for file, src := range sources {
		for _, annotation := range s23Annotations(src)["derived"] {
			// A constant's citation carries a `{ field }` naming the cell its value comes from;
			// a component's names the row and stops there. Both resolve to the same row.
			raw, _ := s23ParseDerived(annotation)
			ref := strings.TrimSpace(strings.TrimPrefix(raw, s23ComponentsDoc))
			if ref == raw {
				t.Errorf("PB-DS-7: %s cites %q, which does not name %s. The derivation table is "+
					"the only place a non-Substrate component is specified; a citation of "+
					"anything else is a value with no authority behind it.", file, raw, s23ComponentsDoc)
				continue
			}
			cited++
			if !s23RowExists(doc, ref) {
				t.Errorf("PB-DS-7: %s cites `%s`, and no such row exists in %s. Either the row "+
					"was renamed and the component now paints to a specification nobody can "+
					"find, or the citation was written from memory.", file, ref, s23ComponentsDoc)
			}
		}
	}
	if cited == 0 {
		t.Errorf("PB-DS-7: no `derived:` citation found anywhere in the kit, yet %d component(s) "+
			"in s23Inbox are specified only by the derivation table. This check passed over an "+
			"empty set.", s23DerivedCount())
	}
}

// s23FindRow returns the table row `#3 Badge` or `§4 Status dots, B134 mapping` names.
//
// The two forms are different tables. §3 is numbered, one row per PB-DS-7 component; §4's
// "adjacent derivations" are not numbered because they are not in PB-DS-7's list of 24 -- they
// are the things the eight screens cannot be built without. Both are found by their leading
// cell, which is the cell that identifies the row.
//
// It returns the ROW rather than a boolean because a citation that resolves to a row and stops
// there checks only that someone wrote a heading: the values in the row are what the constants
// citing it have to agree with.
func s23FindRow(doc, ref string) (string, bool) {
	marker := ""
	switch {
	case s23IsNumberedRef(ref):
		n, name, _ := strings.Cut(strings.TrimPrefix(ref, "#"), " ")
		marker = "| " + n + " | " + name + " |"
	case strings.HasPrefix(ref, "§4 "):
		marker = "| " + strings.TrimPrefix(ref, "§4 ") + " |"
	default:
		return "", false
	}
	// A `§4 Name` REFERENCE IS SEARCHED IN §4 ONLY, and this is not tidiness.
	//
	// §4's marker is `| Name |`, and a §3 row reads `| 14 | Name | ...` -- which CONTAINS it. So an
	// unscoped search resolved every `§4 Name` citation against the numbered row of the same name
	// whenever one existed, in a different section, with different values. It was live: after the
	// duplicate §4 rows were deleted, `ActivityRow.kt` went on citing `§4 Activity row` and this
	// reader kept answering with §3 row 14. The citation test reported nothing, because it asks
	// whether a row was FOUND and one was.
	//
	// PresenceDot.kt is the only reason it was noticed at all -- `Machine presence dot` had no
	// numbered twin, so that one citation failed while its neighbour passed for the wrong reason.
	// A fence that resolves an ambiguous reference by taking the first hit reports agreement
	// between a component and a row that nobody chose.
	body := doc
	if strings.HasPrefix(ref, "§4 ") {
		start := strings.Index(doc, s23Section4Heading)
		if start < 0 {
			return "", false
		}
		body = doc[start:]
		if end := strings.Index(body, "\n## "); end >= 0 {
			body = body[:end]
		}
	}

	// AMBIGUITY IS A MISS, NOT A CHOICE. Two rows matching one marker means the component's
	// specification is in two places and this reader cannot say which one it paints to; returning
	// either is how a value edited in one row and not the other stays green.
	var found string
	hits := 0
	for _, line := range strings.Split(body, "\n") {
		if strings.Contains(line, marker) {
			hits++
			found = line
		}
	}
	if hits != 1 {
		return "", false
	}
	return found, true
}

// s23Section4Heading anchors the scoped search above. It is the literal heading, so renaming the
// section fails every §4 citation loudly rather than widening the search back to the whole file.
const s23Section4Heading = "## 4. Adjacent derivations the same screens need"

func s23IsNumberedRef(ref string) bool {
	n, _, ok := strings.Cut(strings.TrimPrefix(ref, "#"), " ")
	return ok && s23IsNumber(n)
}

func s23RowExists(doc, ref string) bool {
	_, ok := s23FindRow(doc, ref)
	return ok
}

// s23DocMetric reads one named number out of a derivation-table row: `height 16` for the field
// `height`.
//
// EVERY OCCURRENCE MUST AGREE. A row is prose in a table cell and a value can be restated in it
// ("`--p-chip-r` 8 = half the 16 dp height"); taking the first match would make the check depend
// on the order someone wrote a sentence in, and taking any match would let a contradiction pass.
func s23DocMetric(row, field string) (float64, error) {
	if field == s23MinTargetField {
		return s23DocMinTarget(row)
	}
	// BOTH ORDERS, because the table writes labelled values both ways and the reader has no
	// business caring which: row 3 says "height 16" in one cell and "the 16 dp height" in another,
	// and row 23 says "2 dp stroke" and nothing else. Reading only `field N` made the second order
	// invisible, so a value the document states plainly could not be cited at all -- the same
	// defect the touch-target floor had, in the other direction. Both are still ANCHORED on the
	// field name, so this stays a labelled-value reader rather than a number-finder, and the
	// every-occurrence-agrees rule below now spans the two orders as well.
	label := regexp.QuoteMeta(field)
	re, err := regexp.Compile(`\b` + label + `\s+([0-9]*\.?[0-9]+)\b`)
	if err != nil {
		return 0, fmt.Errorf("%q is not a field name", field)
	}
	before, err := regexp.Compile(`\b([0-9]*\.?[0-9]+)\s*(?:dp\s+)?` + label + `\b`)
	if err != nil {
		return 0, fmt.Errorf("%q is not a field name", field)
	}
	matches := append(re.FindAllStringSubmatch(row, -1), before.FindAllStringSubmatch(row, -1)...)
	if matches == nil {
		return 0, fmt.Errorf("the row states no `%s <number>`", field)
	}
	var first float64
	for i, m := range matches {
		v, err := strconv.ParseFloat(m[1], 64)
		if err != nil {
			return 0, err
		}
		if i == 0 {
			first = v
			continue
		}
		if v != first {
			return 0, fmt.Errorf("the row states %s twice and disagrees with itself: %g and %g",
				field, first, v)
		}
	}
	return first, nil
}

// s23MinTargetField is the pseudo-field a constant cites to read a row's touch-target floor:
// `derived: ... #4 Toggle { min-target }`.
//
// IT IS NOT A CELL ENTRY, which is why it is not read by the `field <number>` rule. Every other
// citation names something the row writes as a labelled value -- `height 16`, `thumb 24` -- and a
// minimum target is written as an INEQUALITY over a word that moves: ">=48", "touch target 48",
// "48 dp target", "min 48". A reader keyed on the label would have to be told which of those four
// the row happened to use.
const s23MinTargetField = "min-target"

// s23MinTargetForms are the spellings the derivation table uses for PB-DS-12's touch-target floor.
//
// THE FOUR ARE THE DOCUMENT'S, NOT A GENERALISATION OF IT. Row 4 writes "touch target >=48 with
// the visual unchanged", row 9 "visual height 36, touch target 48" and "Both 48 dp targets", row 10
// "targets >=48", row 13 "48 dp target", row 15 "one >=48 dp target", row 22 "padding `space_12`,
// min 48", §4 "48 dp target". Two of those put the number BEFORE the word and two after, one hides
// it behind a `>=` and one behind the word `min` -- which is the whole reason the metric reader
// could not see any of them and the floor was asserted in prose and checked nowhere.
//
// WHY A LIST RATHER THAN ONE WIDE PATTERN. A pattern loose enough to catch a bare number near the
// word "target" also catches "the mock's fixed 52 dp" three cells away, and a reader that returns
// the wrong number is worse than one that returns none: the constant would be checked against a
// value nobody stated. Each form here is anchored on the word that makes the number a target.
var s23MinTargetForms = []*regexp.Regexp{
	// "touch target >=48", "touch target 48", "targets >=48", "one >=48 dp target".
	regexp.MustCompile(`\btargets?\s+(?:>=\s*)?([0-9]+)\b`),
	// "48 dp target", "Both 48 dp targets", ">=48 dp target".
	regexp.MustCompile(`\b([0-9]+)\s*dp\s+targets?\b`),
	// Row 22's "min 48", the one spelling that names neither `touch` nor `target`.
	regexp.MustCompile(`\bmin\s+([0-9]+)\b`),
}

// s23DocMinTarget reads the minimum touch target a derivation row states.
//
// EVERY OCCURRENCE MUST AGREE, across all four spellings, for [s23DocMetric]'s reason and one more
// that is specific to this value: row 9 states its target twice (once for the field and once for
// the two glyphs beside it) and row 15 states one for a row that CONTAINS another component's. A
// reader that took the first match would make the floor depend on which sentence came first, and
// one that took any match would let a row hold two different floors and satisfy both.
//
// @return the floor, or an error when the row states none -- which is the honest answer for the
// twenty rows that specify components nothing taps.
func s23DocMinTarget(row string) (float64, error) {
	seen := map[string]string{}
	for _, form := range s23MinTargetForms {
		for _, m := range form.FindAllStringSubmatch(row, -1) {
			seen[m[1]] = m[0]
		}
	}
	switch len(seen) {
	case 0:
		return 0, fmt.Errorf("the row states no minimum touch target in any of the forms " +
			"`target >=N`, `N dp target` or `min N`")
	case 1:
		for n := range seen {
			return strconv.ParseFloat(n, 64)
		}
	}
	return 0, fmt.Errorf("the row states %d disagreeing touch targets (%s), so PB-DS-12's floor "+
		"for this component is whichever sentence a reader reached first",
		len(seen), strings.Join(s23SortedKeys(seen), ", "))
}

func s23IsNumber(s string) bool {
	_, err := strconv.Atoi(s)
	return err == nil
}

// s23DerivationShare is the fraction internal/design declares for one named derivation.
func s23DerivationShare(name string) (float64, bool) {
	for _, d := range design.Derivations() {
		if d.Name == name {
			return float64(d.Percent) / 100, true
		}
	}
	return 0, false
}

func s23DerivedCount() int {
	n := 0
	for _, c := range s23Inbox {
		if c.Derived != "" {
			n++
		}
	}
	return n
}

// ---------------------------------------------------------------------------
// PB-DS-5's fence, which B134 puts here rather than in the resources.
// ---------------------------------------------------------------------------

// s23ElevationReach is every way a View acquires a Material shadow.
//
// `elevation` is the obvious implementation of --p-card-fx AND IT IS THE WRONG ONE. Substrate
// bans drop shadows outright -- its own words are that elevation is one ladder step lighter,
// never a shadow -- so the inset key-light is a layer with a 1dp top-edge rect clipped to the
// card radius, and a card that reached for View.elevation would render the one effect the skin
// forbids while looking, in code, exactly like the effect it asks for.
//
// translationZ and outline shadow colours are here because they are the same shadow reached by
// other names: a card with elevation 0 and translationZ 4 casts precisely the shadow this
// forbids.
//
// THE BARE ASSIGNMENT IS THE ONE THAT MATTERS AND IT WAS MISSING. This list first held only
// `setElevation` and `.elevation`, and the negative control caught it: inside an `apply {}` block
// -- which is how every view in this kit is configured -- the Kotlin spelling is `elevation = 2f`,
// with no receiver and no dot. That is the idiomatic form, so the fence was blind to exactly the
// way the mistake would be written.
var s23ElevationReach = []*regexp.Regexp{
	regexp.MustCompile(`\belevation\s*=`),
	regexp.MustCompile(`\btranslationZ\s*=`),
	regexp.MustCompile(`\bsetElevation\s*\(`),
	regexp.MustCompile(`\bsetTranslationZ\s*\(`),
	regexp.MustCompile(`\.elevation\b`),
	regexp.MustCompile(`\.translationZ\b`),
	regexp.MustCompile(`\bsetOutlineSpotShadowColor\b`),
	regexp.MustCompile(`\bsetOutlineAmbientShadowColor\b`),
	regexp.MustCompile(`android:elevation`),
	regexp.MustCompile(`android:translationZ`),
}

func TestPBDS5_TheKitNeverReachesForElevation(t *testing.T) {
	sources := s23KitSources(t)
	for file, src := range sources {
		code := kotlinCodeOnly(src)
		for _, reach := range s23ElevationReach {
			m := reach.FindString(code)
			if m == "" {
				continue
			}
			t.Errorf("PB-DS-5: %s reaches for `%s`. Substrate bans drop shadows -- elevation is one "+
				"ladder step LIGHTER, never a shadow (ADR-007 B134 decision 4) -- so --p-card-fx "+
				"is an INSET 1dp top-edge highlight clipped to the card radius, and --p-elev is "+
				"what a raised surface is made of. This is the wrong implementation despite being "+
				"the obvious one, which is why it is fenced rather than reviewed.", file, m)
		}
	}

	// The fence must recognise the spelling the mistake would actually be written in. Inside an
	// `apply {}` block -- which is how every view in this kit is configured -- that is a bare
	// assignment with no receiver, and an earlier version of this list could not see it.
	for _, probe := range []string{
		"    elevation = 2f",
		"view.elevation = 2f",
		"    setElevation(2f)",
		"    translationZ = 4f",
	} {
		matched := false
		for _, reach := range s23ElevationReach {
			if reach.MatchString(probe) {
				matched = true
			}
		}
		if !matched {
			t.Errorf("the elevation fence does not recognise %q, so a card written that way "+
				"ships the one effect Substrate forbids", probe)
		}
	}
	// And it must not fire on prose. A fence a comment can trip is one the next thorough
	// commenter turns into noise; kotlinCodeOnly is what keeps that true, and this says so.
	for _, notAReach := range []string{"the elevation ladder", "elevationless", "// elevation = 2f"} {
		for _, reach := range s23ElevationReach {
			if reach.MatchString(kotlinCodeOnly(notAReach)) {
				t.Errorf("the elevation fence fires on %q, which reaches for nothing", notAReach)
			}
		}
	}
}

// ---------------------------------------------------------------------------
// PB-DS-6: "styled entirely from the theme".
// ---------------------------------------------------------------------------

// s23ColourLiteral is the same recogniser s22_derived_test.go uses, and it is a separate copy on
// purpose: that one asks whether a DERIVATION's output was transcribed, this one asks whether any
// colour at all was typed. The first is a fence around four values; this is a fence around the
// palette.
var s23ColourLiteral = regexp.MustCompile(`(?i)(?:#|0x)([0-9a-f]{6}|[0-9a-f]{8})\b`)

func TestPBDS6_NoColourIsTypedInTheKit(t *testing.T) {
	sources := s23KitSources(t)
	scanned := 0
	for file, src := range sources {
		code := kotlinCodeOnly(src)
		scanned += len(code)
		for i, line := range strings.Split(code, "\n") {
			if m := s23ColourLiteral.FindStringSubmatch(line); m != nil {
				t.Errorf("PB-DS-6: %s:%d types the colour literal #%s. Every colour the kit paints "+
					"with is R.color.swarm_*, which android/design-tokens.tsv joins to the origin, "+
					"or a documented blend over those. A literal here is a fourth copy of the "+
					"palette in the one file that was supposed to end them.", file, i+1, m[1])
			}
		}
	}
	if scanned == 0 {
		t.Fatal("PB-DS-6: the colour scan read no code at all")
	}
}

// s23TypefaceReach: the type scale is 18 TextAppearance styles, and a Typeface reference is a
// nineteenth chosen at a call site. The three surface files hold five of these today (PB-DS-11
// removes them with the screens, S24); the kit must never acquire one.
var s23TypefaceReach = []string{"Typeface.", "setTypeface(", "setTextSize(", "setLetterSpacing("}

func TestPBDS6_NoTypefaceIsChosenInTheKit(t *testing.T) {
	for file, src := range s23KitSources(t) {
		code := kotlinCodeOnly(src)
		for _, reach := range s23TypefaceReach {
			if strings.Contains(code, reach) {
				t.Errorf("PB-DS-6: %s calls %s. Size, weight, tracking and family come from ONE "+
					"TextAppearance.Swarm.* style per text role (PB-DS-2); setting any of them at "+
					"a call site re-specifies the scale one view at a time, which is the state "+
					"S22 found the app in -- one Typeface.MONOSPACE, one Typeface.BOLD and no "+
					"setTextSize anywhere.", file, reach)
			}
		}
	}
}

// s23SpacingCall is every setter that places a view relative to another, including the
// RTL-correct spellings.
//
// PB-DS-1's own scan (s22b_spacing_test.go) matches `setPadding` and `setMargins` and stops there,
// so `setPaddingRelative`, `setMarginStart` and `setMarginEnd` -- the forms a layout that respects
// RTL actually uses, and the ones this kit uses throughout -- pass through it untouched. That is a
// hole in a fence rather than a decision, and widening PB-DS-1's scan is S22's to do; this closes
// it over the files S23 owns.
// A NESTED CALL IS THE NORMAL CASE HERE, so the argument list is found by balancing parentheses
// rather than by a regexp. Every correct call in this kit reads
// `setPaddingRelative(Kit.dimen(context, R.dimen.swarm_space_12).toInt(), ...)`, and `[^)]*` stops
// at the FIRST close paren -- inside `Kit.dimen(...)` -- so a regexp would inspect a fragment of
// the argument list and report on the rest by accident. The negative control caught this: a raw
// `12` as the first argument of a multi-line call was invisible to it.
var s23SpacingCall = regexp.MustCompile(
	`\bset(?:Padding|PaddingRelative|Margins|MarginStart|MarginEnd)\s*\(`)

// A whole argument that is nothing but a number. Applied to one already-split top-level argument,
// so leading newlines and indentation are stripped first and there is no "is it after a comma"
// question left to get wrong.
var s23LiteralArg = regexp.MustCompile(`^-?\d+$`)

// s23CallArguments returns the top-level arguments of the call whose opening parenthesis is at
// src[open], or nil when the parentheses do not balance.
func s23CallArguments(src string, open int) []string {
	depth := 0
	start := open + 1
	var args []string
	// Kotlin permits a trailing comma, and this kit uses one throughout, so the last split is
	// routinely empty. Dropping empties here rather than at every call site keeps "how many
	// arguments does this call have" the same question a reader would ask.
	add := func(end int) {
		if arg := strings.TrimSpace(src[start:end]); arg != "" {
			args = append(args, arg)
		}
	}
	for i := open; i < len(src); i++ {
		switch src[i] {
		case '(':
			depth++
		case ')':
			depth--
			if depth == 0 {
				add(i)
				return args
			}
		case ',':
			if depth == 1 {
				add(i)
				start = i + 1
			}
		}
	}
	return nil
}

// TestPBDS6_NoRawDimensionIsTypedInTheKit is the dimension half of "styled entirely from the
// theme".
//
// ZERO IS ALLOWED AND NOTHING ELSE IS. A zero has no unit -- 0 px and 0 dp are the same distance
// -- and the design states plenty of them (`.prows { padding: 0 12px }`). Every other number in a
// padding or margin call is a raw PIXEL value, which is the exact defect PB-DS-1 names: the
// constant it replaced was `PADDING = 24` in pixels, rendering at 8 dp on a 3x handset.
func TestPBDS6_NoRawDimensionIsTypedInTheKit(t *testing.T) {
	owned := map[string]bool{"Kit.kt": true, "ColorMix.kt": true, "Surfaces.kt": true}
	for _, c := range s23Inbox {
		owned[c.File] = true
	}
	checked := 0
	for file, src := range s23KitSources(t) {
		if !owned[file] {
			continue
		}
		code := kotlinCodeOnly(src)
		for _, loc := range s23SpacingCall.FindAllStringIndex(code, -1) {
			checked++
			args := s23CallArguments(code, loc[1]-1)
			if args == nil {
				t.Errorf("PB-DS-6: %s: the call at offset %d has unbalanced parentheses, so its "+
					"arguments were not inspected at all", file, loc[0])
				continue
			}
			for _, arg := range args {
				if !s23LiteralArg.MatchString(arg) || arg == "0" {
					continue
				}
				t.Errorf("PB-DS-6: %s calls %s with the literal %s. Every dimension the kit spends "+
					"comes from R.dimen.swarm_* -- the scale PB-DS-1 decided -- or from "+
					"Kit.dp over a KitMetrics constant the design source can be checked against. "+
					"A bare number here is in PIXELS.",
					file, strings.TrimSpace(code[loc[0]:loc[1]]), arg)
			}
		}
	}
	if checked == 0 {
		t.Error("PB-DS-6: the kit makes no padding or margin call at all, so this scan says " +
			"nothing -- and a component set that spaces nothing is not one")
	}

	// The scan must see what it is looking for, in the two spellings that defeated earlier
	// versions of it: PB-DS-1's own scan misses setPaddingRelative entirely, and a regexp-bounded
	// argument list misses a literal that opens a call whose other arguments are nested calls.
	probe := `setPaddingRelative(
        12,
        Kit.dimen(context, R.dimen.swarm_space_12).toInt(),
        0,
        Kit.dp(context, KitMetrics.DOT_DP).toInt(),
    )`
	loc := s23SpacingCall.FindStringIndex(probe)
	if loc == nil {
		t.Fatal("the call scan does not match setPaddingRelative, which is the whole reason it " +
			"exists beside PB-DS-1's")
	}
	args := s23CallArguments(probe, loc[1]-1)
	if len(args) != 4 {
		t.Fatalf("the argument splitter found %d top-level arguments in a four-argument call: %q. "+
			"A splitter that stops at the first close paren inspects a fragment and reports on "+
			"the rest by accident.", len(args), args)
	}
	literals := 0
	for _, a := range args {
		if s23LiteralArg.MatchString(a) {
			literals++
		}
	}
	if literals != 2 {
		t.Fatalf("the literal recogniser found %d bare numbers in %q, want 2 (the raw 12 and the "+
			"zero)", literals, args)
	}
	if s23LiteralArg.MatchString("Kit.dimen(context, R.dimen.swarm_space_12).toInt()") {
		t.Fatal("the literal recogniser matches a resource lookup, so it would fail on the " +
			"correct implementation as readily as on the wrong one")
	}
}

// ---------------------------------------------------------------------------
// PB-DS-6: the spacing a component spends is the step the ledger assigns to its own CSS.
// ---------------------------------------------------------------------------

// s23Spacing binds one CSS spacing declaration to the scale step the kit must spend for it.
//
// The Dimen column is NOT a free choice and this gate does not take it on trust: the step is
// recomputed from s22bScale, PB-DS-1's recorded absorption ledger, against the value the design
// actually declares. What the table contributes is the CLAIM -- that this component's padding is
// that declaration -- which is a decision a reviewer can disagree with, and which no amount of
// scanning could infer.
var s23Spacing = []struct {
	File     string
	Selector string
	Property string
	Index    int
	Dimen    string
}{
	// `.sheet2 .cmd { padding: 10px 11px }`. The horizontal edge is the interesting one: the
	// design declares 11px and PB-DS-1's ledger absorbs it into `space_10`, while derivation row
	// 18 transcribes the same cell as `space_12` -- which no rounding rule produces from 11.
	// Substrate DREW this component, so the source plus the ledger are the authority and the row
	// is a transcription of them. Reported as a documentation defect rather than followed.
	{"MonoWell.kt", ".sheet2 .cmd", "padding", 0, "swarm_space_10"},
	{"MonoWell.kt", ".sheet2 .cmd", "padding", 1, "swarm_space_10"},
	{"SessionRow.kt", ".prow", "padding", 0, "swarm_space_10"},
	{"SessionRow.kt", ".prow", "padding", 1, "swarm_space_12"},
	{"SessionRow.kt", ".prow .t", "gap", 0, "swarm_space_8"},
	{"SessionRow.kt", ".prow .ln", "margin-top", 0, "swarm_space_4"},
	{"SessionRow.kt", ".prows", "padding", 1, "swarm_space_12"},
	{"SessionRow.kt", ".prows", "gap", 0, "swarm_space_8"},
	{"WorkingBar.kt", ".workbar", "margin", 0, "swarm_space_2"},
	{"FilterChip.kt", ".chip", "padding", 0, "swarm_space_8"},
	{"FilterChip.kt", ".chip", "padding", 1, "swarm_space_10"},
	{"FilterChip.kt", ".chip .pd", "margin-right", 0, "swarm_space_4"},
	{"FilterChip.kt", ".chips", "gap", 0, "swarm_space_8"},
	{"FilterChip.kt", ".chips", "padding", 1, "swarm_space_18"},
	{"FilterChip.kt", ".chips", "padding", 2, "swarm_space_12"},
	// `.acts2 button { padding: 12px }` is a single-field shorthand: one step on all four edges.
	// The `.acts2` container's own `gap: 7px` is NOT here, because this slice ships the BUTTON and
	// not the column it sits in -- a container factory with no caller is the second spelling
	// EmptyStateTest's KDoc argues against, and the gap belongs to whoever builds the sheet.
	{"CtaButton.kt", ".acts2 button", "padding", 0, "swarm_space_12"},
	{"SectionLabel.kt", ".plabel", "padding", 0, "swarm_space_12"},
	{"SectionLabel.kt", ".plabel", "padding", 1, "swarm_space_18"},
	{"SectionLabel.kt", ".plabel", "padding", 2, "swarm_space_8"},
	{"NavHeader.kt", ".pnav", "padding", 0, "swarm_space_4"},
	{"NavHeader.kt", ".pnav", "padding", 1, "swarm_space_18"},
	{"NavHeader.kt", ".pnav", "padding", 2, "swarm_space_10"},
	{"NavHeader.kt", ".pnav", "gap", 0, "swarm_space_10"},
	// `.ptabs { padding-bottom: 14px }` is DELIBERATELY NOT LEDGERED, and the absence is the
	// decision. That 14 px reserves the iPhone home indicator INSIDE the bar's own box, and the
	// design has already ruled on this class of constant twice: derivation row 19 calls
	// `screen_top` 54 an iPhone notch constant that on Android must come from
	// `WindowInsets.statusBars`, with 54 as a design-time preview value only, and says
	// `screen_bottom` 76 is the same problem against the gesture-nav inset. Row 20 says where the
	// bottom one lands: the scaffold spends "bottom `screen_bottom` (or inset + `tabbar_height`)"
	// -- the real inset UNDER a bar that is `tabbar_height` tall, not a constant inside it.
	//
	// So the bar spends `tabbar_height` and nothing else, and `PhoneActivity.insetTheSystemBars`
	// puts the measured inset below it. Ledgering the 14 here would have required the bar to keep
	// spending it, and the two together double the bar's bottom air on every handset.
	{"TabBar.kt", ".ptabs div", "gap", 0, "swarm_space_4"},
}

func TestPBDS6_EveryKitSpacingIsTheLedgersStep(t *testing.T) {
	sources := s23KitSources(t)
	css := s22bSharedCSS(t)

	absorbs := map[float64]string{}
	for _, step := range s22bScale {
		for _, literal := range step.Absorbs {
			absorbs[literal] = step.Name
		}
	}
	if len(absorbs) == 0 {
		t.Fatal("PB-DS-6: the absorption ledger is empty; every expectation below would be zero")
	}

	for _, s := range s23Spacing {
		rule, ok := css[s.Selector]
		if !ok {
			t.Errorf("PB-DS-6: the design declares no `%s`, so the %s row claiming its %s is "+
				"pointed at nothing", s.Selector, s.File, s.Property)
			continue
		}
		value, ok := rule.Decls[s.Property]
		if !ok {
			t.Errorf("PB-DS-6: `%s` declares no %s", s.Selector, s.Property)
			continue
		}
		fields := strings.Fields(value)
		if s.Index >= len(fields) {
			t.Errorf("PB-DS-6: `%s { %s: %s }` has no field %d", s.Selector, s.Property, value, s.Index)
			continue
		}
		px, ok := s22bPx(fields[s.Index])
		if !ok {
			t.Errorf("PB-DS-6: `%s { %s }` field %d is %q, not a px length",
				s.Selector, s.Property, s.Index, fields[s.Index])
			continue
		}
		want, ok := absorbs[px]
		if !ok {
			t.Errorf("PB-DS-6: the scale absorbs no %gpx, so `%s { %s }` cannot be spent from it "+
				"at all -- which is a hole in PB-DS-1's ledger, not in this component",
				px, s.Selector, s.Property)
			continue
		}
		if want != s.Dimen {
			t.Errorf("PB-DS-6: `%s { %s }` is %gpx, which PB-DS-1's ledger absorbs into %s, but "+
				"the %s row spends %s. The ledger is the authority; a component that rounds a "+
				"design value its own way is where a 2dp grid stops being one.",
				s.Selector, s.Property, px, want, s.File, s.Dimen)
			continue
		}
		src, ok := sources[s.File]
		if !ok {
			t.Errorf("PB-DS-6: %s does not exist, so its spacing cannot be checked", s.File)
			continue
		}
		if !strings.Contains(kotlinCodeOnly(src), "R.dimen."+s.Dimen) {
			t.Errorf("PB-DS-6: %s never references R.dimen.%s, which is the step PB-DS-1's ledger "+
				"assigns to `%s { %s }` = %gpx. A dimension that is not read from the scale is one "+
				"typed at the call site, and the constant this requirement replaced was "+
				"PhoneSurface's `PADDING = 24` in raw pixels.",
				s.File, s.Dimen, s.Selector, s.Property, px)
		}
	}
}

// s23DerivedSpacing binds a component Substrate never drew to the scale steps ITS ROW in the
// derivation table names.
//
// PB-DS-1's ledger has nothing to say about the badge: there is no `.badge` rule in the design
// source to absorb, because the artifact ships no badge at all (§1.4 adds it beside the live
// counter). Row 3 is the entire authority for its padding -- and until this fence, nothing read
// that row. The Robolectric claim that looked like it did,
//
//	Claim("badge padding-y", dimen("swarm_space_2").toInt(), badge.paddingTop)
//
// has R.dimen.swarm_space_2 on BOTH sides of it: the component spends the step and the assertion
// re-reads the same step, so it certifies that the badge spends whatever the badge spends. The
// step comes from the row here; what the resource table resolves it to is the Robolectric
// suite's, and the two halves now start from different places.
var s23DerivedSpacing = []struct {
	File  string
	Row   string
	Edge  string
	Dimen string
}{
	{"Badge.kt", "#3 Badge", "padding-y", "swarm_space_2"},
	{"Badge.kt", "#3 Badge", "padding-x", "swarm_space_6"},
	// Row 15 states its padding in the shape this reader can express, so the settings row is
	// joined to the table rather than to a transcription of it. EmptyState.kt is NOT here and
	// cannot be: row 8 writes "padding 48 (2 x `space_24`) vertical", a MULTIPLE of a step rather
	// than a step, which s23DocPadding does not match. That join lives in s24_screens_test.go
	// with a reader of its own -- recorded here so the absence reads as a known limit rather than
	// as a component nobody joined.
	{"SettingsRow.kt", "#15 Settings row", "padding-y", "swarm_space_12"},
	{"SettingsRow.kt", "#15 Settings row", "padding-x", "swarm_space_14"},
	// Row 9 states its padding TWICE -- once for the composer bar and once for the field -- and
	// both are `space_8` x `space_14`. This reader takes the first match, which is the bar's, so
	// the join holds only while the two agree. Recorded because if they ever diverge this would
	// silently check the bar's padding against the field's component.
	{"TextField.kt", "#9 Composer", "padding-y", "swarm_space_8"},
	{"TextField.kt", "#9 Composer", "padding-x", "swarm_space_14"},
	// Row 14 states the same two steps the session row spends, which is the point rather than a
	// coincidence: the activity row's card IS `.prow`'s, so `cardSurface` paints it and this join
	// is what stops the padding beside it being retyped. These rows cited `§4 Activity row` for
	// part of a day, while the derivation table carried the activity row twice; the §4 duplicate
	// is deleted and row 14 is the authority, which is also the reference s23FindRow can resolve
	// unambiguously -- see the scoping note on that function.
	{"ActivityRow.kt", "#14 Activity row", "padding-y", "swarm_space_10"},
	{"ActivityRow.kt", "#14 Activity row", "padding-x", "swarm_space_12"},
	// Rows 11 and 12 state the SAME two steps, and the machines screen is where that stops being
	// a coincidence: the machine row and the panel under it are inset alike, which is what makes
	// them read as one screen rather than as two blocks that happen to be stacked. Both are joined
	// here so a change to either row is caught rather than absorbed.
	{"MachineRow.kt", "#11 Machine row", "padding-y", "swarm_space_12"},
	{"MachineRow.kt", "#11 Machine row", "padding-x", "swarm_space_14"},
	{"KillSwitchPanel.kt", "#12 Kill-switch panel", "padding-y", "swarm_space_12"},
	{"KillSwitchPanel.kt", "#12 Kill-switch panel", "padding-x", "swarm_space_14"},
	// Row 1's two steps, which are the widest horizontal padding in this kit -- `space_16` where
	// every row spends 12 or 14. That is the row's, and it is what makes a floating block read as
	// one line of speech rather than as a card that has come loose: it has no neighbours to be
	// consistent with and nothing but its own padding separating it from the screen behind it.
	{"Toast.kt", "#1 Toast", "padding-y", "swarm_space_10"},
	{"Toast.kt", "#1 Toast", "padding-x", "swarm_space_16"},
	// Row 13's padding is the same pair again, and the device row spends it through `settingsRow`
	// rather than through a component of its own -- so the join that holds it is row 15's, above.
	// DenyChip.kt is absent from this table for a different reason: row 13 states the chip's
	// padding as `space_8` x `space_10`, which s23DocPadding reads, but the row states TWO
	// paddings -- the device row's and the chip's -- and this reader takes the first match. It
	// would check the chip against the row's padding and pass while comparing the wrong pair.
	// The chip's two steps are asserted in DenyChipTest against the resource table instead.
}

// s23DocPadding reads the "padding space_2 x space_6" cell out of a row -- vertical first and
// then horizontal, which is the CSS shorthand's own order and the order the table writes it in.
var s23DocPadding = regexp.MustCompile("padding `space_([0-9]+)` x `space_([0-9]+)`")

func s23RowPadding(row, edge string) (string, error) {
	m := s23DocPadding.FindStringSubmatch(row)
	if m == nil {
		return "", fmt.Errorf("the row states no padding of the form `space_N` x `space_N`")
	}
	switch edge {
	case "padding-y":
		return "swarm_space_" + m[1], nil
	case "padding-x":
		return "swarm_space_" + m[2], nil
	}
	return "", fmt.Errorf("%q is not an edge this reader knows", edge)
}

func TestPBDS7_EveryDerivedSpacingIsTheRowsStep(t *testing.T) {
	doc := readFileOrFail(t, filepath.Join(repoRoot(t), filepath.FromSlash(s23ComponentsDoc)), "PB-DS-7")
	sources := s23KitSources(t)

	for _, s := range s23DerivedSpacing {
		row, ok := s23FindRow(doc, s.Row)
		if !ok {
			t.Errorf("PB-DS-7: the %s row claims `%s` states its %s, and %s has no such row",
				s.File, s.Row, s.Edge, s23ComponentsDoc)
			continue
		}
		want, err := s23RowPadding(row, s.Edge)
		if err != nil {
			t.Errorf("PB-DS-7: `%s`: %v", s.Row, err)
			continue
		}
		if want != s.Dimen {
			t.Errorf("PB-DS-7: `%s` states %s for %s's %s, and the s23DerivedSpacing row spends "+
				"%s. The table is the authority for a component the design source never drew.",
				s.Row, want, s.File, s.Edge, s.Dimen)
			continue
		}
		src, ok := sources[s.File]
		if !ok {
			t.Errorf("PB-DS-7: %s does not exist, so its spacing cannot be checked", s.File)
			continue
		}
		if !strings.Contains(kotlinCodeOnly(src), "R.dimen."+want) {
			t.Errorf("PB-DS-7: %s never references R.dimen.%s, which is the step `%s` states for "+
				"its %s. A component whose only specification is prose in a table is the one whose "+
				"spacing has to be read out of that table rather than out of itself.",
				s.File, want, s.Row, s.Edge)
		}
	}

	// The reader must read the ROW, not echo the claim. Perturbing the row it is given has to
	// move its answer, or the loop above is comparing s23DerivedSpacing with itself.
	row, ok := s23FindRow(doc, "#3 Badge")
	if !ok {
		t.Fatal("PB-DS-7: the derivation table has no row 3, so the control below says nothing")
	}
	moved := strings.Replace(row, "padding `space_2`", "padding `space_4`", 1)
	if moved == row {
		t.Fatal("PB-DS-7: row 3 no longer spends space_2 on its padding, so the control below " +
			"perturbs nothing")
	}
	got, err := s23RowPadding(moved, "padding-y")
	if err != nil || got != "swarm_space_4" {
		t.Errorf("PB-DS-7: the row reader answers %q (%v) for a row stating space_4, so it is not "+
			"reading the row and every comparison above holds against a constant", got, err)
	}
	if _, err := s23RowPadding("| 3 | Badge | no padding cell |", "padding-y"); err == nil {
		t.Error("PB-DS-7: the row reader found a padding in a row that states none, so a row that " +
			"lost its spacing cell would leave the badge's padding checked against nothing")
	}
}

// s23DerivedEdge is the same join for the rows that state their spacing PER EDGE rather than as a
// CSS shorthand.
//
// IT EXISTS BECAUSE s23RowPadding CANNOT READ THESE ROWS AND SHOULD NOT BE TAUGHT TO. That reader
// matches "padding `space_N` x `space_N`", which is a two-value shorthand -- vertical then
// horizontal. §4's drill-down header states THREE different vertical/horizontal steps ("`space_6`
// top / `space_18` sides / `space_12` bottom"), and row 22 states a margin rather than a padding
// and only two of its four edges. Widening the shorthand reader to cover both would make it match
// a shape neither row writes and stop reporting a row that lost its cell, which is the one thing
// it is for. Two readers, each of which fails loudly on the form it does not know.
//
// EmptyState.kt IS STILL ABSENT FROM BOTH, for the reason s23DerivedSpacing already records: row 8
// writes a MULTIPLE of a step and that join lives in s24_screens_test.go.
var s23DerivedEdge = []struct {
	File  string
	Row   string
	Edge  string
	Dimen string
}{
	{"NavHeaderDrill.kt", "§4 Drill-down nav header", "top", "swarm_space_6"},
	{"NavHeaderDrill.kt", "§4 Drill-down nav header", "sides", "swarm_space_18"},
	{"NavHeaderDrill.kt", "§4 Drill-down nav header", "bottom", "swarm_space_12"},
	{"NavHeaderDrill.kt", "§4 Drill-down nav header", "gap", "swarm_space_10"},
	{"ReadOnlyNote.kt", "#22 Read-only note", "top", "swarm_space_10"},
	{"ReadOnlyNote.kt", "#22 Read-only note", "sides", "swarm_space_18"},
	// Row 11's two internal gaps. They are the cells that make the row a SHAPE rather than a card
	// with text in it -- `space_8` between the name and the identifier that trails it, `space_4`
	// between that line and the meta line under it -- and neither is expressible as a padding, so
	// this is the reader that can hold them.
	{"MachineRow.kt", "#11 Machine row", "gap", "swarm_space_8"},
	{"MachineRow.kt", "#11 Machine row", "below", "swarm_space_4"},
	// Row 12's MARGINS, which no other component in this kit has: the panel is the one thing on
	// the machines screen that sits on the ground rather than inside a container, so its own inset
	// is the design's rather than a list's. `space_14` sides is 2 dp wider than the `.prows`
	// container the rows above and below it use -- both values are what their rows state, and the
	// difference is what `.cards` becoming `.prows` (§6) costs on this one screen.
	{"KillSwitchPanel.kt", "#12 Kill-switch panel", "top", "swarm_space_8"},
	{"KillSwitchPanel.kt", "#12 Kill-switch panel", "sides", "swarm_space_14"},
}

// s23DocEdgeStep reads one edge's step out of a row.
//
// The two spellings are the two the table actually uses: a step FOLLOWED by the edge it applies to
// ("`space_6` top"), and `gap` FOLLOWED by its step ("gap `space_10`"). Both are anchored on the
// backticked `space_N`, so a sentence that merely mentions an edge word cannot answer.
func s23DocEdgeStep(row, edge string) (string, error) {
	pattern := "`space_([0-9]+)`\\s+" + regexp.QuoteMeta(edge)
	if edge == "gap" {
		pattern = "gap\\s+`space_([0-9]+)`"
	}
	re, err := regexp.Compile(pattern)
	if err != nil {
		return "", fmt.Errorf("%q is not an edge this reader knows", edge)
	}
	matches := re.FindAllStringSubmatch(row, -1)
	if matches == nil {
		return "", fmt.Errorf("the row states no step for the %s edge", edge)
	}
	// Every occurrence must agree, for s23DocMetric's reason: a row is prose and a value can be
	// restated in it, so taking the first match would make the answer depend on sentence order.
	first := matches[0][1]
	for _, m := range matches[1:] {
		if m[1] != first {
			return "", fmt.Errorf("the row states the %s edge twice and disagrees with itself: "+
				"space_%s and space_%s", edge, first, m[1])
		}
	}
	return "swarm_space_" + first, nil
}

func TestPBDS7_EveryPerEdgeSpacingIsTheRowsStep(t *testing.T) {
	doc := readFileOrFail(t, filepath.Join(repoRoot(t), filepath.FromSlash(s23ComponentsDoc)), "PB-DS-7")
	sources := s23KitSources(t)

	for _, s := range s23DerivedEdge {
		row, ok := s23FindRow(doc, s.Row)
		if !ok {
			t.Errorf("PB-DS-7: the %s row claims `%s` states its %s edge, and %s has no such row",
				s.File, s.Row, s.Edge, s23ComponentsDoc)
			continue
		}
		want, err := s23DocEdgeStep(row, s.Edge)
		if err != nil {
			t.Errorf("PB-DS-7: `%s`: %v", s.Row, err)
			continue
		}
		if want != s.Dimen {
			t.Errorf("PB-DS-7: `%s` states %s for %s's %s edge, and the s23DerivedEdge row spends "+
				"%s. The table is the authority for a component the design source never drew.",
				s.Row, want, s.File, s.Edge, s.Dimen)
			continue
		}
		src, ok := sources[s.File]
		if !ok {
			t.Errorf("PB-DS-7: %s does not exist, so its %s edge cannot be checked", s.File, s.Edge)
			continue
		}
		if !strings.Contains(kotlinCodeOnly(src), "R.dimen."+want) {
			t.Errorf("PB-DS-7: %s never references R.dimen.%s, which is the step `%s` states for "+
				"its %s edge. A component whose only specification is prose in a table is the one "+
				"whose spacing has to be read out of that table rather than out of itself.",
				s.File, want, s.Row, s.Edge)
		}
	}
}

// TestPBDS7_ThePerEdgeReaderReadsTheRow is that join's negative control, fed to the SAME function
// the assertion calls.
func TestPBDS7_ThePerEdgeReaderReadsTheRow(t *testing.T) {
	doc := readFileOrFail(t, filepath.Join(repoRoot(t), filepath.FromSlash(s23ComponentsDoc)), "PB-DS-7")
	row, ok := s23FindRow(doc, "§4 Drill-down nav header")
	if !ok {
		t.Fatal("PB-DS-7: §4 has no drill-down nav header row, so the control below says nothing")
	}

	moved := strings.Replace(row, "`space_6` top", "`space_4` top", 1)
	if moved == row {
		t.Fatal("PB-DS-7: the §4 header row no longer states `space_6` top, so this control " +
			"perturbs nothing")
	}
	if got, err := s23DocEdgeStep(moved, "top"); err != nil || got != "swarm_space_4" {
		t.Errorf("PB-DS-7: the edge reader answers %q (%v) for a row stating space_4, so it is not "+
			"reading the row and every comparison above holds against a constant", got, err)
	}

	// A row that lost its cell must be reported rather than answered.
	if _, err := s23DocEdgeStep("| Drill-down nav header | no spacing cell |", "top"); err == nil {
		t.Error("PB-DS-7: the edge reader found a step in a row that states none, so a row that " +
			"lost its spacing cell would leave the header's padding checked against nothing")
	}
	// And a row that contradicts itself must not be silently resolved to its first occurrence.
	if _, err := s23DocEdgeStep("margin `space_10` top and also `space_14` top", "top"); err == nil {
		t.Error("PB-DS-7: the edge reader resolved a row that states two different steps for one " +
			"edge, so a contradiction in the table would be answered rather than reported")
	}
}

// ---------------------------------------------------------------------------
// PB-DS-7: the one glyph §4 asks for that the artifacts do not draw.
// ---------------------------------------------------------------------------

// s23BackGlyph is the drill-down header's chevron.
//
// **ITS PATH HAS NO SOURCE AND THAT IS RECORDED RATHER THAN CHECKED.** The four tab glyphs are
// joined path-for-path to the artifact because the artifact draws them; `.navhead .back` in the
// retired mock is the CHARACTER U+2039 in accent text, and §2 retires accent-text affordances
// wholesale ("every accent-text affordance in the mock becomes either a bordered control or a
// plain `--p-ink` glyph"). So §4 asks for a stroked chevron that neither document draws. What CAN
// be joined is everything §4 does state -- the stroke weight, and that the asset declares one box
// rather than two -- and that is what this checks. The path itself is the implementation's, and
// the drawable says so in its own comment.
const s23BackGlyph = "swarm_nav_back.xml"

func TestPBDS7_TheBackGlyphIsTheStrokeSection4States(t *testing.T) {
	doc := readFileOrFail(t, filepath.Join(repoRoot(t), filepath.FromSlash(s23ComponentsDoc)), "PB-DS-7")
	row, ok := s23FindRow(doc, "§4 Drill-down nav header")
	if !ok {
		t.Fatal("PB-DS-7: §4 has no drill-down nav header row to read the chevron's stroke from")
	}
	want, err := s23DocMetric(row, "stroke")
	if err != nil {
		t.Fatalf("PB-DS-7: `§4 Drill-down nav header`: %v", err)
	}

	file := filepath.Join(tabbarDrawableDir(t), s23BackGlyph)
	raw, readErr := os.ReadFile(file)
	if readErr != nil {
		t.Fatalf("PB-DS-7: §4 gives the drill-down header a chevron glyph and %s does not exist, "+
			"so the back control renders as a bare label: %v", mustRel(t, file), readErr)
	}
	attrs := tabbarAttrs(tabbarPathElemRe.FindString(string(raw)))
	got, parseErr := strconv.ParseFloat(attrs["strokeWidth"], 64)
	if parseErr != nil {
		t.Fatalf("PB-DS-7: %s declares android:strokeWidth=%q, which is not a number: %v",
			mustRel(t, file), attrs["strokeWidth"], parseErr)
	}
	if got != want {
		t.Errorf("PB-DS-7: %s strokes the chevron at %g and `§4 Drill-down nav header` states "+
			"stroke %g. The path is this asset's own -- neither artifact draws one -- so the "+
			"weight is the only thing about it §4 can be held to.", mustRel(t, file), got, want)
	}

	// The box is one number, not two. `.ptabs svg` taught this the expensive way: the box and the
	// viewBox are different coordinate spaces and conflating them scales the glyph.
	box := tabbarAttrs(tabbarVectorElemRe.FindString(string(raw)))
	if box["width"] != box["height"] {
		t.Errorf("PB-DS-7: %s is %s x %s. A chevron in a non-square box is a chevron that leans; "+
			"§4 states one number for the glyph.", mustRel(t, file), box["width"], box["height"])
	}
	if box["viewportWidth"] != box["viewportHeight"] {
		t.Errorf("PB-DS-7: %s has a %s x %s viewport, so the path's coordinate space is not square "+
			"and the stroke is a different weight on each axis",
			mustRel(t, file), box["viewportWidth"], box["viewportHeight"])
	}
}

// ---------------------------------------------------------------------------
// PB-DS-7: the numbers the scale does not govern.
// ---------------------------------------------------------------------------

// s23MetricConst matches a property declaration that binds a NUMERIC LITERAL.
//
// THIS PATTERN WAS WIDENED THREE TIMES AND EACH WIDENING WAS CALLED AN INVERSION. Round one read
// `(?:internal\s+)?const`, which accepted `internal` while rejecting `private`, so
// `private const val ATTENTION_BORDER_SHARE = 0.36f` was never matched and its `origin:` line was
// decoration. Round two added `private`, `protected`, `@JvmField`, `[fF]` and a camelCase name.
// Round three added `var`, a second annotation, any type, a trailing comma and a stripped comment
// view, and the comment here then claimed "there is no spelling left that escapes by not being
// listed". A fourth reviewer falsified that claim in one paste:
//
//	const val EXTRA_PAD_DP =
//	    11f                             -- the standard ktlint wrap; this pattern is line-anchored
//	val gutterDp: Float get() = 13f     -- a `get()` accessor, which has no `=` where it wants one
//	const val SHADE = 0x1F4             -- a hex literal, which the decimal alternation cannot read
//
// and the whole gate reported ok. It also misses expression initialisers (`7f * 2`), two
// declarations on one line, use-site targets (`@get:JvmName`) and `7uL`. THE CLAIM WAS FALSE AND
// A FOURTH WIDENING WOULD MAKE IT FALSE AGAIN, because a regexp over declaration syntax has no
// spelling-complete form and the failure mode of a spelling list is the spelling not on it.
//
// SO THE COMPLETENESS CLAIM NO LONGER LIVES HERE. This is a RECOGNISER -- deliberately wide: any
// annotations, any modifiers in any order, `val` or `var`, any identifier, any optional type, any
// decimal literal, any trailing comma or semicolon -- and what it recognises is required to cite
// an origin and is recomputed from it (s23CheckMetric). What it does NOT recognise is caught by
// two cross-checks that never look at declaration syntax:
//
//	TestPBDS7_EveryMetricSpendResolvesToADeclarationTheScanSaw -- every `KitMetrics.<ident>` the
//	kit spends must be a name this scan RETURNED, so a declaration written in a spelling missed
//	here is a DANGLING REFERENCE and fails.
//	TestPBDS7_EveryNumberInTheKitIsAccountedFor -- every numeric literal token in the fenced files
//	must be either the value of a recognised declaration or one of nine exempt bare literals (and
//	inside object KitMetrics not even that), so a declaration missed here leaves its number
//	UNACCOUNTED FOR and fails.
//
// Both are indifferent to how the declaration is written, which is the property a widening can
// never have. All three constructs above now fail the gate while still being invisible to THIS
// pattern -- that is the difference between lengthening the list and inverting the fence.
//
// WHAT THE PAIR STILL DOES NOT REACH, stated rather than implied, because the last three claims
// here were of completeness and two were false. Outside object KitMetrics -- in the ten component
// files -- an unrecognised declaration escapes both checks if its value is one of the nine exempt
// literals AND nothing spends it through a `KitMetrics.` reference: `private val pad =\n    2f` in
// Surfaces.kt is invisible. Inside the object, which is where a metric belongs and where all three
// injections were placed, there is no such gap. A spend written without the `KitMetrics.` prefix
// (a member import, or a reference from inside the object itself) is also not seen by the spend
// check, though its declaration still faces the literal accounting.
//
// COMMENTS ARE STRIPPED BEFORE THIS PATTERN IS APPLIED, which is why it can keep a hard `$`
// anchor. Round two handled the trailing comment inside the pattern (`(?://.*)?$`) and thereby
// handled exactly one comment syntax; s23ScanMetrics now matches against a kotlinCodeOnly view
// instead, so every comment form is gone before the anchor is reached and the anchor still means
// "the declaration ends here" rather than "the line ends here".
//
// WHAT REMAINS OUTSIDE IT, stated rather than implied: a number that never becomes a property.
// `Kit.dp(context, 7f)` types a metric straight into a call site and no declaration recogniser of
// any width can see it. That is closed separately and mechanically by
// TestPBDS7_NoMetricIsTypedAtADpCallSite, because widening this pattern further could not reach it.
var s23MetricConst = regexp.MustCompile(
	`^\s*(?:@[A-Za-z][A-Za-z0-9_]*(?:\([^)]*\))?\s+)*` +
		`(?:(?:private|internal|public|protected|const|open|override|final|actual|expect|lateinit)\s+)*` +
		`(?:val|var)\s+([A-Za-z_][A-Za-z0-9_]*)\s*` +
		`(?::\s*[A-Za-z][A-Za-z0-9_.]*\??\s*)?` +
		`=\s*(-?[0-9][0-9_]*(?:\.[0-9][0-9_]*)?(?:[eE][+-]?[0-9]+)?)[fFdDL]?\s*[,;]?\s*$`)

// s23MetricCSSOrigin is `origin: .pdot { width }` -- a declaration in the shared block.
var s23MetricCSSOrigin = regexp.MustCompile(`^(?:\s|\*|/)*origin:\s*(\S.*?)\s*\{\s*([a-z-]+)\s*\}\s*(?:\*/)?\s*$`)

// s23MetricDerivationOrigin is `origin: derivation attention-row-border` -- a share whose
// authority is internal/design's derivation table rather than a declaration in the design source.
//
// The three shares this kit carries are inputs to a `color-mix` PB-TOK-7 forbids resolving into a
// literal, so the SHARE is what the kit holds and that table is where it comes from. Naming the
// derivation makes the join machine-read, the same way `origin: .pdot { width }` does for a CSS
// declaration -- and it retires the shape-specific regexp that used to recognise the two glow
// shares by the syntax of the `when` branch they sat in.
var s23MetricDerivationOrigin = regexp.MustCompile(
	`^(?:\s|\*|/)*origin:\s*derivation\s+([a-z0-9-]+)\s*(?:\*/)?\s*$`)

// s23MetricDerivedOrigin is `derived: docs/design/substrate-components.md #3 Badge { height }` --
// the escape hatch for a constant the design source cannot supply because Substrate never
// specified the component.
//
// THE `{ field }` IS WHAT MAKES IT A VALUE CHECK RATHER THAN A CITATION CHECK. Without it the
// annotation asserted only that the cited ROW exists: BADGE_HEIGHT_DP could become 20f and stay
// green, because nothing followed the citation into row 3 to read the `height 16` it states. The
// field names the cell entry, and s23DocMetric reads it.
var s23MetricDerivedOrigin = regexp.MustCompile(`^(?:\s|\*|/)*derived:\s*(\S.*?)\s*(?:\*/)?\s*$`)

// s23DerivedField splits the optional `{ field }` off the end of a derivation citation.
var s23DerivedField = regexp.MustCompile(`^(.*?)\s*\{\s*([a-z-]+)\s*\}$`)

// s23ParseDerived splits `docs/... #3 Badge { height }` into the row citation and the cell field.
//
// A citation with no field names a row and nothing in it, which is what a COMPONENT annotation
// does -- Badge.kt cites row 3 as its specification, and the row has no single number that is
// "the badge". A CONSTANT with no field is refused by s23CheckMetric, because a constant is one
// number and the row it cites has a dozen.
func s23ParseDerived(raw string) (ref, field string) {
	if m := s23DerivedField.FindStringSubmatch(raw); m != nil {
		return m[1], m[2]
	}
	return raw, ""
}

// s23MetricTokenOrigin is `origin: --p-card-fx alpha` -- a part of a token's value, for the four
// effect tokens that have no colour resource and no CSS rule of their own.
// s23MetricTokenOrigin reads `origin: --p-card-fx alpha` -- an effect token, and which part of it.
//
// `opacity` IS THE ONE PART THAT IS THE WHOLE VALUE. The other three read a number OUT of a larger
// value: a px length inside a shadow, an alpha inside an `rgba()`, a stop inside a gradient.
// `--p-grain` is the bare fraction `0.04` and has no larger value to be read out of, so a reader
// that only knew the three would have made the grain's opacity a number with no origin -- which is
// the one thing the annotation exists to make impossible.
var s23MetricTokenOrigin = regexp.MustCompile(`^(?:\s|\*|/)*origin:\s*(--[a-z0-9-]+)\s+(px|alpha|stop|opacity)\s*(?:\*/)?\s*$`)

var s23PxRe = regexp.MustCompile(`([0-9]*\.?[0-9]+)px`)
var s23RGBARe = regexp.MustCompile(`rgba\(\s*[0-9]+\s*,\s*[0-9]+\s*,\s*[0-9]+\s*,\s*([0-9]*\.?[0-9]+)\s*\)`)
var s23StopRe = regexp.MustCompile(`([0-9]*\.?[0-9]+)%`)

// s23OpacityRe is a token whose ENTIRE value is a bare fraction -- `--p-grain` is `"0.04"`.
var s23OpacityRe = regexp.MustCompile(`^\s*([0-9]*\.?[0-9]+)\s*$`)

// s23Metric is one constant the kit declares, paired with the origin annotation above it.
//
// THE SCAN AND THE CHECK ARE SEPARATE FUNCTIONS SO THE NEGATIVE CONTROL CAN DRIVE THE SAME ONES.
// They were one loop inside the test, and a control for a loop has to be a copy of that loop --
// which proves the copy works and says nothing about the fence. Every probe in
// TestPBDS7_TheMetricScanCanActuallyFail goes through [s23ScanMetrics] and [s23CheckMetric], the
// two functions the real assertion calls.
type s23Metric struct {
	Name string
	Line int
	// Raw is the literal as written, parsed by the check so the scan stays a reader.
	Raw string
	// Kind is the annotation form above the constant: "css", "token", "derivation", "derived",
	// or "" for a constant carrying no origin at all.
	Kind   string
	First  string
	Second string
}

// s23ScanMetrics reads one source into the constants it declares and the origins they cite.
//
// IT READS TWO VIEWS OF THE SAME FILE, and the split is load-bearing rather than fussy. The
// `origin:` annotations ARE comments, so they can only be read from the raw source; the
// DECLARATION must not be, because a trailing comment defeated the pattern's end anchor and made
//
//	const val DOT_DP = 7f // the design's dot
//
// invisible to this entire gate. kotlinCodeOnly preserves newlines precisely so the stripped view
// indexes the same lines as the raw one, which is what lets the annotation come from `raw[i]` and
// the declaration from `code[i]`. s23ScanAlignmentFault asserts that invariant rather than
// trusting it -- if the two views ever drift by a line, every constant in the kit would silently
// acquire the annotation belonging to its neighbour.
func s23ScanMetrics(src string) []s23Metric {
	var out []s23Metric
	pending := s23Metric{}
	raw := strings.Split(src, "\n")
	code := strings.Split(kotlinCodeOnly(src), "\n")
	for i, line := range raw {
		if i < len(code) {
			if m := s23MetricConst.FindStringSubmatch(code[i]); m != nil {
				pending.Name, pending.Line = m[1], i+1
				pending.Raw = strings.ReplaceAll(m[2], "_", "")
				out = append(out, pending)
				pending = s23Metric{}
				continue
			}
		}
		if m := s23MetricDerivationOrigin.FindStringSubmatch(line); m != nil {
			pending = s23Metric{Kind: "derivation", First: m[1]}
			continue
		}
		if m := s23MetricTokenOrigin.FindStringSubmatch(line); m != nil {
			pending = s23Metric{Kind: "token", First: m[1], Second: m[2]}
			continue
		}
		if m := s23MetricCSSOrigin.FindStringSubmatch(line); m != nil {
			pending = s23Metric{Kind: "css", First: m[1], Second: m[2]}
			continue
		}
		if m := s23MetricDerivedOrigin.FindStringSubmatch(line); m != nil {
			ref, field := s23ParseDerived(m[1])
			pending = s23Metric{Kind: "derived", First: ref, Second: field}
			continue
		}
		// A PENDING ANNOTATION DOES NOT SURVIVE A LINE OF CODE, or the next declaration inherits
		// an origin that was written for something else. The wrapped form
		//
		//	/** origin: .pdot { width } */
		//	const val DOT_DP =
		//	    7f
		//	const val NEXT_DP = 7f
		//
		// left `.pdot { width }` pending across two unmatched lines and handed it to NEXT_DP, which
		// then passed at 7 on an authority naming the dot. Comment lines are BLANK in the stripped
		// view, so a KDoc block between the annotation and its declaration still carries.
		if i < len(code) && strings.TrimSpace(code[i]) != "" {
			pending = s23Metric{}
		}
	}
	return out
}

// s23ScanAlignmentFault reports the invariant [s23ScanMetrics] depends on: the comment-stripped
// view of a source has the same number of lines as the raw one.
//
// It is a function rather than a comment because the consequence of it being false is silent and
// total -- every constant in the kit would be paired with a neighbour's `origin:` annotation, and
// a gate that checks the dot's diameter against the glow's radius fails for a reason nobody could
// read off the message. kotlinCodeOnly is another slice's helper; this is what makes the coupling
// checkable from here.
func s23ScanAlignmentFault(src string) string {
	raw := strings.Split(src, "\n")
	code := strings.Split(kotlinCodeOnly(src), "\n")
	if len(raw) != len(code) {
		return fmt.Sprintf("the raw source has %d line(s) and the comment-stripped view has %d, so "+
			"the two indexes disagree and every constant would be joined to the wrong annotation",
			len(raw), len(code))
	}
	return ""
}

// s23CheckMetric recomputes one constant from the design authority it cites.
//
// @return the disagreement, or "" when the constant is the design's own number. The caller adds
// the file and line, so this reads as one sentence about the value.
func s23CheckMetric(m s23Metric, css map[string]s22bCSSRule, tokens map[string]string, doc string) string {
	got, err := strconv.ParseFloat(m.Raw, 64)
	if err != nil {
		return fmt.Sprintf("%s = %sf is not a number", m.Name, m.Raw)
	}
	switch m.Kind {
	case "css":
		want, err := s23CSSMetric(css, m.First, m.Second)
		if err != nil {
			return fmt.Sprintf("%s cites `%s { %s }`: %v", m.Name, m.First, m.Second, err)
		}
		if want != got {
			return fmt.Sprintf("%s = %g, but the design's `%s { %s }` is %g. The design's px is "+
				"Android dp at 1:1, so this is a transcription error and nothing else.",
				m.Name, got, m.First, m.Second, want)
		}
	case "token":
		want, err := s23TokenMetric(tokens, m.First, m.Second)
		if err != nil {
			return fmt.Sprintf("%s cites `%s %s`: %v", m.Name, m.First, m.Second, err)
		}
		if want != got {
			return fmt.Sprintf("%s = %g, but %s declares %s = %g in the token origin",
				m.Name, got, m.First, m.Second, want)
		}
	case "derivation":
		want, ok := s23DerivationShare(m.First)
		if !ok {
			return fmt.Sprintf("%s cites the derivation %q, and internal/design.Derivations() "+
				"declares no such name. A share with no derivation behind it is a colour-mix "+
				"input nobody agreed to, which is PB-TOK-7's defect one indirection out.",
				m.Name, m.First)
		}
		if want != got {
			return fmt.Sprintf("%s = %g, and internal/design declares the %s derivation at %g. "+
				"The kit carries the SHARE because the resolved colour may not be typed; a share "+
				"that disagrees with the table produces a colour no fence can recognise as wrong.",
				m.Name, got, m.First, want)
		}
	case "derived":
		if m.Second == "" {
			return fmt.Sprintf("`%s` cites `%s` and names no field of it. The row states a dozen "+
				"numbers; without a `{ field }` there is nothing to compare and the annotation "+
				"asserts only that someone wrote a heading. Cite it as `%s { height }`.",
				m.Name, m.First, m.First)
		}
		ref := strings.TrimSpace(strings.TrimPrefix(m.First, s23ComponentsDoc))
		if ref == m.First {
			return fmt.Sprintf("%s cites %q, which does not name %s", m.Name, m.First, s23ComponentsDoc)
		}
		row, ok := s23FindRow(doc, ref)
		if !ok {
			return fmt.Sprintf("%s cites `%s`, and no such row exists in %s", m.Name, ref, s23ComponentsDoc)
		}
		want, err := s23DocMetric(row, m.Second)
		if err != nil {
			return fmt.Sprintf("%s cites `%s { %s }`: %v", m.Name, ref, m.Second, err)
		}
		if want != got {
			return fmt.Sprintf("%s = %g, and `%s` states %s %g. The design source never drew this "+
				"component, so that row is the only place its numbers exist -- a constant that "+
				"drifts from it drifts from everything.", m.Name, got, ref, m.Second, want)
		}
	default:
		// ZERO IS THE ONE UNANNOTATED NUMBER, for the reason the spacing scan already gives: a zero
		// has no unit, so 0 px and 0 dp are the same distance and there is no design value for it
		// to disagree with. This exemption is what lets the recogniser above be maximal without
		// demanding a design origin for `const val TRANSPARENT: Int = 0` or for a component
		// property defaulted to none -- the alternative was to keep those spellings out of the
		// pattern, which is how the pattern became a list of spellings in the first place.
		if got == 0 {
			return ""
		}
		return fmt.Sprintf("`val %s = %s` carries no `origin:` annotation. A number in this file "+
			"with no design behind it is exactly the thing the kit exists to stop reaching the "+
			"screens -- and it is invisible in review, because a plausible dp value looks like "+
			"every other plausible dp value.", m.Name, m.Raw)
	}
	return ""
}

// TestPBDS7_EveryKitMetricIsTheDesignsOwnNumber joins KitMetrics to the design.
//
// WHY THESE CONSTANTS EXIST AT ALL, since PB-DS-11's whole point is that no visual constant may
// enter the app except through the theme. The 7dp status dot, the 9dp glow radius, the 3dp
// workbar, the 2dp attention rail and the 1dp hairline are values the design states and the
// RESOURCES cannot carry: they are not spacing (a 2dp grid has nothing to say about a dot's
// diameter), not radii, and --p-card-fx / --p-workbar are declared `effect` in tokens.json, so
// PB-TOK-6's converters produce no <color> or <dimen> for them.
//
// THE SET SHRINKS WHEN THE ORIGIN CAN CARRY A VALUE AFTER ALL. The 0.88 tab-bar alpha was one of
// these until --p-tabbg turned out to be typed `effect` only because no parser could read
// `rgba()`; it has a <color> now, so TabBar.kt spends R.color.swarm_tabbar_background and the
// constant is gone. A number here is the last resort, not a category.
//
// So they are named constants with a machine-read origin, and this test is what makes that
// survivable: every one of them is COMPUTED from the design source or from the token it names,
// and a constant with no origin annotation fails rather than being skipped -- which is the
// difference between a small documented set and a place to put numbers.
// SCOPED TO THE FILES s23Inbox NAMES, for the reason the package comment gives about Motion.kt:
// PB-DS-8's constants are durations and easing control points, whose origin is the motion
// decision rather than a rule in the shared CSS block, and requiring them to cite a `{ property }`
// there would be this slice failing on another's file.
func TestPBDS7_EveryKitMetricIsTheDesignsOwnNumber(t *testing.T) {
	sources := s23KitSources(t)
	css := s22bSharedCSS(t)
	tokens := s22bTokenValues(t)

	owned := s23OwnedFiles()

	doc := readFileOrFail(t, filepath.Join(repoRoot(t), filepath.FromSlash(s23ComponentsDoc)), "PB-DS-7")

	// THE INVARIANT EVERY LINE BELOW RESTS ON, ASSERTED OVER THE REAL SOURCES. s23ScanAlignmentFault
	// was exercised only over four synthetic strings, in a control -- while its own comment says the
	// consequence of it being false is "silent and total". Four hand-written examples cannot say
	// anything about the twelve files this gate actually reads: if kotlinCodeOnly drops or adds a
	// line on any one of them, every constant in that file is joined to its neighbour's `origin:`
	// annotation and the gate compares the dot's diameter against the glow's radius, green.
	//
	// FATAL, not an error, and over EVERY kit source rather than the owned eleven: the helper is
	// shared, a drift on any file in the package is the same bug, and no comparison below means
	// anything once the two views disagree.
	for _, file := range s23SortedKeys(sources) {
		if fault := s23ScanAlignmentFault(sources[file]); fault != "" {
			t.Fatalf("PB-DS-7: %s: %s", file, fault)
		}
	}

	checked := 0
	for file, src := range sources {
		if !owned[file] {
			continue
		}
		for _, m := range s23ScanMetrics(src) {
			checked++
			if fault := s23CheckMetric(m, css, tokens, doc); fault != "" {
				t.Errorf("PB-DS-7: %s:%d: %s", file, m.Line, fault)
			}
		}
	}
	if checked == 0 {
		t.Error("PB-DS-7: no metric constant was scanned at all; either the kit declares none (and " +
			"every component's fixed sizes came from somewhere unstated) or the constant pattern " +
			"stopped matching -- which is the shape of the defect this scan already shipped once, " +
			"when `private` was not one of the modifiers it recognised")
	}
}

// s23CSSMetric reads the first px length out of one declaration. One rule for every site --
// `width: 7px`, `border: 1px solid var(--p-hair)`, `box-shadow: 0 0 9px color-mix(...)`,
// `backdrop-filter: blur(16px)` -- because the alternative is a parser per property and four
// chances to read the wrong field.
func s23CSSMetric(css map[string]s22bCSSRule, selector, property string) (float64, error) {
	rule, ok := css[selector]
	if !ok {
		return 0, fmt.Errorf("the shared block declares no such rule")
	}
	value, ok := rule.Decls[property]
	if !ok {
		return 0, fmt.Errorf("the rule declares no %s", property)
	}
	m := s23PxRe.FindStringSubmatch(value)
	if m == nil {
		return 0, fmt.Errorf("%q carries no px length", value)
	}
	return strconv.ParseFloat(m[1], 64)
}

// s23TokenMetric reads a part out of an `effect` token's value.
func s23TokenMetric(tokens map[string]string, token, part string) (float64, error) {
	value, ok := tokens[token]
	if !ok {
		return 0, fmt.Errorf("the token origin declares no %s", token)
	}
	var m []string
	switch part {
	case "px":
		m = s23PxRe.FindStringSubmatch(value)
	case "alpha":
		m = s23RGBARe.FindStringSubmatch(value)
	case "stop":
		m = s23StopRe.FindStringSubmatch(value)
	case "opacity":
		// THE WHOLE VALUE, ANCHORED. A substring match would read `0.04` out of any token that
		// happened to contain those characters, which is how a reader stops being about the token
		// it names; a token cited as an opacity that is not a bare number is a citation error and
		// says so below.
		m = s23OpacityRe.FindStringSubmatch(value)
	}
	if m == nil {
		return 0, fmt.Errorf("%q carries no %s", value, part)
	}
	v, err := strconv.ParseFloat(m[1], 64)
	if err != nil {
		return 0, err
	}
	if part == "stop" {
		v /= 100
	}
	return v, nil
}

// ---------------------------------------------------------------------------
// PB-DS-12: the touch-target floor, joined to the rows that state it.
// ---------------------------------------------------------------------------

// s23TouchTarget is one row that states a minimum touch target, and the factory that carries it.
//
// THE ASSIGNMENT IS A CLAIM AND NOT A DERIVATION, which is why it is checked in rather than
// computed. A row states a floor for the CONTROL it describes, and a control is not always the
// factory named on the same line: row 22 specifies a read-only note and states the floor for the
// `[Take control]` button that row turns into a standalone `.a2-more`, which is `ctaButton`. A gate
// that joined each row to the component citing it would demand a 48 dp target on a centred sentence
// and let the button beside it have none -- the two failure modes that look identical from a
// distance and are opposite up close.
//
// What the gate does NOT let this table do is drop one. Every row any kit component cites is read,
// and a row stating a floor that appears in no entry here fails: assigning a target is a reviewed
// act, ignoring one is not available.
type s23TouchTarget struct {
	Row     string
	Factory string
	Why     string
}

// s23TouchTargets is every stated floor in the derivation table, and where it reaches a pixel.
var s23TouchTargets = []s23TouchTarget{
	{
		Row:     "#4 Toggle",
		Factory: "settingsRow",
		Why: "row 4 says \">=48 WITH THE VISUAL UNCHANGED\" and row 15 says where: \"the whole " +
			"row is one >=48 dp target when it carries a toggle\". The two are one instruction. A " +
			"46x28 control grown to 48 satisfies the number by destroying the drawing the same " +
			"clause protects, and the toggle does not handle its own tap in any case -- the row " +
			"it sits in is the control, so the row is where the floor is spent.",
	},
	{
		Row:     "#9 Composer",
		Factory: "textField",
		Why: "row 9 is the only row that states a target and a SMALLER visual in the same cell -- " +
			"\"visual height 36, touch target 48\" -- so the field is 48 dp of target around 36 dp " +
			"of well, and both numbers are the row's.",
	},
	{
		Row:     "#13 Paired-device row",
		Factory: "denyChip",
		Why: "row 13 ends the revoke control's cell with \"48 dp target\". It states the chip's " +
			"padding, radius and label style and no smaller visual, so unlike row 9 the floor is " +
			"the control's own box.",
	},
	{
		Row:     "#15 Settings row",
		Factory: "settingsRow",
		Why: "\"The whole row is one >=48 dp target when it carries a toggle\" -- the same floor " +
			"row 4 defers to this component, stated from this side.",
	},
	{
		Row:     "#22 Read-only note",
		Factory: "ctaButton",
		Why: "row 22's floor is NOT the note's. The row turns `[Take control]` from an inline span " +
			"into a standalone tertiary button precisely because \"an inline span cannot carry a " +
			"48 dp target (PB-DS-12)\", and states that button as `.a2-more` unchanged: `--p-card`, " +
			"1 dp `--p-hair`, `--p-btn-r` 9, `Label.Button` / `--p-ink`, padding `space_12`, min " +
			"48. That is `ctaButton(kind = MORE)`, which is what ReadOnlyNote.kt's own KDoc says " +
			"builds it.",
	},
	{
		Row:     "§4 Drill-down nav header",
		Factory: "navHeaderDrill",
		Why: "§4 gives the back control a 24 dp chevron, a label and a \"48 dp target\". The " +
			"target is the BACK CONTROL's and not the header's, which is why the header's own " +
			"height is not the subject: a floor on the container is the wrapper that satisfies a " +
			"rule while the thing under the finger stays 24 dp.",
	},
}

// TestPBDS12_EveryStatedTouchTargetIsSpentByItsComponent joins PB-DS-12's floor to the rows.
//
// THE DEFECT IT CLOSES IS "SPECIFIED EVERYWHERE, ENFORCED NOWHERE". Six rows state a minimum touch
// target, in four spellings, and until the metric reader learned to read an inequality none of them
// was joined to anything -- so the accessibility floor was prose in a table, and every one of the
// six components shipped without it while every gate reported ok.
func TestPBDS12_EveryStatedTouchTargetIsSpentByItsComponent(t *testing.T) {
	sources := s23KitSources(t)
	doc := readFileOrFail(t, filepath.Join(repoRoot(t), filepath.FromSlash(s23ComponentsDoc)), "PB-DS-12")

	files := map[string]string{}
	for _, c := range s23Inbox {
		files[c.Factory] = c.File
	}

	for _, target := range s23TouchTargets {
		file, known := files[target.Factory]
		if !known {
			t.Errorf("PB-DS-12: `%s` assigns its touch target to %s(), which no s23Inbox row names",
				target.Row, target.Factory)
			continue
		}
		row, found := s23FindRow(doc, target.Row)
		if !found {
			t.Errorf("PB-DS-12: `%s` is not a row in %s. A floor assigned to a row nobody can find "+
				"is a floor nobody can check.", target.Row, s23ComponentsDoc)
			continue
		}
		stated, err := s23DocMinTarget(row)
		if err != nil {
			t.Errorf("PB-DS-12: `%s` is claimed to state a minimum touch target for %s(): %v",
				target.Row, target.Factory, err)
			continue
		}
		if stated != float64(s23MinTargetDp(t)) {
			t.Errorf("PB-DS-12: `%s` states a %g dp target and KitMetrics.MIN_TARGET_DP is %g. One "+
				"floor differing between two controls is not a floor; either the row moved or the "+
				"constant did.", target.Row, stated, s23MinTargetDp(t))
		}
		if !strings.Contains(kotlinCodeOnly(sources[file]), s23MinTargetConst) {
			t.Errorf("PB-DS-12: `%s` states a %g dp minimum touch target for %s(), and %s spends no "+
				"%s. The floor is in the design and not on the screen, which is the whole of this "+
				"defect: a control smaller than a finger refuses taps that were aimed at it.\n\t%s",
				target.Row, stated, target.Factory, file, s23MinTargetConst, target.Why)
		}
	}
}

// TestPBDS12_NoStatedTouchTargetIsUnassigned is the direction that makes the table above a fence
// rather than a list of six good intentions.
//
// FORWARD would pass with an empty table. Every row the kit cites is read here, and a row that
// states a floor and appears in no entry fails -- so a seventh component whose row states a target
// cannot be added without someone deciding which factory carries it. The reverse of the reverse is
// also checked: a factory spending the constant with no row behind it is a number somebody chose.
func TestPBDS12_NoStatedTouchTargetIsUnassigned(t *testing.T) {
	sources := s23KitSources(t)
	doc := readFileOrFail(t, filepath.Join(repoRoot(t), filepath.FromSlash(s23ComponentsDoc)), "PB-DS-12")

	assigned := map[string]bool{}
	spenders := map[string]bool{}
	for _, target := range s23TouchTargets {
		assigned[target.Row] = true
		spenders[target.Factory] = true
	}

	read := 0
	for _, c := range s23Inbox {
		if c.Derived == "" {
			continue
		}
		row, found := s23FindRow(doc, c.Derived)
		if !found {
			continue // TestPBDS7_EveryDerivationCitationResolvesToARow owns that fault.
		}
		read++
		if _, err := s23DocMinTarget(row); err != nil {
			continue
		}
		if !assigned[c.Derived] {
			t.Errorf("PB-DS-12: `%s`, cited by %s(), states a minimum touch target and no "+
				"s23TouchTargets entry claims it. A floor nobody was assigned is the state this "+
				"whole check exists to end -- add the entry naming the factory that carries it, "+
				"which is a decision rather than a lookup.", c.Derived, c.Factory)
		}
	}
	if read == 0 {
		t.Fatal("PB-DS-12: no derivation row was read, so this direction passed over an empty set")
	}

	// SCOPED TO THE FILE AND NOT TO THE FACTORY, because one file can hold two of them.
	// SettingsRow.kt declares `settingsRow` and `statusLabel`, and only the first carries a floor;
	// a per-factory reading of the same text accuses the second of spending a constant its
	// neighbour wrote. What a text scan can honestly say is that SOMETHING in this file was
	// assigned a target, and TestPBDS12_EveryStatedTouchTargetIsSpentByItsComponent is what says
	// the assignment was met.
	claimed := map[string]bool{}
	for _, c := range s23Inbox {
		if spenders[c.Factory] {
			claimed[c.File] = true
		}
	}
	for file, src := range sources {
		if claimed[file] || !strings.Contains(kotlinCodeOnly(src), s23MinTargetConst) {
			continue
		}
		t.Errorf("PB-DS-12: %s spends %s and no s23TouchTargets entry assigns a factory in it a "+
			"touch target. A floor with no design behind it is a size somebody chose, which is "+
			"what this package exists to prevent.", file, s23MinTargetConst)
	}
}

// s23MinTargetConst is the spend the check above looks for. It is the CONSTANT and not the number:
// a file typing 48 at a call site is TestPBDS7_NoMetricIsTypedAtADpCallSite's fault, and this one
// is about whether the floor the design states reaches the component at all.
const s23MinTargetConst = "KitMetrics.MIN_TARGET_DP"

// s23MinTargetDp is the value the kit declares for the floor, read from the kit rather than typed
// here -- TestPBDS7_EveryKitMetricIsTheDesignsOwnNumber is what holds it to row 4.
func s23MinTargetDp(t *testing.T) float64 {
	t.Helper()
	for _, m := range s23ScanMetrics(s23KitSources(t)["Kit.kt"]) {
		if m.Name == "MIN_TARGET_DP" {
			v, err := strconv.ParseFloat(m.Raw, 64)
			if err != nil {
				t.Fatalf("PB-DS-12: MIN_TARGET_DP = %s is not a number", m.Raw)
			}
			return v
		}
	}
	t.Fatal("PB-DS-12: the kit declares no MIN_TARGET_DP, so PB-DS-12's floor has no value at all")
	return 0
}

// TestPBDS12_TheTouchTargetReaderCanActuallyFail is the negative control the acceptance criteria
// name: proof that the reader reads the ROW.
//
// IT DRIVES [s23DocMinTarget], the function the two assertions above call, for the reason
// s23Metric's own comment gives -- a control that rebuilt the parse inline would prove the copy
// works and say nothing about the fence.
func TestPBDS12_TheTouchTargetReaderCanActuallyFail(t *testing.T) {
	// The four spellings the document actually uses, each read out of the row it comes from.
	for _, probe := range []struct{ name, row string }{
		{"row 4's inequality", "| 4 | Toggle | track 46x28, thumb 24, touch target >=48 with the visual unchanged (PB-DS-12) |"},
		{"row 9's bare value", "| 9 | Composer | visual height 36, touch target 48; glyphs 26 |"},
		{"row 13's number-first form", "| 13 | Paired-device row | `Label.Chip`, 48 dp target |"},
		{"row 22's `min`", "| 22 | Read-only note | padding `space_12`, min 48 |"},
	} {
		got, err := s23DocMinTarget(probe.row)
		if err != nil || got != 48 {
			t.Errorf("PB-DS-12: %s reads as (%g, %v), not 48. A spelling the reader cannot see is "+
				"a floor that is stated and unchecked, which is the defect this reader exists to "+
				"end.", probe.name, got, err)
		}
	}

	// A ROW THAT STATES NO TARGET MUST NOT PRODUCE ONE. This is the half that keeps the patterns
	// from being widened into a number-finder: twenty rows of this table state dimensions near the
	// word "target" is not in, and every one of them would otherwise acquire a floor nobody wrote.
	for _, probe := range []struct{ name, row string }{
		{"row 11's dot", "| 11 | Machine row | Presence dot 7 dp, `--p-dot-r` |"},
		{"row 14's retired fixed column", "| 14 | Activity row | the timestamp column is wrap-content, not the mock's fixed 52 dp |"},
		{"row 8's padding", "| 8 | Empty state | padding 48 (2 x `space_24`) vertical |"},
	} {
		if got, err := s23DocMinTarget(probe.row); err == nil {
			t.Errorf("PB-DS-12: %s produced a %g dp touch target and states none. A reader that "+
				"finds a floor in a padding would hold a component to a number the design never "+
				"wrote.", probe.name, got)
		}
	}

	// A ROW THAT CONTRADICTS ITSELF IS A MISS, NOT A CHOICE -- s23FindRow's rule, one value down.
	both := "| 9 | Composer | touch target 48; the voice glyph takes a 44 dp target |"
	if got, err := s23DocMinTarget(both); err == nil {
		t.Errorf("PB-DS-12: a row stating two different floors read as %g. Returning either is how "+
			"a control held to the smaller one stays green.", got)
	}

	// And the join itself: the value has to come from the row rather than from the constant.
	if got, _ := s23DocMinTarget("| 4 | Toggle | touch target >=44 with the visual unchanged |"); got == s23MinTargetDp(t) {
		t.Errorf("PB-DS-12: a row stating 44 reads as the kit's own %g, so the comparison in "+
			"TestPBDS12_EveryStatedTouchTargetIsSpentByItsComponent is a constant against itself",
			got)
	}
}

// ---------------------------------------------------------------------------
// PB-DS-6: one token, one rendering. The dp call sites.
// ---------------------------------------------------------------------------

// s23DpSpend is one `Kit.dp(...)` or `Kit.dpPx(...)` call: which KitMetrics constant it spends,
// and which of the two quantisations it spends it through.
type s23DpSpend struct {
	File     string
	Line     int
	Accessor string // "dp" (Float, exact) or "dpPx" (Int, rounded the way the platform rounds)
	Metric   string // the KitMetrics constant, or "" when the argument is not one
	Argument string
}

var s23DpCall = regexp.MustCompile(`\bKit\.(dp|dpPx)\s*\(`)

var s23MetricRef = regexp.MustCompile(`^KitMetrics\.([A-Za-z_][A-Za-z0-9_]*)$`)

// s23ScanDpSpends reads every dp call site out of one COMMENT-STRIPPED source.
//
// It reuses s23CallArguments rather than a regexp for the same reason
// TestPBDS6_NoRawDimensionIsTypedInTheKit does: `Kit.dp(context, KitMetrics.DOT_DP)` is the short
// case and `Kit.dpPx(context, KitMetrics.GLOW_RADIUS_DP)` inside a larger call is the normal one,
// and `[^)]*` stops at the first close paren, which is inside the wrong call.
func s23ScanDpSpends(file, code string) []s23DpSpend {
	var out []s23DpSpend
	for _, loc := range s23DpCall.FindAllStringSubmatchIndex(code, -1) {
		spend := s23DpSpend{
			File:     file,
			Line:     strings.Count(code[:loc[0]], "\n") + 1,
			Accessor: code[loc[2]:loc[3]],
		}
		args := s23CallArguments(code, loc[1]-1)
		if len(args) >= 2 {
			spend.Argument = args[1]
			if m := s23MetricRef.FindStringSubmatch(args[1]); m != nil {
				spend.Metric = m[1]
			}
		}
		out = append(out, spend)
	}
	return out
}

// s23DpLiteralFaults reports every dp call site whose value is not a named KitMetrics constant.
//
// THIS IS THE HOLE NO DECLARATION RECOGNISER CAN REACH. s23MetricConst was widened until every
// spelling of a DECLARED number is seen, and `Kit.dp(context, 7f)` declares nothing at all -- the
// metric never becomes a property, so it never acquires an `origin:` line, and the fence that
// exists to join every number in this kit to the design never gets a chance to look at it. It is
// also the shortest way to write the mistake, which is what makes it the likely one.
func s23DpLiteralFaults(spends []s23DpSpend) []string {
	var faults []string
	for _, s := range spends {
		if s.Metric != "" {
			continue
		}
		faults = append(faults, fmt.Sprintf("%s:%d: Kit.%s(..., %s) spends a value that is not a "+
			"KitMetrics constant. A number typed at a dp call site never becomes a property, so it "+
			"never carries an `origin:` annotation and nothing in this gate can join it to the "+
			"design -- it is the one shape of metric that no declaration scan, at any width, can "+
			"see.", s.File, s.Line, s.Accessor, s.Argument))
	}
	return faults
}

// s23DualQuantised names the constants a component legitimately spends BOTH ways, and why.
//
// Kit.dp is exact and Kit.dpPx is the platform's own rounding, and for most constants exactly one
// of those is right. For three of them both are, because the constant describes two different
// quantities that happen to share a number -- and a fence that simply banned the float form would
// be wrong about all three. So the claim is written down, per constant, and the fence requires the
// claim rather than inferring it: a constant that acquires a second quantisation without a row
// here is the "same token, two renderings" defect, which is what this table exists to catch.
var s23DualQuantised = map[string]string{
	"DOT_DP": "the dot is LAID OUT at whole pixels (Kit.dpPx into LayoutParams) and DRAWN as a " +
		"circle whose diameter is a float on a canvas. The layout box and the ink are two " +
		"quantities, and rounding the ink would move the mark off the centre of its own box.",
	"GLOW_RADIUS_DP": "the same split: the halo's ROOM in the layout box is whole pixels, its blur " +
		"radius is Paint.setShadowLayer's float, and a blur radius is meaningful below one pixel " +
		"in a way a layout dimension is not.",
	"PRESENCE_DOT_DP": "the same split again: setBounds takes the whole-pixel box, the drawable " +
		"draws its own diameter.",
	"CTA_BLOOM_DP": "GLOW_RADIUS_DP's split, on the other component that converts a CSS box-shadow. " +
		"The bloom's ROOM -- the inflation of the button's box and the negative margin that gives " +
		"it back -- is a layout dimension and is whole pixels; the value handed to " +
		"Paint.setShadowLayer is a blur radius, which is meaningful below one pixel in a way a " +
		"padding is not. The derivation table's status-dot row names the two conversions as one " +
		"(" + `"the same conversion as --p-cta-fx"` + "), so they answer to the same rule here too.",
}

// s23QuantisationFaults reports every constant rendered two ways without a reason on record.
func s23QuantisationFaults(spends []s23DpSpend) []string {
	accessors := map[string]map[string][]string{}
	for _, s := range spends {
		if s.Metric == "" {
			continue
		}
		if accessors[s.Metric] == nil {
			accessors[s.Metric] = map[string][]string{}
		}
		accessors[s.Metric][s.Accessor] = append(accessors[s.Metric][s.Accessor],
			fmt.Sprintf("%s:%d", s.File, s.Line))
	}

	var faults []string
	for _, metric := range s23SortedKeys(accessors) {
		if len(accessors[metric]) < 2 {
			continue
		}
		if _, declared := s23DualQuantised[metric]; declared {
			continue
		}
		faults = append(faults, fmt.Sprintf("KitMetrics.%s is spent through Kit.dp (exact: %s) AND "+
			"through Kit.dpPx (rounded: %s). One design value, two renderings -- at density 2.625 a "+
			"1dp length is 2.625px one way and 3px the other, so the same rule paints differently in "+
			"two places on one screen. Either spend it one way, or add a row to s23DualQuantised "+
			"saying which two quantities it describes.",
			metric,
			strings.Join(accessors[metric]["dp"], ", "),
			strings.Join(accessors[metric]["dpPx"], ", ")))
	}

	// And the table must not outlive its reason. A row for a constant that is no longer spent both
	// ways is a standing permission nobody is using, which is how the next dual spend gets waved
	// through without anyone arguing for it.
	for _, metric := range s23SortedKeys(s23DualQuantised) {
		if _, spent := accessors[metric]; !spent {
			continue
		}
		if len(accessors[metric]) < 2 {
			faults = append(faults, fmt.Sprintf("s23DualQuantised permits KitMetrics.%s to be "+
				"rendered two ways, and the kit now spends it only through Kit.%s. Delete the row: "+
				"a permission nobody uses is one the next dual spend inherits without argument.",
				metric, s23SortedKeys(accessors[metric])[0]))
		}
	}
	return faults
}

func s23SortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// s23KitDpSpends reads every dp call site in the files this slice owns.
func s23KitDpSpends(t *testing.T) []s23DpSpend {
	t.Helper()
	owned := s23OwnedFiles()
	sources := s23KitSources(t)
	var out []s23DpSpend
	for _, file := range s23SortedKeys(sources) {
		if !owned[file] {
			continue
		}
		out = append(out, s23ScanDpSpends(file, kotlinCodeOnly(sources[file]))...)
	}
	return out
}

// TestPBDS7_NoMetricIsTypedAtADpCallSite closes the one hole the declaration scan cannot reach.
func TestPBDS7_NoMetricIsTypedAtADpCallSite(t *testing.T) {
	spends := s23KitDpSpends(t)
	if len(spends) == 0 {
		t.Fatal("PB-DS-7: the kit makes no Kit.dp or Kit.dpPx call at all, so this scan says " +
			"nothing -- and the six numbers the resource table cannot carry reach the screen " +
			"through those two functions and nowhere else")
	}
	for _, fault := range s23DpLiteralFaults(spends) {
		t.Errorf("PB-DS-7: %s", fault)
	}
}

// TestPBDS6_EveryKitMetricIsRenderedOneWay is "one token, one rendering".
//
// THE DEFECT IT WAS WRITTEN FOR. `cardSurface` and `chipSurface` spend KitMetrics.HAIRLINE_DP
// through Kit.dpPx, which is 3px on a 420dpi handset; TabBar.kt spent the same constant through
// Kit.dp, which is 2.625. Three 1dp hairlines drawn from one token -- the card's border, the
// chip's border and the tab bar's top rule -- rendered at two different widths on the same screen,
// and the only depth cue Substrate has is that line.
func TestPBDS6_EveryKitMetricIsRenderedOneWay(t *testing.T) {
	for _, fault := range s23QuantisationFaults(s23KitDpSpends(t)) {
		t.Errorf("PB-DS-6: %s", fault)
	}
}

// ---------------------------------------------------------------------------
// PB-DS-7: the two cross-checks that make an UNRECOGNISED declaration visible.
// ---------------------------------------------------------------------------
//
// WHY THIS SECTION EXISTS. s23MetricConst is a regexp, so there will always be a way to declare a
// number that it does not match, and for three rounds the consequence of that was SILENCE: the
// declaration carried no origin, was compared to nothing, and failed no assertion however wrong
// its value was. Each round the repair was to widen the pattern and claim the widened version was
// complete; each round the next reviewer wrote a spelling nobody had listed.
//
// Neither check below looks at declaration syntax. They ask what happens to the number afterwards
// -- it is spent, or it is a token in a fenced file -- and an unrecognised declaration fails them
// for the same reason whatever it is spelled like: it is not in the set s23ScanMetrics returned.
// That is the property a widening can never have, because a widening is a longer list.

// s23MetricsObject is the object a SPEND must resolve into.
//
// THE SCOPE IS LOAD-BEARING. s23ScanMetrics also sees `ColorMix.TRANSPARENT` and TabBar's
// `badgeCount`, and taking the union of every declaration in the package as the resolvable set
// would let a local `val EXTRA_PAD_DP = 11f` in any file satisfy `KitMetrics.EXTRA_PAD_DP` --
// which is the fence checking that some name exists somewhere rather than that THIS constant was
// seen where it is declared.
const s23MetricsObject = "KitMetrics"

var s23MetricsObjectHead = regexp.MustCompile(`\bobject\s+` + s23MetricsObject + `\s*\{`)

// s23ObjectLines returns the 1-based line span of `object KitMetrics { ... }` in a
// comment-stripped, string-blanked source. ok is false when the source does not declare it.
func s23ObjectLines(code string) (lo, hi int, ok bool) {
	head := s23MetricsObjectHead.FindStringIndex(code)
	if head == nil {
		return 0, 0, false
	}
	lo = strings.Count(code[:head[0]], "\n") + 1
	depth := 0
	for i := head[1] - 1; i < len(code); i++ {
		switch code[i] {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return lo, strings.Count(code[:i], "\n") + 1, true
			}
		}
	}
	return 0, 0, false
}

// s23SeenMetricNames is the set of constants s23ScanMetrics RECOGNISED inside object KitMetrics.
//
// @return the set, and the reason it could not be built. A missing object is fatal rather than an
// empty set: an empty set makes every spend dangle, which reads as a hundred faults for one cause.
func s23SeenMetricNames(sources map[string]string, owned map[string]bool) (map[string]bool, string) {
	for _, file := range s23SortedKeys(sources) {
		if !owned[file] {
			continue
		}
		lo, hi, ok := s23ObjectLines(s23CodeNoStrings(kotlinCodeOnly(sources[file])))
		if !ok {
			continue
		}
		seen := map[string]bool{}
		for _, m := range s23ScanMetrics(sources[file]) {
			if m.Line >= lo && m.Line <= hi {
				seen[m.Name] = true
			}
		}
		return seen, ""
	}
	return nil, fmt.Sprintf("no kit source this slice owns declares `object %s`, so there is no set "+
		"of recognised constants for a spend to resolve into and this cross-check would pass by "+
		"having nothing to say", s23MetricsObject)
}

// s23MetricSpend is one `KitMetrics.<ident>` reference, wherever it appears.
//
// NOT ONLY AT dp CALL SITES. `fadeStop = KitMetrics.WORKBAR_FADE_STOP` and
// `ColorMix.withAlpha(Color.WHITE, KitMetrics.KEY_LIGHT_ALPHA)` spend a constant without going
// near Kit.dp, and a scan bounded by dp calls would miss the two constants that are not lengths.
type s23MetricSpend struct {
	File string
	Line int
	Name string
}

var s23MetricSpendRe = regexp.MustCompile(`\b` + s23MetricsObject + `\.([A-Za-z_][A-Za-z0-9_]*)`)

// s23ScanMetricSpends reads every KitMetrics reference out of one source that has already had its
// comments stripped and its string contents blanked -- see s23KitMetricSpends for why both.
func s23ScanMetricSpends(file, code string) []s23MetricSpend {
	var out []s23MetricSpend
	for _, loc := range s23MetricSpendRe.FindAllStringSubmatchIndex(code, -1) {
		out = append(out, s23MetricSpend{
			File: file,
			Line: strings.Count(code[:loc[0]], "\n") + 1,
			Name: code[loc[2]:loc[3]],
		})
	}
	return out
}

// s23DanglingSpendFaults reports every spend of a constant the scan did not recognise.
//
// IT IS NOT THE COMPILER'S CHECK, and the distinction is the whole point. kotlinc resolves
// `KitMetrics.EXTRA_PAD_DP` perfectly well when the constant is declared across two lines; what
// this reports is that THE GATE cannot see the declaration, and therefore that the number behind
// a name the app is already spending has no design authority anyone has checked. A dangling
// reference here means "the scan is blind here", which is the fact that was silent three times.
func s23DanglingSpendFaults(spends []s23MetricSpend, seen map[string]bool) []string {
	var faults []string
	for _, s := range spends {
		if seen[s.Name] {
			continue
		}
		faults = append(faults, fmt.Sprintf("%s:%d: %s.%s is spent here, and s23ScanMetrics "+
			"recognised no declaration of %s inside `object %s`. Either it is not declared at all "+
			"(the Kotlin compiler will say so first) or -- far more likely -- it IS declared, in a "+
			"spelling s23MetricConst does not match: a wrapped initialiser, a `get()` accessor, a "+
			"hex literal, an expression. A declaration the scan cannot see carries no `origin:`, is "+
			"compared to nothing and fails no other assertion, so its value reaches the screen "+
			"unchecked. Write it in a form the scan reads, or widen s23MetricConst to read this one.",
			s.File, s.Line, s23MetricsObject, s.Name, s.Name, s23MetricsObject))
	}
	return faults
}

// s23KitMetricSpends reads every KitMetrics reference in the files this slice owns.
//
// COMMENTS AND STRING CONTENTS ARE BOTH GONE FIRST. A KDoc naming `KitMetrics.DOT_DP` in a
// sentence is not a spend, and neither is a string that happens to quote one -- reporting either
// as a dangling reference would be this fence failing on its own documentation, which is the
// defect kotlinCodeOnly was written for.
func s23KitMetricSpends(sources map[string]string, owned map[string]bool) []s23MetricSpend {
	var out []s23MetricSpend
	for _, file := range s23SortedKeys(sources) {
		if !owned[file] {
			continue
		}
		out = append(out, s23ScanMetricSpends(file, s23CodeNoStrings(kotlinCodeOnly(sources[file])))...)
	}
	return out
}

// s23Literal is one numeric literal token as it appears in the kit's code.
type s23Literal struct {
	File string
	Line int
	Text string
}

// s23NumberToken matches a Kotlin numeric literal at the START of the string it is given.
// s23ScanLiterals does the token boundary itself, because Go's regexp has no lookbehind and
// `R.dimen.swarm_space_14` must not read as the number 14.
var s23NumberToken = regexp.MustCompile(
	`^(?:0[xXbB][0-9a-fA-F_]+|[0-9][0-9_]*(?:\.[0-9][0-9_]*)?(?:[eE][+-]?[0-9]+)?)[uU]?[lLfF]?`)

// s23CodeNoStrings blanks the CONTENTS of string and character literals, keeping the quotes and
// every newline, so a source stays line-for-line addressable.
//
// kotlinCodeOnly deliberately leaves strings intact -- a fence that a comment can satisfy is the
// defect it was written for, and a string is code. For counting NUMBERS the opposite is true:
// `text = if (count >= 100) "99+" else ...` holds one number and one piece of copy, and reading
// the copy as a metric would put `99` on the exemption table for no reason.
func s23CodeNoStrings(code string) string {
	var out strings.Builder
	out.Grow(len(code))
	var quote byte
	for i := 0; i < len(code); i++ {
		c := code[i]
		switch {
		case quote == 0 && (c == '"' || c == '\''):
			quote = c
			out.WriteByte(c)
		case quote != 0 && c == '\\' && i+1 < len(code):
			out.WriteString("  ")
			i++
		case quote != 0 && c == quote:
			quote = 0
			out.WriteByte(c)
		case quote != 0:
			if c == '\n' {
				out.WriteByte(c)
			} else {
				out.WriteByte(' ')
			}
		default:
			out.WriteByte(c)
		}
	}
	return out.String()
}

// s23ScanLiterals reads every numeric literal token out of a comment-stripped, string-blanked
// source. A digit preceded by an identifier character is part of a name, not a number.
func s23ScanLiterals(file, code string) []s23Literal {
	var out []s23Literal
	line := 1
	for i := 0; i < len(code); i++ {
		c := code[i]
		if c == '\n' {
			line++
			continue
		}
		if c < '0' || c > '9' {
			continue
		}
		if i > 0 && (code[i-1] == '.' || code[i-1] == '_' || s23IsIdentByte(code[i-1])) {
			continue
		}
		text := s23NumberToken.FindString(code[i:])
		if text == "" {
			continue
		}
		out = append(out, s23Literal{File: file, Line: line, Text: text})
		i += len(text) - 1
	}
	return out
}

func s23IsIdentByte(c byte) bool {
	return c >= '0' && c <= '9' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z'
}

// s23NormaliseLiteral reduces a literal token to the digits s23MetricConst captures: underscores
// gone, type suffix gone. A hex or binary literal keeps its prefix, because no recognised
// declaration can produce one and it must never compare equal to a decimal.
func s23NormaliseLiteral(text string) string {
	text = strings.ReplaceAll(text, "_", "")
	if len(text) > 1 && (text[1] == 'x' || text[1] == 'X' || text[1] == 'b' || text[1] == 'B') {
		return text
	}
	return strings.TrimRight(strings.TrimRight(text, "fFdDlL"), "uU")
}

func s23LiteralKey(file string, line int, value string) string {
	return fmt.Sprintf("%s:%d:%s", file, line, value)
}

// s23BoundLiterals is every literal that IS the value of a declaration the scan recognised, keyed
// by where it is. Position rather than value, so `2f` being a checked constant on one line does
// not excuse a `2f` typed on another.
func s23BoundLiterals(sources map[string]string, owned map[string]bool) map[string]bool {
	bound := map[string]bool{}
	for file, src := range sources {
		if !owned[file] {
			continue
		}
		for _, m := range s23ScanMetrics(src) {
			bound[s23LiteralKey(file, m.Line, strings.TrimPrefix(m.Raw, "-"))] = true
		}
	}
	return bound
}

// s23LiteralExemptions is every bare number the kit is allowed to type without a design origin,
// and why each one is not a design value.
//
// THIS IS THE SMALL LIST THE OTHER CHECK NEEDS, and it is small because the kit was written to
// make it small: sixty-two literal tokens across the eleven fenced files, seventeen of them the
// values of checked constants and the rest these nine spellings. Keyed by the literal AS WRITTEN
// rather than by value, so `0` and `0f` and `2` and `2f` are separate permissions -- an exemption
// is a thing a reader has to agree with, and "2 sides of a core" and "half a diameter" are two
// different arguments that happen to share a digit.
//
// WHAT AN EXEMPTION COSTS, AND WHERE IT IS NOT PAID. A row here is a value the fence stops
// reading -- `2f` is exempt because a radius is half a diameter, and the price is that a 2dp inset
// typed as a bare `2f` in a drawing routine is exempt too. That price is bounded three ways: by
// the other fences (a metric typed at a dp call site is TestPBDS7_NoMetricIsTypedAtADpCallSite's,
// a spacing value is TestPBDS6_NoRawDimensionIsTypedInTheKit's), by the list being nine rows
// rather than thirty, and by s23StrictLines -- INSIDE object KitMetrics no exemption is consulted
// at all, so the one place a metric is supposed to live pays nothing for this table's existence.
var s23LiteralExemptions = map[string]string{
	"0": "zero has no unit, so 0 px and 0 dp are the same distance and there is no design value " +
		"for it to disagree with. Used for a weighted width, a suppressed border, a zero padding.",
	"0f": "the same, as a Float: a zero shadow offset, a suppressed key light, a gradient's first " +
		"stop.",
	"1": "`1 - fraction` in ColorMix: the complement of a share. It is arithmetic on a ratio and " +
		"there is no length in it.",
	"1f": "a LinearLayout weight. Weight is a proportion between siblings, not a dimension -- the " +
		"design has no px for it because CSS expresses the same thing as `flex: 1`.",
	"2": "`corePx + 2 * haloPx`: the halo sits on BOTH sides of the core, so the 2 is a count of " +
		"sides. The dot's diameter and the halo's radius are both checked constants.",
	"2f": "half of a length the design states, at the TWO sites that take a half. `diameterPx / 2f` " +
		"is the status dot's radius; `trackHeightPx / 2f` and `thumbPx / 2f` are the toggle's, " +
		"where row 4 writes the exception out in those words -- \"radius = half the track (14) and " +
		"half the thumb (12)\". A radius is half a diameter by the definition of a circle rather " +
		"than by a decision anyone made, and row 4's pill is that definition applied to a capsule. " +
		"EVERY SITE IS NAMED rather than one being left to borrow another's argument, because " +
		"an exemption is a thing a reader has to agree with and a reader cannot agree with a use " +
		"the row does not mention. The other two are the same definition applied to a STROKE: " +
		"`strokePx / 2f` in FocusRingDrawable and in ScanReticleDrawable insets a stroke by half " +
		"its own width so that `Canvas.drawRoundRect`, which centres a stroke on the path it is " +
		"given, paints the stated width inside the bounds rather than half of it astride them. " +
		"ScanReticleDrawable takes a third half for the same reason a radius does -- a square " +
		"centred on a point reaches half its side in each direction.",
	"100": "Badge's overflow threshold. Three digits either overflow the 16 dp pill or push the " +
		"type below PB-DS-12's 10 sp floor, so the count saturates at `99+`; the 16 dp is the " +
		"checked constant and this is the consequence of it.",
	"255": "the top of the 8-bit channel range Color.argb takes. It is the platform's encoding of " +
		"a colour, not a number the design chose. THE SECOND SITE IS NAMED for the reason `2f` " +
		"gives: Drawable.setAlpha takes the same range, and CtaSurface spends it to stop the CTA's " +
		"halo painting when derivation row 24 removes `--p-cta-fx` from a dead button. A reader " +
		"cannot agree with a use the row does not mention.",
	"255f": "the same range in the float arithmetic that produces it.",
}

// s23UnaccountedLiteralFaults reports every number in the kit that is neither the value of a
// recognised declaration nor an exempt bare literal.
//
// THIS IS THE HALF OF THE INVERSION THAT DOES NOT NEED THE NUMBER TO BE SPENT. A constant
// declared in a spelling the scan misses and never referenced -- `const val SHADE = 0x1F4` -- is
// invisible to the spend cross-check and to s23CheckMetric alike, because nothing resolves it and
// nothing recognised it. Its LITERAL is still a token in a fenced file, and that is the one
// property no declaration syntax can take away from it.
//
// NO EXEMPTION APPLIES INSIDE object KitMetrics, and that closes the one case the value-keyed
// table would otherwise leave open. `const val QUIET_PAD_DP =\n    2f` is unrecognised (so no
// origin is required), unspent (so nothing dangles) and its literal is on the table (so the
// accounting passed it) -- three fences, all silent, for a number sitting in the middle of the
// object every fence exists to police. Inside that object the exemptions are simply not
// consulted: every number in it is a design metric by construction, which is what the object is.
func s23UnaccountedLiteralFaults(lits []s23Literal, bound, strict map[string]bool) []string {
	var faults []string
	for _, l := range lits {
		if bound[s23LiteralKey(l.File, l.Line, s23NormaliseLiteral(l.Text))] {
			continue
		}
		inObject := strict[fmt.Sprintf("%s:%d", l.File, l.Line)]
		_, exempt := s23LiteralExemptions[l.Text]
		if exempt && !inObject {
			continue
		}
		where := "and it is not one of the nine bare literals on s23LiteralExemptions"
		if inObject {
			where = fmt.Sprintf("and it is inside `object %s`, where s23LiteralExemptions does not "+
				"apply -- every number in that object is a design metric, which is the whole of what "+
				"the object is for", s23MetricsObject)
		}
		faults = append(faults, fmt.Sprintf("%s:%d: the number `%s` is typed here and nothing "+
			"accounts for it. It is not the value of a declaration s23ScanMetrics recognised, so no "+
			"`origin:` annotation is being required of it and no design value is being compared to "+
			"it; %s. If it is a design metric, declare it in object %s with an `origin:` line. If it "+
			"is not, add a row to s23LiteralExemptions saying what it is instead.",
			l.File, l.Line, l.Text, where, s23MetricsObject))
	}
	return faults
}

// s23StrictLines is every line inside object KitMetrics, keyed `file:line`. A number there is a
// design metric or it is a fault; there is no third answer, so no exemption is consulted.
func s23StrictLines(sources map[string]string, owned map[string]bool) map[string]bool {
	strict := map[string]bool{}
	for _, file := range s23SortedKeys(sources) {
		if !owned[file] {
			continue
		}
		lo, hi, ok := s23ObjectLines(s23CodeNoStrings(kotlinCodeOnly(sources[file])))
		if !ok {
			continue
		}
		for line := lo; line <= hi; line++ {
			strict[fmt.Sprintf("%s:%d", file, line)] = true
		}
	}
	return strict
}

// s23DeadExemptionFaults reports rows the kit no longer uses, for the reason s23DualQuantised
// gives: a permission nobody is exercising is one the next bare number inherits without argument.
//
// A LITERAL INSIDE object KitMetrics IS NOT A USE. s23UnaccountedLiteralFaults consults no
// exemption in the strict zone, so a `2f` in there exercises nothing -- counting it as use kept
// rows alive on the strength of an occurrence that never reads them, which is precisely the stale
// permission this check exists to report.
func s23DeadExemptionFaults(lits []s23Literal, strict map[string]bool) []string {
	used := map[string]bool{}
	for _, l := range lits {
		if strict[fmt.Sprintf("%s:%d", l.File, l.Line)] {
			continue
		}
		used[l.Text] = true
	}
	var faults []string
	for _, text := range s23SortedKeys(s23LiteralExemptions) {
		if !used[text] {
			faults = append(faults, fmt.Sprintf("s23LiteralExemptions permits the bare literal `%s` "+
				"and the kit types it nowhere. Delete the row: an exemption nobody uses is one the "+
				"next number of that value inherits without anyone arguing for it.", text))
		}
	}
	return faults
}

// ---------------------------------------------------------------------------
// PB-DS-7: a number that is not a numeric token.
// ---------------------------------------------------------------------------

// s23TextLiteral is one string or character literal as it appears in the kit's CODE.
//
// THIS IS THE REGION s23CodeNoStrings ERASES, AND ERASING IT WAS A HOLE THE WHOLE LANE FELL
// THROUGH. s23KitLiterals blanks string and char contents BEFORE s23ScanLiterals runs, for the good
// reason that helper gives -- `"99+"` is copy, not a metric. The consequence nobody had drawn is
// that the digits go with it. The fourth review spent, at live call sites in Badge.kt:
//
//	private val badgeMinWidthPx = "21".toInt()
//	private val probeCodePx     = '%'.code
//	private val probeConcatPx   = ("1" + "1").toFloat()
//
// 21 px, 37 px and 11f: three design metrics with no origin, compared to nothing, and the complete
// lane -- Go, vet, the manifest check and `./gradlew test --rerun-tasks` on both variants -- was
// green. s23ScanLiterals cannot see them because by the time it runs the digits are spaces, and no
// declaration recogniser reaches them because `"21".toInt()` is not a numeric literal.
//
// TWO RULES, BECAUSE NEITHER ONE COVERS THE OTHER'S CASE. `'%'.code` carries no digit anywhere in
// the source, so a content rule cannot see it; `("1" + "1").toFloat()` has the conversion applied to
// a parenthesised expression rather than to the literal, so a receiver rule cannot see it. Both
// spellings were demonstrated, so both rules are here.
type s23TextLiteral struct {
	File string
	Line int
	Text string // as written, quotes included
	// Receiver is true when the literal is immediately followed by `.` or `[` -- that is, when it
	// is the thing a member access or an index is applied TO.
	Receiver bool
}

// s23ScanTextLiterals reads every string and char literal out of a COMMENT-STRIPPED source whose
// strings are still INTACT. The other scans in this file take the string-blanked view; this one is
// the only thing that looks at what was blanked.
func s23ScanTextLiterals(file, code string) []s23TextLiteral {
	var out []s23TextLiteral
	line := 1
	for i := 0; i < len(code); i++ {
		c := code[i]
		if c == '\n' {
			line++
			continue
		}
		if c != '"' && c != '\'' {
			continue
		}
		start, startLine := i, line
		for i++; i < len(code); i++ {
			if code[i] == '\\' {
				i++
				continue
			}
			if code[i] == '\n' {
				line++
				continue
			}
			if code[i] == c {
				break
			}
		}
		if i >= len(code) {
			break // unterminated; the Kotlin compiler reports this one first
		}
		next := byte(0)
		if i+1 < len(code) {
			next = code[i+1]
		}
		out = append(out, s23TextLiteral{
			File:     file,
			Line:     startLine,
			Text:     code[start : i+1],
			Receiver: next == '.' || next == '[',
		})
	}
	return out
}

// s23TextLiteralExemptions is every string the kit types that CONTAINS a digit, and the argument
// that the digit is copy rather than a metric. Keyed by the literal exactly as written.
//
// EXACT TEXT, NOT A SHAPE. "a long human-readable message cannot plausibly be a metric" is a rule
// that fails open on the first short one, and "cannot plausibly" is the reasoning every hole in
// this file was made of. The cost is that editing an exempt string means editing its row, which is
// one line and is the same discipline s23LiteralExemptions already imposes.
var s23TextLiteralExemptions = map[string]string{
	`"99+"`: "Badge's saturated count. The 100 that produces it is on s23LiteralExemptions with its " +
		"argument; this is the text that gets drawn when the count exceeds it, and the 9s are two " +
		"glyphs rather than a quantity -- nothing measures them.",
	`"PB-TOK-8: $group is not a status.Group this kit can colour. The phone renders the "`: "the " +
		"requirement ID in a failure message. It names a row in the requirements table, which is " +
		"the one place a digit in this kit is an identifier rather than a length.",
	`".sheet2 .cmd"`: "a CSS SELECTOR, carried as a KitTag so the mono well says which rule it " +
		"renders. The 2 is part of Substrate's own class name -- `.sheet2` is the directions " +
		"page's inline sheet -- and no arithmetic can reach it. Every other KitTag names a " +
		"selector too; this is the first one whose selector happens to contain a digit, which is " +
		"why the table did not need this row until now.",
}

// s23TextLiteralFaults reports every string or char literal that could be carrying a number.
//
// NO EXEMPTION APPLIES INSIDE object KitMetrics, for the same reason s23UnaccountedLiteralFaults
// consults none there: every value in that object is a design metric by construction, so a string
// holding digits inside it is a metric written in the one notation the numeric scan cannot read.
func s23TextLiteralFaults(lits []s23TextLiteral, strict map[string]bool) []string {
	var faults []string
	for _, l := range lits {
		inObject := strict[fmt.Sprintf("%s:%d", l.File, l.Line)]
		if l.Receiver {
			faults = append(faults, fmt.Sprintf("%s:%d: the literal %s is the receiver of a member "+
				"access or an index. A string or a character is TEXT in this kit and nothing else -- "+
				"the kit types no method call on one anywhere -- so the only thing reaching into one "+
				"can be doing is turning it into a number that s23ScanLiterals will never see: "+
				"`\"21\".toInt()` is 21 px with no origin, `'%%'.code` is 37 px with no digit in the "+
				"source at all. If the number is a design metric, declare it in object %s with an "+
				"`origin:` line. If this literal genuinely needs a method called on it, that is a new "+
				"kind of use in this package and belongs in review, not in a widened pattern.",
				l.File, l.Line, l.Text, s23MetricsObject))
			continue
		}
		if !strings.ContainsAny(l.Text, "0123456789") {
			continue
		}
		if _, exempt := s23TextLiteralExemptions[l.Text]; exempt && !inObject {
			continue
		}
		where := "and it is not on s23TextLiteralExemptions"
		if inObject {
			where = fmt.Sprintf("and it is inside `object %s`, where s23TextLiteralExemptions does "+
				"not apply -- every value in that object is a design metric, and one written as text "+
				"is a metric in the one notation the numeric scan cannot read", s23MetricsObject)
		}
		faults = append(faults, fmt.Sprintf("%s:%d: the literal %s contains a digit, %s. The numeric "+
			"scan blanks string and char contents before it counts anything, so a number spelled in "+
			"here is accounted for by nothing: `(\"1\" + \"1\").toFloat()` is 11f that no fence in "+
			"this gate can see. If it is copy, add a row saying so. If it is a metric, declare it in "+
			"object %s with an `origin:` line.",
			l.File, l.Line, l.Text, where, s23MetricsObject))
	}
	return faults
}

// s23DeadTextExemptionFaults is s23DeadExemptionFaults' twin, for the same reason: a permission
// nobody exercises is one the next digit-bearing string inherits without anyone arguing for it.
func s23DeadTextExemptionFaults(lits []s23TextLiteral) []string {
	used := map[string]bool{}
	for _, l := range lits {
		used[l.Text] = true
	}
	var faults []string
	for _, text := range s23SortedKeys(s23TextLiteralExemptions) {
		if !used[text] {
			faults = append(faults, fmt.Sprintf("s23TextLiteralExemptions permits the literal %s and "+
				"the kit types it nowhere. Delete the row: an exemption nobody uses is one the next "+
				"string of that text inherits without anyone arguing for it.", text))
		}
	}
	return faults
}

// s23KitTextLiterals reads every string and char literal in the files this slice owns.
func s23KitTextLiterals(sources map[string]string, owned map[string]bool) []s23TextLiteral {
	var out []s23TextLiteral
	for _, file := range s23SortedKeys(sources) {
		if !owned[file] {
			continue
		}
		out = append(out, s23ScanTextLiterals(file, kotlinCodeOnly(sources[file]))...)
	}
	return out
}

// s23KitLiterals reads every numeric literal in the files this slice owns.
func s23KitLiterals(t *testing.T, sources map[string]string, owned map[string]bool) []s23Literal {
	t.Helper()
	var out []s23Literal
	for _, file := range s23SortedKeys(sources) {
		if !owned[file] {
			continue
		}
		// THE GUARD READS THE STRIPPED VIEW, NOT THE RAW SOURCE. It tested the raw text, so a KDoc
		// that merely MENTIONED `"""` -- documenting this very restriction, say -- hard-failed the
		// whole fence on a source containing no raw string at all. Comments are not code and cannot
		// confuse s23CodeNoStrings; only a raw string in the code can.
		if strings.Contains(kotlinCodeOnly(sources[file]), `"""`) {
			t.Fatalf("PB-DS-7: %s contains a raw string, and s23CodeNoStrings reads only `\"` and "+
				"`'` literals. Every number inside that raw string would be counted as a metric and "+
				"every number after it could be missed -- this fence fails loudly rather than "+
				"reporting on a source it is misreading.", file)
		}
		out = append(out, s23ScanLiterals(file, s23CodeNoStrings(kotlinCodeOnly(sources[file])))...)
	}
	return out
}

// TestPBDS7_EveryMetricSpendResolvesToADeclarationTheScanSaw is the cross-check between the two
// scans this file already had, and it is what makes s23MetricConst's blind spots FAIL.
//
// THE DEFECT IT WAS WRITTEN FOR, found by the third audit committee round. Injected into
// object KitMetrics:
//
//	const val EXTRA_PAD_DP =
//	    11f
//
// plus `Kit.dpPx(context, KitMetrics.EXTRA_PAD_DP)` in Badge.kt, and the whole gate reported ok.
// s23ScanMetrics is line-anchored, so it saw no declaration; s23DpLiteralFaults accepted the
// argument because it MATCHED `KitMetrics.<ident>` and never asked whether that name was one the
// other scan had returned. Two scans of the same file, each satisfied, and between them a number
// with no design origin reaching a live call site.
func TestPBDS7_EveryMetricSpendResolvesToADeclarationTheScanSaw(t *testing.T) {
	sources := s23KitSources(t)
	owned := s23OwnedFiles()

	seen, fault := s23SeenMetricNames(sources, owned)
	if fault != "" {
		t.Fatalf("PB-DS-7: %s", fault)
	}

	spends := s23KitMetricSpends(sources, owned)
	if len(spends) == 0 {
		t.Fatal("PB-DS-7: the kit spends no KitMetrics constant at all, so this cross-check has " +
			"nothing to resolve and would pass over any declaration whatsoever")
	}
	for _, fault := range s23DanglingSpendFaults(spends, seen) {
		t.Errorf("PB-DS-7: %s", fault)
	}
}

// TestPBDS7_EveryNumberInTheKitIsAccountedFor closes the case the spend cross-check cannot: a
// number declared in an unrecognised spelling that nothing references.
//
// THE DEFECT IT WAS WRITTEN FOR, from the same review: `val gutterDp: Float get() = 13f` and
// `const val SHADE = 0x1F4` in object KitMetrics, unreferenced, gate green. Neither is a
// declaration s23MetricConst matches -- an accessor has no `=` where the pattern wants one, and a
// hex literal is not the decimal it reads for -- so neither was required to cite anything.
func TestPBDS7_EveryNumberInTheKitIsAccountedFor(t *testing.T) {
	sources := s23KitSources(t)
	owned := s23OwnedFiles()

	lits := s23KitLiterals(t, sources, owned)
	if len(lits) == 0 {
		t.Fatal("PB-DS-7: the literal scan found no number anywhere in the kit, which cannot be " +
			"true of eleven files that draw dots, rails and hairlines -- the scan is broken and " +
			"every number in the package is passing through it unseen")
	}
	strict := s23StrictLines(sources, owned)
	if len(strict) == 0 {
		t.Fatalf("PB-DS-7: no owned kit source declares `object %s`, so the strict zone is empty and "+
			"an exemption would apply inside the very object this fence polices", s23MetricsObject)
	}
	for _, fault := range s23UnaccountedLiteralFaults(lits, s23BoundLiterals(sources, owned), strict) {
		t.Errorf("PB-DS-7: %s", fault)
	}
	for _, fault := range s23DeadExemptionFaults(lits, strict) {
		t.Errorf("PB-DS-7: %s", fault)
	}

	// And the region the numeric scan blanks. See s23TextLiteral: the three spellings that defeated
	// the complete lane were all numbers written where s23ScanLiterals cannot look.
	textLits := s23KitTextLiterals(sources, owned)
	if len(textLits) == 0 {
		t.Fatal("PB-DS-7: the text-literal scan found no string or char anywhere in the kit, which " +
			"cannot be true of eleven files that set content descriptions and name CSS selectors -- " +
			"the scan is broken, and a number spelled inside a string would pass through it unseen")
	}
	for _, fault := range s23TextLiteralFaults(textLits, strict) {
		t.Errorf("PB-DS-7: %s", fault)
	}
	for _, fault := range s23DeadTextExemptionFaults(textLits) {
		t.Errorf("PB-DS-7: %s", fault)
	}
}

// ---------------------------------------------------------------------------
// PB-DS-7 / PB-TOK-8: the four Groups, and which two of them glow.
// ---------------------------------------------------------------------------

var s23GroupBinding = regexp.MustCompile(`"([a-z_]+)"\s*->\s*R\.color\.([a-z_]+)`)

// s23GroupShare is a `when` branch binding a Group to its glow share, in either spelling: the
// named constant the kit carries, or a literal.
//
// IT READS THE BINDING, NOT THE VALUE. The literal spelling was all this recognised, which made
// the check a hostage to the syntax of one `when` branch -- move the two numbers behind names,
// the innocuous refactor that puts them under an `origin:` annotation, and the regexp silently
// matches nothing while the test goes on passing. So the branch says WHICH share a Group takes
// and s23ResolveShares turns a name into a number through the constant scan; the number itself is
// joined to internal/design by the annotation on the constant.
// The branch's value ENDS THE LINE, which is what keeps this off the four colour branches in the
// same file: `"needs_input" -> R.color.swarm_state_attention` offers `R` to the name alternative
// and then has a `.` where the line was required to end.
var s23GroupShare = regexp.MustCompile(`(?m)"([a-z_]+)"\s*->\s*([A-Z][A-Z0-9_]*|[0-9]*\.?[0-9]+f)\s*$`)

// s23ResolveShares reads the Group -> share table out of Kit.kt, following a named constant to
// the value declared for it in the same file.
//
// @return the shares by Group, and one line per branch naming something the file does not declare
// -- which is a binding pointing at nothing, not a Group that does not glow.
func s23ResolveShares(src string) (map[string]float64, []string) {
	declared := map[string]string{}
	for _, m := range s23ScanMetrics(src) {
		declared[m.Name] = m.Raw
	}
	out := map[string]float64{}
	var faults []string
	for _, m := range s23GroupShare.FindAllStringSubmatch(kotlinCodeOnly(src), -1) {
		raw := strings.TrimSuffix(m[2], "f")
		if named, ok := declared[m[2]]; ok {
			raw = named
		} else if !s23LooksNumeric(raw) {
			faults = append(faults, fmt.Sprintf("group %s takes the share %s, which this file "+
				"declares no `const val` for -- so the value it glows at is somewhere this gate "+
				"cannot follow", m[1], m[2]))
			continue
		}
		v, err := strconv.ParseFloat(raw, 64)
		if err != nil {
			faults = append(faults, fmt.Sprintf("group %s takes the share %q, which is not a number", m[1], raw))
			continue
		}
		out[m[1]] = v
	}
	return out, faults
}

func s23LooksNumeric(s string) bool {
	_, err := strconv.ParseFloat(s, 64)
	return err == nil
}

// TestPBDS7_TheStatusDotBindingIsTheCheckedInMapping joins the kit's Group table THROUGH the two
// checked-in joins to the origin: group-tokens.tsv says which token a Group is, and
// design-tokens.tsv says which <color> that token is. The kit may not shortcut either hop.
//
// This is the requirement's one genuinely load-bearing colour decision. B134 moved green from
// Completed to ReadyForReview and gave Completed the recessive grey; an implementer reading only
// Substrate's artifact would paint the green dot "Done", because that is what the artifact's demo
// phone labels it.
func TestPBDS7_TheStatusDotBindingIsTheCheckedInMapping(t *testing.T) {
	sources := s23KitSources(t)
	src, ok := sources["Kit.kt"]
	if !ok {
		t.Fatal("PB-DS-7: the kit has no Kit.kt, which is where the Group binding lives")
	}
	code := kotlinCodeOnly(src)

	tokenOf := map[string]string{}
	for _, row := range loadGroupTokenMap(t) {
		tokenOf[row.Value] = row.Token
	}
	resourceOf := map[string]string{}
	for _, row := range loadTokenMap(t) {
		resourceOf[row.Token] = row.Resource
	}
	if len(tokenOf) == 0 || len(resourceOf) == 0 {
		t.Fatal("PB-DS-7: one of the two checked-in joins read empty; the comparison below would " +
			"pass over nothing")
	}

	bound := map[string]string{}
	for _, m := range s23GroupBinding.FindAllStringSubmatch(code, -1) {
		bound[m[1]] = m[2]
	}
	for group, token := range tokenOf {
		want, ok := resourceOf[token]
		if !ok {
			t.Errorf("PB-DS-7: group %s is bound to %s, which android/design-tokens.tsv maps to "+
				"no colour resource, so the kit has nothing to paint it with", group, token)
			continue
		}
		got, ok := bound[group]
		if !ok {
			t.Errorf("PB-DS-7: the kit binds no colour to status.Group %q. All four Groups are "+
				"rendered on the inbox at once; a Group the dot cannot colour is a section of "+
				"sessions with no state.", group)
			continue
		}
		if got != want {
			t.Errorf("PB-DS-7: the kit paints group %s with R.color.%s, but PB-TOK-8 binds it to "+
				"%s = R.color.%s. ADR-007 B134 decision 1 is the rebinding -- green moved to "+
				"ReadyForReview and Completed took the recessive grey -- and Substrate's own demo "+
				"labels the green dot \"Done\", so getting this from the artifact gives the wrong "+
				"answer.", group, got, token, want)
		}
	}
	for group := range bound {
		if _, ok := tokenOf[group]; !ok {
			t.Errorf("PB-DS-7: the kit binds a colour to %q, which is not a status.Group in "+
				"android/group-tokens.tsv. The phone renders the server's Group verbatim and "+
				"never invents one.", group)
		}
	}
}

// TestPBDS7_TheDotGlowsAreTheDeclaredDerivations checks the other half of the dot: which Groups
// glow, and by how much.
//
// The shares are read out of internal/design.Derivations() rather than out of the CSS, because
// that table is what PB-TOK-7 already fences the RESOLVED values against -- so the kit computing
// the blend from a share this gate joined to the same table is the supported way to obtain a
// colour the gate forbids typing.
//
// Substrate's rule is "nothing glows unless it is alive". Exactly two Groups are alive.
func TestPBDS7_TheDotGlowsAreTheDeclaredDerivations(t *testing.T) {
	src, ok := s23KitSources(t)["Kit.kt"]
	if !ok {
		t.Fatal("PB-DS-7: the kit has no Kit.kt")
	}

	tokenOf := map[string]string{}
	for _, row := range loadGroupTokenMap(t) {
		tokenOf[row.Value] = row.Token
	}

	// The dot glows, keyed by the token they are a blend of. Selected by Site rather than by name
	// so that a derivation renamed upstream fails loudly instead of silently dropping out.
	wantShare := map[string]float64{}
	for _, d := range design.Derivations() {
		if strings.HasPrefix(d.Site, ".pdot") {
			wantShare[d.Base] = float64(d.Percent) / 100
		}
	}
	if len(wantShare) == 0 {
		t.Fatal("PB-DS-7: internal/design declares no .pdot derivation, so this test would " +
			"require the kit to glow nowhere and pass over an empty set")
	}

	got, faults := s23ResolveShares(src)
	for _, fault := range faults {
		t.Errorf("PB-DS-7: %s", fault)
	}
	if len(got) == 0 {
		t.Fatal("PB-DS-7: no Group -> share binding was read out of Kit.kt at all. Every " +
			"comparison below would then report each live Group as glowing at 0, which is a " +
			"reader that stopped matching wearing the costume of a kit that stopped glowing.")
	}

	for group, token := range tokenOf {
		want, glows := wantShare[token]
		switch {
		case glows && got[group] != want:
			t.Errorf("PB-DS-7: group %s (%s) must glow at %g of its own colour -- the derivation "+
				"internal/design declares for `.pdot` -- and the kit declares %g. The glow is "+
				"`Paint.setShadowLayer(9dp, 0, 0, blend)` on a software layer; the blend itself "+
				"may not be typed (PB-TOK-7), so the share is what the kit carries.",
				group, token, want, got[group])
		case !glows && got[group] != 0:
			t.Errorf("PB-DS-7: group %s (%s) glows at %g in the kit, and the design declares no "+
				"glow for it. Substrate's stated rule is that nothing glows unless it is alive: "+
				"ReadyForReview is finished work waiting on a human and Completed is finished, so "+
				"neither is. `.pdot.ok` sets `box-shadow: none` explicitly.",
				group, token, got[group])
		}
	}

	// The reader must follow a NAME to its value, must object to a name that leads nowhere, and
	// must not mistake a colour branch for a share. The regexp this replaced did none of the
	// three: it matched a bare literal and nothing else, so moving the two numbers behind names --
	// the refactor that puts them under an `origin:` annotation -- would have left it matching
	// nothing while this test went on passing over an empty map.
	probe := strings.Join([]string{
		`    private const val NEEDS_INPUT_GLOW_SHARE = 0.70f`,
		`        "needs_input" -> NEEDS_INPUT_GLOW_SHARE`,
		`        "working" -> 0.55f`,
		`        "ready_for_review" -> MISSING_SHARE`,
		`        "completed" -> R.color.swarm_text_tertiary`,
	}, "\n")
	shares, probeFaults := s23ResolveShares(probe)
	if shares["needs_input"] != 0.70 {
		t.Errorf("the share reader answers %g for a Group bound to a named constant declared at "+
			"0.70 in the same file, so a kit that moved its shares behind names would be checked "+
			"against nothing", shares["needs_input"])
	}
	if shares["working"] != 0.55 {
		t.Errorf("the share reader answers %g for a Group bound to the literal 0.55f", shares["working"])
	}
	if _, ok := shares["ready_for_review"]; ok || len(probeFaults) != 1 {
		t.Errorf("the share reader accepted a branch naming a constant the file does not declare: "+
			"%v, faults %v. A binding that points at nothing is not a Group that does not glow.",
			shares, probeFaults)
	}
	if _, ok := shares["completed"]; ok {
		t.Errorf("the share reader read the COLOUR branch `-> R.color.swarm_text_tertiary` as a "+
			"share: %v. Both tables are `when (group)` blocks in one file, and reading the wrong "+
			"one would report every Group as glowing at a colour resource.", shares)
	}
}

// ---------------------------------------------------------------------------
// Negative controls.
// ---------------------------------------------------------------------------

// TestPBDS7_TheMetricJoinCanActuallyFail is the control PB-DS-10 names, applied to the half of
// this gate that is arithmetic rather than presence.
//
// Two ways TestPBDS7_EveryKitMetricIsTheDesignsOwnNumber is green while proving nothing: the
// design readers return the same number for everything, or they return nothing and the loop
// never runs. Both are exercised against facts the design states and that are NOT equal.
func TestPBDS7_TheMetricJoinCanActuallyFail(t *testing.T) {
	css := s22bSharedCSS(t)
	tokens := s22bTokenValues(t)

	cases := []struct {
		selector string
		property string
		want     float64
	}{
		{".pdot", "width", 7},
		{".pdot.att", "box-shadow", 9},
		{".workbar", "height", 3},
		{".workbar", "border-radius", 2},
		{".prow", "border", 1},
		{".prow.attention::before", "width", 2},
		{".chip .pd", "width", 5},
		{".ptabs svg", "width", 22},
		{".ptabs", "backdrop-filter", 16},
	}
	seen := map[float64]bool{}
	for _, c := range cases {
		got, err := s23CSSMetric(css, c.selector, c.property)
		if err != nil {
			t.Errorf("`%s { %s }`: %v", c.selector, c.property, err)
			continue
		}
		if got != c.want {
			t.Errorf("the CSS metric reader returns %g for `%s { %s }`, and the design says %g. "+
				"Every expectation in this gate goes through this reader.",
				got, c.selector, c.property, c.want)
		}
		seen[got] = true
	}
	if len(seen) < 6 {
		t.Errorf("the CSS metric reader produced only %d distinct values across %d different "+
			"declarations, so it is not reading the declarations -- and every equality built on "+
			"it passes over values that differ", len(seen), len(cases))
	}

	for _, c := range []struct {
		token string
		part  string
		want  float64
	}{
		// AUTHORIZED VALUE MIGRATION, ADR-009 O2. These four are the reader's KNOWN ANSWERS,
		// re-read out of the origin so the control still contradicts a reader that returned a
		// constant. The alpha row said 0.045 until ADR-009 D3 strengthened and warmed the
		// key-light to `inset 0 1px 0 rgba(246,243,236,0.10)`:
		//
		//	{"--p-card-fx", "alpha", 0.045},
		//
		// The other three did not move, and that is the useful part: --p-tabbg's alpha and
		// --p-workbar's stop are unchanged by a skin that repainted both of their colours, so a
		// reader that had started returning whatever it was handed would show up here.
		{"--p-card-fx", "px", 1},
		{"--p-card-fx", "alpha", 0.10},
		{"--p-tabbg", "alpha", 0.88},
		{"--p-workbar", "stop", 0.85},
	} {
		got, err := s23TokenMetric(tokens, c.token, c.part)
		if err != nil {
			t.Errorf("`%s %s`: %v", c.token, c.part, err)
			continue
		}
		if got != c.want {
			t.Errorf("the token metric reader returns %g for `%s %s`, and the origin declares %g",
				got, c.token, c.part, c.want)
		}
	}

	// And it must refuse what it cannot read, rather than returning zero. A reader that answered
	// 0 for a missing declaration would make every constant that happens to be 0 pass, and would
	// silently accept an origin annotation naming a rule that does not exist.
	if _, err := s23CSSMetric(css, ".no-such-rule", "width"); err == nil {
		t.Error("the CSS metric reader accepted a selector the design does not declare")
	}
	if _, err := s23CSSMetric(css, ".pdot", "no-such-property"); err == nil {
		t.Error("the CSS metric reader accepted a property the rule does not declare")
	}
	if _, err := s23TokenMetric(tokens, "--p-card-fx", "stop"); err == nil {
		t.Error("the token metric reader found a gradient stop in a box-shadow")
	}
}

// TestPBDS7_TheMetricScanCanActuallyFail is the control over the OTHER half of the metric join:
// not the arithmetic, but whether a constant is seen at all and whether its citation is followed
// far enough to compare a number.
//
// Three ways TestPBDS7_EveryKitMetricIsTheDesignsOwnNumber was green over a value nobody checked,
// all three found by an audit committee rather than by this file:
//
//   - THE FENCE HAD A VISIBILITY MODIFIER IN IT. `private const val ATTENTION_BORDER_SHARE` was
//     never matched, so its `origin:` annotation was decoration and its 0.36 was unchecked.
//     `private` is the modifier a number acquires the moment someone decides it is an
//     implementation detail, which is exactly when it stops being reviewed.
//   - A `derived:` CITATION WAS CHECKED FOR EXISTENCE, NEVER FOR VALUE. BADGE_HEIGHT_DP could
//     become 20f and stay green, because nothing followed the citation into row 3 and read the
//     `height 16` the row states.
//   - THE THREE color-mix SHARES HAD NO ANNOTATION FORM AT ALL, so they were checked by a
//     shape-specific regexp elsewhere in this file that an innocuous refactor would silently
//     stop matching.
//
// Every probe below goes through [s23ScanMetrics] and [s23CheckMetric] -- the functions the real
// assertion calls -- rather than through a copy of them.
func TestPBDS7_TheMetricScanCanActuallyFail(t *testing.T) {
	css := s22bSharedCSS(t)
	tokens := s22bTokenValues(t)
	doc := readFileOrFail(t, filepath.Join(repoRoot(t), filepath.FromSlash(s23ComponentsDoc)), "PB-DS-7")

	check := func(src string) []string {
		var faults []string
		for _, m := range s23ScanMetrics(src) {
			if fault := s23CheckMetric(m, css, tokens, doc); fault != "" {
				faults = append(faults, fault)
			}
		}
		return faults
	}

	// 1. Every spelling of a constant is SEEN. A constant the scan does not reach is one the
	//    gate cannot fail on, however wrong its value is.
	//
	//    THE MATRIX IS GENERATED, NOT CURATED, AND THAT IS THE POINT OF IT. This was four
	//    hand-picked forms -- `const val`, `internal const val`, `private const val`,
	//    `private val ...: Float` -- and it passed on the day `= 7F`, `@JvmField`, `var`, a
	//    camelCase name and a trailing comment all walked through the pattern, because a control
	//    made of examples can only ever confirm the examples someone thought of. That is the same
	//    defect as the fence it is checking, one level up.
	//
	//    So the axes are crossed instead: every combination of annotation, visibility, `const`,
	//    keyword, name shape, type annotation, literal form and trailing punctuation. A failure
	//    ENUMERATES the combinations the scan rejected rather than naming one, so what a reader
	//    gets is the shape of the hole and not a single instance of it.
	var missed []string
	for _, annotation := range []string{"", "@JvmField ", "@JvmStatic ", `@Suppress("unused") `} {
		for _, visibility := range []string{"", "private ", "internal ", "public ", "protected "} {
			for _, constness := range []string{"", "const "} {
				for _, keyword := range []string{"val", "var"} {
					if constness != "" && keyword == "var" {
						continue // `const var` is not Kotlin.
					}
					for _, name := range []string{"DOT_DP", "dotDp", "dot_dp", "_dotDp"} {
						for _, typed := range []string{"", ": Float", ": Double", ": Number"} {
							for _, literal := range []string{"7f", "7F", "7.0f", "7.0", "7", "7e0f", "0.045f"} {
								for _, trailing := range []string{"", ",", ";", "   ", " // the design's dot", " /* the design's dot */"} {
									spelling := "    " + annotation + visibility + constness +
										keyword + " " + name + typed + " = " + literal + trailing
									found := s23ScanMetrics(spelling)
									if len(found) != 1 {
										missed = append(missed, spelling)
										continue
									}
									if fault := s23CheckMetric(found[0], css, tokens, doc); fault == "" {
										missed = append(missed, spelling+"   [seen, but passes with NO origin]")
									}
								}
							}
						}
					}
				}
			}
		}
	}
	if len(missed) > 0 {
		shown := missed
		if len(shown) > 12 {
			shown = shown[:12]
		}
		t.Errorf("PB-DS-7: the metric scan does not refuse %d of the generated spellings. A number "+
			"written any of these ways carries no origin, is checked against nothing, and fails no "+
			"assertion in this gate however wrong its value is -- which is the defect this fence has "+
			"now shipped twice, each time as \"a spelling nobody listed\". First %d:\n\t%s",
			len(missed), len(shown), strings.Join(shown, "\n\t"))
	}

	// The two views s23ScanMetrics indexes in parallel must stay line-for-line aligned, or every
	// constant silently acquires its neighbour's annotation.
	for _, src := range []string{
		"/** origin: .pdot { width } */\nconst val DOT_DP = 7f\n",
		"/**\n * origin: .pdot { width }\n */\nconst val DOT_DP = 7f // trailing\n",
		"// a line comment\n/* a block\n   comment */\nconst val DOT_DP = 7f\n",
		`val s = "a string with /* not a comment */ in it"` + "\nconst val DOT_DP = 7f\n",
	} {
		if fault := s23ScanAlignmentFault(src); fault != "" {
			t.Errorf("PB-DS-7: %s\n\tsource: %q", fault, src)
		}
	}

	// And the recogniser must not have widened into matching things that are not declarations.
	// A pattern that matched everything would satisfy the matrix above and refuse nothing real.
	for _, notADeclaration := range []string{
		"    val size = Kit.dp(context, KitMetrics.PRESENCE_DOT_DP)",
		"    layoutParams = LinearLayout.LayoutParams(0, WRAP, 1f)",
		"    val gap = if (indexOfChild(child) == 0) 0 else gapPx",
		"    marginEnd = -Kit.dimenPx(context, R.dimen.swarm_space_6)",
		"    const val TAG = \"7f\"",
		"    fun dp(context: Context, value: Float): Float = value * 2f",
	} {
		if found := s23ScanMetrics(notADeclaration); len(found) != 0 {
			t.Errorf("PB-DS-7: the metric scan reads %q as a constant declaration (%v). A "+
				"recogniser that matches ordinary kit code would report a fault on every correct "+
				"line, and the fence would be switched off by whoever hit it first.",
				notADeclaration, found)
		}
	}

	// An annotation must not outlive the declaration it was written for. A wrapped initialiser
	// leaves the pattern unmatched on two lines, and the origin stayed pending across them: the
	// NEXT constant inherited it and passed on an authority naming a different quantity.
	stolen := "/** origin: .pdot { width } */\nconst val WRAPPED_DP =\n    7f\nconst val NEXT_DP = 7f\n"
	if found := s23ScanMetrics(stolen); len(found) != 1 || found[0].Name != "NEXT_DP" {
		t.Errorf("PB-DS-7: the metric scan reads %v out of a wrapped declaration followed by a "+
			"plain one; it should see exactly NEXT_DP, and the wrapped one is the cross-checks'", found)
	} else if fault := s23CheckMetric(found[0], css, tokens, doc); fault == "" {
		t.Error("PB-DS-7: a constant declared after an UNMATCHED wrapped declaration inherits that " +
			"declaration's `origin:` line and passes on it. `.pdot { width }` is 7, so NEXT_DP = 7f " +
			"is green against an authority that names the status dot's diameter and was never " +
			"written about NEXT_DP at all.")
	}

	// 2. A share cites internal/design's derivation table, and the VALUE is recomputed from it.
	//    The two glow shares and the attention border are colour-mix inputs: PB-TOK-7 forbids
	//    typing the resolved colour, so the share is what the kit holds and the table is where
	//    it comes from.
	if faults := check("/** origin: derivation attention-row-border */\nprivate const val S = 0.36f"); len(faults) != 0 {
		t.Errorf("PB-DS-7: a share annotated `origin: derivation attention-row-border` and equal "+
			"to the 36%% internal/design declares is reported as a fault: %v", faults)
	}
	if faults := check("/** origin: derivation attention-row-border */\nprivate const val S = 0.5f"); len(faults) == 0 {
		t.Error("PB-DS-7: a share annotated `origin: derivation attention-row-border` passes at " +
			"0.5 while internal/design declares 36%. The citation is being taken on trust, which " +
			"is the same defect as a derived citation nobody follows into its row.")
	}
	if faults := check("/** origin: derivation no-such-derivation */\nprivate const val S = 0.36f"); len(faults) == 0 {
		t.Error("PB-DS-7: a share citing a derivation internal/design does not declare passes. A " +
			"citation of nothing is a value with no authority behind it.")
	}

	// 3. A `derived:` citation names a FIELD of the row, and the field's value is compared.
	badge := "/** derived: " + s23ComponentsDoc + " #3 Badge { height } */\n    const val BADGE_HEIGHT_DP = "
	if faults := check(badge + "16f"); len(faults) != 0 {
		t.Errorf("PB-DS-7: the badge height annotated against row 3 and equal to the 16 the row "+
			"states is reported as a fault: %v", faults)
	}
	if faults := check(badge + "20f"); len(faults) == 0 {
		t.Error("PB-DS-7: BADGE_HEIGHT_DP passes at 20f against a row that states `height 16`. " +
			"The citation resolves to a row and the number is never compared to it, so the one " +
			"constant in this kit whose authority is prose is the one nothing checks.")
	}
	if faults := check("/** derived: " + s23ComponentsDoc + " #3 Badge */\n    const val BADGE_HEIGHT_DP = 16f"); len(faults) == 0 {
		t.Error("PB-DS-7: a constant citing a row but naming no field of it passes. `#3 Badge` " +
			"identifies a row with a dozen numbers in it; without a field there is nothing to " +
			"compare and the annotation asserts only that the row exists.")
	}
	if faults := check("/** derived: " + s23ComponentsDoc + " #3 Badge { no-such-field } */\n    const val X = 16f"); len(faults) == 0 {
		t.Error("PB-DS-7: a constant citing a field row 3 does not state passes, so a renamed " +
			"cell would leave the value checked against nothing")
	}
}

// TestPBDS7_TheCrossChecksCanActuallyFail is the control over the two cross-checks, and it is
// deliberately NOT a matrix of spellings.
//
// WHY NOT A MATRIX. TestPBDS7_TheMetricScanCanActuallyFail generates 53760 spellings across eight
// axes, and the fourth review said what that proves: every axis is a construct s23MetricConst
// already enumerates, and every axis is single-line. So the matrix confirms the enumeration and
// is silent about everything outside it -- "a control made of examples confirms only the examples
// someone thought of", which was round one's finding about the FENCE, reproduced one level up at
// the axis level. Adding a multi-line axis and a hex axis would reproduce it a third time, and
// the axis after that would be the one nobody thought of.
//
// WHAT IS CONTROLLED INSTEAD IS THE CHANNEL. An unrecognised declaration -- any spelling, including
// spellings nobody has written yet -- reaches these functions only through what the scans report
// about the source, so the states it can arrive in are enumerable:
//
//  1. its NAME is absent from the set s23ScanMetrics returned;
//  2. its LITERAL is absent from the positions s23BoundLiterals bound, while still being a token
//     s23ScanLiterals returned;
//  3. its LITERAL is absent from s23ScanLiterals' output ENTIRELY.
//
// STATE 3 WAS MISSING AND THE CLAIM THAT THERE WERE TWO WAS FALSE. It said an unrecognised
// declaration "reaches these two functions through exactly one observable state", and the loop
// below enumerated over `bound` -- which can only ever visit literals the numeric scan already saw.
// A number the scan never tokenised at all is invisible to that enumeration by construction, and
// `private val badgeMinWidthPx = "21".toInt()` is exactly that number: s23CodeNoStrings blanks the
// digits before s23ScanLiterals runs, so 21 px reached a live call site past a control that claimed
// to be complete over the spelling axis. The completeness argument was doing the work of a proof
// while enumerating two thirds of its own domain.
//
// So the control now puts the functions in all three states, over the REAL kit, one name and one
// literal at a time, and state 3 is closed by s23TextLiteralFaults rather than by the two functions
// the old claim named. That is the image of the spelling axis under the functions that can observe
// it -- and it is stated as three checked states rather than as a completeness result, because the
// last two completeness claims here were both falsified by the next reader. The constructs the
// third and fourth reviews injected are then run end to end as regressions: they are the instances
// that falsified the old claims, not the argument for the new one.
func TestPBDS7_TheCrossChecksCanActuallyFail(t *testing.T) {
	sources := s23KitSources(t)
	owned := s23OwnedFiles()

	seen, fault := s23SeenMetricNames(sources, owned)
	if fault != "" {
		t.Fatalf("PB-DS-7: %s", fault)
	}
	spends := s23KitMetricSpends(sources, owned)
	if len(seen) == 0 || len(spends) == 0 {
		t.Fatalf("PB-DS-7: the scan returned %d recognised constant(s) and %d spend(s); with either "+
			"at zero every probe below is vacuous", len(seen), len(spends))
	}
	if faults := s23DanglingSpendFaults(spends, seen); len(faults) != 0 {
		t.Fatalf("PB-DS-7: the spend cross-check already reports %d fault(s) against the kit as it "+
			"stands, so nothing below can tell a working fence from a broken one: %v",
			len(faults), faults)
	}

	// THE SCOPING IS REAL. If every declaration the scan sees anywhere in the kit were also inside
	// object KitMetrics, s23SeenMetricNames would be an identity function and a local
	// `val EXTRA_PAD_DP = 11f` in any file would resolve a KitMetrics spend.
	all := map[string]bool{}
	for file, src := range sources {
		if !owned[file] {
			continue
		}
		for _, m := range s23ScanMetrics(src) {
			all[m.Name] = true
		}
	}
	if len(all) <= len(seen) {
		t.Errorf("PB-DS-7: the scan sees %d declaration(s) across the kit and %d of them are inside "+
			"object %s, so the scoping in s23SeenMetricNames is not narrowing anything and a "+
			"declaration in any other file would resolve a spend", len(all), len(seen), s23MetricsObject)
	}

	// 1. For every constant the kit actually spends: make the scan blind to that ONE name -- which
	//    is what every unrecognised spelling of its declaration looks like from here -- and the
	//    spend must become a dangling reference that names it.
	spent := map[string]bool{}
	for _, s := range spends {
		spent[s.Name] = true
	}
	for _, name := range s23SortedKeys(seen) {
		if !spent[name] {
			continue // declared and never referenced; that case is the literal accounting's.
		}
		blind := map[string]bool{}
		for k, v := range seen {
			blind[k] = v
		}
		delete(blind, name)
		named := 0
		for _, f := range s23DanglingSpendFaults(spends, blind) {
			if strings.Contains(f, s23MetricsObject+"."+name) {
				named++
			}
		}
		if named == 0 {
			t.Errorf("PB-DS-7: %s.%s is spent by the kit, and the spend cross-check reports nothing "+
				"about it when the scan does not return that name. Absence from that set is the ONLY "+
				"form an unrecognised declaration takes here, so a fence silent about it is silent "+
				"about every spelling s23MetricConst misses -- now and in future.",
				s23MetricsObject, name)
		}
	}

	// 2. The same, for the literal accounting: unbind ONE literal -- which is what an unrecognised
	//    declaration does to its own number -- and it must become unaccounted for, UNLESS its text
	//    is exempt AND it sits outside object KitMetrics. Both directions are asserted, because
	//    that exception is the residual this pair does not close and it must not quietly widen.
	lits := s23KitLiterals(t, sources, owned)
	bound := s23BoundLiterals(sources, owned)
	strict := s23StrictLines(sources, owned)
	if faults := s23UnaccountedLiteralFaults(lits, bound, strict); len(faults) != 0 {
		t.Fatalf("PB-DS-7: the literal accounting already reports %d fault(s) against the kit as it "+
			"stands: %v", len(faults), faults)
	}
	textAt := map[string]string{}
	at := map[string]string{}
	for _, l := range lits {
		key := s23LiteralKey(l.File, l.Line, s23NormaliseLiteral(l.Text))
		textAt[key] = l.Text
		at[key] = fmt.Sprintf("%s:%d", l.File, l.Line)
	}
	for _, key := range s23SortedKeys(bound) {
		blind := map[string]bool{}
		for k := range bound {
			blind[k] = true
		}
		delete(blind, key)
		reported := len(s23UnaccountedLiteralFaults(lits, blind, strict)) > 0
		_, exempt := s23LiteralExemptions[textAt[key]]
		excused := exempt && !strict[at[key]]
		if reported == excused {
			t.Errorf("PB-DS-7: unbinding the literal at %s (written `%s`) reports=%v excused=%v. The "+
				"rule is that a number stops being accounted for the moment the scan stops "+
				"recognising its declaration, and the one exception is an exempt value OUTSIDE object "+
				"%s -- if those two disagree, either the fence is silent about a real number or the "+
				"exemption has reached inside the object it is not supposed to reach into.",
				key, textAt[key], reported, excused, s23MetricsObject)
		}
	}

	// 2b. STATE 3: the literal is not in the scan's output at all. Unbinding cannot produce this
	//     state -- the loop above enumerates `bound`, and every key in it is a literal the numeric
	//     scan tokenised -- so it is probed by writing the number where s23ScanLiterals cannot look.
	//     Each probe asserts BOTH halves: that the numeric scan really is blind to it (or the probe
	//     is controlling nothing), and that the text-literal accounting reports it anyway.
	for _, probe := range []struct {
		what string
		decl string
	}{
		{"a number spelled inside a string", `    private val badgeMinWidthPx = "21".toInt()` + "\n"},
		{"a character's code point", `    private val probeCodePx = '%'.code` + "\n"},
		{"a number assembled from two strings", `    private val probeConcatPx = ("1" + "1").toFloat()` + "\n"},
	} {
		src := "package dev.swarm.phone.ui.kit\n\n" + probe.decl
		files := map[string]string{"Probe.kt": src}
		only := map[string]bool{"Probe.kt": true}

		if got := s23ScanLiterals("Probe.kt", s23CodeNoStrings(kotlinCodeOnly(src))); len(got) != 0 {
			t.Errorf("PB-DS-7: %s is now a token s23ScanLiterals returns (%v), so this probe no "+
				"longer reaches state 3. Replace it with a number the numeric scan still cannot "+
				"see; the point of the probe is the blind spot, not the constant.", probe.what, got)
			continue
		}
		if faults := s23TextLiteralFaults(s23KitTextLiterals(files, only), nil); len(faults) == 0 {
			t.Errorf("PB-DS-7: %s reaches a live call site and NOTHING reports it. This is the "+
				"injection the fourth review used to defeat the complete lane -- Go, vet, the "+
				"manifest check and both Gradle variants:\n\t%s", probe.what, strings.TrimSpace(probe.decl))
		}
	}

	// 2c. And the text-literal rules must DISCRIMINATE. A scan that flagged every string would be
	//     satisfied by the probes above and would refuse the kit's copy, its selectors and its
	//     failure messages -- which is how an over-strict fence becomes a deleted one.
	for _, ok := range []string{
		`    contentDescription = "decorative"`,
		`    const val TITLE = ".pnav .big"`,
		`    text = count.toString()`,
		`    error("$group is not a Group this kit can colour")`,
	} {
		if faults := s23TextLiteralFaults(s23ScanTextLiterals("Probe.kt", ok), nil); len(faults) != 0 {
			t.Errorf("PB-DS-7: the text-literal scan flags %q: %v. Strings are how this kit spells "+
				"copy, CSS selectors and failure messages; a fence that refuses them gets switched "+
				"off by whoever hits it first.", ok, faults)
		}
	}
	// The exemption applies OUTSIDE object KitMetrics and nowhere inside it, exactly as the numeric
	// table does. `"99+"` is Badge's saturated count in a component file, and a design metric
	// written as text in the metrics object.
	exempt := s23ScanTextLiterals("Probe.kt", `    text = "99+"`)
	if faults := s23TextLiteralFaults(exempt, nil); len(faults) != 0 {
		t.Errorf("PB-DS-7: the exempt literal `\"99+\"` is reported outside object %s: %v",
			s23MetricsObject, faults)
	}
	if faults := s23TextLiteralFaults(exempt, map[string]bool{"Probe.kt:1": true}); len(faults) != 1 {
		t.Errorf("PB-DS-7: `\"99+\"` inside object %s produces %d fault(s), want 1. No exemption is "+
			"consulted in the strict zone, or a metric could be written as text in the one object "+
			"every fence exists to police.", s23MetricsObject, len(faults))
	}

	// 3. And both exemption tables must be capable of reporting a dead row.
	if len(s23DeadExemptionFaults(nil, nil)) != len(s23LiteralExemptions) {
		t.Errorf("PB-DS-7: against a kit with no literals at all, the dead-exemption check reports "+
			"%d of %d rows. A permission nobody uses has to be visible, or the table only grows.",
			len(s23DeadExemptionFaults(nil, nil)), len(s23LiteralExemptions))
	}
	if len(s23DeadTextExemptionFaults(nil)) != len(s23TextLiteralExemptions) {
		t.Errorf("PB-DS-7: against a kit with no text literals at all, the dead-exemption check "+
			"reports %d of %d rows of s23TextLiteralExemptions",
			len(s23DeadTextExemptionFaults(nil)), len(s23TextLiteralExemptions))
	}

	// And a literal inside object KitMetrics must not count as USE of an exemption, because no
	// exemption is consulted there. Counting it kept rows alive on an occurrence that never read
	// them -- a stale permission held open by the one zone that ignores permissions.
	inObject := []s23Literal{{File: "Probe.kt", Line: 2, Text: "0"}}
	if faults := s23DeadExemptionFaults(inObject, map[string]bool{"Probe.kt:2": true}); len(faults) != len(s23LiteralExemptions) {
		t.Errorf("PB-DS-7: a `0` inside object %s is counted as use of the `0` exemption: the "+
			"dead-exemption check reports %d of %d rows, want all %d. Inside that object no "+
			"exemption is consulted, so nothing there can be exercising one.",
			s23MetricsObject, len(faults), len(s23LiteralExemptions), len(s23LiteralExemptions))
	}

	// 4. The constructs the third audit round injected, verbatim, end to end, plus the one the
	//    strict zone was added for. Each is still invisible to s23MetricConst -- that is asserted,
	//    not assumed, because a probe the recogniser has since learned to read would be controlling
	//    nothing -- and each must now fail through a cross-check instead.
	for _, probe := range []struct {
		what  string
		decl  string
		spend string
	}{
		{"a wrapped initialiser", "    const val EXTRA_PAD_DP =\n        11f\n", "EXTRA_PAD_DP"},
		{"a get() accessor", "    val gutterDp: Float get() = 13f\n", ""},
		{"a hex literal", "    const val SHADE = 0x1F4\n", ""},
		{"a wrapped initialiser holding a WRONG value", "    const val DOT_DP =\n        13f\n", "DOT_DP"},
		// Unrecognised, unspent, AND holding a value the exemption table permits: the case all
		// three fences were silent about until s23StrictLines stopped exempting anything inside
		// the metrics object.
		{"an unrecognised declaration holding an exempt value", "    const val QUIET_PAD_DP =\n        2f\n", ""},
	} {
		src := "internal object " + s23MetricsObject + " {\n" + probe.decl + "}\n"
		files := map[string]string{"Probe.kt": src}
		only := map[string]bool{"Probe.kt": true}

		if found := s23ScanMetrics(src); len(found) != 0 {
			t.Errorf("PB-DS-7: %s is now recognised by s23MetricConst (%v), so this probe no longer "+
				"reaches the cross-checks. Replace it with a construct the recogniser still misses; "+
				"the point of the probe is the blind spot, not the constant.", probe.what, found)
			continue
		}
		probeLits := s23ScanLiterals("Probe.kt", s23CodeNoStrings(kotlinCodeOnly(src)))
		probeStrict := s23StrictLines(files, only)
		if len(probeStrict) == 0 {
			t.Errorf("PB-DS-7: %s: the probe declares object %s and s23StrictLines found no span in "+
				"it, so the strict zone is not being exercised", probe.what, s23MetricsObject)
			continue
		}
		if faults := s23UnaccountedLiteralFaults(probeLits, s23BoundLiterals(files, only), probeStrict); len(faults) == 0 {
			t.Errorf("PB-DS-7: %s declares a number s23MetricConst does not see, and the literal "+
				"accounting reports nothing about it:\n\t%s", probe.what, strings.TrimSpace(probe.decl))
		}
		if probe.spend == "" {
			continue
		}
		probeSeen, fault := s23SeenMetricNames(files, only)
		if fault != "" {
			t.Errorf("PB-DS-7: %s: %s", probe.what, fault)
			continue
		}
		use := s23ScanMetricSpends("Use.kt", "    Kit.dpPx(context, "+s23MetricsObject+"."+probe.spend+")\n")
		if faults := s23DanglingSpendFaults(use, probeSeen); len(faults) == 0 {
			t.Errorf("PB-DS-7: %s is spent at a live call site and the spend cross-check reports "+
				"nothing. This is the injection the third review used to falsify the fence:\n\t%s",
				probe.what, strings.TrimSpace(probe.decl))
		}
	}
}

// TestPBDS6_TheDpScanCanActuallyFail is the control for the two assertions above, and every probe
// goes through s23ScanDpSpends, s23DpLiteralFaults and s23QuantisationFaults -- the three
// functions the real assertions call -- rather than through a copy of them.
//
// Both assertions are currently GREEN against the kit, which is exactly the state in which a
// broken scan is indistinguishable from a clean tree: a scanner that read zero call sites, or a
// fault function that returned nothing, would report the same silence.
func TestPBDS6_TheDpScanCanActuallyFail(t *testing.T) {
	// The scan reads the accessor, the constant and the line, through a nested call -- which is
	// the normal shape here, and the shape a regexp-bounded argument list gets wrong.
	source := strings.Join([]string{
		`fun tabBar(context: Context) {`,
		`    background = TopRule(`,
		`        rulePx = Kit.dp(context, KitMetrics.HAIRLINE_DP),`,
		`    )`,
		`    val strokePx = Kit.dpPx(context, KitMetrics.HAIRLINE_DP)`,
		`    val iconPx = Kit.dpPx(context, KitMetrics.TAB_ICON_DP)`,
		`}`,
	}, "\n")
	spends := s23ScanDpSpends("Probe.kt", source)
	if len(spends) != 3 {
		t.Fatalf("PB-DS-6: the dp scan found %d call site(s) in a source with three: %+v. A scan "+
			"that reads fewer call sites than exist reports the same clean result as a kit with "+
			"nothing wrong in it.", len(spends), spends)
	}
	if spends[0].Accessor != "dp" || spends[0].Metric != "HAIRLINE_DP" || spends[0].Line != 3 {
		t.Errorf("PB-DS-6: the dp scan read the first call site as %+v, want Kit.dp of "+
			"HAIRLINE_DP at line 3. Every fault message below names the accessor, the constant "+
			"and the line, and all three come from here.", spends[0])
	}

	// The quantisation fault fires on a constant rendered two ways, and NOT on one rendered one
	// way. HAIRLINE_DP is above in both spellings; TAB_ICON_DP is in only one.
	faults := s23QuantisationFaults(spends)
	if len(faults) != 1 || !strings.Contains(faults[0], "HAIRLINE_DP") {
		t.Errorf("PB-DS-6: a source spending HAIRLINE_DP through both Kit.dp and Kit.dpPx, and "+
			"TAB_ICON_DP through one, produces %d fault(s): %v. Want exactly one, naming "+
			"HAIRLINE_DP -- this is the shipped defect verbatim, and if the check cannot see it "+
			"here it did not see it in TabBar.kt either.", len(faults), faults)
	}
	if faults := s23QuantisationFaults(s23ScanDpSpends("Probe.kt",
		`val a = Kit.dpPx(context, KitMetrics.HAIRLINE_DP)`+"\n"+
			`val b = Kit.dpPx(context, KitMetrics.HAIRLINE_DP)`)); len(faults) != 0 {
		t.Errorf("PB-DS-6: a constant spent TWICE through the same accessor is reported as two "+
			"renderings: %v. The fault is one token rendered two ways, not one token spent twice.",
			faults)
	}

	// A constant listed in s23DualQuantised is permitted both ways -- and the permission is read
	// from the table, not from the check agreeing with itself.
	dual := s23ScanDpSpends("Probe.kt",
		`val corePx = Kit.dpPx(context, KitMetrics.DOT_DP)`+"\n"+
			`val diameterPx = Kit.dp(context, KitMetrics.DOT_DP)`)
	if faults := s23QuantisationFaults(dual); len(faults) != 0 {
		t.Errorf("PB-DS-6: DOT_DP is on s23DualQuantised with its reason, and spending it both "+
			"ways is reported as a fault anyway: %v", faults)
	}
	if _, listed := s23DualQuantised["HAIRLINE_DP"]; listed {
		t.Error("PB-DS-6: HAIRLINE_DP is on s23DualQuantised. It describes ONE quantity -- the " +
			"width of a 1dp rule -- drawn by the card, the chip and the tab bar, so a row here " +
			"would restore the defect the table exists to prevent by permitting it.")
	}

	// The table must not outlive its reason either. A row for a constant the kit now spends ONE
	// way is a standing permission nobody is using, and the next dual spend inherits it without
	// anyone arguing for it -- which is how a justified exception becomes an unexamined one.
	stale := s23QuantisationFaults(s23ScanDpSpends("Probe.kt",
		`val corePx = Kit.dpPx(context, KitMetrics.DOT_DP)`))
	if len(stale) != 1 || !strings.Contains(stale[0], "Delete the row") {
		t.Errorf("PB-DS-6: DOT_DP is on s23DualQuantised and a source spending it through only "+
			"Kit.dpPx produces %d fault(s): %v. Want exactly one telling the reader to delete the "+
			"row. A permission that survives the reason for it is the next defect's excuse.",
			len(stale), stale)
	}
	if faults := s23QuantisationFaults(s23ScanDpSpends("Probe.kt",
		`val iconPx = Kit.dpPx(context, KitMetrics.TAB_ICON_DP)`)); len(faults) != 0 {
		t.Errorf("PB-DS-6: TAB_ICON_DP is NOT on s23DualQuantised and is spent one way, which is "+
			"the ordinary correct case, and it is reported as a fault: %v", faults)
	}

	// And the literal fault fires on a metric typed at the call site rather than named.
	literal := s23ScanDpSpends("Probe.kt", `val px = Kit.dp(context, 7f)`)
	if faults := s23DpLiteralFaults(literal); len(faults) != 1 {
		t.Errorf("PB-DS-6: `Kit.dp(context, 7f)` produces %d fault(s), want 1. A number typed at "+
			"the call site never becomes a property and never acquires an origin, so no "+
			"declaration scan can reach it.", len(faults))
	}
	if faults := s23DpLiteralFaults(s23ScanDpSpends("Probe.kt",
		`val px = Kit.dp(context, KitMetrics.DOT_DP)`)); len(faults) != 0 {
		t.Errorf("PB-DS-6: the correct spelling is reported as a literal: %v. A check that fails "+
			"on the right answer as readily as on the wrong one gets deleted.", faults)
	}
}

// TestPBDS6_TheAnnotationParserCanActuallyFail guards the other half: the origin annotations are
// what let this gate and the Robolectric suite compute from the design, and a parser that matched
// nothing would make every "the component cites its origin" assertion fail constantly -- while a
// parser that matched everything would make them pass over any text at all.
func TestPBDS6_TheAnnotationParserCanActuallyFail(t *testing.T) {
	got := s23Annotations(strings.Join([]string{
		"/**",
		" * The triage row.",
		" *",
		" * origin: .prow",
		" * derived: docs/design/substrate-components.md #3 Badge",
		" */",
		"fun sessionRow() {}",
		"// this line mentions an origin story and must not be read as one",
	}, "\n"))

	if !s23Contains(got["origin"], ".prow") {
		t.Errorf("the annotation parser missed `origin: .prow` in a KDoc block: %v", got["origin"])
	}
	if !s23Contains(got["derived"], s23ComponentsDoc+" #3 Badge") {
		t.Errorf("the annotation parser missed the derived citation: %v", got["derived"])
	}
	if len(got["origin"]) != 1 {
		t.Errorf("the annotation parser read %d origins from a source with one: %v",
			len(got["origin"]), got["origin"])
	}

	// The row lookup must distinguish a real row from a plausible one.
	doc := readFileOrFail(t, filepath.Join(repoRoot(t), filepath.FromSlash(s23ComponentsDoc)), "PB-DS-7")
	if !s23RowExists(doc, "#3 Badge") {
		t.Error("the row lookup cannot find `#3 Badge`, which the derivation table declares")
	}
	if !s23RowExists(doc, "§4 Status dots, B134 mapping") {
		t.Error("the row lookup cannot find the §4 status-dot row")
	}
	if s23RowExists(doc, "#3 Toast") {
		t.Error("the row lookup matched `#3 Toast`; row 3 is the Badge, so it is matching on the " +
			"number alone and a citation of the wrong row would pass")
	}
	if s23RowExists(doc, "#99 Nothing") {
		t.Error("the row lookup matched a row that does not exist")
	}
}

// ---------------------------------------------------------------------------

func s23DeclaresFun(src, name string) bool {
	for _, m := range s23TopLevelFun.FindAllStringSubmatch(kotlinCodeOnly(src), -1) {
		if m[1] == name {
			return true
		}
	}
	return false
}

func s23Contains(haystack []string, want string) bool {
	for _, v := range haystack {
		if v == want {
			return true
		}
	}
	return false
}
