package gate

// FAILING-FIRST (TDD RED, GG-5) tests for slice S15 / PB-SEC-10 and PB-STATE-6: the phone's
// durable state must be excluded from Android backup AND from device-to-device restore, and
// PB-STATE-6 asserts that JOINTLY with the PB-STATE-9 tier split.
//
// WHAT THESE TESTS MODEL, said plainly so nothing here can be mistaken for more than it is:
// they read the app's MANIFEST and its BACKUP RULES, which is configuration, not a device.
// No test in this file attempts an ADB backup, and none may be read as evidence that one was
// attempted and failed. The physical-handset gate is PB-E2E-5, which is DEFERRED and may not
// be reclassified by anything here.
//
// WHY THE RULES FILE IS A SEPARATE REQUIREMENT FROM allowBackup. The manifest already carries
// android:allowBackup="false". On the version matrix this app actually ships to that is not
// the whole of PB-SEC-10: android/supported-versions.tsv sets minSdk 33 and targetSdk 35, so
// EVERY supported device is Android 12 or later, and Android's own backup documentation states
// that on devices from some manufacturers allowBackup="false" disables cloud backup and does
// NOT disable device-to-device transfer, and that the pre-12 include/exclude mechanism "doesn't
// affect D2D transfers" at all. The attribute that governs D2D is android:dataExtractionRules
// and its <device-transfer> section. PB-SEC-10 names device-to-device restore explicitly and
// lists "allowBackup=false / backup rules" as two things. The rules file is the second one and
// it does not exist.
//
// WHY THERE IS NO KOTLIN HALF. The natural Robolectric assertion -- ApplicationInfo's
// FLAG_ALLOW_BACKUP on the MERGED manifest -- duplicates the first test below against an
// artifact that cannot differ here (the app's own attribute wins the merge, and AGP lint
// already fails a dangling @xml reference), while the D2D half has no public ApplicationInfo
// accessor to assert at all. A Kotlin test would therefore restate one assertion and be unable
// to make the one that matters.

import (
	"bytes"
	"encoding/xml"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/Nathandela/swarm/internal/phonecore"
	"github.com/Nathandela/swarm/internal/protocol/schema"
)

// ---------------------------------------------------------------------------
// A minimal XML tree, so the assertions below are about ELEMENTS and ATTRIBUTES rather than
// about substrings. A substring search cannot tell an attribute on <application> from the same
// text inside a comment, and "allowBackup is false somewhere in the file" is not the assertion.
// ---------------------------------------------------------------------------

type xmlNode struct {
	name  string
	attrs map[string]string // keyed by LOCAL name: android:allowBackup is "allowBackup"
	kids  []*xmlNode
}

func parseXMLFile(t *testing.T, path, requirement string) *xmlNode {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("%s: cannot read %s: %v", requirement, mustRel(t, path), err)
	}
	defer func() { _ = f.Close() }()

	dec := xml.NewDecoder(f)
	var root *xmlNode
	var stack []*xmlNode
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("%s: %s is not well-formed XML: %v", requirement, mustRel(t, path), err)
		}
		switch e := tok.(type) {
		case xml.StartElement:
			n := &xmlNode{name: e.Name.Local, attrs: map[string]string{}}
			for _, a := range e.Attr {
				n.attrs[a.Name.Local] = a.Value
			}
			if len(stack) > 0 {
				parent := stack[len(stack)-1]
				parent.kids = append(parent.kids, n)
			} else {
				root = n
			}
			stack = append(stack, n)
		case xml.EndElement:
			if len(stack) > 0 {
				stack = stack[:len(stack)-1]
			}
		}
	}
	if root == nil {
		t.Fatalf("%s: %s holds no XML element at all; every assertion below would pass vacuously",
			requirement, mustRel(t, path))
	}
	return root
}

// find returns the first descendant (or self) with this element name.
func (n *xmlNode) find(name string) *xmlNode {
	if n.name == name {
		return n
	}
	for _, k := range n.kids {
		if got := k.find(name); got != nil {
			return got
		}
	}
	return nil
}

// findAll returns every descendant (or self) with this element name.
func (n *xmlNode) findAll(name string) []*xmlNode {
	var out []*xmlNode
	if n.name == name {
		out = append(out, n)
	}
	for _, k := range n.kids {
		out = append(out, k.findAll(name)...)
	}
	return out
}

func manifestPath(t *testing.T) string {
	return filepath.Join(appModule(t), "src", "main", "AndroidManifest.xml")
}

