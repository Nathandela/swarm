package upgrade

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

// maxAssetBytes caps every download and every extracted member: a hostile or
// broken release must exhaust neither disk nor patience (committee gate list).
const maxAssetBytes = 512 << 20

// Options configures one transaction run.
type Options struct {
	StateDir  string // the swarm state dir; staging, lock and upgrade.json live under it
	BinPath   string // the binary being upgraded (os.Executable, unresolved)
	Installed string // internal/version.Version of the running binary
	BaseURL   string // "" means DefaultBaseURL; tests point it at a fixture server
	// AllowDowngrade permits latest < installed, for the yanked-release day.
	// Never set by any unattended path (committee M-4: a re-pointed `latest`
	// must not silently downgrade a fleet).
	AllowDowngrade bool
}

// Decision is Check's answer: what a run of Stage would do, and why.
type Decision struct {
	Action string // "stage" | "current" | "refuse"
	Reason string // human sentence; for refuse, names the owning delegate
	Latest string // the resolved tag, when resolution succeeded
	Owner  Owner
}

// Check resolves the latest release and decides, WITHOUT downloading anything.
// The order is deliberate: the cheap local refusals (dev build, foreign owner)
// come before the network, so an air-gapped dev machine never even dials.
func Check(ctx context.Context, opts Options) (Decision, error) {
	installed, err := parseSemver(opts.Installed)
	if err != nil {
		return Decision{Action: "refuse", Reason: fmt.Sprintf(
			"installed version %q is not a release build; nothing to compare, nothing touched", opts.Installed)}, nil
	}
	owner := ClassifyOwner(opts.BinPath)
	if owner != OwnerSelf {
		return Decision{Action: "refuse", Owner: owner, Reason: ownerDelegate(owner)}, nil
	}
	base := opts.BaseURL
	if base == "" {
		base = DefaultBaseURL
	}
	latest, err := LatestVersion(ctx, base)
	if err != nil {
		return Decision{Owner: owner}, err
	}
	latestV, err := parseSemver(latest)
	if err != nil {
		return Decision{Owner: owner, Latest: latest}, fmt.Errorf("upgrade: latest tag %q is not a release version", latest)
	}
	switch c := latestV.compare(installed); {
	case c == 0:
		return Decision{Action: "current", Latest: latest, Owner: owner,
			Reason: fmt.Sprintf("%s is current", opts.Installed)}, nil
	case c < 0 && !opts.AllowDowngrade:
		return Decision{Action: "refuse", Latest: latest, Owner: owner, Reason: fmt.Sprintf(
			"latest %s is OLDER than installed %s; a re-pointed release must not silently downgrade (--allow-downgrade overrides)",
			latest, opts.Installed)}, nil
	default:
		return Decision{Action: "stage", Latest: latest, Owner: owner,
			Reason: fmt.Sprintf("%s -> %s", opts.Installed, latest)}, nil
	}
}

// ownerDelegate names the command that owns updates for a non-self install.
func ownerDelegate(o Owner) string {
	switch o {
	case OwnerBrew:
		return "this install is Homebrew's: `brew upgrade --cask swarm` owns it"
	case OwnerDpkg:
		return "this install is dpkg's: apt owns it, and a self-replaced binary corrupts its books"
	case OwnerRpm:
		return "this install is rpm's: dnf owns it, and a self-replaced binary corrupts its books"
	case OwnerGo:
		return "this install is `go install`'s: rebuild with `go install github.com/Nathandela/swarm/cmd/swarm@latest`"
	default:
		return "this install's owner could not be classified; refusing to touch it"
	}
}

// StageDir is where a verified build awaits activation.
func StageDir(stateDir string) string { return filepath.Join(stateDir, "upgrade", "stage") }

// Stage runs Check and, on "stage", downloads, verifies (sha256 against
// checksums.txt, checksums.txt against its ed25519 signature) and unpacks the
// build into StageDir. It NEVER touches BinPath -- activation is R3 -- so a
// machine mid-work loses nothing when this runs at 04:00. Every outcome,
// including each refusal, lands in upgrade.json. The whole run holds an
// exclusive flock; a second concurrent run reports "busy" and touches nothing
// (committee M-3).
func Stage(ctx context.Context, opts Options) (State, error) {
	if err := os.MkdirAll(opts.StateDir, 0o700); err != nil {
		return State{}, err
	}
	s := State{Installed: opts.Installed}
	unlock, err := lockUpgrade(opts.StateDir)
	if err != nil {
		s.Outcome, s.Detail = "busy", err.Error()
		return s, recordState(opts.StateDir, &s)
	}
	defer unlock()

	dec, err := Check(ctx, opts)
	s.Latest, s.Owner = dec.Latest, string(dec.Owner)
	switch {
	case errors.Is(err, ErrOffline):
		s.Outcome, s.Detail = "offline", err.Error()
		return s, recordState(opts.StateDir, &s)
	case err != nil:
		s.Outcome, s.Detail = "failed-resolve", err.Error()
		return s, recordState(opts.StateDir, &s)
	case dec.Action == "current":
		s.Outcome, s.Detail = "current", dec.Reason
		// A stale staging dir from a superseded run is garbage once current.
		_ = os.RemoveAll(StageDir(opts.StateDir))
		return s, recordState(opts.StateDir, &s)
	case dec.Action == "refuse":
		s.Outcome, s.Detail = refusalOutcome(dec), dec.Reason
		return s, recordState(opts.StateDir, &s)
	}

	base := opts.BaseURL
	if base == "" {
		base = DefaultBaseURL
	}
	if err := downloadAndStage(ctx, base, dec.Latest, opts.StateDir); err != nil {
		s.Outcome, s.Detail = failedOutcome(err), err.Error()
		return s, recordState(opts.StateDir, &s)
	}
	s.Outcome, s.Detail, s.StagedVersion = "staged", dec.Reason, dec.Latest
	return s, recordState(opts.StateDir, &s)
}

