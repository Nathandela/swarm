package voice

import (
	"errors"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/managerproof"
	"github.com/Nathandela/swarm/internal/protocol"
)

func TestDiscussionUnconfirmedEffectReportsFirstAdmissionWithoutResending(t *testing.T) {
	c, store, native, b := newControllerFixture(t)
	c.discussion = &DiscussionScope{VMProjectAccess: true, InstallationID: "install", GuildID: "guild", ThreadID: "thread"}
	for _, id := range []string{"first", "second"} {
		if _, err := c.submitDiscussion(discussionFixtureOrigin(id), id, "", ""); err != nil {
			t.Fatal(err)
		}
	}
	d := &DiscussionController{controller: c, store: store, now: time.Now, cfg: Config{ConversationID: b.ConversationID}}
	native.status.Turn.ExternalInput++
	var paused *DiscussionInputHold
	if err := d.Sync(t.Context(), nil); !errors.Is(err, ErrDiscussionDirectInputHeld) || !errors.As(err, &paused) || paused.AdmissionID != "first" || len(native.sent) != 0 {
		t.Fatalf("queued owner input silently held by manual scheduling pause: %v", err)
	}
	if err := c.ResumeManual(t.Context(), native.status.Turn.ExternalInput); err != nil {
		t.Fatal(err)
	}
	if err := d.Sync(t.Context(), nil); err != nil {
		t.Fatal(err)
	}
	if len(native.sent) != 1 {
		t.Fatal("first send missing")
	}
	delete(native.receipts, native.operations[0])
	for i := 0; i < 2; i++ {
		err := d.Sync(t.Context(), nil)
		var hold *DiscussionInputHold
		if !errors.As(err, &hold) || hold.AdmissionID != "first" || !errors.Is(err, ErrDiscussionEffectUncertain) {
			t.Fatalf("missing exact unconfirmed-effect hold: %v %+v", err, hold)
		}
	}
	st, err := store.Snapshot()
	if err != nil || len(native.sent) != 1 || st.Admissions[admissionKey(b.ConversationID, 1)].Phase != "uncertain" || st.Admissions[admissionKey(b.ConversationID, 2)].Phase != "" {
		t.Fatalf("unconfirmed effect replayed or advanced FIFO: sends=%d err=%v", len(native.sent), err)
	}
}

func TestDraftAfterNativeSubmissionReportsPendingDiscordReply(t *testing.T) {
	c, store, native, binding := newControllerFixture(t)
	c.discussion = &DiscussionScope{InstallationID: "install", GuildID: "guild", ThreadID: "thread"}
	if _, err := c.submitDiscussion(discussionFixtureOrigin("first"), "what is the result?", "", ""); err != nil {
		t.Fatal(err)
	}
	runtime := &discussionTestRuntime{controllerRuntimeFixture: native}
	c.runtime = runtime
	d := &DiscussionController{controller: c, runtime: runtime, store: store, cfg: Config{ConversationID: binding.ConversationID}, now: time.Now}
	if err := d.Sync(t.Context(), nil); err != nil {
		t.Fatal(err)
	}
	native.status.Turn = protocol.ManagerNativeTurn{State: "running", NativeInputID: native.sent[0].ManagerAdmission.Nonce, NativeTurnID: "current-turn"}
	if err := d.Sync(t.Context(), nil); err != nil {
		t.Fatal(err)
	}
	runtime.managerErr = managerproof.ErrDirectInputUnresolved
	for range 2 {
		var hold *DiscussionInputHold
		err := d.Sync(t.Context(), nil)
		if !errors.As(err, &hold) || hold.AdmissionID != "first" || !errors.Is(err, ErrDiscussionDirectInputHeld) || !errors.Is(err, ErrDiscussionEffectUncertain) {
			t.Fatalf("in-flight Discord input lost its draft diagnosis: %v", err)
		}
	}
	if len(native.sent) != 1 || len(runtime.retirements) != 0 {
		t.Fatal("a terminal hold replayed the Discord input or retired the manager")
	}
}
