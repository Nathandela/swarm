package loginworker

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/Nathandela/swarm/internal/persist"
)

func TestRefreshVersionRejectsStopFailure(t *testing.T) {
	for _, test := range []struct {
		name       string
		publishErr error
	}{{"version-success", nil}, {"publish-failure", errors.New("receipt write failed")}} {
		t.Run(test.name, func(t *testing.T) {
			dir, err := filepath.EvalSymlinks(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			executable, err := os.Executable()
			if err != nil {
				t.Fatal(err)
			}
			native := newRefreshFixtureTransport(t, dir, refreshFixtureTransport{Target: executable, Version: "0.164.0"})
			fingerprint, err := persist.CLIFingerprint(native)
			if err != nil {
				t.Fatal(err)
			}
			stopped := false
			err = refreshVersion(context.Background(), RefreshConfig{NativePath: native, NativeVersion: "0.164.0", NativeFingerprint: fingerprint}, &jobFiles{path: dir}, nil, func(child *nativeChild) error {
				if err := child.stop(); err != nil {
					t.Errorf("actual native cleanup: %v", err)
				}
				stopped = true
				return ErrUnavailable
			}, func(ProcessIdentity) error { return test.publishErr })
			if !stopped || err != nativeFailure("refresh_version_stop_failed") {
				t.Fatalf("stop failure lost: stopped=%v err=%v", stopped, err)
			}
		})
	}
}
