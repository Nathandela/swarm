package skeleton

// RE-REVIEW fences for the two seams the closure claims lean on hardest and nothing else
// exercises end to end.
//
//  1. §3.5's content_hash still names the item AS SHIPPED when §5's caps actually BIND. R4's own
//     hash fence uses a small item, so truncate-then-hash is asserted where nothing truncates;
//     R2's maxima cases are ASCII, so IS-CAP-1's rune boundary is asserted where every cut is a
//     byte boundary anyway. This drives both at once: an approval_request on §5's documented
//     maxima whose prompt lines are four-byte runes, which is 32 000 bytes of prompt against an
//     8 KiB item cap -- so the uniform ceiling halves several times, every cut lands inside a
//     multi-byte rune, and the digest is taken after all of it.
//
//  2. IS-LIFE-2's `expired` arm through sweepExpiredApprovals, which is otherwise unreached:
//     approvalTTL is a bare constant with no clock seam, so the shipped 120 s window cannot be
//     waited out, and the inline re-check inside approveInteraction is a different code path.
//     `expired` is the resolution for the card NOBODY answers -- the one case where the phone's
//     IS-LIFE-3 retention exemption would otherwise never lift.

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/adapter"
)

// maximaApproval is an approval_request sitting exactly on §5's documented maxima, with the
// prompt lines made of four-byte runes so the rune/byte distinction is load-bearing.
func maximaApproval(ref string) adapter.Interaction {
	in := pendingApprovalInteraction(ref, strings.Repeat("s", specMaxSummaryBytes))
	in.Mode = adapter.ModePromptCard
	in.Action = adapter.ToolAction{
		Type:    "execute",
		Path:    strings.Repeat("p", specMaxSummaryBytes),
		Query:   strings.Repeat("q", specMaxSummaryBytes),
		Command: strings.Repeat("c", specMaxSummaryBytes),
	}
	in.Decisions = nil
	for i := 0; i < specMaxDecisions; i++ {
		in.Decisions = append(in.Decisions, adapter.DecisionChoice{
			ID: "d" + string(rune('0'+i)), Label: strings.Repeat("L", specMaxSummaryBytes),
		})
	}
	line := strings.Repeat("\U0001F600", specMaxPromptLineRunes)
	for i := 0; i < specMaxPromptLines; i++ {
		in.PromptLines = append(in.PromptLines, line)
	}
	return in
}

// TestApprovalRequest_AtTheMaximaTheHashStillNamesTheBytesItShipped.
func TestApprovalRequest_AtTheMaximaTheHashStillNamesTheBytesItShipped(t *testing.T) {
	sk := assemble(t)
	item, raw := captureOne(t, sk, "s-maxima", maximaApproval("req-max"))

	if len(raw) > 8<<10 {
		t.Fatalf("the shipped item is %d bytes, over §5's 8 KiB MaxItemBytes", len(raw))
	}
	if item["truncated"] != true {
		t.Errorf("truncated = %v; want true -- §5's caps bound on this item (IS-CAP-1)", item["truncated"])
	}
	// IS-CAP-1: never mid-rune. encoding/json substitutes U+FFFD for a split rune, so a
	// replacement character in the shipped bytes is proof of a byte-wise cut of a 4-byte rune.
	if strings.ContainsRune(string(raw), '�') {
		t.Errorf("the shipped item carries U+FFFD, so the fit ceiling cut inside a rune. IS-CAP-1: "+
			"\"truncation SHALL be at a UTF-8 rune boundary, never mid-rune\"\n%s", raw)
	}

	hash, _ := item["content_hash"].(string)
	if len(hash) != 64 {
		t.Fatalf("content_hash = %q; want 64 hex characters even after the ceiling fell (it is in "+
			"the never-clipped set precisely because half a digest is a permanently unanswerable card)", hash)
	}
	zeroed := strings.Replace(string(raw),
		`"content_hash":"`+hash+`"`, `"content_hash":"`+strings.Repeat("0", 64)+`"`, 1)
	sum := sha256.Sum256([]byte(zeroed))
	if got := hex.EncodeToString(sum[:]); got != hash {
		t.Errorf("content_hash = %s, but SHA-256 over the SHIPPED bytes with the slot zeroed is %s.\n"+
			"The digest must be taken AFTER the fit (truncate, then hash): a hash over the "+
			"pre-truncation content names a body no surface holds, IS-APR-2 forbids the phone "+
			"recomputing one, and every approve echoed off the truncated card is refused as stale.\n%s",
			hash, got, raw)
	}
}

// TestApprovalResolved_TheDaemonWindowLapsingResolvesACardNobodyAnswered.
func TestApprovalResolved_TheDaemonWindowLapsingResolvesACardNobodyAnswered(t *testing.T) {
	sk := assemble(t)
	m := launchFake(t, sk, "print EXPIRE\nidle 60s\n")
	item := openApprovalOn(t, sk, m.ID, "req-1")
	itemID := itemString(t, item, "item_id")

	// The daemon's own stored window, wound back: what a card minted approvalTTL ago looks
	// like. Expiry is DAEMON-authoritative (§3.5), so this is the whole of the observation.
	sk.itemMu.Lock()
	ap := sk.approvals[m.ID]
	if ap == nil {
		sk.itemMu.Unlock()
		t.Fatalf("no pending approval is held for %s, so there is nothing for the window to lapse on", m.ID)
	}
	ap.expiresAt = time.Now().Add(-time.Second)
	sk.itemMu.Unlock()

	sk.sweepExpiredApprovals()
	res := awaitResolution(t, sk, m.ID, itemID)
	if res["decision"] != "expired" {
		t.Errorf("decision = %v; want \"expired\" (§3.6)", res["decision"])
	}
	if res["by"] != "daemon" {
		t.Errorf("by = %v; want \"daemon\" -- §3.6 attributes an expiry to the daemon, which is the "+
			"only party that observes it", res["by"])
	}

	// EXACTLY ONE, under the sweep's own repetition: the ticker calls it every window, and a
	// resolver that fired twice would put two approval_resolved records on one request.
	sk.sweepExpiredApprovals()
	sk.sweepExpiredApprovals()
	time.Sleep(500 * time.Millisecond)
	n := 0
	for _, it := range interactionItems(t, sk, m.ID) {
		if it["kind"] == adapter.KindApprovalResolved && it["interaction_id"] == itemID {
			n++
		}
	}
	if n != 1 {
		t.Errorf("%d approval_resolved records name %s; IS-LIFE-2 says EXACTLY one. The resolver is "+
			"a no-op when nothing is pending, which is what makes the guarantee hold when the "+
			"expiry ticker races an arriving answer", n, itemID)
	}
}
