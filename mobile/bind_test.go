package swarmmobile_test

// FAILING-FIRST (TDD RED, GG-5) guards for PB-BIND-1, PB-BIND-2 and the PB-BIND-0
// allowlist move (docs/specifications/remote-phaseB-requirements.md 6.2).
//
// Requirements 4.1 records the defect these close: internal/phonecore/journal.go:1
// documents the package as "gomobile-ready", THE CLAIM IS UNENFORCED -- no test guards
// it -- and it is false. v1 published "34 of 48 exported symbols fail"; two reviewers
// could not reproduce the count and it was withdrawn. PB-BIND-2 therefore requires the
// guard to produce the true number MECHANICALLY rather than restate a claim.
//
// VERIFIED BEHAVIOUR OF THE TOOL, which dictates the shape of this guard (measured
// 2026-07-25 against golang.org/x/mobile@v0.0.0-20260709172247-6129f5bee9d5):
//
//   - gobind HARD-FAILS (exit 1) on only a few illegal classes, e.g. a (T, bool) return:
//     "second result value must be of type error".
//   - For every OTHER illegal export -- arrays, unsigned ints, non-[]byte slices, maps,
//     variadics, cross-package types -- gobind EXITS 0 and SILENTLY DROPS the symbol,
//     leaving only a "// skipped function X with unsupported parameter or return types"
//     comment in the generated binding.
//
// So "gobind succeeded" is NOT a bind-legality check. A guard that only checked the exit
// status would pass on a facade whose entire surface had been silently deleted from the
// AAR -- precisely the failure mode 4.1 describes, reproduced one layer up. This guard
// therefore checks BOTH: the exit status, and every generated file for skip markers.

