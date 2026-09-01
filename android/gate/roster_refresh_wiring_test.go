package gate

import (
	"path/filepath"
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

// Foreground/network convergence may ask for current authority, but a lifecycle callback does
// not authorize deletion. This pins the two intentionally different roster-only paths end to end:
// automatic SyncRoster is non-destructive; the visible Inbox Reload retains the guarded recovery.
func TestWiring_AutomaticRosterSyncCannotEnterMailboxDiscardRecovery(t *testing.T) {
	activityPath := filepath.Join(androidRoot(t), "app/src/main/kotlin/dev/swarm/phone/PhoneActivity.kt")
	resume := reachableInFile(t, activityPath, "onResume", 2)
	if !strings.Contains(resume, "surface.resume(") {
		t.Fatalf("PhoneActivity.onResume does not arm PhoneSurface's foreground anti-entropy seam")
	}

	surfacePath := phoneSurfacePath(t)
	surfaceResume := reachableInFile(t, surfacePath, "resume", 2)
	for _, want := range []string{"rosterAntiEntropy.foreground", "render()"} {
		if !strings.Contains(surfaceResume, want) {
			t.Errorf("PhoneSurface.resume does not contain %q", want)
		}
	}
	drive := reachableInFile(t, surfacePath, "driveRosterAntiEntropy", 2)
	for _, want := range []string{"rosterAntiEntropy.observe", "ConnectionState.ONLINE", ".syncRoster("} {
		if !strings.Contains(drive, want) {
			t.Errorf("automatic roster convergence does not contain %q\n%s", want, s17Indent(drive))
		}
	}
	for _, forbidden := range []string{".refreshRoster(", ".repairTranscript("} {
		if strings.Contains(drive, forbidden) {
			t.Errorf("automatic roster convergence reaches %s; only visible Reload may repair/discard", forbidden)
		}
	}

	bridge := reachableInFile(t, facadeBridgePath(t), "syncRoster", 1)
	if !s17NamesVerb(bridge, "SyncRoster") {
		t.Errorf("FacadeBridge.syncRoster does not terminate in App.SyncRoster\n%s", s17Indent(bridge))
	}
	mobileApp := string(readFileOrFail(t, filepath.Join(repoRoot(t), "mobile/app.go"), "passive roster facade"))
	marker := "func (a *App) SyncRoster()"
	start := strings.Index(mobileApp, marker)
	if start < 0 {
		t.Fatal("mobile.App.SyncRoster is absent")
	}
	open := start + strings.Index(mobileApp[start:], "{")
	passive, ok := braceBody(mobileApp, open)
	if !ok {
		t.Fatal("mobile.App.SyncRoster body is unreadable")
	}
	if strings.Contains(passive, "requestMailboxDiscard") {
		t.Error("automatic App.SyncRoster enters requestMailboxDiscard; lifecycle did not authorize deletion")
	}
}

// TestWiring_InboxRefreshHasABoundedForegroundDeadline pins the part the facade cannot own:
// RefreshRoster returns when its request was appended, while completion is a later durable roster
// generation. Without a foreground-owned deadline, a lost reply leaves the Inbox saying
// "Refreshing…" forever and disables the only retry affordance.
func TestWiring_InboxRefreshHasABoundedForegroundDeadline(t *testing.T) {
	path := phoneSurfacePath(t)
	surface := string(readFileOrFail(t, path, "Inbox refresh deadline"))
	if !strings.Contains(surface, "INBOX_REFRESH_TIMEOUT_MILLIS = 20_000L") {
		t.Errorf("PhoneSurface does not give Inbox refresh its specified 20 second deadline")
	}
	if !strings.Contains(surface,
		"inboxRefreshHandler = android.os.Handler(android.os.Looper.getMainLooper())") {
		t.Errorf("Inbox refresh's deadline is not owned by the Android main looper")
	}
	refresh := reachableInFile(t, path, "refreshInbox", 2)
	if !strings.Contains(refresh, "scheduleInboxRefreshTimeout") {
		t.Errorf("Inbox refresh arms no completion deadline. A request whose roster reply is lost "+
			"will remain in-flight forever.\nreachable from refreshInbox:\n%s", s17Indent(refresh))
	}
	if !strings.Contains(refresh, "deferInboxRefreshRetry") {
		t.Errorf("a retry refused by the still-crossing dispatch key forgets the original request's " +
			"late authoritative roster")
	}
	if !strings.Contains(refresh, "if (deferInboxRefreshRetry())") {
		t.Errorf("a key-busy retry reports failure even when its immediate redraw already observed " +
			"an authoritative roster")
	}
	schedule := reachableInFile(t, path, "scheduleInboxRefreshTimeout", 1)
	for _, want := range []string{"postDelayed", "INBOX_REFRESH_TIMEOUT_MILLIS"} {
		if !strings.Contains(schedule, want) {
			t.Errorf("Inbox refresh deadline scheduler does not contain %q; the helper may be hollow.\n%s",
				want, s17Indent(schedule))
		}
	}
	cancel := reachableInFile(t, path, "cancelInboxRefreshTimeout", 1)
	if !strings.Contains(cancel, "removeCallbacks") {
		t.Errorf("Inbox refresh cancellation removes no main-looper callback")
	}
	expiry := reachableInFile(t, path, "expireInboxRefresh", 1)
	for _, want := range []string{".expire()", "REFRESH_TIMEOUT", "render()"} {
		if !strings.Contains(expiry, want) {
			t.Errorf("Inbox refresh expiry does not contain %q, so it cannot return the button to "+
				"retry, explain the failure, and redraw cached rows.\nreachable from expiry:\n%s",
				want, s17Indent(expiry))
		}
	}
	if firstRender, expire := strings.Index(expiry, "render()"), strings.Index(expiry, ".expire()"); firstRender < 0 || expire < 0 || firstRender > expire {
		t.Errorf("Inbox refresh reports timeout before its final synchronous roster observation; " +
			"a roster already committed at the deadline can produce a false failure toast")
	}
	if strings.Contains(expiry, "inboxCache.clear") {
		t.Errorf("Inbox refresh expiry clears cached conversations instead of keeping them visible")
	}
	deferred := reachableInFile(t, path, "deferInboxRefreshRetry", 1)
	for _, want := range []string{"cancelInboxRefreshTimeout", ".expire()"} {
		if !strings.Contains(deferred, want) {
			t.Errorf("key-busy refresh retry does not contain %q, so it cannot return idle while "+
				"retaining late-roster recognition", want)
		}
	}
	if strings.Contains(deferred, ".refused()") {
		t.Errorf("key-busy retry is treated as a definitive refusal and forgets the original late reply")
	}
	refusal := reachableInFile(t, path, "refuseInboxRefresh", 1)
	for _, want := range []string{"cancelInboxRefreshTimeout", ".refused()"} {
		if !strings.Contains(refusal, want) {
			t.Errorf("definitive refresh refusal does not contain %q", want)
		}
	}
	settle := reachableInFile(t, path, "renderReady", 2)
	if !strings.Contains(settle, "cancelInboxRefreshTimeout") {
		t.Errorf("an authoritative roster generation does not cancel the old refresh deadline")
	}
	release := reachableInFile(t, path, "release", 2)
	if !strings.Contains(release, "cancelInboxRefreshTimeout") {
		t.Errorf("PhoneSurface.release leaves the Inbox refresh callback holding a background screen")
	}
}
