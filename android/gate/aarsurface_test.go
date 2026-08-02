package gate

// PB-BIND-7's ARTIFACT half.
//
// WHY THIS FILE EXISTS. PB-BIND-7 pins the facade's exported surface in
// mobile/testdata/exported_surface.golden and fails the build when the Go source drifts
// from it. That fence holds one end of the binding. The other end -- the AAR the Kotlin
// module actually compiles against -- was fenced by nothing at all.
//
// On 2026-08-01 the two ends were a day apart and every lane was green. The AAR on disk
// was built Jul 31; commit 5f45f34 added Session.Agent to the facade on Aug 1; javap on
// the shipped classes.jar listed getID/getTitle/getGroup/getNeed/getPresent and no
// getAgent. The Go tests passed (the source matched the golden), the surface golden
// passed (it had been regenerated), the Kotlin tests passed (nothing referenced the new
// field yet). The first symptom of a stale artifact is a compile error in whichever
// Kotlin file finally uses the field -- which reads as a source bug, not as an artifact
// that is a day behind, and cost real time before it was recognised.
//
// So this gate compares the BUILT AAR's exported members against the golden. It is not a
// second source of truth: the golden is already regenerated on every reviewed facade
// change (go test ./mobile/ -update-surface), and this file only asks whether the shipped
// artifact agrees with it.
//
// IT NEEDS NO JVM. The AAR is a zip holding classes.jar, which is a zip of .class files,
// and a .class file's constant pool, access flags, fields and methods are readable with
// archive/zip and encoding/binary. Every other assertion in this package runs on a plain
// runner with no JDK and no Android SDK, and that property is worth more than the few
// lines javap would save.
//
// IT SKIPS WHEN THERE IS NO AAR. The artifact is a build output and is gitignored, so a
// fresh clone and the plain `test` CI job have none. A gate that went red for everyone
// who had not run build-aar.sh would be switched off within a week.

import (
	"archive/zip"
	"bytes"
	"encoding/binary"
	"fmt"
	"go/parser"
	"go/token"
	"io"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"unicode"
	"unicode/utf8"
)

// aarSurfaceFloor is the "cannot pass by measuring nothing" floor. The golden's 171
// elements translate to well over two hundred bound members (every struct field binds as
// a getter AND a setter). A run that derived fewer than this has stopped reading the
// golden, and every comparison below would then be between two nearly empty sets.
const aarSurfaceFloor = 100

// jul31MissingMember is the member the Jul 31 artifact lacked, named here so the negative
// control replays the actual incident rather than an invented one.
const jul31MissingMember = "Session.getAgent"

// TestPBBIND7_TheBuiltAARExportsThePinnedFacadeSurface is the gate itself.
func TestPBBIND7_TheBuiltAARExportsThePinnedFacadeSurface(t *testing.T) {
	path := builtAARPath(t)
	if !exists(path) {
		t.Skipf("PB-BIND-7: no built AAR at %s, so there is no artifact to compare. It is a "+
			"build output and is gitignored; run android/build-aar.sh to give this gate "+
			"something to measure.", mustRel(t, path))
	}

	want := pinnedFacadeSurface(t)
	got := builtAARSurface(t, path)
	missing, extra := surfaceDiff(want, got)
	if len(missing) == 0 && len(extra) == 0 {
		return
	}
	t.Errorf("PB-BIND-7: %s does not export the surface pinned in "+
		"mobile/testdata/exported_surface.golden, so the Kotlin module is compiling against a "+
		"facade the Go source no longer describes.\n"+
		"IN THE GOLDEN, NOT IN THE AAR (the artifact is behind the facade):\n\t%s\n"+
		"IN THE AAR, NOT IN THE GOLDEN (the artifact is ahead of, or diverged from, the facade):"+
		"\n\t%s\n"+
		"Rebuild with android/build-aar.sh. If the artifact is current and the golden is not, "+
		"regenerate it with `go test ./mobile/ -update-surface` and justify the diff.",
		mustRel(t, path), joinOrNoneSurface(missing), joinOrNoneSurface(extra))
}

