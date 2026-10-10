package upgrade

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Nathandela/swarm/internal/codexstock"
)

// Claude Code's legacy proper-lockfile lock is the sibling directory
// profiles/<profile>.lock; a crash can leave it behind (ADR-031).
func TestAccountStockCustodyGuardAcceptsLeftoverLegacyLockDirectory(t *testing.T) {
	for name, mode := range map[string]os.FileMode{"private": 0700, "umask-default": 0755} {
		t.Run(name, func(t *testing.T) {
			state, profile, _ := stockCustodyFixture(t, codexstock.PendingFile)
			lock := profile + ".lock"
			if err := os.Mkdir(lock, 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(lock, mode); err != nil {
				t.Fatal(err)
			}
			if err := accountStateGuard(state, boundaryCard(4)); err != nil {
				t.Fatal("leftover legacy lock directory failed the custody guard", err)
			}
			if err := accountStateGuard(state, boundaryCard(3)); err == nil {
				t.Fatal("legacy lock directory hid the stock custody requirement")
			}
		})
	}
}

func TestAccountStockCustodyGuardRejectsOtherProfileEntries(t *testing.T) {
	hex := strings.Repeat("a", 32)
	for _, change := range []string{"lock-file", "lock-symlink", "uppercase-lock", "short-lock", "other-suffix", "bare-suffix"} {
		t.Run(change, func(t *testing.T) {
			state, profile, _ := stockCustodyFixture(t, codexstock.PendingFile)
			profiles := filepath.Dir(profile)
			var err error
			switch change {
			case "lock-file":
				err = os.WriteFile(profile+".lock", nil, 0600)
			case "lock-symlink":
				target := filepath.Join(state, "lock-target")
				if err = os.Mkdir(target, 0700); err == nil {
					err = os.Symlink(target, profile+".lock")
				}
			case "uppercase-lock":
				err = os.Mkdir(filepath.Join(profiles, strings.ToUpper(strings.Repeat("b", 32))+".lock"), 0700)
			case "short-lock":
				err = os.Mkdir(filepath.Join(profiles, hex[:31]+".lock"), 0700)
			case "other-suffix":
				err = os.Mkdir(filepath.Join(profiles, hex+".locked"), 0700)
			case "bare-suffix":
				err = os.Mkdir(filepath.Join(profiles, ".lock"), 0700)
			}
			if err != nil {
				t.Fatal(err)
			}
			if err := accountStateGuard(state, boundaryCard(4)); err == nil {
				t.Fatal("unexpected profiles entry passed the custody guard")
			}
		})
	}
}
