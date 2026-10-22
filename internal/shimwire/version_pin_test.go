//go:build !compat_shim_old && !compat_shim_new

package shimwire

import "testing"

func TestShimwireVersionRequiresOfflineLoginCutover(t *testing.T) {
	if Version != 2 {
		t.Fatalf("shimwire.Version = %d, want 2; update the guarded offline migration before changing the wire contract", Version)
	}
}
