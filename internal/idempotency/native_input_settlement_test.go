package idempotency

import "testing"

func TestNativeInputSettlementRequiresExactRetainedDisposition(t *testing.T) {
	for _, state := range []string{"submitted", "started", "rejected"} {
		t.Run(state, func(t *testing.T) {
			store, err := Open(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = store.f.Close() })
			if _, _, err := store.PrepareBound("op", "semantic_send", "worker", "instance", "hash"); err != nil {
				t.Fatal(err)
			}
			if err := store.Begin("op"); err != nil {
				t.Fatal(err)
			}
			evidence := []byte(`{"operation_id":"op","state":"` + state + `"}`)
			if err := store.SetEvidence("op", evidence); err != nil {
				t.Fatal(err)
			}
			if err := store.SettleNativeInput("op", []byte(`{}`), []byte(`{"ok":false}`), true); err == nil {
				t.Fatal("stale receipt settled native operation")
			}
			failed := state == "rejected"
			if err := store.SettleNativeInput("op", evidence, []byte(`{"ok":false}`), !failed); err == nil {
				t.Fatal("opposite receipt disposition was accepted")
			}
			outcome := []byte(`{"ok":true}`)
			if failed {
				outcome = []byte(`{"ok":false}`)
			}
			if err := store.SettleNativeInput("op", evidence, outcome, failed); err != nil {
				t.Fatal(err)
			}
			got, _ := store.Get("op")
			want := PhaseCompleted
			if failed {
				want = PhaseFailed
			}
			if got.Phase != want {
				t.Fatalf("phase=%s want %s", got.Phase, want)
			}
		})
	}
}

func TestNativeInputSettlementCannotDemoteAdmissionEvidence(t *testing.T) {
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.f.Close() })
	_, _, _ = store.PrepareBound("op", "semantic_send", "worker", "instance", "hash")
	_ = store.Begin("op")
	evidence := []byte(`{"status":"admitted"}`)
	_ = store.SetEvidence("op", evidence)
	if store.SettleNativeInput("op", evidence, []byte(`{"ok":false}`), true) == nil {
		t.Fatal("admission evidence became rejected delivery")
	}
}

func TestNativeInputSettlementRejectsStaleSuccessAfterRetainedRejection(t *testing.T) {
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.f.Close() })
	_, _, _ = store.PrepareBound("op", "semantic_send", "worker", "instance", "hash")
	_ = store.Begin("op")
	submitted := []byte(`{"operation_id":"op","state":"submitted"}`)
	rejected := []byte(`{"operation_id":"op","state":"rejected"}`)
	_ = store.SetEvidence("op", submitted)
	if err := store.CompareEvidence("op", submitted, rejected); err != nil {
		t.Fatal(err)
	}
	if store.SettleNativeInput("op", submitted, []byte(`{"ok":true}`), false) == nil {
		t.Fatal("stale success overwrote native rejection")
	}
	outcome := []byte(`{"ok":false,"code":"input_busy","message":"native queue dropped request"}`)
	if err := store.SettleNativeInput("op", rejected, outcome, true); err != nil {
		t.Fatal(err)
	}
	if err := store.Fail("op", outcome); err != nil {
		t.Fatalf("original sender could not repeat the identical rejected outcome: %v", err)
	}
}