// TestPBBIND7_ThePinnedSurfaceTranslatesToBoundMembers runs the golden-to-JNI translation
// on every runner, including those with no AAR. Without it, a facade change that the
// translation cannot express would be invisible everywhere the gate above skips -- and
// the gate above skips on exactly the machines that have never built an artifact.
func TestPBBIND7_ThePinnedSurfaceTranslatesToBoundMembers(t *testing.T) {
	want := pinnedFacadeSurface(t)
	if len(want) < aarSurfaceFloor {
		t.Fatalf("PB-BIND-7: the pinned surface translated to %d bound members, below the floor "+
			"of %d. The golden holds 171 elements and every struct field binds as a getter and a "+
			"setter, so a number this small means the translation stopped reading the golden and "+
			"every comparison against an artifact would be vacuous.", len(want), aarSurfaceFloor)
	}
}

// TestPBBIND7_TheComparisonReportsAMemberTheAARIsMissing is the negative control, and it
// is the Jul 31 artifact replayed exactly: a surface identical to the pinned one except
// that Session.getAgent is absent. A gate nobody has watched fail is not evidence.
func TestPBBIND7_TheComparisonReportsAMemberTheAARIsMissing(t *testing.T) {
	want := pinnedFacadeSurface(t)

	stale := map[string]bool{}
	var dropped []string
	for member := range want {
		if strings.HasPrefix(member, jul31MissingMember+"(") {
			dropped = append(dropped, member)
			continue
		}
		stale[member] = true
	}
	if len(dropped) != 1 {
		t.Fatalf("PB-BIND-7: the pinned surface holds %d members named %s (want exactly 1), so "+
			"this control is not removing what it claims to remove. Found: %v",
			len(dropped), jul31MissingMember, dropped)
	}

	missing, extra := surfaceDiff(want, stale)
	if len(extra) != 0 {
		t.Errorf("PB-BIND-7: removing one member reported %d EXTRA members; the comparison is "+
			"not symmetric. Extra: %v", len(extra), extra)
	}
	if len(missing) != 1 || missing[0] != dropped[0] {
		t.Fatalf("PB-BIND-7: an artifact missing %s was reported as missing %v. The comparison "+
			"cannot see the exact defect this gate was written for, so its green means nothing.",
			dropped[0], missing)
	}
}

// TestPBBIND7_TheComparisonReportsAMemberTheAARStillCarries is the other direction, which
// a presence-only check would miss: a field DELETED from the facade leaves the golden but
// stays in a stale artifact, and the Kotlin module goes on compiling against it.
func TestPBBIND7_TheComparisonReportsAMemberTheAARStillCarries(t *testing.T) {
	want := pinnedFacadeSurface(t)

	const withdrawn = "Session.getFieldTheFacadeNoLongerDeclares()Ljava/lang/String;"
	stale := map[string]bool{withdrawn: true}
	for member := range want {
		stale[member] = true
	}

	missing, extra := surfaceDiff(want, stale)
	if len(missing) != 0 {
		t.Errorf("PB-BIND-7: adding one member reported %d MISSING members; the comparison is "+
			"not symmetric. Missing: %v", len(missing), missing)
	}
	if len(extra) != 1 || extra[0] != withdrawn {
		t.Fatalf("PB-BIND-7: an artifact still carrying %s was reported as carrying %v. A member "+
			"the facade has withdrawn would stay linkable from Kotlin indefinitely.",
			withdrawn, extra)
	}
}