// refusalOutcome distinguishes the refusals in upgrade.json without parsing
// prose: doctor keys off these.
func refusalOutcome(dec Decision) string {
	switch {
	case dec.Owner != "" && dec.Owner != OwnerSelf:
		return "refused-owner"
	case strings.Contains(dec.Reason, "OLDER"):
		return "refused-downgrade"
	default:
		return "refused-dev"
	}
}

type stepError struct {
	step string
	err  error
}

func (e stepError) Error() string { return e.err.Error() }
func (e stepError) Unwrap() error { return e.err }

func failedOutcome(err error) string {
	var se stepError
	if errors.As(err, &se) {
		return "failed-" + se.step
	}
	return "failed"
}

// recordState never masks the run's own answer: a state-write failure joins the
// detail rather than replacing the outcome.
func recordState(stateDir string, s *State) error {
	s.CheckedAt = nowFn()
	if err := writeState(stateDir, *s); err != nil {
		return fmt.Errorf("upgrade: outcome %q, and recording it failed: %w", s.Outcome, err)
	}
	return nil
}

// lockUpgrade takes the transaction's exclusive advisory lock.
func lockUpgrade(stateDir string) (func(), error) {
	f, err := os.OpenFile(filepath.Join(stateDir, "upgrade.lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = f.Close()
		return nil, errors.New("another upgrade run holds the lock; nothing touched")
	}
	return func() { _ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN); _ = f.Close() }, nil
}

// downloadAndStage fetches the platform tarball plus checksums and signature,
// verifies signature-then-checksum, and unpacks into a FRESH staging dir. The
// order matters: nothing from the tarball is trusted -- not even parsed --
// until the signature over the checksums has verified and the tarball hashes
// to its checksum line.
func downloadAndStage(ctx context.Context, base, tag, stateDir string) error {
	asset := assetName(tag)
	dl := base + "/releases/download/" + tag + "/"

	tarball, err := fetch(ctx, dl+asset)
	if err != nil {
		return stepError{"download", err}
	}
	checksums, err := fetch(ctx, dl+"checksums.txt")
	if err != nil {
		return stepError{"download", err}
	}
	sig, err := fetch(ctx, dl+"checksums.txt.sig")
	if err != nil {
		return stepError{"download", err}
	}
	if err := VerifyChecksums(checksums, sig); err != nil {
		return stepError{"signature", err}
	}
	if err := verifySHA256(checksums, asset, tarball); err != nil {
		return stepError{"checksum", err}
	}

	stage := StageDir(stateDir)
	if err := os.RemoveAll(stage); err != nil {
		return stepError{"stage", err}
	}
	if err := os.MkdirAll(stage, 0o700); err != nil {
		return stepError{"stage", err}
	}
	if err := extractBinaries(tarball, stage); err != nil {
		return stepError{"extract", err}
	}
	if err := os.WriteFile(filepath.Join(stage, "VERSION"), []byte(tag+"\n"), 0o600); err != nil {
		return stepError{"stage", err}
	}
	return nil
}

// fetch is one bounded GET, capped at maxAssetBytes.
func fetch(ctx context.Context, url string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, resolveTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrOffline, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("upgrade: GET %s: status %d", url, resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxAssetBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxAssetBytes {
		return nil, fmt.Errorf("upgrade: %s exceeds the %d-byte cap", url, maxAssetBytes)
	}
	return data, nil
}

// verifySHA256 finds asset's line in checksums.txt (sha256sum format:
// "<hex>  <name>") and compares digests.
func verifySHA256(checksums []byte, asset string, data []byte) error {
	sum := sha256.Sum256(data)
	want := ""
	for _, line := range strings.Split(string(checksums), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 2 && fields[1] == asset {
			want = fields[0]
			break
		}
	}
	if want == "" {
		return fmt.Errorf("upgrade: checksums.txt has no entry for %s", asset)
	}
	if got := hex.EncodeToString(sum[:]); got != want {
		return fmt.Errorf("upgrade: %s hashes to %s, checksums.txt says %s", asset, got, want)
	}
	return nil
}

// extractBinaries unpacks EXACTLY the members named swarm and swarm-remote --
// bare names only, so a hostile tarball's path traversal, symlink or device
// entries are never even considered -- and requires swarm to be present.
func extractBinaries(tarball []byte, stage string) error {
	gz, err := gzip.NewReader(strings.NewReader(string(tarball)))
	if err != nil {
		return err
	}
	tr := tar.NewReader(gz)
	found := false
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		name := filepath.Base(filepath.Clean(hdr.Name))
		if (name != "swarm" && name != "swarm-remote") || hdr.Typeflag != tar.TypeReg {
			continue
		}
		if hdr.Size > maxAssetBytes {
			return fmt.Errorf("upgrade: tar member %s exceeds the size cap", name)
		}
		dst, err := os.OpenFile(filepath.Join(stage, name), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o755)
		if err != nil {
			return err
		}
		if _, err := io.Copy(dst, io.LimitReader(tr, maxAssetBytes)); err != nil {
			_ = dst.Close()
			return err
		}
		if err := dst.Close(); err != nil {
			return err
		}
		if name == "swarm" {
			found = true
		}
	}
	if !found {
		return errors.New("upgrade: the release archive carries no swarm binary")
	}
	return nil
}