// applicationElement is the <application> the backup attributes live on.
func applicationElement(t *testing.T, requirement string) *xmlNode {
	t.Helper()
	app := parseXMLFile(t, manifestPath(t), requirement).find("application")
	if app == nil {
		t.Fatalf("%s: the manifest declares no <application> element", requirement)
	}
	return app
}

// ---------------------------------------------------------------------------
// PB-SEC-10.
// ---------------------------------------------------------------------------

// TestPBSEC10_TheManifestDisablesCloudBackup is the half of PB-SEC-10 that ADB backup answers
// to: `adb backup` returns nothing for an application whose allowBackup is false.
//
// LEGITIMATE PASSER TODAY: the manifest already carries it, and this test exists so a later
// edit -- or a merged library manifest -- cannot remove it silently. It is not the whole of
// PB-SEC-10; see the file header for why the rules file is the other half.
func TestPBSEC10_TheManifestDisablesCloudBackup(t *testing.T) {
	app := applicationElement(t, "PB-SEC-10")
	if got := app.attrs["allowBackup"]; got != "false" {
		t.Errorf("PB-SEC-10: <application android:allowBackup=%q>, want \"false\". With backup enabled "+
			"the phone's whole state directory -- the sealed key containers included -- is copied off "+
			"the device by `adb backup` and by cloud backup", got)
	}
	// fullBackupContent is the PRE-Android-12 rules attribute. Declaring it alongside
	// allowBackup="false" is harmless but misleading, and on a minSdk-33 app it is dead
	// configuration that reads like the D2D exclusion without being it.
	if got, ok := app.attrs["fullBackupContent"]; ok {
		t.Errorf("PB-SEC-10: <application android:fullBackupContent=%q> is declared. Every device this "+
			"app supports is Android 13 or later (android/supported-versions.tsv), where the governing "+
			"attribute is dataExtractionRules; the legacy attribute is dead configuration that reads "+
			"like the device-to-device exclusion and is not it", got)
	}
}

// TestPBSEC10_TheManifestDeclaresDataExtractionRules. allowBackup governs the CLOUD path.
// PB-SEC-10 also names device-to-device restore, and from Android 12 -- which is every device
// on this app's version matrix -- that path is configured by android:dataExtractionRules.
// Nothing declares it, so the requirement's second half has no mechanism at all.
func TestPBSEC10_TheManifestDeclaresDataExtractionRules(t *testing.T) {
	app := applicationElement(t, "PB-SEC-10")
	rules, ok := app.attrs["dataExtractionRules"]
	if !ok {
		t.Fatalf("PB-SEC-10: <application> declares no android:dataExtractionRules. It is the attribute "+
			"that governs DEVICE-TO-DEVICE transfer on every Android release this app supports (minSdk "+
			"%s per android/supported-versions.tsv), and PB-SEC-10 names device-to-device restore "+
			"explicitly. android:allowBackup=\"false\" is the cloud half and is already there", "33")
	}
	if !strings.HasPrefix(rules, "@xml/") {
		t.Fatalf("PB-SEC-10: android:dataExtractionRules=%q does not reference an @xml resource", rules)
	}
	path := dataExtractionRulesPath(t, rules)
	if !exists(path) {
		t.Fatalf("PB-SEC-10: android:dataExtractionRules references %s, which does not exist", mustRel(t, path))
	}
}

// dataExtractionRulesPath resolves an @xml/<name> reference to the file it names.
func dataExtractionRulesPath(t *testing.T, ref string) string {
	t.Helper()
	name := strings.TrimPrefix(ref, "@xml/")
	return filepath.Join(appModule(t), "src", "main", "res", "xml", name+".xml")
}

