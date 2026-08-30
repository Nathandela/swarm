package gate

import (
	"path/filepath"
	"strings"
	"testing"
)

// TestConversationReloadUsesBoundedNewestHistory fences the full Android call path that
// replaces a potentially multi-megabyte whole-journal reseed. Both the stale notice's Reload
// control and an in-transcript gap must reach one claimed, session-scoped latest-page read.
func TestConversationReloadUsesBoundedNewestHistory(t *testing.T) {
	path := phoneSurfacePath(t)
	surface := string(readFileOrFail(t, path, "conversation reload wiring"))
	start := strings.Index(surface, "private val resyncControl")
	if start < 0 {
		t.Fatal("PhoneSurface has no conversation Reload control")
	}
	rest := surface[start:]
	end := strings.Index(rest, "\n    private val ")
	if end < 0 {
		t.Fatal("cannot isolate the conversation Reload control")
	}
	control := rest[:end]
	if !strings.Contains(control, "conversationReloadPlan") {
		t.Errorf("conversation Reload does not delegate to the one bounded newest-page plan:\n%s", control)
	}
	plan := reachableInFile(t, path, "conversationReloadPlan", 2)
	if !strings.Contains(plan, "SessionHistoryIntent.NEWEST") ||
		!strings.Contains(plan, "loadEarlierInteractions(target, before, HISTORY_PAGE)") {
		t.Errorf("conversation Reload does not request this session's bounded newest history page:\n%s", plan)
	}
	for _, forbidden := range []string{"repairTranscript(", ".resync("} {
		if strings.Contains(plan, forbidden) {
			t.Errorf("conversation Reload still reaches whole-journal repair through %q:\n%s", forbidden, plan)
		}
	}

	open := reachableInFile(t, path, "backfillOnOpen", 1)
	if !strings.Contains(open, "SessionHistoryIntent.NEWEST") ||
		!strings.Contains(open, "loadEarlierInteractions(session, before, HISTORY_PAGE)") {
		t.Errorf("cold-open does not issue the anchorless newest-page read:\n%s", open)
	}
	if strings.Contains(open, "if (before.isEmpty()) return") {
		t.Errorf("cold-open still drops the intentional empty newest-page anchor:\n%s", open)
	}
	if !strings.Contains(open, "answer.onSuccess") ||
		!strings.Contains(open, "rememberHistoryRead(issued, session, aloud = false)") ||
		!strings.Contains(open, "render()") {
		t.Errorf("cold-open hands Result<T> to an Op reader instead of unwrapping and claiming it:\n%s", open)
	}

	detail := reachableInFile(t, path, "drawDetail", 2)
	if !strings.Contains(detail, "onRepair = ::reloadConversation") {
		t.Errorf("conversation gap Reload is not wired to the bounded conversation repair:\n%s", detail)
	}

	reload := reachableInFile(t, path, "reloadConversation", 2)
	if !strings.Contains(surface, "private fun reloadConversation(control: View)") ||
		!strings.Contains(reload, "press(control, ::conversationReloadPlan)") ||
		strings.Contains(reload, "resyncControl.performClick") {
		t.Errorf("gap Reload does not dispatch against the row the reader actually pressed:\n%s", reload)
	}
}