// TestPBBIND7_TheBuiltAARSurfaceIsReadWithoutAJVM guards the property that lets the gate
// above run in the plain test lane: the extraction reads bytes and never shells out.
//
// It is an assertion rather than a comment because the tempting shortcut for any future
// gap in the class reader is to run the JDK's disassembler instead -- and that would make
// the gate skip, or die on a missing binary, on every runner without a JDK, which is the
// same "reports itself run while measuring nothing" defect this file exists to end. Only
// the IMPORT list is inspected, so the guard cannot be tripped by its own prose.
func TestPBBIND7_TheBuiltAARSurfaceIsReadWithoutAJVM(t *testing.T) {
	path := filepath.Join(repoRoot(t), "android", "gate", "aarsurface_test.go")
	f, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.ImportsOnly)
	if err != nil {
		t.Fatalf("PB-BIND-7: parse %s: %v", mustRel(t, path), err)
	}
	if len(f.Imports) == 0 {
		t.Fatalf("PB-BIND-7: %s imports nothing, so this guard is reading the wrong file and would "+
			"pass however the surface came to be extracted", mustRel(t, path))
	}
	for _, imp := range f.Imports {
		if strings.Contains(imp.Path.Value, "exec") {
			t.Errorf("PB-BIND-7: this file imports %s. The AAR's exported members are readable from "+
				"the class files directly; running an external disassembler would make the gate "+
				"inert on every runner without a JDK.", imp.Path.Value)
		}
	}
}

func joinOrNoneSurface(in []string) string {
	if len(in) == 0 {
		return "(none)"
	}
	sort.Strings(in)
	return strings.Join(in, "\n\t")
}

// ---------------------------------------------------------------------------
// The two surfaces, rendered into one comparable vocabulary.
//
// A member is written the way the JVM writes it, because that is the form in which the
// two ends can actually be compared without either side guessing:
//
//	Session.getAgent()Ljava/lang/String;   a bound method, with its descriptor
//	Swarmmobile.ErrClassOffline            a bound constant, which the golden gives no type
//	type Session struct                    a bound type, and which kind it is
//
// Descriptors are carried rather than names alone because a field whose TYPE changed
// keeps its getter's name. Without them, swapping Session.Present from bool to string
// would leave a stale artifact looking identical to a fresh one.
// ---------------------------------------------------------------------------

// bindPackage is the Java package gobind emits for the facade (Go package swarmmobile),
// and bindClass is the class it hangs package-level funcs and consts on.
const (
	bindPackage = "swarmmobile"
	bindClass   = "Swarmmobile"
)

// builtAARPath is where android/build-aar.sh writes the artifact the Kotlin module links
// against. The path is checked against the script rather than merely assumed: if the two
// ever diverge, this gate would find no artifact and SKIP forever -- reporting itself run
// while measuring nothing, which is the failure class it was written to end.
func builtAARPath(t *testing.T) string {
	t.Helper()
	const rel = "app/libs/swarm.aar"
	script := readFileOrFail(t, filepath.Join(androidRoot(t), "build-aar.sh"), "PB-BIND-7")
	if !strings.Contains(script, rel) {
		t.Fatalf("PB-BIND-7: android/build-aar.sh no longer writes %s, so this gate is looking for "+
			"an artifact nothing produces and would skip on every machine forever. Point it at the "+
			"path the script actually emits.", rel)
	}
	return filepath.Join(appModule(t), filepath.FromSlash("libs/swarm.aar"))
}

// surfaceDiff is the whole comparison: what the golden has and the artifact lacks, and
// what the artifact carries and the golden does not. Both directions are needed. A member
// DELETED from the facade leaves the golden immediately but survives in a stale AAR, and
// Kotlin goes on compiling against it until the next rebuild silently breaks the build.
func surfaceDiff(want, got map[string]bool) (missing, extra []string) {
	for member := range want {
		if !got[member] {
			missing = append(missing, member)
		}
	}
	for member := range got {
		if !want[member] {
			extra = append(extra, member)
		}
	}
	sort.Strings(missing)
	sort.Strings(extra)
	return missing, extra
}

// ---------------------------------------------------------------------------
// The golden, translated into the members gobind would emit for it.
// ---------------------------------------------------------------------------

