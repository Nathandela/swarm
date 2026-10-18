package skeleton

import (
 "context"
 "encoding/json"
 "strings"
 "testing"
 "github.com/Nathandela/swarm/internal/opencodebridge"
)

func ocHistoryPage(identity opencodebridge.Identity,mid,cursor string) opencodebridge.Snapshot {
 info,_:=json.Marshal(map[string]any{"id":identity.RootID,"directory":identity.Directory,"version":"1.19.3","time":map[string]int64{"created":identity.NativeCreatedAtMS}})
 page:=opencodebridge.Snapshot{Identity:identity,Session:info,NextCursor:cursor}
 if mid!="" {row,_:=json.Marshal(map[string]any{"info":map[string]any{"id":mid,"sessionID":identity.RootID,"role":"user","time":map[string]int64{"created":1}},"parts":[]any{map[string]any{"id":"prt_"+strings.Repeat("p",26),"sessionID":identity.RootID,"messageID":mid,"type":"text","text":"literal native content"}}});page.Messages=[]json.RawMessage{row}}
 return page
}
func TestOpenCodeHistoryExportUsesExactRootAndOpaquePagination(t *testing.T){
 id:=opencodebridge.Identity{Version:1,WorkerID:"worker",RootID:"ses_"+strings.Repeat("a",32),Directory:"/native ü",SessionInstance:strings.Repeat("b",32),BackendGeneration:strings.Repeat("b",32),NativeCreatedAtMS:123}
 pages:=[]opencodebridge.Snapshot{ocHistoryPage(id,"msg_"+strings.Repeat("b",26),"opaque-cursor"),ocHistoryPage(id,"msg_"+strings.Repeat("a",26),"")}
 n:=0
 result,err:=collectOpenCodeHistory(context.Background(),id,func(_ context.Context,limit int,before string)(opencodebridge.Snapshot,error){if limit!=100{t.Fatal("unbounded native page")};if n==0&&before!=""||n==1&&before!="opaque-cursor"{t.Fatal("cursor invented")};p:=pages[n];n++;return p,nil})
 if err!=nil{t.Fatal(err)}
 if n!=2||len(result.Messages)!=2{t.Fatal("history page missing")}
 raw,_:=json.Marshal(result);if !strings.Contains(string(raw),"literal native content")||strings.Contains(string(raw),"jsonl"){t.Fatal("native history rewritten or mislabeled")}
}
func TestOpenCodeHistoryRefusesForeignRowsLoopsAndBounds(t *testing.T){
 id:=opencodebridge.Identity{Version:1,WorkerID:"worker",RootID:"ses_"+strings.Repeat("a",32),Directory:"/native",SessionInstance:strings.Repeat("b",32),BackendGeneration:strings.Repeat("b",32),NativeCreatedAtMS:123}
 for _,kind:=range []string{"root","created","part","repeat_cursor","duplicate","oversize"}{t.Run(kind,func(t *testing.T){
 n:=0
 _,err:=collectOpenCodeHistory(context.Background(),id,func(_ context.Context,_ int,_ string)(opencodebridge.Snapshot,error){
 p:=ocHistoryPage(id,"msg_"+strings.Repeat("a",26),"");n++
 switch kind{case "root":p.RootID="ses_"+strings.Repeat("c",32);case "created":p.NativeCreatedAtMS++;case "part":p.Messages[0]=json.RawMessage(strings.Replace(string(p.Messages[0]),`"messageID":"msg_`,`"messageID":"msg_x`,1));case "repeat_cursor":p.Messages=nil;p.NextCursor="repeat";case "duplicate":if n==1{p.NextCursor="next"};case "oversize":p.Messages[0]=append(p.Messages[0],[]byte(strings.Repeat(" ",openCodeHistoryMaxBytes))...)}
 return p,nil})
 if err==nil{t.Fatal("unsafe native history accepted")}
 })}
}