import (
	"archive/zip"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// skipMarker is the uniform prefix every gobind generator emits for a dropped export
// (bind/gengo.go, bind/genjava.go, bind/genobjc.go).
const skipMarker = "// skipped "

var skipLine = regexp.MustCompile(`(?m)^\s*// skipped .*$`)

// TestPBBIND2_NoExportedSymbolIsBindIllegal is the standing guard 4.1 showed was
// missing. It runs gobind over the facade and fails on ANY bind-illegal export, by
// either detection route, and emits the true legal/illegal counts.
func TestPBBIND2_NoExportedSymbolIsBindIllegal(t *testing.T) {
	src := loadFacade(t)

	total := len(exportedSurface(src))
	if total == 0 {
		t.Fatalf("PB-BIND-2: the facade exports nothing; the guard would be vacuous")
	}

	res := runGobind(t, facadePkgPath)
	if res.err != nil {
		t.Fatalf("PB-BIND-2: gobind REFUSED %s -- %d exported elements, at least one of them "+
			"bind-illegal in a class gobind hard-fails on:\n%s\ngobind: %v",
			facadePkgPath, total, res.output, res.err)
	}

	skipped := res.skipped()
	legal := total - len(skipped)
	t.Logf("PB-BIND-2 counts for %s: %d exported elements, %d bind-legal, %d bind-illegal",
		facadePkgPath, total, legal, len(skipped))

	if len(skipped) > 0 {
		t.Errorf("PB-BIND-2: %d of %d exported elements are bind-illegal and were SILENTLY "+
			"DROPPED from the generated binding (gobind still exited 0):\n\t%s",
			len(skipped), total, strings.Join(skipped, "\n\t"))
	}
}

// TestPBBIND2_GuardDetectsAKnownIllegalSurface is the negative control. A guard that
// cannot fail proves nothing, and this repo has been bitten by vacuous guards before
// (S1's empty-closure check, S3's no-op QR substitution). internal/phonecore is the
// known-illegal surface from 4.1, so the detector must report it as illegal.
//
// It also produces, mechanically, the number 4.1 withdrew: how many of phonecore's
// exported elements actually fail to bind.
func TestPBBIND2_GuardDetectsAKnownIllegalSurface(t *testing.T) {
	res := runGobind(t, phonecorePkgPath)

	if res.err == nil && len(res.skipped()) == 0 {
		t.Fatalf("PB-BIND-2 negative control: gobind reported %s as fully bind-legal. Either the "+
			"detector is broken or requirements 4.1 is wrong; both must be resolved before this "+
			"guard means anything", phonecorePkgPath)
	}
	if res.err != nil {
		t.Logf("PB-BIND-2 negative control: gobind hard-refuses %s (this is 4.1's blocker, "+
			"reproduced mechanically):\n%s", phonecorePkgPath, res.output)
		return
	}
	t.Logf("PB-BIND-2 negative control: %d bind-illegal exports in %s:\n\t%s",
		len(res.skipped()), phonecorePkgPath, strings.Join(res.skipped(), "\n\t"))
}

// TestPBBIND1_FacadeIsTheOnlyBoundSurface pins the topology: exactly one package, at a
// NON-INTERNAL path, is bindable. gobind's generated wrapper lives outside the module's
// internal boundary, so an internal package cannot be bound directly (requirements 4.2);
// a SECOND non-internal package over phonecore would be a second, unguarded AAR surface.
func TestPBBIND1_FacadeIsTheOnlyBoundSurface(t *testing.T) {
	loadFacade(t)

	if strings.Contains(facadePkgPath, "/internal/") {
		t.Fatalf("PB-BIND-1: the bound package %s is under internal/; gobind cannot bind it", facadePkgPath)
	}
	if !strings.Contains(phonecorePkgPath, "/internal/") {
		t.Errorf("PB-BIND-1: %s left internal/. The CORE must stay internal -- only the bound "+
			"facade is public API", phonecorePkgPath)
	}

	candidates := nonInternalPackagesImporting(t, phonecorePkgPath)
	if len(candidates) != 1 || candidates[0] != facadePkgPath {
		t.Errorf("PB-BIND-1: expected exactly one non-internal, non-cmd bindable package over %s, "+
			"namely %s; found %d:\n\t%s",
			phonecorePkgPath, facadePkgPath, len(candidates), strings.Join(candidates, "\n\t"))
	}
}

// TestPBBIND1_GomobileBindProducesAnAAR is PB-BIND-1's literal criterion: `gomobile bind`
// SUCCEEDS on the facade. gobind (the tests above) is only the front half of that
// pipeline; the AAR is what the app consumes, and 2 records that gomobile defaults to
// -androidapi 16 and FAILS on NDK 27, so the flag is part of the contract.
//
// Skipped under -short and when no Android SDK/NDK is configured; PB-TOOL-2 (S13) owns
// the reproducible build script.
func TestPBBIND1_GomobileBindProducesAnAAR(t *testing.T) {
	if testing.Short() {
		t.Skip("gomobile bind is minutes-long; -short skips it")
	}
	if os.Getenv("ANDROID_HOME") == "" && os.Getenv("ANDROID_SDK_ROOT") == "" {
		t.Skip("no ANDROID_HOME/ANDROID_SDK_ROOT; PB-BIND-1's AAR leg needs the SDK+NDK from requirements 2")
	}
	loadFacade(t)

	bin := toolPath(t, "gomobile")
	aar := filepath.Join(t.TempDir(), "swarmmobile.aar")
	cmd := exec.Command(bin, "bind", "-target=android/arm64", "-androidapi", "21", "-o", aar, facadePkgPath)
	cmd.Dir = repoRoot(t)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("PB-BIND-1: gomobile bind %s failed: %v\n%s", facadePkgPath, err, out)
	}

	zr, err := zip.OpenReader(aar)
	if err != nil {
		t.Fatalf("open produced AAR: %v", err)
	}
	defer func() { _ = zr.Close() }()
	want := "jni/arm64-v8a/libgojni.so"
	for _, f := range zr.File {
		if f.Name == want {
			return
		}
	}
	t.Errorf("PB-BIND-1: the AAR contains no %s; PB-TOOL-2 requires arm64-v8a explicitly "+
		"(an x86-only AAR installs on no handset)", want)
}

