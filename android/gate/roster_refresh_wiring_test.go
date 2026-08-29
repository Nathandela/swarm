package gate

import (
	"strings"
	"testing"
)

// TestWiring_InboxRefreshRequestsOnlyAnAuthoritativeRoster is the cross-language fence for the
// all-agent Inbox's user refresh. A general journal repair can carry the entire retained event
// backlog in one reseed; on a cursor-zero handset that can exceed the relay's 1 MiB frame ceiling
// before the roster arrives. This path needs the narrower bound verb whose reply is one roster-only
// reseed at the phone's prior cursor, leaving backlog events to the ordinary paged drain.
func TestWiring_InboxRefreshRequestsOnlyAnAuthoritativeRoster(t *testing.T) {
	surface := reachableInFile(t, phoneSurfacePath(t), "refreshInbox", 2)
	if !strings.Contains(surface, ".refreshRoster(") {
		t.Errorf("Inbox pull-to-refresh does not call FacadeBridge.refreshRoster.\n"+
			"A call to transcript repair can make a cursor-zero backlog one oversized reseed and "+
			"prevent the authoritative roster this gesture asks for from landing.\n"+
			"reachable from refreshInbox:\n%s", s17Indent(surface))
	}
	if strings.Contains(surface, ".repairTranscript(") {
		t.Errorf("Inbox pull-to-refresh calls FacadeBridge.repairTranscript.\n"+
			"That is the stale-transcript repair path, not the roster-only user refresh, and can "+
			"overflow the relay frame ceiling on a large cursor-zero backlog.\n"+
			"reachable from refreshInbox:\n%s", s17Indent(surface))
	}
	bridge := reachableInFile(t, facadeBridgePath(t), "refreshRoster", 1)
	if !s17NamesVerb(bridge, "RefreshRoster") {
		t.Errorf("FacadeBridge.refreshRoster does not call App.RefreshRoster, so the Android "+
			"gesture terminates in a hollow adapter.\nreachable from refreshRoster:\n%s", s17Indent(bridge))
	}
}
