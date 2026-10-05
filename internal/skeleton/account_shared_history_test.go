package skeleton

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/Nathandela/swarm/internal/accountconfig"
)

func TestNativeAccountHistoryHandoffKeepsOriginalCustodyAndDoesNotCopy(t *testing.T) {
	store, state, bindings := accountTestStore(t, 2)
	home, cwd, env := accountTestLaunchEnvironment(t, state)
	lease := func(_ string, install func() error) error { return install() }
	var ref string
	for i := range bindings {
		profile, err := store.ProfilePath(bindings[i])
		if err != nil {
			t.Fatal(err)
		}
		p, err := accountconfig.PrepareNative(state, "codex", profile, cwd, env, []string{"codex", "--model", "exact-model"}, ref, "", lease, true)
		if err != nil {
			t.Fatal(err)
		}
		bindings[i].ConfigurationGeneration = p.Generation
		ref = p.Ref
	}
	source := accountTestSource(t, state, bindings[0])
	source.Cwd, source.Env, source.AccountProjectionRef = cwd, env, ref
	rel := accountTestRollout(t, store, bindings[0], cwd, "opaque native history\n")
	original := filepath.Join(home, ".codex", rel)
	before, err := os.Stat(original)
	if err != nil {
		t.Fatal(err)
	}
	resolver := newAccountResumeHistoryResolver(state, nil)
	manifest, err := preflightAccountHistory(store, resolver, source, bindings[1], accountHistoryOwnership{}, "shared-first")
	if err != nil {
		t.Fatal(err)
	}
	if manifest.SchemaVersion != 2 || manifest.NativeContextRef != ref {
		t.Fatal("shared history lost context authority")
	}
	prepared, err := prepareAccountHistory(state, store, manifest)
	if err != nil || !prepared.Published {
		t.Fatalf("shared history transaction failed: %v", err)
	}
	if err := verifyAccountHistory(store, prepared); err != nil {
		t.Fatal(err)
	}
	after, err := os.Stat(original)
	if err != nil || !os.SameFile(before, after) {
		t.Fatal("handoff replaced original history")
	}
	for _, binding := range bindings {
		profile, _ := store.ProfilePath(binding)
		target, err := os.Readlink(filepath.Join(profile, "sessions"))
		if err != nil || target != filepath.Join(home, ".codex", "sessions") {
			t.Fatal("account did not retain explicit original history alias")
		}
	}
	bad := prepared
	bad.Destination.ConfigurationGeneration++
	if err := validateAccountManifest(bad); err == nil {
		t.Fatal("shared history accepted another configuration generation")
	}
	// A stale original history root cannot retain its authority by pathname.
	sessions := filepath.Join(home, ".codex", "sessions")
	if err := os.Rename(sessions, sessions+"-retained"); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(sessions, 0700); err != nil {
		t.Fatal(err)
	}
	if err := verifyAccountHistory(store, prepared); err == nil {
		t.Fatal("replaced shared history root was accepted")
	}
}