// TestPBSEC10_TheRulesExcludeBothCloudBackupAndDeviceTransfer reads the rules themselves. A
// declared-but-empty rules file is worse than none: it looks like the requirement is met and
// transfers everything.
//
// WHAT IS ACCEPTED, and why it is a closed list rather than a property test. "Exclude the app's
// private data" has exactly one form this project will accept: an <exclude> naming
// domain="root" in each section, with no <include> anywhere in the file. The reasoning is that
// an <include> list inverts the rules -- only what is listed is transferred -- so a file with
// includes can be correct AND can be made incorrect by one more line, and reading it correctly
// requires knowing where the Go state directory lands, which no Kotlin code sets yet
// (mobile.Config.StateDir has no caller in android/app). domain="root" is the app's whole
// private data directory, so it covers the state directory wherever it ends up.
//
// If a later slice needs another form, this list is where that gets recorded -- deliberately,
// with the reasoning -- rather than discovered from a passing test.
func TestPBSEC10_TheRulesExcludeBothCloudBackupAndDeviceTransfer(t *testing.T) {
	app := applicationElement(t, "PB-SEC-10")
	ref, ok := app.attrs["dataExtractionRules"]
	if !ok {
		t.Fatal("PB-SEC-10: <application> declares no android:dataExtractionRules, so there are no rules " +
			"to read. See TestPBSEC10_TheManifestDeclaresDataExtractionRules")
	}
	root := parseXMLFile(t, dataExtractionRulesPath(t, ref), "PB-SEC-10")
	if root.name != "data-extraction-rules" {
		t.Fatalf("PB-SEC-10: the rules file's root element is <%s>, want <data-extraction-rules>", root.name)
	}

	for _, section := range []struct {
		element string
		what    string
	}{
		{"cloud-backup", "cloud backup and ADB backup"},
		{"device-transfer", "device-to-device restore"},
	} {
		sec := root.find(section.element)
		if sec == nil {
			t.Errorf("PB-SEC-10: the rules file has no <%s> section, so %s is governed by the platform "+
				"default, which transfers the app's data", section.element, section.what)
			continue
		}
		if includes := sec.findAll("include"); len(includes) > 0 {
			t.Errorf("PB-SEC-10: <%s> carries %d <include> element(s). An include list INVERTS the rules "+
				"-- only what is listed is transferred -- so the exclusion stops being readable from this "+
				"file alone", section.element, len(includes))
		}
		var domains []string
		excludesRoot := false
		for _, ex := range sec.findAll("exclude") {
			domains = append(domains, ex.attrs["domain"])
			if ex.attrs["domain"] == "root" && (ex.attrs["path"] == "" || ex.attrs["path"] == ".") {
				excludesRoot = true
			}
		}
		sort.Strings(domains)
		if !excludesRoot {
			t.Errorf("PB-SEC-10: <%s> does not exclude the app's private data root. It excludes domains "+
				"%v; PB-STATE-6 needs the phone's state directory out of %s, and no Kotlin code sets "+
				"mobile.Config.StateDir yet, so only an exclusion of domain=\"root\" covers it wherever "+
				"it lands", section.element, domains, section.what)
		}
	}
}

// ---------------------------------------------------------------------------
// PB-STATE-6: the joint assertion.
// ---------------------------------------------------------------------------

// s15 sentinels. They are spelled out here rather than shared with the phonecore tests because
// this package deliberately reaches the core only through its public surface, exactly as
// android/gate's PB-SEC-1 pair does: these tests read the bytes the core wrote, so they cannot
// be satisfied by any declaration -- in-package or otherwise.
const (
	s15GateMachine  = "s15-gate-machine-6c2f8a"
	s15GateSession  = "s15-gate-session-31d9b7"
	s15GateSnapLine = "s15-gate-terminal-a47e05"
	s15GateOpID     = "s15-gate-op-9b3c1d"
	s15GateOutcome  = "s15-gate-outcome-2e8f4a"
	s15GateToken    = "s15-gate-push-token-7d15c3"
)

