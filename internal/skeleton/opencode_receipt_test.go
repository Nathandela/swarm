package skeleton

import(
 "crypto/sha256"
 "encoding/hex"
 "encoding/json"
 "strings"
 "testing"
 "github.com/Nathandela/swarm/internal/opencodebridge"
)
func nativeReceiptFixture()(openCodeSendIntent,json.RawMessage){
 hash:=sha256.Sum256([]byte("exact\ntext"));intent:=openCodeSendIntent{Identity:opencodebridge.Identity{Version:1,WorkerID:"worker",RootID:"ses_"+strings.Repeat("a",32),Directory:"/project",SessionInstance:strings.Repeat("b",32),BackendGeneration:strings.Repeat("b",32),NativeCreatedAtMS:123},MessageID:"msg_"+strings.Repeat("c",32),TextHash:hex.EncodeToString(hash[:]),NotBeforeMS:500}
 raw,_:=json.Marshal(map[string]any{"info":map[string]any{"id":intent.MessageID,"sessionID":intent.Identity.RootID,"role":"user","agent":"plan","model":map[string]string{"providerID":"opencode","modelID":"deepseek-v4.1-flash-free","variant":"max"},"time":map[string]int64{"created":501}},"parts":[]any{map[string]any{"id":"prt_owned","sessionID":intent.Identity.RootID,"messageID":intent.MessageID,"type":"text","text":"exact\ntext"}}})
 return intent,raw
}
func TestOpenCodeReceiptRequiresExactNativeUserAndActualTuple(t *testing.T){
 intent,raw:=nativeReceiptFixture();receipt,err:=qualifyOpenCodeAdmission("semantic:owned",intent,raw)
 if err!=nil{t.Fatal(err)}
 if receipt.MessageID!=intent.MessageID||receipt.Identity!=intent.Identity||receipt.Agent!="plan"||receipt.Model.ModelID!="deepseek-v4.1-flash-free"||receipt.Variant!="max"||receipt.SelectionBasis!="native_message_receipt"{t.Fatal("actual native receipt was reconstructed from defaults")}
 for _,change:=range []struct{old,new string}{{`"role":"user"`,`"role":"assistant"`},{`"created":501`,`"created":499`},{`"text":"exact\ntext"`,`"text":"other"`},{`"type":"text"`,`"type":"file"`},{`"agent":"plan"`,`"agent":""`},{`"text":"exact\ntext"`,`"synthetic":true,"text":"exact\ntext"`},{`"modelID":"deepseek-v4.1-flash-free"`,`"modelID":""`},{`"sessionID":"ses_`,`"sessionID":"ses_x`},{`"messageID":"msg_`,`"messageID":"msg_x`}}{
  if _,err:=qualifyOpenCodeAdmission("semantic:owned",intent,json.RawMessage(strings.Replace(string(raw),change.old,change.new,1)));err==nil{t.Fatalf("conflicting row admitted: %s",change.old)}
 }
}
