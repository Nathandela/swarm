package gate

// FAILING-FIRST (TDD RED, GG-5) for Wave R3 round 3, the review's production-reachability
// finding, Kotlin half: WakeReceiptPolicy -- the tested, total-and-quiet decision object
// for one wake verdict -- had ZERO production callers. SwarmMessagingService rendered its
// own notification inline, so the policy the round-1 tests pinned ("a dropped wake renders
// nothing and is reported exactly once; an accepted wake renders only the existing generic
// notification") governed no shipped path, which is this project's standing defect class
// written into PB-PUSH-9 ("a facade method can exist while no Android code ever calls
// it").
//
// Source-level, like the neighbouring fences: matching on comment-stripped Kotlin
// (kotlinCodeOnly), because this package was once defeated by a fence a comment satisfied.
// Nothing here runs Gradle. The POLICY's behavior is pinned by its own Robolectric suite
// (WakeReceiptPolicyTest); this fence pins only that the shipped service routes through
// it.

import (
	"path/filepath"
	"strings"
	"testing"
)

// TestR3AR3_TheMessagingServiceRoutesItsVerdictThroughWakeReceiptPolicy: the one OS entry
// point for a wake must translate the core's answer into a WakeVerdict and hand it to
// WakeReceiptPolicy.handle -- never render inline. Inline rendering is a second copy of
// the drop/render decision, and a second copy is where "a refused wake renders NOTHING"
// silently stops being true on the shipped path while the policy's tests stay green.
func TestR3AR3_TheMessagingServiceRoutesItsVerdictThroughWakeReceiptPolicy(t *testing.T) {
	path := filepath.Join(appModule(t), "src", "main", "kotlin", "dev", "swarm", "phone",
		"push", "SwarmMessagingService.kt")
	code := kotlinCodeOnly(readFileOrFail(t, path,
		"the FCM entry point routes its verdict through the tested receipt policy"))

	if !strings.Contains(code, "WakeReceiptPolicy.handle(") {
		t.Errorf("%s: the service never calls WakeReceiptPolicy.handle; the tested "+
			"drop/render policy has no production caller and the shipped receipt decides "+
			"rendering by a second, untested copy of the rules", mustRel(t, path))
	}
	if !strings.Contains(code, "WakeVerdict.Dropped") || !strings.Contains(code, "WakeVerdict.Accepted(") {
		t.Errorf("%s: the service does not translate the core's answer into WakeVerdict "+
			"(Dropped on refusal, Accepted on success); the policy cannot govern what it is "+
			"never told", mustRel(t, path))
	}

	// The rendering itself belongs to the policy: a service that still notifies inline
	// has kept the second copy alive beside the first.
	if strings.Contains(code, ".notify(") {
		t.Errorf("%s: the service still renders a notification inline (.notify(...)); "+
			"rendering is WakeReceiptPolicy's, and an inline copy is the one that drifts "+
			"toward rendering on a refused wake", mustRel(t, path))
	}
}
