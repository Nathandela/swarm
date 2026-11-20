"""Generate a Go build overlay for the isolated native quota rehearsal."""
import json
from pathlib import Path
import sys

root = Path(__file__).resolve().parents[3]
scratch = Path(sys.argv[1]).resolve()
scratch.mkdir(parents=True, exist_ok=True)
source = root / "internal/skeleton/backend.go"
code = source.read_text()
anchor = "\td.captureCodexManagerFrame(local, feed, method, frame)"
if code.count(anchor) != 1:
    raise SystemExit("Codex callback entry point changed")
code = code.replace('import (', 'import (\n "os"\n "path/filepath"', 1)
code = code.replace(anchor, r'''
	if method=="turn/completed" && d.api!=nil {
		path:=filepath.Join(d.api.stateDir,".hosted-quota-target")
		var target struct{Session,Instance,Thread,Turn string}
		var envelope struct{Params struct{ Thread string `json:"threadId"`; Turn struct{ID,Status string} }}
		raw,err:=os.ReadFile(path)
		if err==nil && json.Unmarshal(raw,&target)==nil && json.Unmarshal(frame,&envelope)==nil &&
			target.Session==local && target.Instance==expectedInstance && target.Thread==envelope.Params.Thread && target.Turn==envelope.Params.Turn.ID &&
			envelope.Params.Turn.Status=="completed" {
			var event map[string]any
			if json.Unmarshal(frame,&event)==nil {
				turn:=event["params"].(map[string]any)["turn"].(map[string]any)
				turn["status"]="failed"
				turn["error"]=map[string]any{"message":"Isolated rehearsal quota exhaustion","codexErrorInfo":"usageLimitExceeded"}
				replacement,err:=json.Marshal(event)
				if err==nil && os.Rename(path,path+".consumed")==nil {frame=replacement}
			}
		}
	}
''' + anchor)
patched = scratch / "backend.go"
patched.write_text(code)
(scratch / "overlay.json").write_text(json.dumps({"Replace": {str(source): str(patched)}}))
