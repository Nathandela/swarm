package logins

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestCodexAuxiliaryQualifiedVersions(t *testing.T) {
	for _, version := range []string{"0.163.0", "0.164.0"} {
		t.Run(version, func(t *testing.T) {
			_, _, lease := ownedGrantFixture(t)
			if err := lease.PrepareAuxiliary(version); err != nil {
				t.Fatal(err)
			}
			if err := os.Mkdir(filepath.Join(lease.Home(), "tmp"), 0700); err != nil {
				t.Fatal(err)
			}
			if err := lease.CheckAuxiliary(); err != nil {
				t.Fatal(err)
			}
			if err := lease.CleanupAuxiliary(); err != nil {
				t.Fatal(err)
			}
			if _, err := os.Stat(filepath.Join(lease.Home(), "auth.json")); err != nil {
				t.Fatalf("grant lost during auxiliary cleanup: %v", err)
			}
			if _, err := os.Lstat(filepath.Join(lease.Home(), "tmp")); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("qualified scratch not retired: %v", err)
			}
		})
	}
}

func TestCodexAuxiliaryUnqualifiedVersionDoesNotClaimScratch(t *testing.T) {
	_, _, lease := ownedGrantFixture(t)
	for _, version := range []string{"0.162.0", "0.165.0", "0.164.0-beta.1", "0.164.0+build", "v0.164.0", "0.164"} {
		if err := lease.PrepareAuxiliary(version); !errors.Is(err, ErrIneligible) {
			t.Fatalf("version %q: %v", version, err)
		}
	}
	if _, err := os.Lstat(filepath.Join(lease.Home(), codexAuxFile)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("unqualified version claimed scratch: %v", err)
	}
}
