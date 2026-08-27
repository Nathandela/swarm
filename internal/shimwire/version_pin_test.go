//go:build !compat_shim_v0 && !compat_shim_v2

package shimwire

// FAILING-FIRST guard for auto-upgrade-plan.md section 6: "a shimwire.Version bump turns
// the unattended upgrade into every running session dropped from the board while the
// agents keep running" (internal/daemon/shimclient.go's reconnect hello rejects a
// mismatched WireVersion and the session is marked lost, never driven over a mismatched
// protocol). This test makes a bump a DELIBERATE act -- it must fail the moment someone
// changes the default build's constant without also running the drain procedure in
// docs/ops/auto-upgrade.md.
//
// Built only for the default (untagged) build: version_compat_v0.go and
// version_compat_v2.go each redefine Version under their own build tag for the E14.3
// compat matrix (internal/daemon/compatmatrix_test.go), and this file's tag excludes both
// so it never collides with either redefinition.
import "testing"

func TestShimwireVersion_IsPinnedAtOne(t *testing.T) {
	if Version != 1 {
		t.Fatalf("shimwire.Version = %d, want 1; a bump drops every running session from the "+
			"board across an unattended upgrade -- follow the drain procedure in "+
			"docs/ops/auto-upgrade.md before changing this", Version)
	}
}
