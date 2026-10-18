package skeleton

import (
 "encoding/json"
 "testing"
)

func TestOpenCodeTerminalEventNeedsFullNativeParts(t *testing.T) {
 s,e:=newOCState(t,"ses_root")
 s.Apply(ocUserMessage("ses_root","msg_owned",1000),e)
 s.Apply(ocUserTextPart("ses_root","msg_owned","prt_u","request"),e)
 got:=s.Apply(ocAssistantMessage("ses_root","msg_tool","msg_owned",1100,1200,"stop"),e)
 if got.Disposition!=OCNeedsSnapshot||ocInput(s.View(),"msg_owned").Outcome!=""{t.Fatal("bare stop metadata completed an input before native tool parts were read")}
}

func TestOpenCodeTerminalFullRowChecksNativeContinuationGuard(t *testing.T) {
 for _,tc:=range []struct{name,part string;completed bool}{
  {"ordinary stop",``,true},
  {"stop with tool",`{"id":"prt_t","sessionID":"ses_root","messageID":"msg_tool","type":"tool","callID":"call_1","tool":"bash","state":{"status":"completed","output":"ok"}}`,false},
  {"provider executed tool",`{"id":"prt_t","sessionID":"ses_root","messageID":"msg_tool","type":"tool","callID":"call_1","tool":"bash","metadata":{"providerExecuted":true},"state":{"status":"completed","output":"ok"}}`,true},
  {"interrupted orphan",`{"id":"prt_t","sessionID":"ses_root","messageID":"msg_tool","type":"tool","callID":"call_1","tool":"bash","state":{"status":"error","error":"abandoned","metadata":{"interrupted":true}}}`,true},
 }{t.Run(tc.name,func(t *testing.T){
  s,e:=newOCState(t,"ses_root")
  s.Apply(ocUserMessage("ses_root","msg_owned",1000),e)
  s.Apply(ocUserTextPart("ses_root","msg_owned","prt_u","request"),e)
  row:=json.RawMessage(`{"info":{"id":"msg_tool","sessionID":"ses_root","role":"assistant","parentID":"msg_owned","time":{"created":1100,"completed":1200},"finish":"stop"},"parts":[`+tc.part+`]}`)
  if err:=s.ImportHistory([]json.RawMessage{row},e);err!=nil{t.Fatal(err)}
  if got:=ocInput(s.View(),"msg_owned").Outcome; (got==OpenCodeOutcomeCompleted)!=tc.completed{t.Fatalf("stop+tool outcome=%q want complete=%v",got,tc.completed)}
 })}
}

func TestOpenCodeCompactionCannotTransferOutcomeAcrossFreshNativeUserIDs(t *testing.T) {
 for _,synthetic:=range []bool{false,true}{
  s,e:=newOCState(t,"ses_root")
  for _,id:=range []string{"msg_owned","msg_manual"}{s.Apply(ocUserMessage("ses_root",id,1000),e);s.Apply(ocUserTextPart("ses_root",id,"prt_"+id,"same request"),e)}
  s.Apply(ocUserMessage("ses_root","msg_compaction",1100),e)
  s.Apply([]byte(`{"type":"message.part.updated","properties":{"part":{"id":"prt_c","messageID":"msg_compaction","sessionID":"ses_root","type":"compaction","auto":true}}}`),e)
  s.Apply(ocUserMessage("ses_root","msg_continue",1200),e)
  text:=ocUserTextPart("ses_root","msg_continue","prt_continue","same request")
  if synthetic{text=ocSyntheticTextPart("ses_root","msg_continue","prt_continue","continue")}
  s.Apply(text,e)
  row:=json.RawMessage(`{"info":{"id":"msg_final","sessionID":"ses_root","role":"assistant","parentID":"msg_continue","time":{"created":1300,"completed":1400},"finish":"stop"},"parts":[]}`)
  if err:=s.ImportHistory([]json.RawMessage{row},e);err!=nil{t.Fatal(err)}
  for _,id:=range []string{"msg_owned","msg_manual"}{if ocInput(s.View(),id).Outcome!=""{t.Fatal("compaction outcome assigned without a native original-input link")}}
  if !s.View().Degraded{t.Fatal("unqualified continuation presented as fully reconciled")}
 }
}
