// FAILING-FIRST (TDD RED, GG-5) test for CR-4: the per-mailbox depth cap must
// be ON by default, not left at 0 (off). Enforcement already exists in
// server.go (an append past Quotas.MailboxMaxItems is refused with a clean
// ErrQuotaExceeded — see TestMailbox_DepthCapCleanQuota in r2_test.go), but
// DefaultConfig never sets MailboxMaxItems, so capN > 0 is false and the DoS
// control is silently off for every caller that boots from defaults (the
// shipped binary, cmd/swarm-relay, included). This test FAILS today because
// DefaultConfig().Quotas.MailboxMaxItems is the zero value.
package relay

import "testing"

// TestDefaultConfig_MailboxDepthCapOnByDefault asserts DefaultConfig enables
// the CR-4 per-mailbox depth cap out of the box. The exact number is an
// implementer choice (deliberately not asserted here, so this test stays
// robust to whatever default value is picked) — only that it is a positive,
// enforced cap rather than the unbounded-by-default 0.
func TestDefaultConfig_MailboxDepthCapOnByDefault(t *testing.T) {
	got := DefaultConfig().Quotas.MailboxMaxItems
	if got <= 0 {
		t.Fatalf("DefaultConfig().Quotas.MailboxMaxItems = %d, want > 0: CR-4 requires the per-mailbox depth DoS cap to be enabled by default (a device that never drains must not be able to drive unbounded mailbox storage growth on a default-configured relay)", got)
	}
}
