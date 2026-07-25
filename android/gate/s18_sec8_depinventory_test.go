package gate

// FAILING-FIRST (TDD RED, GG-5) tests for PB-SEC-8, slice S18:
//
//	"No analytics/telemetry SDKs; dependencies minimal and justified."
//	Criterion: "Dependency inventory AS AN EVIDENCE ARTIFACT; assertion that no analytics
//	 dependency is present."
//
// THE CRITERION SAYS "ARTIFACT" BECAUSE "REVIEWED" WAS REJECTED AS UNENFORCEABLE, and this
// file's whole design follows from that. Two ways of satisfying PB-SEC-8 look identical in a
// green run and are not:
//
//	(a) reading android/app/build.gradle.kts, finding three `implementation` lines, and
//	    reporting "no analytics present";
//	(b) reading the RESOLVED module set and reporting the same thing.
//
// (a) is the defect. android/app/build.gradle.kts declares exactly three dependencies, and one
// of them is com.google.firebase:firebase-messaging, which pulls the Google Play Services
// client libraries -- com.google.android.gms:play-services-basement and friends -- into the
// APK transitively. A declaration-reading inventory reports a clean bill of health for an app
// that ships Google's measurement client. PB-PAIR-3 already records this tension in the other
// direction (ML Kit was NAMED AND REJECTED under PB-SEC-14 for exactly this pull); an
// inventory that cannot see what PB-PAIR-3 reasoned about is not an inventory.
//
// So the inventory here is derived from gradle/verification-metadata.xml -- the artifact
// PB-SEC-14 requires anyway, which enumerates every module that actually resolves, with its
// checksum. That choice also means THIS FILE NEVER SKIPS: no Android SDK, no Gradle daemon and
// no network are needed to read it, so the verdict on a bare CI runner is the verdict here.
//
// RED AT THE TIME OF WRITING: neither android/dependency-inventory.tsv nor
// android/gradle/verification-metadata.xml exists, so there is no inventory and no resolved
// module set to build one from.

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// The analytics denylist.
// ---------------------------------------------------------------------------

// analyticsCoordinates are module-coordinate substrings that identify an analytics, telemetry,
// crash-reporting or attribution SDK. The list is matched against the RESOLVED closure, so it
// catches a transitive pull as readily as a declared one.
//
// IT IS DELIBERATELY BROADER THAN "ANALYTICS". PB-SEC-8's subject is data leaving the handset
// about a user whose whole threat model is a device someone else may be holding, and a crash
// reporter is the sharpest case: it exfiltrates stack traces and, on some configurations,
// logs -- from an app whose stack traces name session ids and whose logs PB-SEC-3 is separately
// trying to keep clean.
//
// Each entry says WHY, because a denylist nobody can audit gets entries deleted when it fires.
var analyticsCoordinates = map[string]string{
	"firebase-analytics":   "Google Analytics for Firebase: per-user event stream",
	"firebase-crashlytics": "crash reports carry stack traces naming session ids",
	"firebase-perf":        "Firebase Performance Monitoring: per-request timing telemetry",
	"play-services-measurement": "the Google Analytics measurement client, usually pulled " +
		"transitively rather than declared",
	"play-services-analytics":                  "legacy Google Analytics client",
	"com.google.android.gms:play-services-ads": "advertising identifier and ad telemetry",
	"com.appsflyer":                            "attribution SDK",
	"com.amplitude":                            "product analytics",
	"com.mixpanel":                             "product analytics",
	"io.sentry":                                "crash/error reporting",
	"com.bugsnag":                              "crash reporting",
	"com.newrelic":                             "APM telemetry",
	"com.datadoghq":                            "APM telemetry",
	"com.segment":                              "analytics routing",
	"com.facebook.android:facebook-core":       "attribution and app-event reporting",
	"com.flurry":                               "analytics",
	"com.microsoft.appcenter":                  "analytics and crash reporting",
	"com.google.firebase:firebase-inappmessaging": "delivers server-composed UI and reports " +
		"engagement events",
}

// ---------------------------------------------------------------------------
// The inventory artifact.
// ---------------------------------------------------------------------------

// inventoryRow is one line of android/dependency-inventory.tsv:
//
//	module <TAB> why <TAB> requirement
//
// `why` is the JUSTIFICATION half of "dependencies minimal and justified". It is a required
// column rather than an optional note because an inventory with no reasons is a list, and a
// list does not make a dependency justified -- it makes it recorded.
type inventoryRow struct {
	Module string
	Why    string
	Req    string
	Line   int
}

