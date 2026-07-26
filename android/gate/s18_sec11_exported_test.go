package gate

// FAILING-FIRST (TDD RED, GG-5) tests for PB-SEC-11, slice S18:
//
//	"Exported-component hygiene: an explicit android:exported ALLOWLIST, validated
//	 intents/deep links, no component reachable by a third-party app that can act on the
//	 session."
//	Criterion: "Manifest assertion + intent-validation tests."
//
// WHAT IS ALREADY RIGHT, said first so the RED below is not mistaken for an indictment. The
// manifest declares exactly two components, both carry an explicit android:exported, and both
// values are defensible: BootReceiver is exported="true" because BOOT_COMPLETED is an implicit
// system broadcast that cannot be delivered otherwise, and SwarmMessagingService is
// exported="false" with a comment explaining that a wake handler reachable from outside is a
// way to drive the phone's push path from another process. BootReceiver already validates its
// intent action. Three of this requirement's four clauses are met by the components that exist.
//
// WHAT IS MISSING IS THE WORD "ALLOWLIST". There is no artifact naming which components may be
// exported and why, and nothing fails when a third one appears. That is not a hypothetical:
// PB-SEC-4's window configuration has no home yet because the module declares no Activity, and
// the Activity that eventually lands will be exported="true" with a LAUNCHER filter -- the
// single most reachable surface an Android app has. An allowlist written after that arrives is
// an allowlist written to match what shipped.
//
// So the RED here is an EMPTY-SET problem, which is standing defect class (i) in its most
// literal form: a manifest assertion over the currently-declared components cannot fail, so it
// guards nothing. The allowlist is what gives it something to fail against.
//
// THIS FILE NEVER SKIPS: it reads the manifest and one checked-in table.

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// componentTags are the four manifest element types Android can launch into. A component that
// is not one of these cannot be reached by another app at all.
var componentTags = []string{"activity", "activity-alias", "service", "receiver", "provider"}

// declaredComponent is one manifest component as the allowlist has to talk about it.
type declaredComponent struct {
	Tag      string
	Name     string
	Exported string // the literal attribute value; "" means the attribute is absent
	Filters  int    // how many <intent-filter> children it declares
}

