package tui

// v0.4 committee fix wave — failing-first tests for two auto-upgrade defects:
//
//   item 2 (codex): compareSemver ignored pre-release suffixes, so a pre-release
//     build (0.4.0-rc.1) compared EQUAL to its release (0.4.0). A client on the
//     final build then saw skewNone against an rc daemon and never upgraded it. A
//     pre-release must sort BEFORE its release (semver ordering), so an rc daemon vs
//     a final client is skewUpgrade and a final daemon vs an rc client is skewNotify.
//
//   item 3 (codex+Fable): the daemonRestartedMsg handler dropped the post-upgrade
//     Subscribe error, leaving m.events nil and the board silently dead. A failed
//     re-subscribe must surface on the banner (never a silent nil stream).

import (
	"errors"
	"strings"
	"testing"

	"github.com/Nathandela/swarm/internal/protocol"
)

// ---------------------------------------------------------------------------
// item 2 — pre-release ordering in compareSemver and classifySkew.
// ---------------------------------------------------------------------------

func TestCompareSemver_PrereleaseOrdersBeforeRelease(t *testing.T) {
	cases := []struct {
		a, b string
		want int
		note string
	}{
		{"0.4.0-rc.1", "0.4.0", -1, "a pre-release sorts before its release"},
		{"0.4.0", "0.4.0-rc.1", 1, "a release sorts after its pre-release"},
		{"0.4.0-rc.1", "0.4.0-rc.2", -1, "rc.1 before rc.2 (suffix string order)"},
		{"0.4.0-rc.2", "0.4.0-rc.1", 1, "rc.2 after rc.1 (suffix string order)"},
		{"0.4.0-rc.1", "0.4.0-rc.1", 0, "identical pre-releases are equal"},
		{"0.4.0", "0.4.0", 0, "identical releases are equal"},
		{"0.4.1-rc.1", "0.4.0", 1, "a higher numeric core wins over the suffix"},
		{"0.4.0-rc.1", "0.5.0", -1, "a lower numeric core loses regardless of suffix"},
	}
	for _, c := range cases {
		if got := compareSemver(c.a, c.b); got != c.want {
			t.Errorf("compareSemver(%q,%q) = %d, want %d (%s)", c.a, c.b, got, c.want, c.note)
		}
	}
}

// The auto-upgrade decision must fire when the daemon is a pre-release of the
// client's final build, and only warn in the reverse direction.
func TestClassifySkew_PrereleaseDirections(t *testing.T) {
	if got := classifySkew("0.4.0-rc.1", "0.4.0"); got != skewUpgrade {
		t.Errorf("rc daemon vs final client must auto-upgrade; got %v", got)
	}
	if got := classifySkew("0.4.0", "0.4.0-rc.1"); got != skewNotify {
		t.Errorf("final daemon vs rc client must warn (cannot self-heal); got %v", got)
	}
	// dev suppression is preserved.
	if got := classifySkew("dev", "0.4.0-rc.1"); got != skewNone {
		t.Errorf("a dev daemon must stay suppressed; got %v", got)
	}
}

// ---------------------------------------------------------------------------
// item 3 — a failed post-upgrade re-subscribe banners instead of silently
// nil-ing the event stream.
// ---------------------------------------------------------------------------

// subscribeErrClient is a fresh reconnected client whose Subscribe fails: it
// exercises the post-upgrade re-subscribe error path without touching the shared
// fakeClient's happy path.
type subscribeErrClient struct {
	*fakeClient
	subErr error
}

func (c subscribeErrClient) Subscribe() (<-chan protocol.Event, error) {
	return nil, c.subErr
}

func TestDaemonRestarted_SubscribeFailureBanners(t *testing.T) {
	newDaemon := subscribeErrClient{fakeClient: newFakeClient(), subErr: errors.New("subscribe refused after restart")}
	gm := newGeneralModel(nil)
	gm.width = testCols
	m := rootModel{general: gm, width: testCols, height: testRows, daemonVersion: "0.1.0", clientVersion: "0.2.0", restartAttempted: true}

	m2 := send(m, daemonRestartedMsg{client: newDaemon})
	rm := m2.(rootModel)

	if rm.events != nil {
		t.Fatal("a failed re-subscribe must leave the event stream nil, not a live-but-broken channel")
	}
	v := stripANSI(m2.View().Content)
	if !strings.Contains(v, "event stream") || !strings.Contains(v, "subscribe refused after restart") {
		t.Fatalf("a failed re-subscribe must banner the reason, never silently dead; view:\n%s", v)
	}
}
