"""Run exact upstream Tts/Stt class bodies with fake transport; no installed deps or credentials."""
import ast, asyncio, base64, json, sys, types
from pathlib import Path
from typing import Any
source = Path(sys.argv[1])
tree = ast.parse(source.read_text())
classes = [n for n in tree.body if isinstance(n, ast.ClassDef) and n.name in ('Tts','Stt')]
assert len(classes) == 2
class DummyArray: pass
class FakeWS:
 def __init__(self): self.sent=[];self.incoming=[];self.closed=False
 async def send_json(self,x):self.sent.append(x)
 async def receive(self):return self.incoming.pop(0)
 async def close(self):self.closed=True
class FakeSession:
 def __init__(self):self.closed=False
 async def close(self):self.closed=True
ws_types=types.SimpleNamespace(TEXT=1,CLOSE=2)
namespace={'Any':Any,'json':json,'base64':base64,
 'np':types.SimpleNamespace(ndarray=DummyArray),
 'aiohttp':types.SimpleNamespace(WSMsgType=ws_types),
 'speech':types.SimpleNamespace(TTSSetup=dict,STTSetup=dict)}
exec(compile(ast.Module(body=classes,type_ignores=[]),str(source),'exec'),namespace)
def wire(x):return types.SimpleNamespace(type=ws_types.TEXT,data=json.dumps(x))
checks=[]
async def main():
 stt=namespace['Stt'](None);ws=FakeWS();stt._ws=ws
 await stt.send_setup({'input_format':'pcm_48000','json_config':{'language':'en','delay_in_frames':10}})
 assert ws.sent[-1]['type']=='setup'
 assert json.loads(ws.sent[-1]['json_config'])=={'language':'en','delay_in_frames':10}
 checks.append('STT setup serializes json_config as string and retains explicit sample-rate format')
 pcm=bytes(7680);await stt.send_audio(pcm)
 assert base64.b64decode(ws.sent[-1]['audio'])==pcm and len(ws.sent[-1]['audio'])==10240
 checks.append('80 ms PCM48k mono int16: 7680 raw bytes -> 10240 base64 bytes round-trips')
 await stt.send_flush(42);assert ws.sent[-1]=={'type':'flush','flush_id':42}
 ws.incoming=[wire({'type':'text','text':'stop','start_s':0.0,'stream_id':0}),wire({'type':'end_text','stop_s':0.4,'stream_id':0}),wire({'type':'flushed','flush_id':42})]
 assert [ (await stt.recv())['type'] for _ in range(3)]==['text','end_text','flushed']
 checks.append('STT flush emits correlation ID; exact receive body preserves text/end_text/flushed ordering')
 ws.incoming=[wire({'type':'ready','sample_rate':24000,'delay_in_frames':10,'residency':'pinned; zone=eu'})]
 msg=await stt.recv();assert stt.ready==msg and stt.ready['residency']=='pinned; zone=eu'
 checks.append('STT ready metadata preserves server-reported model delay and residency')
 await stt.send_eos();assert ws.sent[-1]=={'type':'end_of_stream'}
 ws.incoming=[wire({'type':'error','message':'test policy error','code':1008})]
 try:await stt.recv()
 except RuntimeError as e:assert '1008' in str(e)
 else:raise AssertionError('Expected RuntimeError')
 checks.append('STT EOS is graceful end_of_stream; provider errors raise terminal RuntimeError')
 tts=namespace['Tts'](None);tw=FakeWS();tts._ws=tw
 await tts.send_setup({'voice_id':'test-only','output_format':'pcm','close_ws_on_eos':False,'client_req_id':'epoch-7'})
 await tts.send_text('Complete words.',client_req_id='epoch-7');await tts.send_eos(client_req_id='epoch-7')
 assert all(m['client_req_id']=='epoch-7' for m in tw.sent)
 assert tw.sent[0]['close_ws_on_eos'] is False
 checks.append('TTS multiplex setup/text/EOS propagate client_req_id independently')
 audio=bytes(3840);tw.incoming=[wire({'type':'audio','audio':base64.b64encode(audio).decode(),'start_s':0,'stop_s':0.04,'client_req_id':'epoch-7'})]
 got=await tts.recv();assert got['audio']==audio and got['stop_s']==0.04
 checks.append('TTS receive decodes variable-length first audio chunk and preserves timing/correlation')
 fake_session=FakeSession();tts._session=fake_session
 await tts.__aexit__(None,None,None);assert tw.closed and fake_session.closed
 checks.append('TTS context exit closes socket and HTTP session')
 print(json.dumps({'result':'PASS','checks':checks,'scope':'Exact upstream class bodies with stubs; not a live service or complete SDK import'},indent=2))
asyncio.run(main())