// TestPBSTATE6_StateAtRestIsSealedPerTierAndExcludedFromBackup is PB-STATE-6, whose acceptance
// is literally "asserted jointly with those requirements". Both halves run here against one
// state directory, because either alone is a hole the other does not cover:
//
//   - sealed but backed up: the sealed containers leave the device. They are sealed under a
//     Keystore-backed KEK that does NOT leave with them, so this is not a break today -- but it
//     is the whole extraction surface handed to an attacker, and PB-SEC-10 exists to shut it.
//   - excluded from backup but written in the clear: the defect this slice was written for.
//     ADB backup is one of several ways to read an app's data directory and the least
//     interesting; a restored image and a rooted handset need no backup at all.
//
// The sealing half is measured from the BYTES, in Go, in the same package and for the same
// reason as the PB-SEC-1 pair: a Kotlin test that read an at-rest inventory could be made green
// by writing sealedByKeystore = true beside a file in the clear.
func TestPBSTATE6_StateAtRestIsSealedPerTierAndExcludedFromBackup(t *testing.T) {
	dir := t.TempDir()
	wake, content := newGateSealer(t), newGateSealer(t)

	core, err := phonecore.Resume(phonecore.Config{
		Dir: dir, Machine: s15GateMachine,
		WakeSealer: wake, ContentSealer: content,
	})
	if err != nil {
		t.Fatalf("phonecore.Resume: %v", err)
	}

	st := core.State()
	st.Machine = s15GateMachine
	st.PushToken = s15GateToken
	st.Sessions = []phonecore.CachedSession{{SessionID: s15GateSession, Present: true}}
	st.Snapshots = []phonecore.Snapshot{{Session: s15GateSession, Lines: []string{s15GateSnapLine}, Cols: 80, Rows: 24}}
	st.OpOutcomes = map[string]schema.Control{s15GateOpID: {Op: "kill", Error: s15GateOutcome}}
	for i := range st.Keys.ContentKey {
		st.Keys.WakeKey[i] = byte(0x21 + i)
		st.Keys.ContentKey[i] = byte(0x65 + i)
	}
	if err := core.Save(st); err != nil {
		t.Fatalf("saving decrypted session content: %v", err)
	}

	body, err := os.ReadFile(filepath.Join(dir, phonecore.StateFileName))
	if err != nil {
		t.Fatalf("PB-STATE-6: reading the persisted state blob: %v", err)
	}
	if len(body) == 0 {
		t.Fatal("PB-STATE-6: the state blob is empty; every assertion below would pass vacuously")
	}
	// The positive control: something the blob MUST carry in the clear, so a run against a
	// truncated or unwritten file fails here rather than passing every absence check.
	if !bytes.Contains(body, []byte(s15GateMachine)) {
		t.Fatalf("PB-STATE-6: %s does not carry the machine id this fixture wrote. The blob is not the "+
			"one this test provisioned, so every assertion below would measure nothing",
			phonecore.StateFileName)
	}

	for _, item := range []struct {
		what   string
		needle string
		why    string
	}{
		{"the cached session model", s15GateSession,
			"the decrypted journal: which sessions exist and what they are called"},
		{"a cached terminal snapshot", s15GateSnapLine,
			"a server-rendered grid of the user's terminal, verbatim"},
		{"a durable operation outcome", s15GateOutcome,
			"the decrypted reply cache PB-KEY-7 names beside sessions and snapshots"},
		{"the operation id of an outcome", s15GateOpID,
			"which commands the user ran, even where the outcome body is empty"},
	} {
		if bytes.Contains(body, []byte(item.needle)) ||
			bytes.Contains(body, []byte(base64Std([]byte(item.needle)))) {
			t.Errorf("PB-STATE-6/PB-STATE-9: %s sits in the clear in %s. It is %s -- CONTENT tier, so a "+
				"locked handset must not hold it readable, and PB-KEY-7's lock purge clears it from "+
				"memory while nothing touches this copy",
				item.what, filepath.Join("<stateDir>", phonecore.StateFileName), item.why)
		}
	}
	// And the wake tier: sealed too, under its own KEK. Sealed does not mean absent -- the wake
	// path must read it with no user present -- but "readable by anything that opens the file"
	// is not a tier.
	if bytes.Contains(body, []byte(s15GateToken)) {
		t.Errorf("PB-STATE-9: the push token sits in the clear in %s. It is WAKE tier: sealed under the "+
			"KEK that opens with no user present, not unsealed",
			filepath.Join("<stateDir>", phonecore.StateFileName))
	}

	// The backup half, in the same test, because PB-STATE-6 is the joint assertion.
	app := applicationElement(t, "PB-STATE-6")
	if got := app.attrs["allowBackup"]; got != "false" {
		t.Errorf("PB-STATE-6: <application android:allowBackup=%q>, want \"false\": the state directory "+
			"asserted above is copied off the device wholesale", got)
	}
	ref, ok := app.attrs["dataExtractionRules"]
	if !ok {
		t.Fatal("PB-STATE-6: <application> declares no android:dataExtractionRules, so the state " +
			"directory asserted above travels on a device-to-device transfer")
	}
	root := parseXMLFile(t, dataExtractionRulesPath(t, ref), "PB-STATE-6")
	for _, section := range []string{"cloud-backup", "device-transfer"} {
		sec := root.find(section)
		if sec == nil {
			t.Errorf("PB-STATE-6: the rules file has no <%s> section", section)
			continue
		}
		if err := excludesAppData(sec); err != nil {
			t.Errorf("PB-STATE-6: <%s> does not exclude the state directory: %v", section, err)
		}
	}
}

// excludesAppData reports whether one rules section excludes the app's private data root with
// no include list re-admitting it. It is the same closed accepted-form list
// TestPBSEC10_TheRulesExcludeBothCloudBackupAndDeviceTransfer states its reasoning for.
func excludesAppData(sec *xmlNode) error {
	if includes := sec.findAll("include"); len(includes) > 0 {
		return fmt.Errorf("it carries %d <include> element(s), which inverts the rules", len(includes))
	}
	for _, ex := range sec.findAll("exclude") {
		if ex.attrs["domain"] == "root" && (ex.attrs["path"] == "" || ex.attrs["path"] == ".") {
			return nil
		}
	}
	return fmt.Errorf("no <exclude domain=\"root\"> covering the app's private data directory")
}
