package opencode

import (
 "encoding/json"
 "testing"
 "github.com/Nathandela/swarm/internal/adapter"
)

func nativeRow(info,parts string) []byte {return []byte(`{"info":`+info+`,"parts":`+parts+`}`)}
const nativeUserInfo=`{"id":"msg_user","sessionID":"ses_root","role":"user","time":{"created":1000}}`
const nativeAssistantInfo=`{"id":"msg_reply","sessionID":"ses_root","role":"assistant","parentID":"msg_user","time":{"created":1100,"completed":1200},"finish":"tool-calls"}`

func TestNativeInteractionSourceMessagesAreSnapshotsWithExactInputRefs(t *testing.T) {
 source:=NativeInteractions()
 rows:=[][]byte{
  nativeRow(nativeUserInfo,`[{"id":"prt_user","messageID":"msg_user","sessionID":"ses_root","type":"text","text":"request"},{"id":"prt_context","messageID":"msg_user","sessionID":"ses_root","type":"text","text":"private injected context","synthetic":true}]`),
  nativeRow(nativeAssistantInfo,`[{"id":"prt_reply","messageID":"msg_reply","sessionID":"ses_root","type":"text","text":"working"}]`),
 }
 for i,row:=range rows {
  items:=source.Interactions(adapter.HookPayload{Event:NativeMessageRowEvent,Raw:row})
  if len(items)!=1||items[0].Kind!=adapter.KindMessageSnapshot||items[0].TurnRef!="msg_user" {t.Fatalf("snapshot/input grouping: %+v",items)}
  if i==0&&(items[0].Text!="request"||items[0].MessageRole!="user"||items[0].Source!=adapter.SourceOwner){t.Fatalf("owner text: %+v",items)}
  if i==1&&(items[0].Text!="working"||items[0].MessageRole!="assistant"||items[0].StopReason!=""){t.Fatalf("tool step invented input completion: %+v",items)}
  if err:=items[0].Validate();err!=nil{t.Fatal(err)}
 }
 if _,ok:=source.Decision("per_native","always");ok{t.Fatal("read source invented native decision authority")}
}

func TestNativeInteractionSourcePreservesToolResultAndAppliedWrite(t *testing.T) {
 row:=nativeRow(nativeAssistantInfo,`[{"id":"prt_write","messageID":"msg_reply","sessionID":"ses_root","type":"tool","tool":"write","callID":"call_write","state":{"status":"completed","input":{"filePath":"/work/new.go","content":"package main"},"output":"written","metadata":{"filepath":"/work/new.go","exists":false}}}]`)
 items:=NativeInteractions().Interactions(adapter.HookPayload{Event:NativeMessageRowEvent,Raw:row})
 if len(items)!=2||items[0].Kind!=adapter.KindToolRun||items[0].OutputExcerpt!="written"||items[1].Kind!=adapter.KindFileChange||items[1].Change!="create"||items[1].Path!="/work/new.go"{t.Fatalf("native applied write: %+v",items)}
 var page map[string]json.RawMessage;if err:=json.Unmarshal(row,&page);err!=nil{t.Fatal(err)}
 page["parts"]=json.RawMessage(`[{"id":"prt_write","messageID":"msg_reply","sessionID":"ses_root","type":"tool","tool":"write","state":{"status":"running","input":{"filePath":"/work/new.go"}}}]`)
 pending,_:=json.Marshal(page)
 items=NativeInteractions().Interactions(adapter.HookPayload{Event:NativeMessageRowEvent,Raw:pending})
 if len(items)!=1||items[0].Status!=adapter.StatusInProgress{t.Fatal("unexecuted write presented as applied file change")}
}

func TestNativeInteractionSourceUnknownOrMalformedIsNotInvented(t *testing.T) {
 for _,raw:=range [][]byte{nil,[]byte(`null`),[]byte(`{}`),nativeRow(nativeAssistantInfo,`[{"id":"prt_wrong","sessionID":"ses_other","messageID":"msg_reply","type":"text","text":"foreign"}]`)} {
  if got:=NativeInteractions().Interactions(adapter.HookPayload{Event:NativeMessageRowEvent,Raw:raw});len(got)!=0{t.Fatalf("unqualified row shaped: %+v",got)}
 }
 row:=nativeRow(nativeAssistantInfo,`[{"id":"prt_tool","messageID":"msg_reply","sessionID":"ses_root","type":"tool","tool":"custom","state":{"status":"future","input":{"command":"do not infer bash"},"output":"unknown"}}]`)
 if got:=NativeInteractions().Interactions(adapter.HookPayload{Event:NativeMessageRowEvent,Raw:row});len(got)!=0{t.Fatal("unknown native tool status invented completion")}
}
