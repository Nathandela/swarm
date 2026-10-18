package idempotency

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestNativeEvidenceDoesNotSettleUncommittedExecution(t *testing.T) {
	s,err:=Open(t.TempDir())
	if err!=nil { t.Fatal(err) }
	if _,_,err:=s.Prepare("op","semantic_send","worker"); err!=nil { t.Fatal(err) }
	if err:=s.Begin("op"); err!=nil { t.Fatal(err) }
	if err:=s.SetEvidence("op",[]byte(`{"status":"admitted"}`)); err!=nil { t.Fatal(err) }
	if !s.UnobservedExecution("semantic_send","worker","later") { t.Fatal("unverified or not-yet-committed native evidence let a later input overtake delivery reconciliation") }
	if err:=s.Complete("op",[]byte(`{"ok":true}`)); err!=nil { t.Fatal(err) }
	if s.UnobservedExecution("semantic_send","worker","later") { t.Fatal("completed delivery kept blocking later input") }
}

func TestUnknownStoredPhaseCannotAuthorizeLaterSemanticWrites(t *testing.T) {
	dir:=t.TempDir()
	rec:=Record{OperationID:"op",Action:"semantic_send",SessionID:"worker",Phase:Phase("unqualified")}
	raw,_:=json.Marshal(rec)
	if err:=os.WriteFile(filepath.Join(dir,logFile),append(raw,'\n'),0o600); err!=nil { t.Fatal(err) }
	s,err:=Open(dir)
	if err!=nil { t.Fatal(err) }
	if !s.UnobservedExecution("semantic_send","worker","later") { t.Fatal("unqualified durable phase was treated as settled") }
}