func declaredComponents(t *testing.T, requirement string) []declaredComponent {
	t.Helper()
	app := applicationElement(t, requirement)
	var out []declaredComponent
	for _, tag := range componentTags {
		for _, n := range app.findAll(tag) {
			out = append(out, declaredComponent{
				Tag:      tag,
				Name:     n.attrs["name"],
				Exported: n.attrs["exported"],
				Filters:  len(n.findAll("intent-filter")),
			})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// ---------------------------------------------------------------------------
// The allowlist artifact.
// ---------------------------------------------------------------------------

// exportRow is one line of android/exported-components.tsv:
//
//	component <TAB> exported <TAB> why <TAB> validation
//
// `validation` is what the component does to an intent it did not originate. For an
// exported=false component the honest value is the reason it needs none; for an exported=true
// one it must name the check, and the intent-validation test below reads this column to decide
// what to go and look for in the Kotlin.
type exportRow struct {
	Component  string
	Exported   string
	Why        string
	Validation string
	Line       int
}

func exportedAllowlistPath(t *testing.T) string {
	return filepath.Join(androidRoot(t), "exported-components.tsv")
}

func readExportedAllowlist(t *testing.T) []exportRow {
	t.Helper()
	path := exportedAllowlistPath(t)
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("PB-SEC-11: %s does not exist. The requirement's word is \"an explicit "+
			"android:exported ALLOWLIST\", and without one a manifest assertion can only "+
			"restate what the manifest already says -- a guard that cannot fail. The file the "+
			"allowlist has to survive is the one nobody has written yet: the launcher Activity "+
			"PB-SEC-4 is waiting on, which will be exported=\"true\" with an intent filter "+
			"anything on the device can send: %v", mustRel(t, path), err)
	}
	var rows []exportRow
	for i, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimRight(line, "\r")
		if strings.TrimSpace(line) == "" || strings.HasPrefix(strings.TrimSpace(line), "#") {
			continue
		}
		parts := strings.Split(line, "\t")
		if len(parts) < 4 {
			t.Errorf("PB-SEC-11: %s:%d has %d column(s), want 4 (component, exported, why, "+
				"validation): %q", mustRel(t, path), i+1, len(parts), line)
			continue
		}
		rows = append(rows, exportRow{
			Component:  strings.TrimSpace(parts[0]),
			Exported:   strings.TrimSpace(parts[1]),
			Why:        strings.TrimSpace(parts[2]),
			Validation: strings.TrimSpace(parts[3]),
			Line:       i + 1,
		})
	}
	if len(rows) == 0 {
		t.Fatalf("PB-SEC-11: %s lists no components; every assertion below would pass vacuously",
			mustRel(t, path))
	}
	return rows
}

// ---------------------------------------------------------------------------
// PB-SEC-11: the manifest half.
// ---------------------------------------------------------------------------

// TestPBSEC11_EveryComponentStatesExportedExplicitly.
//
// LEGITIMATE PASSER TODAY: both declared components carry the attribute. It is kept because
// the default is version-dependent -- a component with an intent-filter defaults to exported
// on older AGP and is a build error on newer -- so an omission is an exported component on
// some toolchains and a red build on others, which is the worst pair of outcomes to choose
// between at review time.
func TestPBSEC11_EveryComponentStatesExportedExplicitly(t *testing.T) {
	components := declaredComponents(t, "PB-SEC-11")
	if len(components) == 0 {
		t.Fatalf("PB-SEC-11: the manifest declares no components at all. Reported as a failure " +
			"rather than a skip: an assertion with no subject is not a satisfied assertion, and " +
			"a green run here would read as \"exported-component hygiene verified\"")
	}
	for _, c := range components {
		if c.Exported == "" {
			t.Errorf("PB-SEC-11: <%s android:name=%q> declares no android:exported. The default "+
				"depends on whether it has an intent-filter AND on the AGP version, so this is "+
				"an exported component on some toolchains", c.Tag, c.Name)
		} else if c.Exported != "true" && c.Exported != "false" {
			t.Errorf("PB-SEC-11: <%s android:name=%q android:exported=%q> is neither true nor "+
				"false", c.Tag, c.Name, c.Exported)
		}
	}
}

// TestPBSEC11_TheManifestAndTheAllowlistAgree is the bidirectional join, for the reason
// mobile/screen_coverage.tsv's coverage test already establishes: a one-way check lets the
// artifact drift into fiction.
//
//   - a manifest component with no allowlist row is a surface nobody signed off;
//   - an allowlist row for a component the manifest no longer declares is a stale approval,
//     which is how an allowlist comes to bless something that was replaced;
//   - a row whose `exported` disagrees with the manifest is the dangerous one: the artifact a
//     reviewer reads says false and the artifact Android reads says true.
func TestPBSEC11_TheManifestAndTheAllowlistAgree(t *testing.T) {
	components := declaredComponents(t, "PB-SEC-11")
	rows := readExportedAllowlist(t)

	inManifest := map[string]declaredComponent{}
	for _, c := range components {
		inManifest[c.Name] = c
	}
	inAllowlist := map[string]exportRow{}
	for _, r := range rows {
		inAllowlist[r.Component] = r
	}

	var unlisted, stale, disagree []string
	for name, c := range inManifest {
		r, ok := inAllowlist[name]
		if !ok {
			unlisted = append(unlisted, name+" (exported="+c.Exported+")")
			continue
		}
		if r.Exported != c.Exported {
			disagree = append(disagree, name+": manifest says "+quote(c.Exported)+
				", allowlist says "+quote(r.Exported))
		}
	}
	for name := range inAllowlist {
		if _, ok := inManifest[name]; !ok {
			stale = append(stale, name)
		}
	}
	sort.Strings(unlisted)
	sort.Strings(stale)
	sort.Strings(disagree)

	if len(unlisted) > 0 {
		t.Errorf("PB-SEC-11: %d manifest component(s) have no row in %s, so nothing recorded a "+
			"decision about their reachability:\n\t%s", len(unlisted),
			mustRel(t, exportedAllowlistPath(t)), strings.Join(unlisted, "\n\t"))
	}
	if len(stale) > 0 {
		t.Errorf("PB-SEC-11: %d row(s) in %s name components the manifest no longer declares:\n\t%s",
			len(stale), mustRel(t, exportedAllowlistPath(t)), strings.Join(stale, "\n\t"))
	}
	if len(disagree) > 0 {
		t.Errorf("PB-SEC-11: %d component(s) where the reviewed artifact and the shipped "+
			"manifest disagree about exportedness. The allowlist is the thing a reviewer reads "+
			"and the manifest is the thing Android obeys:\n\t%s",
			len(disagree), strings.Join(disagree, "\n\t"))
	}
}

// TestPBSEC11_EveryExportedComponentNamesItsIntentValidation is the criterion's second half at
// the artifact level; the Kotlin half follows below.
//
// An exported component receives intents from any app on the device, with any extras, at any
// time, including before the user has ever opened this one. The requirement's phrase is
// "validated intents/deep links", and the allowlist has to say what the validation IS -- a row
// that records exported=true with an empty validation column is the approval without the
// reasoning, which is the thing this artifact exists to prevent.
func TestPBSEC11_EveryExportedComponentNamesItsIntentValidation(t *testing.T) {
	rows := readExportedAllowlist(t)
	var weak []string
	for _, r := range rows {
		if r.Exported != "true" {
			continue
		}
		v := strings.ToLower(strings.TrimSpace(r.Validation))
		if v == "" || v == "-" || v == "none" || v == "n/a" || len(v) < 20 {
			weak = append(weak, mustRel(t, exportedAllowlistPath(t))+":"+itoa(r.Line)+" "+
				r.Component+" -> "+quote(r.Validation))
		}
	}
	sort.Strings(weak)
	if len(weak) > 0 {
		t.Errorf("PB-SEC-11: %d exported component(s) record no intent validation. Any app on "+
			"the device can send these an intent with any extras:\n\t%s",
			len(weak), strings.Join(weak, "\n\t"))
	}
}

// TestPBSEC11_NoExportedComponentCanActOnTheSession is the requirement's last clause, asserted
// against the Kotlin rather than against the table -- because the table is a claim and this is
// the check on it.
//
// The forbidden shape: an exported component whose handler reaches a facade verb that acts on
// a live session. A third-party app could then drive the session by sending an intent, with no
// lease, no biometric and no user present.
//
// LEGITIMATE PASSER TODAY, and it has real content: BootReceiver's onReceive body reaches
// nothing at all, and SwarmMessagingService -- which DOES reach handlePushWake -- is
// exported="false", so this fence is what keeps those two facts from drifting apart.
//
// BOTH SPELLINGS ARE MATCHED. gobind lowercases the first letter when it emits the Java
// binding, so a correct Kotlin call site contains `sendInput(` and never `SendInput(`. Matching
// the Go casing alone made five assertions in s17_pushclient_test.go unsatisfiable by any
// correct implementation, one of which carried that requirement's actual security property.
func TestPBSEC11_NoExportedComponentCanActOnTheSession(t *testing.T) {
	exported := map[string]bool{}
	for _, c := range declaredComponents(t, "PB-SEC-11") {
		if c.Exported == "true" {
			// android:name is ".runtime.BootReceiver"; the Kotlin class is the last segment.
			exported[c.Name[strings.LastIndex(c.Name, ".")+1:]] = true
		}
	}
	if len(exported) == 0 {
		t.Fatalf("PB-SEC-11: no component is exported, so this fence has no subject. Reported " +
			"as a failure rather than a skip so it cannot read as \"no exported component can " +
			"act on the session\" -- which would be true and unearned. BootReceiver is " +
			"exported=\"true\" in the tree; if that changed, this test needs rewriting, not " +
			"silencing")
	}

	// Verbs that act on a live session or on key custody.
	//
	// PurgeKeys and UnlockContent were added when PB-KEY-7 was finally wired, and they are the
	// reason this list is not only about exfiltration. ADR-007 B36 recorded that PB-SEC-11 and
	// PB-KEY-7 pull in opposite directions -- the obvious home for the lock purge is
	// PhoneActivity.onPause, and that Activity is exported with a LAUNCHER filter -- and that
	// only the first had ever been resolved. The resolution is dev.swarm.phone.runtime
	// .ContentLock, owned by the Application object, which is not a component and which nothing
	// outside the process can start. Naming both verbs here is what stops the next author
	// moving them one file over: UnlockContent asks Keystore to restore content custody, and
	// PurgeKeys reachable from an intent is a denial of service against the session.
	sessionVerbs := []string{
		"SendInput", "Paste", "Resize", "TakeControl", "ReleaseControl", "Interrupt",
		"Kill", "Launch", "Delete", "TerminalWatch", "SubscribeJournal", "Peek", "Roster",
		"InstallContentKey", "InstallWakeKey", "RevokeThisDevice",
		"PurgeKeys", "UnlockContent",
	}

	var findings []string
	for _, f := range kotlinFiles(t, kotlinMainRoot(t)) {
		base := strings.TrimSuffix(filepath.Base(f), ".kt")
		if !exported[base] {
			continue
		}
		src := stripKotlinComments(readFileOrFail(t, f, "PB-SEC-11"))
		for _, verb := range sessionVerbs {
			lowered := strings.ToLower(verb[:1]) + verb[1:]
			if strings.Contains(src, verb+"(") || strings.Contains(src, lowered+"(") {
				findings = append(findings, mustRel(t, f)+" reaches "+verb)
			}
		}
	}
	sort.Strings(findings)
	if len(findings) > 0 {
		t.Errorf("PB-SEC-11: %d exported component(s) can act on the session. Any app on the "+
			"device can trigger this by sending an intent, with no lease, no biometric and no "+
			"user present:\n\t%s", len(findings), strings.Join(findings, "\n\t"))
	}
}
