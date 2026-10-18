package idempotency

import (
	"bytes"
	"testing"
)

func TestRequestCustodySurvivesExecutionAndReopenWithoutAliasing(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.PrepareBound("op", "semantic_send", "worker", "incarnation", "hash"); err != nil {
		t.Fatal(err)
	}
	request := []byte(`{"native_id":"msg_owned","text_hash":"hash"}`)
	if err := s.SetRequest("op", request); err != nil {
		t.Fatal(err)
	}
	if err := s.Begin("op"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetRequest("op", []byte(`{"native_id":"other"}`)); err == nil {
		t.Fatal("executing request was rebound")
	}
	request[2] = 'X'
	reopened, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	rec, ok := reopened.Get("op")
	if !ok || rec.Phase != PhaseExecuting || !bytes.Equal(rec.Request, []byte(`{"native_id":"msg_owned","text_hash":"hash"}`)) {
		t.Fatalf("lost durable native request: %+v", rec)
	}
	rec.Request[2] = 'X'
	list := reopened.List()
	if len(list) != 1 || bytes.Equal(list[0].Request, rec.Request) {
		t.Fatal("Get exposed mutable custody")
	}
	list[0].Request[2] = 'Y'
	if got, _ := reopened.Get("op"); got.Request[2] != 'n' {
		t.Fatal("List exposed mutable custody")
	}
}

func TestRequestCustodyRefusesMissingInvalidAndLateDescriptors(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetRequest("missing", []byte(`{}`)); err == nil {
		t.Fatal("missing operation accepted a descriptor")
	}
	if _, _, err := s.Prepare("op", "semantic_send", "worker"); err != nil {
		t.Fatal(err)
	}
	for _, raw := range [][]byte{nil, []byte(`{`), bytes.Repeat([]byte("x"), (64<<10)+1)} {
		if err := s.SetRequest("op", raw); err == nil {
			t.Fatal("invalid descriptor accepted")
		}
	}
	if err := s.Begin("op"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetRequest("op", []byte(`{}`)); err == nil {
		t.Fatal("post-effect descriptor invented request authority")
	}
}

func TestObservedEvidenceRequiresExecutionAndSurvivesTerminalCommit(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil { t.Fatal(err) }
	if _, _, err := s.Prepare("op", "semantic_send", "worker"); err != nil { t.Fatal(err) }
	evidence := []byte(`{"status":"admitted","message_id":"msg_exact"}`)
	if err := s.SetEvidence("op", evidence); err == nil { t.Fatal("evidence preceded native execution") }
	if err := s.Begin("op"); err != nil { t.Fatal(err) }
	if err := s.SetEvidence("op", evidence); err != nil { t.Fatal(err) }
	if err := s.SetEvidence("op", []byte(`{"message_id":"msg_other"}`)); err == nil { t.Fatal("native proof was rebound") }
	if err := s.Complete("op", []byte(`{"ok":true}`)); err != nil { t.Fatal(err) }
	rec, _ := s.Get("op")
	if !bytes.Equal(rec.Evidence, evidence) || string(rec.Outcome) != `{"ok":true}` { t.Fatalf("terminal commit lost native proof: %+v", rec) }
	rec.Evidence[2] = 'X'
	if next, _ := s.Get("op"); bytes.Equal(next.Evidence, rec.Evidence) { t.Fatal("native evidence aliases durable state") }
}
