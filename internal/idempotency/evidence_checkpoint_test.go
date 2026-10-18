package idempotency
import("bytes";"testing")
func TestEvidenceCheckpointCASRetainsDeliveryAndSurvivesReopen(t *testing.T){
 dir:=t.TempDir();s,err:=Open(dir);if err!=nil{t.Fatal(err)}
 _,_,err=s.PrepareBound("op","semantic_send","worker","instance","hash");if err!=nil{t.Fatal(err)}
 if err=s.SetRequest("op",[]byte(`{"message_id":"msg_owned"}`));err!=nil{t.Fatal(err)}
 if err=s.Begin("op");err!=nil{t.Fatal(err)}
 first:=[]byte(`{"version":1,"receipt":{"messageID":"msg_owned"}}`)
 if err=s.SetEvidence("op",first);err!=nil{t.Fatal(err)}
 if err=s.Complete("op",[]byte(`{"ok":true}`));err!=nil{t.Fatal(err)}
 final:=[]byte(`{"version":1,"receipt":{"messageID":"msg_owned"},"outcome":"completed"}`)
 if err=s.CompareEvidence("op",first,final);err!=nil{t.Fatal(err)}
 final[2]='X';reopened,err:=Open(dir);if err!=nil{t.Fatal(err)};got,_:=reopened.Get("op")
 if got.Phase!=PhaseCompleted||!bytes.Contains(got.Evidence,[]byte(`"outcome":"completed"`))||string(got.Outcome)!=`{"ok":true}`{t.Fatal("checkpoint lost native or delivery custody")}
 if s.CompareEvidence("op",first,[]byte(`{}`))==nil{t.Fatal("stale checkpoint replaced newer evidence")}
 if s.CompareEvidence("op",got.Evidence,[]byte(`{`))==nil{t.Fatal("malformed evidence accepted")}
 ids:=s.RequestOperations("semantic_send","worker");if len(ids)!=1||ids[0]!="op"||len(s.RequestOperations("semantic_send","other"))!=0{t.Fatal("request lookup crossed worker boundary")}
}
