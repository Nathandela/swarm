//go:build linux

package accountcheck

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestOwnedCheckHomeLocalStateReachesNativeAndRetainsContainment(t *testing.T) {
	for _, mode := range []string{ModeAuthStatus, ModeAvailability} {
		t.Run(mode, func(t *testing.T) {
			exe, cfg, fixture := checkFixture(t, "success", mode)
			home := filepath.Join(filepath.Dir(cfg.StateRoot), "home")
			state := filepath.Join(home, ".local", "state", "swarm")
			if err := os.MkdirAll(filepath.Dir(state), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.Rename(cfg.StateRoot, state); err != nil {
				t.Fatal(err)
			}
			cfg.StateRoot = state
			cfg.Cwd = filepath.Join(state, "accounts", "jobs", "synthetic-access")
			if err := os.MkdirAll(cfg.Cwd, 0o700); err != nil {
				t.Fatal(err)
			}
			global := filepath.Join(home, ".claude")
			if err := os.Mkdir(global, 0o700); err != nil {
				t.Fatal(err)
			}
			for _, name := range []string{"settings.json", "settings.local.json"} {
				if err := os.WriteFile(filepath.Join(global, name), []byte(`{"hooks":{"Stop":[]}}`), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			var ref Ref
			raw, err := Run(context.Background(), exe, cfg, func(got Ref) error { ref = got; return nil })
			if err != nil {
				t.Fatalf("normal HOME-local check was refused: %v", err)
			}
			want := `"loggedIn":true`
			if mode == ModeAvailability {
				want = `"subtype":"success"`
			}
			if !strings.Contains(string(raw), want) {
				t.Fatal("synthetic native did not run")
			}
			if fixtureWriter(t, fixture).Alive() || !CustodyStopped(state, ref.Worker, cfg.Binding) || !fixtureWritersStopped(t, state, cfg.Binding) {
				t.Fatal("successful HOME-local check lost exact writer containment proof")
			}
		})
	}
}
