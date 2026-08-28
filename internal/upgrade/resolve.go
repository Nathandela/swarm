package upgrade

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"path"
	"runtime"
	"strings"
	"time"
)

// DefaultBaseURL is the release host. A test substitutes its own httptest
// server; production never overrides it.
const DefaultBaseURL = "https://github.com/Nathandela/swarm"

// resolveTimeout bounds every network call the transaction makes. A nightly
// check that hangs is a oneshot job wedged until somebody looks (committee #5);
// offline is an ordinary outcome, reported and retried tomorrow, never an error
// that reddens anything.
const resolveTimeout = 60 * time.Second

// ErrOffline wraps every network failure of the resolve step, so callers can
// fold "no network at 04:00" into a quiet skip.
var ErrOffline = errors.New("upgrade: release host unreachable")

// LatestVersion resolves the latest published tag from the /releases/latest
// HTTP redirect -- one HEAD, no API, and therefore no per-IP unauthenticated
// rate limit for a NAT'd fleet to exhaust (committee: Gemini #8, Fable LOW).
// The Location it follows ends .../releases/tag/<tag>.
func LatestVersion(ctx context.Context, baseURL string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, resolveTimeout)
	defer cancel()
	var loc string
	client := &http.Client{
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			loc = req.URL.Path
			return http.ErrUseLastResponse // the Location is the answer; never follow
		},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodHead, baseURL+"/releases/latest", nil)
	if err != nil {
		return "", err
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("%w: %v", ErrOffline, err)
	}
	_ = resp.Body.Close()
	if loc == "" {
		if l := resp.Header.Get("Location"); l != "" {
			loc = l
		}
	}
	tag := path.Base(loc)
	if tag == "" || tag == "latest" || tag == "/" || tag == "." {
		return "", fmt.Errorf("upgrade: /releases/latest did not redirect to a tag (status %d)", resp.StatusCode)
	}
	return tag, nil
}

// assetName is the goreleaser archive name for this platform at ver (no "v"):
// swarm_<ver>_linux_<goarch>.tar.gz, and swarm_<ver>_darwin_all.tar.gz -- darwin
// ships ONE universal archive (.goreleaser.yaml universal_binaries), and a port
// that mapped uname to goarch there would 404 on every Mac (committee C4).
func assetName(ver string) string {
	osArch := runtime.GOOS + "_" + runtime.GOARCH
	if runtime.GOOS == "darwin" {
		osArch = "darwin_all"
	}
	return fmt.Sprintf("swarm_%s_%s.tar.gz", strings.TrimPrefix(ver, "v"), osArch)
}