// pinnedFacadeSurface reads mobile/testdata/exported_surface.golden and returns the bound
// members a correct artifact must export.
//
// EVERY UNRECOGNISED LINE IS A FAILURE, never a skip. The translation below covers the
// facade's whole type vocabulary today; the moment the facade grows a kind it does not
// cover, the honest outcome is a red gate that says "extend this" rather than a green one
// that quietly stopped checking part of the surface.
func pinnedFacadeSurface(t *testing.T) map[string]bool {
	t.Helper()
	path := filepath.Join(repoRoot(t), "mobile", "testdata", "exported_surface.golden")
	lines := strings.Split(readFileOrFail(t, path, "PB-BIND-7"), "\n")

	// Pass one: the declared types, needed before any signature can be translated -- a
	// bare Owner in a signature is a bound interface, and becomes a class reference.
	declared := map[string]bool{}
	for _, line := range lines {
		if kind, rest, ok := strings.Cut(strings.TrimSpace(line), " "); ok && kind == "type" {
			if name, _, ok := strings.Cut(rest, " "); ok {
				declared[name] = true
			}
		}
	}

	out := map[string]bool{}
	for n, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if err := addPinnedElement(out, line, declared); err != nil {
			t.Fatalf("PB-BIND-7: %s:%d: %v\nThe golden is the pinned contract and this gate must "+
				"translate ALL of it; a line it cannot read is a piece of the surface silently "+
				"going unchecked.", mustRel(t, path), n+1, err)
		}
	}
	return out
}

func addPinnedElement(out map[string]bool, line string, declared map[string]bool) error {
	kind, rest, ok := strings.Cut(line, " ")
	if !ok {
		return fmt.Errorf("cannot read %q as a golden element", line)
	}
	switch kind {
	case "const":
		// gobind emits a Go constant as a public static final field on the package class.
		// The golden carries no type for it, so the name is all there is to compare.
		out[bindClass+"."+rest] = true
		return nil

	case "type":
		name, form, ok := strings.Cut(rest, " ")
		if !ok || (form != "struct" && form != "interface") {
			return fmt.Errorf("type %q is neither a struct nor an interface; gobind binds those "+
				"two as classes and this translation knows no other shape", rest)
		}
		out["type "+name+" "+form] = true
		return nil

	case "field":
		lhs, goType, ok := strings.Cut(rest, " ")
		if !ok {
			return fmt.Errorf("cannot read %q as a field", rest)
		}
		owner, name, ok := strings.Cut(lhs, ".")
		if !ok {
			return fmt.Errorf("cannot read %q as Owner.Field", lhs)
		}
		desc, err := jvmDescriptor(goType, declared)
		if err != nil {
			return err
		}
		// A struct field binds as a getter/setter PAIR, and the field name keeps its Go
		// capitalisation: Session.Agent becomes getAgent/setAgent, Session.ID getID/setID.
		out[owner+".get"+name+"()"+desc] = true
		out[owner+".set"+name+"("+desc+")V"] = true
		return nil

	case "func", "method", "ifacemethod":
		owner := bindClass
		if kind != "func" {
			var ok bool
			if owner, rest, ok = strings.Cut(rest, "."); !ok {
				return fmt.Errorf("cannot read %q as Owner.Method", rest)
			}
		}
		open := strings.IndexByte(rest, '(')
		if open < 0 {
			return fmt.Errorf("cannot read %q as a signature", rest)
		}
		desc, err := jvmMethodDescriptor(rest[open:], declared)
		if err != nil {
			return err
		}
		out[owner+"."+lowerFirstJava(rest[:open])+desc] = true
		return nil
	}
	return fmt.Errorf("golden element kind %q has no translation to a bound member", kind)
}

