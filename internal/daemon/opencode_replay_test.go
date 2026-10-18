package daemon

import (
 "errors"
 "testing"
 "github.com/Nathandela/swarm/internal/persist"
 "github.com/Nathandela/swarm/internal/status"
)
func TestOpenCodeLaunchReplayRetainsUncertainNativeIdentity(t *testing.T){
 for _,process:=range []status.Process{status.ProcessRunning,status.ProcessLost}{t.Run(string(process),func(t *testing.T){
  d:=openDaemon(t,daemonConfig(t));m:=openCodeMeta();m.Status.Process=process;m.ShimPID=123
  if err:=d.saveMeta(m);err!=nil{t.Fatal(err)}
  if _,_,err:=d.idem.Prepare("native-op","launch",m.ID);err!=nil{t.Fatal(err)}
  d.resolveStaleLaunches()
  redrive,_,_,_,err:=d.resolveReplay("native-op","replacement")
  if redrive||!errors.Is(err,ErrLaunchOutcomeUnknown){t.Fatalf("uncertain native launch replayed or claimed success: %v %v",redrive,err)}
  out,ok:=d.OperationOutcome("native-op");if !ok||out.State!=OpStateOutcomeUnknown{t.Fatal("shim liveness substituted for native binding")}
  got,_:=d.Get(m.ID);if got.ConversationID!=m.ConversationID||got.OpenCodeBinding.Phase!=persist.OpenCodePhasePlanned{t.Fatal("retained native identity changed")}
 })}
}
