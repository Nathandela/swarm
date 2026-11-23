package idempotency

import (
	"os"
	"path/filepath"
	"testing"
)

func TestInspectUnresolvedKeepsNativeOutcomeAndDoesNotRepairLog(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.f.Close() })
	if _, _, err := s.PrepareBound("admitted", "semantic_send", "worker", "instance", "hash"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetRequest("admitted", []byte(`{"private":"fixture"}`)); err != nil {
		t.Fatal(err)
	}
	if err := s.Complete("admitted", []byte(`{"state":"admitted"}`)); err != nil {
		t.Fatal(err)
	}
	records, err := InspectUnresolved(t.Context(), dir)
	if err != nil || len(records) != 1 || records[0].Phase != "native_outcome_unresolved" || records[0].SessionInstance != "instance" {
		t.Fatalf("native outcome disappeared behind transport completion: %+v %v", records, err)
	}
	path := filepath.Join(dir, logFile)
	if _, err := s.f.WriteString(`{"operation_id":`); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := InspectUnresolved(t.Context(), dir); err == nil {
		t.Fatal("torn receipt log was treated as complete")
	}
	after, err := os.ReadFile(path)
	if err != nil || string(before) != string(after) {
		t.Fatalf("read-only inventory repaired the file: %v", err)
	}
}
