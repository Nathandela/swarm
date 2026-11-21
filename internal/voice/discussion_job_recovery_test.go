package voice

import (
	"encoding/json"
	"testing"
)

func TestJobStatusReportsOnlyExactRecoveredNativeTurn(t *testing.T) {
	_, store, _, binding := newControllerFixture(t)
	d := &DiscussionController{cfg: Config{ConversationID: binding.ConversationID}, store: store}
	w := WorkerAssociation{ConversationID: binding.ConversationID, Session: "machine/worker", SessionInstance: "first", Project: "project", ActionID: "create", JobID: "job", AttemptID: "attempt", RecoveryRequired: true}
	workerKey := key(w.ConversationID, key(w.Session, w.SessionInstance))
	id := discussionEvidenceID(w)
	err := store.update(func(st *State) error {
		putRecord(st, "workers", &st.Workers, workerKey, w)
		event := Event{ID: id, ConversationID: w.ConversationID, JobID: w.JobID, AttemptID: w.AttemptID, WorkerProof: true, NativeTurnID: "original-turn", WakeID: "wake", RecoveryOnly: true, Metadata: json.RawMessage(`{"session":"machine/worker","instance":"first","data":{"turn_id":"original-turn","state":"completed"}}`)}
		putRecord(st, "events", &st.Events, key(w.ConversationID, id), event)
		publication := Publication{ID: "final", ConversationID: w.ConversationID, Final: true, OutputSequence: 1, DiscussionSource: DiscussionOutputSource{AdmissionID: event.WakeID, JobID: w.JobID, AttemptID: w.AttemptID}}
		putRecord(st, "publications", &st.Publications, key(w.ConversationID, publication.ID), publication)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	check := func(want string) {
		t.Helper()
		status, err := d.Status()
		if err != nil || len(status.Jobs) != 1 || status.Jobs[0].State != want || !status.Jobs[0].RecoveryRequired {
			t.Fatalf("want %s, got %+v, %v", want, status.Jobs, err)
		}
		job := status.Jobs[0]
		if want == "turn_completed" {
			p := job.NativeTurnPublication
			if job.NativeTurnID != "original-turn" || p == nil || p.ID != "final" || p.State != "awaiting_delivery_receipt" || len(p.MessageIDs) != 0 {
				t.Fatalf("invalid native outcome or delivery status: %+v", job)
			}
		} else if job.NativeTurnID != "" || job.NativeTurnPublication != nil {
			t.Fatalf("new action inherited the old result: %+v", job)
		}
	}
	check("turn_completed")
	if err := store.update(func(st *State) error {
		w.ActionID = "new-steering"
		putRecord(st, "workers", &st.Workers, workerKey, w)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	check("uncertain")
}