// jvmDescriptor maps one Go type from the facade's vocabulary to its JVM descriptor.
//
// The int mapping is the one worth stating: gobind binds Go int as Java LONG, not int,
// because Go's int is 64-bit on every platform this ships to. Reading it as I would put
// every int-taking method in the "missing" column against a perfectly good artifact.
func jvmDescriptor(goType string, declared map[string]bool) (string, error) {
	switch goType {
	case "string":
		return "Ljava/lang/String;", nil
	case "bool":
		return "Z", nil
	case "int", "int64":
		return "J", nil
	case "[]byte":
		return "[B", nil
	}
	name := strings.TrimPrefix(goType, "*")
	if declared[name] {
		return "L" + bindPackage + "/" + name + ";", nil
	}
	return "", fmt.Errorf("Go type %q has no known JVM binding; extend jvmDescriptor rather than "+
		"letting this element go unchecked", goType)
}

// jvmMethodDescriptor translates a rendered Go signature -- "(string, []byte) error" --
// into a JVM method descriptor.
//
// A trailing error is not part of the descriptor: gobind turns it into `throws Exception`,
// so "(string) (*Op, error)" and "(string) *Op" bind to the same descriptor. That is the
// one distinction this comparison cannot make, and it is a small one next to the arity and
// type changes it can.
func jvmMethodDescriptor(sig string, declared map[string]bool) (string, error) {
	if !strings.HasPrefix(sig, "(") {
		return "", fmt.Errorf("signature %q does not start with a parameter list", sig)
	}
	end := strings.IndexByte(sig, ')')
	if end < 0 {
		return "", fmt.Errorf("signature %q has no closing parenthesis", sig)
	}
	params, results := sig[1:end], strings.TrimSpace(sig[end+1:])
	if strings.ContainsRune(params, '(') {
		return "", fmt.Errorf("parameter list %q nests a parenthesis; the split below would "+
			"mis-read it", params)
	}

	var desc strings.Builder
	desc.WriteByte('(')
	for _, p := range splitTypes(params) {
		d, err := jvmDescriptor(p, declared)
		if err != nil {
			return "", err
		}
		desc.WriteString(d)
	}
	desc.WriteByte(')')

	switch {
	case results == "" || results == "error":
		desc.WriteString("V")
	case strings.HasPrefix(results, "(") && strings.HasSuffix(results, ")"):
		got := splitTypes(results[1 : len(results)-1])
		if len(got) != 2 || got[1] != "error" {
			return "", fmt.Errorf("result list %q is not (T, error); gobind binds no other "+
				"multiple return", results)
		}
		d, err := jvmDescriptor(got[0], declared)
		if err != nil {
			return "", err
		}
		desc.WriteString(d)
	default:
		d, err := jvmDescriptor(results, declared)
		if err != nil {
			return "", err
		}
		desc.WriteString(d)
	}
	return desc.String(), nil
}

func splitTypes(list string) []string {
	var out []string
	for _, part := range strings.Split(list, ",") {
		if part = strings.TrimSpace(part); part != "" {
			out = append(out, part)
		}
	}
	return out
}

// lowerFirstJava is gobind's own Go-name-to-Java-name rule, reproduced because the whole
// comparison rests on it. It lowercases the LEADING RUN of capitals, then re-capitalises
// the last of them if a lowercase letter follows: SAS becomes sas, IsRunning isRunning,
// DecodeQR decodeQR, URLPath urlPath.
func lowerFirstJava(s string) string {
	var conv []rune
	for len(s) > 0 {
		r, n := utf8.DecodeRuneInString(s)
		if !unicode.IsUpper(r) {
			if l := len(conv); l > 1 {
				conv[l-1] = unicode.ToUpper(conv[l-1])
			}
			return string(conv) + s
		}
		conv = append(conv, unicode.ToLower(r))
		s = s[n:]
	}
	return string(conv)
}

// ---------------------------------------------------------------------------
// The artifact, read as bytes.
// ---------------------------------------------------------------------------

// gobindBoilerplate are the members every bound class carries regardless of what the Go
// side declares. They are skipped by NAME rather than by signature, so a Go method that
// collided with one would land in the "missing" column -- a false red, which is the safe
// direction for a skip list to be wrong in.
var gobindBoilerplate = map[string]bool{
	"<init>": true, "<clinit>": true, "incRefnum": true,
	"equals": true, "hashCode": true, "toString": true,
	// Swarmmobile.touch() exists only to force the class to load.
	"touch": true,
}

