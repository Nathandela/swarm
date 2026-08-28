package upgrade

// The update transaction, proven hermetically (lifecycle plan R2): a fixture
// release server serves the redirect, the tarball, the checksums and their
// signature, and every gate the committee demanded is a differential here --
// signature-before-trust, checksum mismatch, downgrade refusal, dev-build
// refusal (with ZERO network), foreign-owner refusal, the flock, and the
// nothing-toucbed guarantees of every refusal. Nothing in this file dials
// past the fixture.

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
)

// fixture is one in-memory release: a signed checksums.txt and the platform
// tarball for tag, served exactly as GitHub serves them.
type fixture struct {
	srv      *httptest.Server
	tag      string
	requests atomic.Int64
	// corrupt hooks let one test serve a lying artifact.
	corruptChecksum, corruptSig bool
}

func newFixture(t *testing.T, tag string) *fixture {
	t.Helper()
	f := &fixture{tag: tag}

	// The test keypair replaces the release table for this package's tests.
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	orig := releasePublicKeys
	releasePublicKeys = []string{hex.EncodeToString(pub)}
	t.Cleanup(func() { releasePublicKeys = orig })

	tarball := buildTarball(t, "release "+tag)
	asset := assetName(tag)
	sum := sha256.Sum256(tarball)
	checksums := fmt.Sprintf("%s  %s\n%s  something_else.tar.gz\n", hex.EncodeToString(sum[:]), asset, "00")
	sig := base64.StdEncoding.EncodeToString(ed25519.Sign(priv, []byte(checksums)))

	mux := http.NewServeMux()
	mux.HandleFunc("/releases/latest", func(w http.ResponseWriter, r *http.Request) {
		f.requests.Add(1)
		http.Redirect(w, r, "/Nathandela/swarm/releases/tag/"+tag, http.StatusFound)
	})
	mux.HandleFunc("/releases/download/"+tag+"/", func(w http.ResponseWriter, r *http.Request) {
		f.requests.Add(1)
		switch filepath.Base(r.URL.Path) {
		case asset:
			_, _ = w.Write(tarball)
		case "checksums.txt":
			c := checksums
			if f.corruptChecksum {
				c = "deadbeef  " + asset + "\n"
			}
			_, _ = w.Write([]byte(c))
		case "checksums.txt.sig":
			s := sig
			if f.corruptSig {
				s = base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{1}, ed25519.SignatureSize))
			}
			_, _ = w.Write([]byte(s))
		default:
			http.NotFound(w, r)
		}
	})
	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)
	return f
}