func inventoryPath(t *testing.T) string {
	return filepath.Join(androidRoot(t), "dependency-inventory.tsv")
}

func readInventory(t *testing.T) []inventoryRow {
	t.Helper()
	path := inventoryPath(t)
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("PB-SEC-8: %s does not exist. The criterion is a dependency inventory AS AN "+
			"EVIDENCE ARTIFACT -- \"reviewed\" was rejected as unenforceable -- so there is "+
			"nothing here to enforce: %v", mustRel(t, path), err)
	}
	var rows []inventoryRow
	for i, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimRight(line, "\r")
		if strings.TrimSpace(line) == "" || strings.HasPrefix(strings.TrimSpace(line), "#") {
			continue
		}
		parts := strings.Split(line, "\t")
		if len(parts) < 3 {
			t.Errorf("PB-SEC-8: %s:%d has %d tab-separated column(s), want at least 3 "+
				"(module, why, requirement): %q", mustRel(t, path), i+1, len(parts), line)
			continue
		}
		rows = append(rows, inventoryRow{
			Module: strings.TrimSpace(parts[0]),
			Why:    strings.TrimSpace(parts[1]),
			Req:    strings.TrimSpace(parts[2]),
			Line:   i + 1,
		})
	}
	if len(rows) == 0 {
		t.Fatalf("PB-SEC-8: %s has no rows. Every assertion below would pass vacuously "+
			"against an empty inventory", mustRel(t, path))
	}
	return rows
}

// ---------------------------------------------------------------------------
// PB-SEC-8.
// ---------------------------------------------------------------------------

// TestPBSEC8_NoAnalyticsSDKIsPresentInTheRESOLVEDClosure is the requirement's headline, made
// against the resolved module set rather than the declarations.
//
// THE MUTATION THAT MUST FAIL IT: adding com.google.firebase:firebase-analytics to
// app/build.gradle.kts and regenerating the verification metadata. A declaration-only check
// would also catch that one -- but adding a library that pulls play-services-measurement
// transitively is the case this formulation exists for, and a declaration-only check cannot
// see it at all.
func TestPBSEC8_NoAnalyticsSDKIsPresentInTheRESOLVEDClosure(t *testing.T) {
	vm := readVerificationMetadata(t, "PB-SEC-8")
	modules := vm.resolvedModules()
	if len(modules) == 0 {
		t.Fatalf("PB-SEC-8: the resolved module set is empty, so \"no analytics dependency is " +
			"present\" would be true of a build with no dependencies at all -- and of one with " +
			"every analytics SDK in existence, since nothing was read")
	}

	var found []string
	for _, m := range modules {
		lower := strings.ToLower(m)
		for needle, why := range analyticsCoordinates {
			if strings.Contains(lower, strings.ToLower(needle)) {
				found = append(found, m+" -- "+why)
			}
		}
	}
	sort.Strings(found)
	if len(found) > 0 {
		t.Errorf("PB-SEC-8: %d analytics/telemetry module(s) resolve into the app:\n\t%s\n"+
			"Note these are RESOLVED coordinates; a module here need not appear in "+
			"app/build.gradle.kts to ship in the APK", len(found), strings.Join(found, "\n\t"))
	}
}

// TestPBSEC8_TheInventoryCoversEveryResolvedModule is the "minimal and justified" half, and it
// is BIDIRECTIONAL for the reason mobile/screen_coverage.tsv's coverage test already
// establishes: a one-way check lets the artifact drift into fiction.
//
//   - a resolved module with no inventory row is an UNJUSTIFIED dependency -- something ships
//     in the APK that no one wrote a reason for;
//   - an inventory row naming a module that no longer resolves is a STALE justification, which
//     is how an inventory comes to describe a build that no longer exists.
func TestPBSEC8_TheInventoryCoversEveryResolvedModule(t *testing.T) {
	vm := readVerificationMetadata(t, "PB-SEC-8")
	rows := readInventory(t)

	resolved := map[string]bool{}
	for _, c := range vm.Components.Component {
		resolved[c.Group+":"+c.Name] = true // version-independent: a bump is not a new dependency
	}
	inventoried := map[string]bool{}
	for _, r := range rows {
		inventoried[stripVersion(r.Module)] = true
	}

	var unjustified, stale []string
	for m := range resolved {
		if !inventoried[m] {
			unjustified = append(unjustified, m)
		}
	}
	for m := range inventoried {
		if !resolved[m] {
			stale = append(stale, m)
		}
	}
	sort.Strings(unjustified)
	sort.Strings(stale)

	if len(unjustified) > 0 {
		t.Errorf("PB-SEC-8: %d module(s) resolve into the app with no row in %s, so nothing "+
			"records why they are there:\n\t%s", len(unjustified), mustRel(t, inventoryPath(t)),
			strings.Join(unjustified, "\n\t"))
	}
	if len(stale) > 0 {
		t.Errorf("PB-SEC-8: %d row(s) in %s name modules that no longer resolve. An inventory "+
			"describing a build that does not exist is not evidence about the one that "+
			"does:\n\t%s", len(stale), mustRel(t, inventoryPath(t)), strings.Join(stale, "\n\t"))
	}
}

