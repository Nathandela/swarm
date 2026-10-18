package phonecore

import (
 "encoding/json"
 "strings"
 "testing"
 "time"
 "github.com/Nathandela/swarm/internal/protocol/schema"
)

func nativeTestRecord(t *testing.T,cursor uint64,item map[string]any) schema.JournalRecord {t.Helper();raw,err:=json.Marshal(item);if err!=nil{t.Fatal(err)};return schema.JournalRecord{Cursor:cursor,SessionID:"ep/worker",Type:RecordTypeInteraction,Item:raw}}
func nativeTestMessage(text string) map[string]any{return map[string]any{"v":2,"item_id":"native-message","kind":"message_snapshot","role":"assistant","text":text,"ts":"2026-10-18T12:00:00Z","provenance":"live"}}
func nativeTestRequest() map[string]any {expiry:=time.Now().Add(time.Hour).UTC();return map[string]any{"v":2,"item_id":"native-request","kind":"permission_request","status":"in_progress","ts":"2026-10-18T12:00:00Z","provenance":"reconciled","authority_valid":true,"request":schema.NativeRequestView{Session:"ep/worker",SessionInstance:"instance",RootID:"ses_root",Generation:"generation",NativeSessionID:"ses_child",RequestID:"per_child",RequestHash:strings.Repeat("a",64),ExpiresAt:&expiry,Kind:"permission",Permission:"bash",Patterns:[]string{"echo *"},Always:[]string{"echo *"}}}}

func TestNativeSnapshotsReplaceWithoutChangingLegacyDeltas(t *testing.T) {
 s:=NewItemStore()
 s.Apply(schema.JournalRecord{Cursor:1,SessionID:"ep/worker",Type:RecordTypeInteraction,Item:json.RawMessage(agentMessage("legacy","A","in_progress"))})
 if !s.Apply(nativeTestRecord(t,2,nativeTestMessage("Hello"))) {t.Fatal("native snapshot skipped")}
 s.Apply(nativeTestRecord(t,3,nativeTestMessage("Hello world")))
 s.Apply(schema.JournalRecord{Cursor:4,SessionID:"ep/worker",Type:RecordTypeInteraction,Item:json.RawMessage(agentMessage("legacy","B","in_progress"))})
 items:=s.Session("ep/worker")
 if len(items)!=2||items[0].Text!="AB"||items[1].Text!="Hello world"||items[1].Kind!="message_snapshot"||items[1].Degraded{t.Fatalf("native/legacy folds mixed: %+v",items)}
 if knownKind("message_snapshot") {t.Fatal("v1 fold would concatenate a native replacement snapshot")}
 s.Apply(nativeTestRecord(t,2,nativeTestMessage("stale")))
 if s.Session("ep/worker")[1].Text!="Hello world"{t.Fatal("old cursor regressed a native snapshot")}
 detail:=nativeTestRecord(t,0,nativeTestMessage("expanded complete text"));if !s.ApplyDetail(detail)||s.Session("ep/worker")[1].Text!="expanded complete text"{t.Fatal("native detail did not replace whole text")}
}

func TestNativeRequestBindingRequiresCurrentUntruncatedAuthority(t *testing.T) {
 s:=NewItemStore();card:=nativeTestRequest();s.Apply(nativeTestRecord(t,1,card))
 binding,ok:=s.PendingNativeRequest("ep/worker","native-request")
 if !ok||binding.Request.Kind!="permission_reply"||binding.Request.NativeSessionID!="ses_child"||binding.Request.RequestHash!=strings.Repeat("a",64){t.Fatalf("exact native request binding missing: %+v %v",binding,ok)}
 for _,change:=range []string{"gap","truncated","expired","resolved","foreign","historical"}{t.Run(change,func(t *testing.T){
  store:=NewItemStore();item:=nativeTestRequest();request:=item["request"].(schema.NativeRequestView)
  switch change{case "gap":item["authority_valid"]=false;case "truncated":item["truncated"]=true;case "expired":past:=time.Now().Add(-time.Second);request.ExpiresAt=&past;case "resolved":item["status"]="completed";case "foreign":request.Session="ep/other";case "historical":item["provenance"]="imported"}
  item["request"]=request;store.Apply(nativeTestRecord(t,1,item));if _,ok:=store.PendingNativeRequest("ep/worker","native-request");ok{t.Fatal("stale/unshown native authority enabled a decision")}
 })}
}

func TestNativeRequestsCoexistAndOnlyExactResolutionReleasesRetention(t *testing.T) {
 store:=NewItemStore()
 for i,id:=range []string{"request-one","request-two"}{card:=nativeTestRequest();card["item_id"]=id;store.Apply(nativeTestRecord(t,uint64(i+1),card))}
 for i:=0;i<MaxItemsPerSession+5;i++{msg:=nativeTestMessage("prose");msg["item_id"]=strings.Repeat("x",i+1);store.Apply(nativeTestRecord(t,uint64(i+3),msg))}
 if _,ok:=store.PendingNativeRequest("ep/worker","request-one");!ok{t.Fatal("pending request evicted by prose")}
 if _,ok:=store.PendingNativeRequest("ep/worker","request-two");!ok{t.Fatal("second native request superseded")}
 done:=nativeTestRequest();done["item_id"]="request-one";done["status"]="completed";done["authority_valid"]=false;store.Apply(nativeTestRecord(t,999,done))
 if _,ok:=store.PendingNativeRequest("ep/worker","request-one");ok{t.Fatal("resolved request still actionable")}
 if _,ok:=store.PendingNativeRequest("ep/worker","request-two");!ok{t.Fatal("one resolution removed another request")}
}
