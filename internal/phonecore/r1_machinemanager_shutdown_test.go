package phonecore

import (
	"testing"
	"time"
)

func newRegistryRelayForTest(t *testing.T, events chan Event) (*RegistryManager, *CoreMachineClient) {
	t.Helper()
	reg, err := NewMachineRegistry(t.TempDir())
	if err != nil {
		t.Fatalf("NewMachineRegistry: %v", err)
	}
	if _, err := reg.AddMachine(MachineDescriptor{ID: "m1"}); err != nil {
		t.Fatalf("AddMachine: %v", err)
	}
	mgr, err := NewRegistryManager(reg, ManagerOptions{Cap: 1})
	if err != nil {
		t.Fatalf("NewRegistryManager: %v", err)
	}
	client := NewCoreMachineClient("m1", newMachineTestCore(t, "m1"), events)
	if err := mgr.Add(MachineDescriptor{ID: "m1"}, client); err != nil {
		t.Fatalf("manager Add: %v", err)
	}
	t.Cleanup(func() { _ = mgr.Close() })
	return mgr, client
}

func TestRegistryManager_AggregateStreamRelaysCoreEventsUnchanged(t *testing.T) {
	events := make(chan Event, 4)
	mgr, _ := newRegistryRelayForTest(t, events)
	want := []Event{
		{Kind: "journal", Stream: "journal", Cursor: 3},
		{Kind: "presence", State: "online"},
		{Kind: "terminal", Stream: "terminal", SessionID: "m1/s1"},
		{Kind: "overflow", Dropped: 2, Message: "queue overflow"},
	}
	for _, event := range want {
		events <- event
	}
	for i, wantEvent := range want {
		select {
		case got := <-mgr.Events():
			if got.MachineID != "m1" || got.Event != wantEvent {
				t.Fatalf("event %d = %+v; want MachineID m1 and Event %+v", i, got, wantEvent)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("event %d did not reach the aggregate stream", i)
		}
	}
}

func TestRegistryManager_CloseClosesAggregateStream(t *testing.T) {
	mgr, _ := newRegistryRelayForTest(t, make(chan Event))
	if err := mgr.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if err := mgr.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}

	select {
	case _, ok := <-mgr.Events():
		if ok {
			t.Fatal("Events remained open after Close")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Events did not close after Close")
	}
}

func TestCoreMachineClient_StopAbandonsEventParkedInRegistryRelay(t *testing.T) {
	events := make(chan Event, 1)
	mgr, client := newRegistryRelayForTest(t, events)
	events <- Event{Kind: "in-flight"}
	time.Sleep(200 * time.Millisecond)

	if err := client.Stop(); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	select {
	case got := <-mgr.Events():
		t.Fatalf("event parked before Stop was forwarded: %+v", got)
	case <-time.After(300 * time.Millisecond):
	}
}

func TestCoreMachineClient_RedundantStartDoesNotOrphanRegistryStopSignal(t *testing.T) {
	events := make(chan Event, 1)
	mgr, client := newRegistryRelayForTest(t, events)
	events <- Event{Kind: "in-flight"}
	time.Sleep(200 * time.Millisecond)

	if err := client.Start(); err != nil {
		t.Fatalf("redundant Start: %v", err)
	}
	if err := client.Stop(); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	select {
	case got := <-mgr.Events():
		t.Fatalf("event crossed after Stop: %+v", got)
	case <-time.After(300 * time.Millisecond):
	}
}

func TestCoreMachineClient_StopHaltsRegistryRelayAndStartResumes(t *testing.T) {
	events := make(chan Event, 4)
	mgr, client := newRegistryRelayForTest(t, events)
	events <- Event{Kind: "before-stop"}
	select {
	case got := <-mgr.Events():
		if got.Kind != "before-stop" {
			t.Fatalf("Kind = %q; want before-stop", got.Kind)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("event before Stop was not forwarded")
	}

	if err := client.Stop(); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	events <- Event{Kind: "after-stop"}
	select {
	case got := <-mgr.Events():
		t.Fatalf("event after Stop was forwarded: %+v", got)
	case <-time.After(200 * time.Millisecond):
	}

	if err := client.Start(); err != nil {
		t.Fatalf("Start after Stop: %v", err)
	}
	events <- Event{Kind: "after-restart"}
	select {
	case got := <-mgr.Events():
		if got.Kind != "after-restart" {
			t.Fatalf("Kind = %q; want after-restart", got.Kind)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("event after restart was not forwarded")
	}
}