// TestPBSEC8_EveryInventoryRowCarriesARealJustification stops the inventory from being
// satisfied by filling the column with nothing. A row reading "dependency" or "needed" is the
// plausible-but-wrong value that makes a bidirectional coverage check green while the
// requirement's actual word -- justified -- goes unmet.
func TestPBSEC8_EveryInventoryRowCarriesARealJustification(t *testing.T) {
	rows := readInventory(t)

	// Words that are the whole of a non-reason. A justification has to say what the module is
	// FOR; these say only that it is present.
	empty := map[string]bool{
		"": true, "-": true, "n/a": true, "na": true, "tbd": true, "todo": true,
		"dependency": true, "needed": true, "required": true, "transitive": true, "?": true,
	}
	var weak []string
	for _, r := range rows {
		why := strings.ToLower(strings.TrimSpace(r.Why))
		if empty[why] || len(why) < 20 {
			weak = append(weak, mustRel(t, inventoryPath(t))+":"+itoa(r.Line)+" "+r.Module+
				" -> "+quote(r.Why))
		}
		if strings.TrimSpace(r.Req) == "" {
			weak = append(weak, mustRel(t, inventoryPath(t))+":"+itoa(r.Line)+" "+r.Module+
				" -> no requirement column")
		}
	}
	sort.Strings(weak)
	if len(weak) > 0 {
		t.Errorf("PB-SEC-8: %d inventory row(s) record a dependency without justifying it. "+
			"The requirement's word is \"justified\", and a reason under 20 characters is a "+
			"placeholder:\n\t%s", len(weak), strings.Join(weak, "\n\t"))
	}
}

// TestPBSEC8_TheFirebasePullIsNamedInTheInventory is the specific case this project already
// knows about, pinned so it cannot be quietly dropped.
//
// firebase-messaging is REQUIRED -- PB-PUSH-9 has no other way to receive a wake, and the app
// build file already explains at length why the google-services plugin is deliberately absent.
// What PB-SEC-8 needs is that its COST is written down where a dependency review looks, next
// to the same reasoning PB-PAIR-3 applied when it rejected ML Kit for the same Play Services
// pull. An inventory that lists firebase-messaging as though it were a leaf is describing a
// dependency graph the app does not have.
func TestPBSEC8_TheFirebasePullIsNamedInTheInventory(t *testing.T) {
	rows := readInventory(t)
	for _, r := range rows {
		if !strings.Contains(r.Module, "firebase-messaging") {
			continue
		}
		why := strings.ToLower(r.Why)
		if !strings.Contains(why, "play services") && !strings.Contains(why, "play-services") &&
			!strings.Contains(why, "gms") {
			t.Errorf("PB-SEC-8: the firebase-messaging row in %s does not mention what it "+
				"drags in. It is the app's only large dependency and the reason the resolved "+
				"closure is not three modules; a review that reads this row must be told, as "+
				"PB-PAIR-3 was told when it rejected ML Kit on the same grounds. Row: %q",
				mustRel(t, inventoryPath(t)), r.Why)
		}
		return
	}
	t.Errorf("PB-SEC-8: %s has no row for firebase-messaging, which app/build.gradle.kts "+
		"declares. Either the inventory is not derived from the build, or the dependency the "+
		"whole push path rests on is undocumented", mustRel(t, inventoryPath(t)))
}

func stripVersion(module string) string {
	parts := strings.Split(module, ":")
	if len(parts) >= 2 {
		return parts[0] + ":" + parts[1]
	}
	return module
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}

func quote(s string) string { return "\"" + s + "\"" }
