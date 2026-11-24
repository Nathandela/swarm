package voice

import (
	"testing"
	"time"
)

func TestColdParkedDiscussionRemainsActiveForItsWorker(t *testing.T) {
	_, store, _, b := newControllerFixture(t)
	d := DiscussionController{store: store, cfg: Config{ConversationID: b.ConversationID}, now: time.Now}
	worker := WorkerAssociation{ConversationID: b.ConversationID, Session: "child", SessionInstance: "incarnation", VMChild: true, Mailbox: true}
	workerKey := key(b.ConversationID, key(worker.Session, worker.SessionInstance))
	for _, ended := range []bool{false, true} {
		worker.Ended = ended
		if err := store.update(func(st *State) error {
			conversation := st.Conversations[b.ConversationID]
			conversation.Parked = true
			putRecord(st, "conversations", &st.Conversations, b.ConversationID, conversation)
			putRecord(st, "workers", &st.Workers, workerKey, worker)
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		if dormant, err := d.Dormant(); err != nil || dormant != ended {
			t.Fatalf("ended=%v dormant=%v err=%v", ended, dormant, err)
		}
	}
}
