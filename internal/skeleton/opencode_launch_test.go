package skeleton

import (
 "os"
 "path/filepath"
 "testing"
 "github.com/Nathandela/swarm/internal/daemon"
 "github.com/Nathandela/swarm/internal/persist"
)

func TestOpenCodeLaunchPinsNativeVersionAndPreservesSelectors(t *testing.T) {
 dir:=t.TempDir()
 bin:=filepath.Join(dir,"opencode")
 write:=func(v string){t.Helper(); if err:=os.WriteFile(bin,[]byte("#!/bin/sh\nprintf '%s\\n' '"+v+"'\n"),0700);err!=nil{t.Fatal(err)}}
 write("1.19.3")
 a:=&coreAPI{}
 spec:=daemon.LaunchSpec{AgentType:"opencode",Cwd:dir,Argv:[]string{bin,"--model","opencode/deepseek-v4.1-flash-free","--agent","plan","--prompt","genuine"},ClientEnv:[]string{"PATH="+dir},OpenCodeEnv:[]string{"XDG_DATA_HOME="+dir}}
 if err:=a.qualifyOpenCodeLaunch(&spec);err!=nil{t.Fatal(err)}
 if spec.RequiredRuntime!=persist.RequiredRuntimeOpenCode||spec.CLIIdentity==nil||spec.CLIIdentity.Version!="1.19.3"||spec.ExpectedCLIIdentity==nil{t.Fatal("native launch was not pinned")}
 if len(spec.Argv)!=7||spec.Argv[2]!="opencode/deepseek-v4.1-flash-free"||spec.Argv[4]!="plan"{t.Fatal("native selections rewritten")}
 write("1.19.4")
 if a.qualifyOpenCodeLaunch(&spec)==nil {t.Fatal("unqualified native version admitted")}
}

func TestOpenCodeLaunchRejectsLegacyExternalResume(t *testing.T){
 a:=&coreAPI{}
 spec:=daemon.LaunchSpec{AgentType:"opencode",ConversationID:"ses_0123456789abcdef0123456789abcdef"}
 if a.qualifyOpenCodeLaunch(&spec)==nil{t.Fatal("resume without exact durable native identity admitted")}
}
