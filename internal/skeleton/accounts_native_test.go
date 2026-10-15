//go:build linux

package skeleton

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/Nathandela/swarm/internal/accountconfig"
	"github.com/Nathandela/swarm/internal/accounts"
	"github.com/Nathandela/swarm/internal/persist"
	"github.com/Nathandela/swarm/internal/protocol"
)

func nativeRetentionFixture(t *testing.T) (*accountManager, []string, string, string) {
	t.Helper()
	m := accountTestManager(t, accountTestState(t))
	home := filepath.Join(m.stateRoot, "home")
	bin := filepath.Join(home, "bin")
	versions := filepath.Join(home, ".local", "share", "claude", "versions")
	for _, path := range []string{bin, versions} {
		if err := os.MkdirAll(path, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	source := filepath.Join(versions, accountconfig.CharacterizedClaudeVersion)
	copyRetentionELF(t, source)
	alias := filepath.Join(bin, "claude")
	if err := os.Symlink(source, alias); err != nil {
		t.Fatal(err)
	}
	return m, []string{"HOME=" + home, "PATH=" + bin + ":/usr/bin:/bin"}, source, alias
}

func copyRetentionELF(t *testing.T, path string) {
	t.Helper()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	source, err := os.Open(executable)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = source.Close() }()
	target, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o700)
	if err != nil {
		t.Fatal(err)
	}
	_, copyErr := io.Copy(target, source)
	closeErr := target.Close()
	if copyErr != nil || closeErr != nil {
		t.Fatalf("copy synthetic ELF: %v %v", copyErr, closeErr)
	}
}

func TestManagedClaudeCustomELFStaysInPlace(t *testing.T) {
	m, env, _, alias := nativeRetentionFixture(t)
	if err := os.Remove(alias); err != nil {
		t.Fatal(err)
	}
	copyRetentionELF(t, alias)
	selected, err := m.managedNative(accounts.ProviderClaude, env, m.stateRoot)
	if err != nil || selected.Path != alias || strings.HasPrefix(selected.Fingerprint, "sha256:") {
		t.Fatalf("custom wrapper falsely relocated: %+v %v", selected, err)
	}
	if _, err := os.Lstat(filepath.Join(m.stateRoot, "accounts", "native")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("custom wrapper created cache: %v", err)
	}
}

func TestManagedClaudeRetentionConcurrentManagersAndPresentation(t *testing.T) {
	m, env, _, _ := nativeRetentionFixture(t)
	t.Setenv("PATH", "/no-retention-provider")
	other := openAccountManager(m.stateRoot, "", nil)
	t.Cleanup(other.close)
	var wg sync.WaitGroup
	identities := make([]*persist.CLIIdentity, 2)
	errors := make([]error, 2)
	for index, manager := range []*accountManager{m, other} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			identities[index], errors[index] = manager.managedNative(accounts.ProviderClaude, env, m.stateRoot)
		}()
	}
	wg.Wait()
	if errors[0] != nil || errors[1] != nil || *identities[0] != *identities[1] {
		t.Fatalf("publishers replaced winner: %+v %+v", identities, errors)
	}
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			for n := 0; n < 3; n++ {
				if index == 0 {
					_, _ = m.managedNative(accounts.ProviderClaude, env, m.stateRoot)
				} else {
					_, _ = m.Accounts(protocol.AccountsReq{Action: "list"})
				}
			}
		}(i)
	}
	wg.Wait()
}

