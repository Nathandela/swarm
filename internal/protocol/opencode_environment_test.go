package protocol
import("reflect";"testing")
func TestOpenCodeLaunchKeepsNativeOriginsBeforeChildAllowlist(t *testing.T){
 env:=[]string{"HOME=/owner","PATH=/bin","XDG_DATA_HOME=/native","OPENCODE_CONFIG_CONTENT=unsupported-origin","OPENAI_API_KEY=not-captured"}
 got:=daemonLaunchSpec(&LaunchReq{Agent:"opencode",Env:env},false,"")
 want:=[]string{"HOME=/owner","XDG_DATA_HOME=/native","OPENCODE_CONFIG_CONTENT=unsupported-origin"}
 if !reflect.DeepEqual(got.OpenCodeEnv,want){t.Fatal("native selectors lost before admission")}
 for _,remote:=range []bool{false,true}{for _,source:=range [][]string{nil,{},env}{x:=daemonLaunchSpec(&LaunchReq{Agent:"opencode",Env:source},remote,"");if remote||source==nil {if x.OpenCodeEnv!=nil{t.Fatal("origin injected into daemon policy")}}else if x.OpenCodeEnv==nil {t.Fatal("explicit empty origin became daemon policy")}}}
 if daemonLaunchSpec(&LaunchReq{Agent:"claude",Env:env},false,"").OpenCodeEnv!=nil{t.Fatal("ordinary provider policy changed")}
}
