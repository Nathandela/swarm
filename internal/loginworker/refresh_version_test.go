package loginworker

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/persist"
)

func TestCodexRefreshVersionQualification(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	native, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	native, err = filepath.EvalSymlinks(native)
	if err != nil {
		t.Fatal(err)
	}
	fingerprint, err := persist.CLIFingerprint(native)
	if err != nil {
		t.Fatal(err)
	}
	for _, version := range []string{"0.163.0", "0.164.0", "0.165.0", "0.164.0-beta.1", "0.164.0+build", "v0.164.0"} {
		config := RefreshConfig{StateRoot: root, LoginID: "00112233445566778899aabbccddeeff", LaunchNonce: "ffeeddccbbaa99887766554433221100", Generation: 1, NativePath: native, NativeVersion: version, NativeFingerprint: fingerprint, Deadline: time.Now().Add(5 * time.Second)}
		err := validateRefresh(config)
		if version == "0.163.0" || version == "0.164.0" {
			if err != nil {
				t.Errorf("qualified version %s: %v", version, err)
			}
		} else if !errors.Is(err, ErrUnsupported) {
			t.Errorf("unqualified version %s: %v", version, err)
		}
	}
}
