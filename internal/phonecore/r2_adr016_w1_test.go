package phonecore

// ADR-016 W1 (FAILING FIRST, RED phase, bd agents-tracker-hggx.3.5): "The policy is a
// field of ... phonecore.State, alongside relay_host". This pins the DURABLE half of W1's
// four artifacts: a new State.RelayTLSPolicy field, independent of the RelaySPKIPin field
// that already exists (ADR-007 B33/B34), surviving a Save/Resume round trip through the
// real production path -- not a hand-authored fixture literal, since the literal-pinning
// discipline (TestStateSchemaVersion_IsPinnedToTheDurableFieldSet, stateFixtures) is
// GREEN-phase work that names the exact StateSchemaVersion bump and its byte-literal once
// the field actually exists on stateFile.
//
// W4.4 is exercised here too: "The commit does not clear RelaySPKIPin... Retention is the
// ruling and clearing is the defect." So this file also pins that writing RelayTLSPolicy
// alone never touches RelaySPKIPin, and vice versa.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// r2w1Resume mirrors this package's own restart convention (state_test.go): the SAME
// wake/content sealer pair must be reused across the two Resume calls that model a
// restart, since each is backed by the same persistent Keystore alias in production. A
// fresh sealer per call would model losing the Keystore key on every restart, not a
// restart.
func r2w1Resume(t *testing.T, dir string, wake, content Sealer) *Core {
	t.Helper()
	c, err := Resume(Config{Dir: dir, Machine: "m1", WakeSealer: wake, ContentSealer: content})
	if err != nil {
		t.Fatalf("Resume: %v", err)
	}
	return c
}

// TestADR016W1_StateCarriesRelayTLSPolicyIndependentOfThePin is the Conformance table's
// "Policy / pin independence" row at the durable-state layer: setting RelayTLSPolicy
// leaves RelaySPKIPin untouched, setting RelaySPKIPin leaves RelayTLSPolicy untouched, and
// BOTH survive a Save then a fresh Resume over the same directory (a process restart).
func TestADR016W1_StateCarriesRelayTLSPolicyIndependentOfThePin(t *testing.T) {
	dir := t.TempDir()
	wake, content := s14aNewSealer(t), s14aNewSealer(t)
	c := r2w1Resume(t, dir, wake, content)

	pin := []byte("32-byte-sha256-digest-of-spki!!")
	if err := c.Mutate(func(s *State) {
		s.Machine = "m1"
		s.RelayTLSPolicy = "webpki"
		s.RelaySPKIPin = pin // W4.4: retained even under webpki, just not consulted (W3)
	}); err != nil {
		t.Fatalf("Mutate: %v", err)
	}

	reopened := r2w1Resume(t, dir, wake, content)
	got := reopened.State()
	if got.RelayTLSPolicy != "webpki" {
		t.Errorf("RelayTLSPolicy after restart = %q, want %q", got.RelayTLSPolicy, "webpki")
	}
	if string(got.RelaySPKIPin) != string(pin) {
		t.Errorf("RelaySPKIPin after restart = %x, want %x (W4.4: the commit must not clear it)",
			got.RelaySPKIPin, pin)
	}

	// Now flip ONLY the pin (a rotation, or a fresh pairing under B54) and confirm the
	// policy field is untouched by it.
	if err := reopened.Mutate(func(s *State) {
		s.RelaySPKIPin = nil
	}); err != nil {
		t.Fatalf("Mutate (clear pin): %v", err)
	}
	if reopened.State().RelayTLSPolicy != "webpki" {
		t.Errorf("clearing RelaySPKIPin changed RelayTLSPolicy to %q; the two fields must be "+
			"independent in both directions", reopened.State().RelayTLSPolicy)
	}
}

// TestADR016W1_StateFileJSONKeyIsRelayTLSPolicy pins the on-disk key name, matching the
// same wire spelling relaycfg.Config and MachinePayload use ("relay_tls_policy").
func TestADR016W1_StateFileJSONKeyIsRelayTLSPolicy(t *testing.T) {
	dir := t.TempDir()
	c := r2w1Resume(t, dir, s14aNewSealer(t), s14aNewSealer(t))
	if err := c.Mutate(func(s *State) {
		s.Machine = "m1"
		s.RelayTLSPolicy = "pinned_spki"
	}); err != nil {
		t.Fatalf("Mutate: %v", err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, StateFileName))
	if err != nil {
		t.Fatalf("read state file: %v", err)
	}
	if !strings.Contains(string(raw), `"relay_tls_policy":"pinned_spki"`) {
		t.Errorf("phone-state.json does not carry relay_tls_policy=pinned_spki verbatim: %s", raw)
	}
}
