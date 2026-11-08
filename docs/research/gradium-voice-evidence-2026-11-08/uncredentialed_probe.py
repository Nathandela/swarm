"""Read-only authentication reachability; no credentials, audio, text, or retries."""
import json,time
from urllib.request import Request,urlopen
from urllib.error import HTTPError
out=[]
for url,headers in [
 ('https://api.gradium.ai/api/api-keys/token',{}),
 ('https://eu.api.gradium.ai/api/speech/asr',{'Connection':'Upgrade','Upgrade':'websocket','Sec-WebSocket-Version':'13','Sec-WebSocket-Key':'dGhlIHNhbXBsZSBub25jZQ=='}),
 ('https://eu.api.gradium.ai/api/speech/tts',{'Connection':'Upgrade','Upgrade':'websocket','Sec-WebSocket-Version':'13','Sec-WebSocket-Key':'dGhlIHNhbXBsZSBub25jZQ=='})]:
 t=time.monotonic()
 try:
  with urlopen(Request(url,headers=headers),timeout=10) as r:result={'url':url,'status':r.status,'body':r.read(1024).decode(errors='replace')}
 except HTTPError as e:result={'url':url,'status':e.code,'body':e.read(1024).decode(errors='replace')}
 except Exception as e:result={'url':url,'error':str(e)}
 result['elapsed_ms']=round((time.monotonic()-t)*1000);out.append(result)
print(json.dumps(out,indent=2))