// builtAARSurface returns the bound members the artifact actually exports.
func builtAARSurface(t *testing.T, path string) map[string]bool {
	t.Helper()
	out := map[string]bool{}
	for _, c := range aarClasses(t, path) {
		simple := strings.TrimPrefix(c.name, bindPackage+"/")
		if simple != bindClass {
			form := "struct"
			if c.iface {
				form = "interface"
			}
			out["type "+simple+" "+form] = true
		}
		for _, f := range c.fields {
			if f.public {
				out[simple+"."+f.name] = true
			}
		}
		for _, m := range c.methods {
			if m.public && !gobindBoilerplate[m.name] {
				out[simple+"."+m.name+m.descriptor] = true
			}
		}
	}
	if len(out) == 0 {
		t.Fatalf("PB-BIND-7: %s exports no bound members at all. Either the artifact is empty or "+
			"the class reader found nothing, and in both cases the comparison below would pass by "+
			"agreeing with nothing.", mustRel(t, path))
	}
	return out
}

// aarClasses unpacks the AAR's classes.jar and parses every class gobind emitted for the
// facade package. Inner classes are excluded: Swarmmobile$proxyEventListener and its kin
// are the runtime's plumbing for calling Kotlin implementations back, not bound surface.
func aarClasses(t *testing.T, path string) []*classFile {
	t.Helper()
	jar := zipEntry(t, path, "classes.jar", "PB-BIND-7")

	inner, err := zip.NewReader(bytes.NewReader(jar), int64(len(jar)))
	if err != nil {
		t.Fatalf("PB-BIND-7: classes.jar inside %s is not a readable zip: %v", mustRel(t, path), err)
	}
	var out []*classFile
	for _, f := range inner.File {
		name := strings.TrimSuffix(f.Name, ".class")
		if name == f.Name || !strings.HasPrefix(name, bindPackage+"/") {
			continue
		}
		if simple := strings.TrimPrefix(name, bindPackage+"/"); strings.ContainsAny(simple, "/$") {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			t.Fatalf("PB-BIND-7: open %s: %v", f.Name, err)
		}
		raw, err := io.ReadAll(rc)
		rc.Close()
		if err != nil {
			t.Fatalf("PB-BIND-7: read %s: %v", f.Name, err)
		}
		c, err := parseClassFile(raw)
		if err != nil {
			t.Fatalf("PB-BIND-7: %s in %s: %v", f.Name, mustRel(t, path), err)
		}
		out = append(out, c)
	}
	if len(out) == 0 {
		t.Fatalf("PB-BIND-7: classes.jar inside %s holds no %s/*.class entries. gobind names the "+
			"Java package after the Go one; if that moved, this gate is reading an empty set.",
			mustRel(t, path), bindPackage)
	}
	return out
}

// zipEntry reads one member out of the AAR. The requirement is a parameter because the
// artifact is measured by more than one gate -- PB-BIND-7 reads classes.jar, the 16 KB page
// gate reads jni/<abi>/libgojni.so -- and a failure that names the wrong requirement sends
// its reader to the wrong document.
func zipEntry(t *testing.T, path, want, requirement string) []byte {
	t.Helper()
	r, err := zip.OpenReader(path)
	if err != nil {
		t.Fatalf("%s: %s is not a readable zip: %v", requirement, mustRel(t, path), err)
	}
	defer r.Close()
	for _, f := range r.File {
		if f.Name != want {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			t.Fatalf("%s: open %s in %s: %v", requirement, want, mustRel(t, path), err)
		}
		defer rc.Close()
		b, err := io.ReadAll(rc)
		if err != nil {
			t.Fatalf("%s: read %s in %s: %v", requirement, want, mustRel(t, path), err)
		}
		return b
	}
	t.Fatalf("%s: %s holds no %s, so it is not an AAR this gate can read",
		requirement, mustRel(t, path), want)
	return nil
}