// TestConversationReloadSurvivesIncrementalRedraw protects both repair affordances: the stale
// control remains above the transcript, and a gap inserted or rebound by the streaming patch
// keeps the callback that turns its own row into the pressed control.
func TestConversationReloadSurvivesIncrementalRedraw(t *testing.T) {
	repo := repoRoot(t)
	viewPath := filepath.Join(repo, "android/app/src/main/kotlin/dev/swarm/phone/ui/screens/SessionDetailView.kt")
	view := string(readFileOrFail(t, viewPath, "conversation repair redraw wiring"))
	if !strings.Contains(view, "if (panel.offersResync) column.addView(resync.tagged(DetailTag.RESYNC)") {
		t.Error("the top stale Reload control is no longer composed beside its stale notice")
	}
	if strings.Count(view, "onRepair = onRepair") < 3 ||
		!strings.Contains(view, "onRepair: ((View) -> Unit)?") ||
		!strings.Contains(view, "onDetail, onRepair, onOutput") {
		t.Error("the gap Reload handler is lost on a transcript rebuild, insert, or rebind")
	}

	surfacePath := phoneSurfacePath(t)
	surface := string(readFileOrFail(t, surfacePath, "global stale honesty"))
	if strings.Count(surface, "onRepair = ::reloadConversation") < 2 {
		t.Error("PhoneSurface does not preserve gap Reload on both initial composition and patch")
	}
	detail := reachableInFile(t, surfacePath, "detailPanel", 1)
	if !strings.Contains(detail, "journalStale = chat.stale") {
		t.Error("a bounded conversation page is being presented as proof that global journal staleness cleared")
	}
}

// TestEveryBoundedHistoryOperationIsClaimed prevents a second session read from overwriting the
// first operation id before its machine outcome has been adopted into the transcript.
func TestEveryBoundedHistoryOperationIsClaimed(t *testing.T) {
	path := phoneSurfacePath(t)
	surface := string(readFileOrFail(t, path, "bounded history operation claims"))
	for _, scalar := range []string{
		"private var historyOp:",
		"private var historyFor:",
		"private var historySpeaks:",
	} {
		if strings.Contains(surface, scalar) {
			t.Errorf("bounded history reads still use overwriteable scalar state %q", scalar)
		}
	}
	if !strings.Contains(surface, "private val historyReads = linkedMapOf<String, PendingHistoryRead>()") {
		t.Error("PhoneSurface has no per-operation bounded-history claim ledger")
	}
	remember := reachableInFile(t, path, "rememberHistoryRead", 1)
	if !strings.Contains(remember, "historyReads[issued.operationID]") {
		t.Errorf("a sealed bounded-history operation is not added to the claim ledger:\n%s", remember)
	}
	render := reachableInFile(t, path, "renderReadVerdicts", 1)
	if !strings.Contains(render, "for ((operationID, pending) in historyReads.toMap())") ||
		!strings.Contains(render, "historyReads.remove(operationID)") {
		t.Errorf("bounded-history outcomes are not each claimed and retired by operation id:\n%s", render)
	}
}

// TestLinkRepairKeepsItsOwnControlAndGlobalVerb prevents the session-scoped Reload refactor from
// either redirecting transport repair or sharing the hidden conversation button's in-flight key.
func TestLinkRepairKeepsItsOwnControlAndGlobalVerb(t *testing.T) {
	path := phoneSurfacePath(t)
	surface := string(readFileOrFail(t, path, "link repair control identity"))
	repair := reachableInFile(t, path, "repairSync", 2)
	if !strings.Contains(surface, "private fun repairSync(control: View)") ||
		!strings.Contains(repair, "press(control)") {
		t.Errorf("link-sheet repair does not disable/settle the row that was actually pressed:\n%s", repair)
	}
	if !strings.Contains(repair, "FacadeBridge(app).repairTranscript()") ||
		strings.Contains(repair, "loadEarlierInteractions") {
		t.Errorf("link-sheet repair no longer owns the global transport-repair verb:\n%s", repair)
	}

	syncPath := filepath.Join(repoRoot(t), "android/app/src/main/kotlin/dev/swarm/phone/ui/screens/SyncStatusView.kt")
	sync := string(readFileOrFail(t, syncPath, "link repair control identity"))
	if !strings.Contains(sync, "onRepair: (View) -> Unit") ||
		!strings.Contains(sync, "setOnClickListener { control -> onRepair(control) }") {
		t.Errorf("the link sheet discards the identity of the repair row it invokes:\n%s", sync)
	}
}