// TestPBBIND0_AllowlistMovedToTheFacade holds PB-BIND-0 green across the move. Its own
// allowlist file says so: "The guarded package is internal/phonecore until the
// non-internal facade of PB-BIND-1 exists; the guard then moves to the facade, which is
// the only bound surface." The closure that matters is the FACADE's -- everything it
// pulls in is code shipped to a handset an adversary may hold.
//
// Host, android/arm64 and ios/arm64 are all checked, because those closures already
// differ (S1 review R5: golang.org/x/sys/cpu is in the darwin closure and absent from
// android, so a //go:build android import of a forbidden package would leave a host-only
// check green while the daemon shipped to the handset).
func TestPBBIND0_AllowlistMovedToTheFacade(t *testing.T) {
	src := loadFacade(t)

	allowPath := filepath.Join(src.Dir, "deps_allowlist.txt")
	raw, err := os.ReadFile(allowPath)
	if err != nil {
		t.Fatalf("PB-BIND-0: the facade has no %s. The allowlist must MOVE with the guard: "+
			"the facade is now the bound surface, and its closure is what ships to the handset. "+
			"%v", allowPath, err)
	}
	allow := map[string]bool{}
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		allow[line] = true
	}
	if len(allow) == 0 {
		t.Fatalf("PB-BIND-0: %s lists no import paths", allowPath)
	}

	for _, target := range []struct{ goos, goarch string }{
		{"", ""}, // host
		{"android", "arm64"},
		{"ios", "arm64"},
	} {
		name := "host"
		if target.goos != "" {
			name = target.goos + "/" + target.goarch
		}
		t.Run(name, func(t *testing.T) {
			goos, goarch := target.goos, target.goarch
			if goos == "" {
				goos, goarch = os.Getenv("GOOS"), os.Getenv("GOARCH")
			}
			deps, err := goListDeps(t, facadePkgPath, goos, goarch)
			if err != nil {
				t.Fatalf("go list -deps %s for %s: %v", facadePkgPath, name, err)
			}
			if len(deps) == 0 {
				t.Fatalf("empty closure for %s; the guard would be vacuous", facadePkgPath)
			}
			var extra []string
			for _, d := range deps {
				if !allow[d] {
					extra = append(extra, d)
				}
			}
			if len(extra) > 0 {
				t.Errorf("PB-BIND-0: %d of %d non-standard packages in the %s closure of %s are not "+
					"in deps_allowlist.txt:\n\t%s",
					len(extra), len(deps), name, facadePkgPath, strings.Join(extra, "\n\t"))
			}
		})
	}
}

// ---- gobind driver -------------------------------------------------------------

type gobindResult struct {
	err    error
	output string
	files  []string // generated file contents
}

func (r gobindResult) skipped() []string {
	var out []string
	for _, body := range r.files {
		for _, line := range skipLine.FindAllString(body, -1) {
			line = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), skipMarker))
			out = append(out, line)
		}
	}
	sort.Strings(out)
	return dedupe(out)
}

func runGobind(t *testing.T, pkg string) gobindResult {
	t.Helper()

	bin := toolPath(t, "gobind")
	outDir := t.TempDir()
	cmd := exec.Command(bin, "-lang=go,java", "-outdir="+outDir, pkg)
	cmd.Dir = repoRoot(t)
	out, err := cmd.CombinedOutput()

	if err != nil && strings.Contains(string(out), "golang.org/x/mobile/bind") {
		t.Fatalf("PB-BIND-2's guard cannot run: golang.org/x/mobile is not in the module "+
			"dependency graph. requirements 2 records the fix -- `go get -tool "+
			"golang.org/x/mobile/cmd/gobind` (a Go 1.24+ tool directive, not linked into the "+
			"daemon binaries). Until it lands there is NO enforcement of PB-BIND-2 at all, "+
			"which is exactly the state 4.1 found.\ngobind said:\n%s", out)
	}

	res := gobindResult{err: err, output: string(out)}
	_ = filepath.WalkDir(outDir, func(path string, d os.DirEntry, werr error) error {
		if werr != nil || d.IsDir() {
			return nil
		}
		body, rerr := os.ReadFile(path)
		if rerr == nil {
			res.files = append(res.files, string(body))
		}
		return nil
	})
	return res
}

func toolPath(t *testing.T, name string) string {
	t.Helper()
	if p, err := exec.LookPath(name); err == nil {
		return p
	}
	out, err := exec.Command("go", "env", "GOPATH").Output()
	if err == nil {
		p := filepath.Join(strings.TrimSpace(string(out)), "bin", name)
		if _, serr := os.Stat(p); serr == nil {
			return p
		}
	}
	t.Fatalf("%s not found on PATH or in $GOPATH/bin. requirements 2 pins it: "+
		"`go install golang.org/x/mobile/cmd/%s@latest`", name, name)
	return ""
}

// nonInternalPackagesImporting lists module packages that (transitively) depend on pkg,
// are outside internal/, are not command binaries and have non-test Go files -- i.e.
// every candidate bound surface.
func nonInternalPackagesImporting(t *testing.T, pkg string) []string {
	t.Helper()
	cmd := exec.Command("go", "list", "-f",
		"{{if and .GoFiles (ne .Name \"main\")}}{{.ImportPath}} {{join .Deps \",\"}}{{end}}", "./...")
	cmd.Dir = repoRoot(t)
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("go list ./...: %v", err)
	}
	var hits []string
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, " ", 2)
		path := parts[0]
		if strings.Contains(path, "/internal/") || path == pkg {
			continue
		}
		if len(parts) < 2 {
			continue
		}
		for _, dep := range strings.Split(parts[1], ",") {
			if dep == pkg {
				hits = append(hits, path)
				break
			}
		}
	}
	sort.Strings(hits)
	return hits
}

func dedupe(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range in {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}