// ---------------------------------------------------------------------------
// A .class file, read far enough to name its members. JVMS 4.1.
// ---------------------------------------------------------------------------

type classMember struct {
	name       string
	descriptor string
	public     bool
}

type classFile struct {
	name    string
	iface   bool
	fields  []classMember
	methods []classMember
}

const (
	accPublic    = 0x0001
	accInterface = 0x0200
)

func parseClassFile(raw []byte) (*classFile, error) {
	r := &classReader{buf: raw}
	if magic := r.u4(); magic != 0xCAFEBABE {
		return nil, fmt.Errorf("bad magic 0x%08X; not a class file", magic)
	}
	r.u2() // minor version
	r.u2() // major version

	// The constant pool is walked in full because entries are variable width and the
	// class's own name lives behind two indirections into it.
	count := int(r.u2())
	strs := make(map[int]string, count)
	classNames := make(map[int]int, count)
	for i := 1; i < count && r.err == nil; i++ {
		switch tag := r.u1(); tag {
		case 1: // Utf8
			strs[i] = string(r.bytes(int(r.u2())))
		case 7: // Class
			classNames[i] = int(r.u2())
		case 8, 16, 19, 20: // String, MethodType, Module, Package
			r.u2()
		case 15: // MethodHandle
			r.bytes(3)
		case 5, 6: // Long, Double -- these take TWO pool slots (JVMS 4.4.5)
			r.bytes(8)
			i++
		case 3, 4, 9, 10, 11, 12, 17, 18:
			r.bytes(4)
		default:
			return nil, fmt.Errorf("unknown constant pool tag %d at entry %d", tag, i)
		}
	}

	c := &classFile{}
	access := r.u2()
	c.iface = access&accInterface != 0
	c.name = strs[classNames[int(r.u2())]]
	r.u2()                   // super class
	r.bytes(int(r.u2()) * 2) // interfaces
	c.fields = r.members(strs)
	c.methods = r.members(strs)

	if r.err != nil {
		return nil, r.err
	}
	if c.name == "" {
		return nil, fmt.Errorf("class file declares no name")
	}
	return c, nil
}

func (r *classReader) members(strs map[int]string) []classMember {
	n := int(r.u2())
	out := make([]classMember, 0, n)
	for ; n > 0 && r.err == nil; n-- {
		m := classMember{public: r.u2()&accPublic != 0}
		m.name = strs[int(r.u2())]
		m.descriptor = strs[int(r.u2())]
		r.skipAttributes()
		out = append(out, m)
	}
	return out
}

func (r *classReader) skipAttributes() {
	for n := int(r.u2()); n > 0 && r.err == nil; n-- {
		r.u2() // name index
		r.bytes(int(r.u4()))
	}
}

// classReader is a sticky-error cursor: once it runs off the end it stops, and the single
// error is reported by the caller rather than checked at forty call sites.
type classReader struct {
	buf []byte
	pos int
	err error
}

func (r *classReader) bytes(n int) []byte {
	if r.err != nil {
		return nil
	}
	if n < 0 || r.pos+n > len(r.buf) {
		r.err = fmt.Errorf("truncated class file: want %d bytes at offset %d of %d",
			n, r.pos, len(r.buf))
		return nil
	}
	b := r.buf[r.pos : r.pos+n]
	r.pos += n
	return b
}

func (r *classReader) u1() byte {
	b := r.bytes(1)
	if b == nil {
		return 0
	}
	return b[0]
}

func (r *classReader) u2() uint16 {
	b := r.bytes(2)
	if b == nil {
		return 0
	}
	return binary.BigEndian.Uint16(b)
}

func (r *classReader) u4() uint32 {
	b := r.bytes(4)
	if b == nil {
		return 0
	}
	return binary.BigEndian.Uint32(b)
}