// buildTarball is a goreleaser-shaped archive: README.md, swarm, swarm-remote
// at the root -- plus a hostile ../escape member every extraction must ignore.
func buildTarball(t *testing.T, stamp string) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	add := func(name, body string, mode int64) {
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: mode, Size: int64(len(body)), Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	add("README.md", "readme", 0o644)
	add("compat.json", `{"version":"`+stamp+`","shimwire":1,"protocol":1,"schema":1}`, 0o644)
	add("swarm", "#!/bin/sh\necho "+stamp+"\n", 0o755)
	add("swarm-remote", "#!/bin/sh\necho remote\n", 0o755)
	add("../escape", "hostile", 0o755)
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// selfOwnedBin places the "installed binary" somewhere ClassifyOwner calls
// OwnerSelf, clear of GOBIN.
func selfOwnedBin(t *testing.T) string {
	t.Helper()
	t.Setenv("GOBIN", filepath.Join(t.TempDir(), "gobin"))
	dir := t.TempDir()
	p := filepath.Join(dir, "swarm")
	if err := os.WriteFile(p, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestStageDownloadsVerifiesAndStagesWithoutTouchingTheBinary(t *testing.T) {
	f := newFixture(t, "v9.9.9")
	state := t.TempDir()
	bin := selfOwnedBin(t)
	before, _ := os.ReadFile(bin)

	st, err := Stage(context.Background(), Options{StateDir: state, BinPath: bin, Installed: "0.1.0", BaseURL: f.srv.URL})
	if err != nil {
		t.Fatalf("Stage: %v", err)
	}
	if st.Outcome != "staged" || st.StagedVersion != "v9.9.9" {
		t.Fatalf("outcome %q staged %q, want staged v9.9.9 (%s)", st.Outcome, st.StagedVersion, st.Detail)
	}
	for _, name := range []string{"swarm", "swarm-remote", "VERSION"} {
		if _, err := os.Stat(filepath.Join(StageDir(state), name)); err != nil {
			t.Errorf("staged %s missing: %v", name, err)
		}
	}
	if _, err := os.Stat(filepath.Join(StageDir(state), "escape")); err == nil {
		t.Error("the hostile ../escape member was extracted")
	}
	if after, _ := os.ReadFile(bin); !bytes.Equal(before, after) {
		t.Error("Stage modified the installed binary -- activation is R3's, staging must never touch it")
	}
	if rec, err := ReadState(state); err != nil || rec.Outcome != "staged" {
		t.Errorf("upgrade.json outcome = %q, %v; want staged", rec.Outcome, err)
	}
}

func TestStageRefusesACorruptChecksum(t *testing.T) {
	f := newFixture(t, "v9.9.9")
	f.corruptChecksum = true
	state := t.TempDir()
	st, err := Stage(context.Background(), Options{StateDir: state, BinPath: selfOwnedBin(t), Installed: "0.1.0", BaseURL: f.srv.URL})
	if err != nil {
		t.Fatalf("Stage: %v", err)
	}
	// The lying checksums file also no longer matches its signature, and the
	// SIGNATURE gate must answer first: nothing from the release is trusted --
	// not even the checksum table -- before the signature verifies.
	if st.Outcome != "failed-signature" {
		t.Fatalf("outcome = %q (%s), want failed-signature", st.Outcome, st.Detail)
	}
	if _, err := os.Stat(filepath.Join(StageDir(state), "swarm")); err == nil {
		t.Error("a build was staged from an unverifiable release")
	}
}

func TestStageRefusesABadSignature(t *testing.T) {
	f := newFixture(t, "v9.9.9")
	f.corruptSig = true
	state := t.TempDir()
	st, err := Stage(context.Background(), Options{StateDir: state, BinPath: selfOwnedBin(t), Installed: "0.1.0", BaseURL: f.srv.URL})
	if err != nil {
		t.Fatalf("Stage: %v", err)
	}
	if st.Outcome != "failed-signature" {
		t.Fatalf("outcome = %q (%s), want failed-signature", st.Outcome, st.Detail)
	}
}

func TestStageRefusesADowngradeWithoutTheFlag(t *testing.T) {
	f := newFixture(t, "v0.0.1")
	state := t.TempDir()
	st, err := Stage(context.Background(), Options{StateDir: state, BinPath: selfOwnedBin(t), Installed: "0.13.4", BaseURL: f.srv.URL})
	if err != nil {
		t.Fatalf("Stage: %v", err)
	}
	if st.Outcome != "refused-downgrade" {
		t.Fatalf("outcome = %q, want refused-downgrade", st.Outcome)
	}
	st, err = Stage(context.Background(), Options{StateDir: state, BinPath: selfOwnedBin(t), Installed: "0.13.4", BaseURL: f.srv.URL, AllowDowngrade: true})
	if err != nil {
		t.Fatalf("Stage --allow-downgrade: %v", err)
	}
	if st.Outcome != "staged" {
		t.Fatalf("outcome with --allow-downgrade = %q (%s), want staged", st.Outcome, st.Detail)
	}
}

func TestADevBuildRefusesWithZeroNetwork(t *testing.T) {
	f := newFixture(t, "v9.9.9")
	state := t.TempDir()
	st, err := Stage(context.Background(), Options{StateDir: state, BinPath: selfOwnedBin(t), Installed: "dev", BaseURL: f.srv.URL})
	if err != nil {
		t.Fatalf("Stage: %v", err)
	}
	if st.Outcome != "refused-dev" {
		t.Fatalf("outcome = %q, want refused-dev", st.Outcome)
	}
	if n := f.requests.Load(); n != 0 {
		t.Errorf("a dev build made %d network requests; the go-install machine must never even dial (committee C4)", n)
	}
}

func TestAGoInstallOwnedBinaryRefuses(t *testing.T) {
	f := newFixture(t, "v9.9.9")
	gobin := t.TempDir()
	t.Setenv("GOBIN", gobin)
	bin := filepath.Join(gobin, "swarm")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	st, err := Stage(context.Background(), Options{StateDir: t.TempDir(), BinPath: bin, Installed: "0.1.0", BaseURL: f.srv.URL})
	if err != nil {
		t.Fatalf("Stage: %v", err)
	}
	if st.Outcome != "refused-owner" || st.Owner != string(OwnerGo) {
		t.Fatalf("outcome = %q owner %q, want refused-owner/go", st.Outcome, st.Owner)
	}
}

func TestASecondRunAgainstAHeldLockIsBusy(t *testing.T) {
	f := newFixture(t, "v9.9.9")
	state := t.TempDir()
	unlock, err := lockUpgrade(state)
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	st, err := Stage(context.Background(), Options{StateDir: state, BinPath: selfOwnedBin(t), Installed: "0.1.0", BaseURL: f.srv.URL})
	if err != nil {
		t.Fatalf("Stage: %v", err)
	}
	if st.Outcome != "busy" {
		t.Fatalf("outcome = %q, want busy", st.Outcome)
	}
}

func TestOfflineIsAQuietOutcomeNotAnError(t *testing.T) {
	f := newFixture(t, "v9.9.9")
	f.srv.Close() // the release host is unreachable at 04:00
	state := t.TempDir()
	st, err := Stage(context.Background(), Options{StateDir: state, BinPath: selfOwnedBin(t), Installed: "0.1.0", BaseURL: f.srv.URL})
	if err != nil {
		t.Fatalf("Stage offline: %v -- offline is an outcome, tomorrow retries", err)
	}
	if st.Outcome != "offline" {
		t.Fatalf("outcome = %q, want offline", st.Outcome)
	}
}

func TestParseSemverAndCompare(t *testing.T) {
	for _, bad := range []string{"dev", "", "1.2", "v1.2.3.4", "a.b.c", "1.-2.3"} {
		if _, err := parseSemver(bad); err == nil {
			t.Errorf("parseSemver(%q) accepted", bad)
		}
	}
	a, _ := parseSemver("v0.13.4")
	b, _ := parseSemver("0.13.10")
	if a.compare(b) != -1 || b.compare(a) != 1 || a.compare(a) != 0 {
		t.Error("semver ordering wrong: 0.13.4 < 0.13.10 numerically, not lexically")
	}
}

func TestLatestVersionReadsTheRedirect(t *testing.T) {
	f := newFixture(t, "v1.2.3")
	got, err := LatestVersion(context.Background(), f.srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	if got != "v1.2.3" {
		t.Errorf("LatestVersion = %q, want v1.2.3", got)
	}
}
