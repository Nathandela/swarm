import os,sys,json,time,threading,http.server,subprocess,signal,select,shutil,hashlib
from pathlib import Path
sys.path.insert(0, str(Path(os.environ['SWARM_PROVENANCE_REPO']) / 'scripts/verification'))
from native_fixture_sandbox import guarded,check_guards
from custody import birth
root=Path(__file__).parent;home=root/'home';config=home/'.codex';config.mkdir(parents=True);(home/'tmp').mkdir()
source=Path(sys.argv[1] if len(sys.argv)>1 else shutil.which('codex')).resolve();binary=root/'codex'
shutil.copyfile(source,binary);binary.chmod(0o700)
print('private Codex sha256='+hashlib.sha256(binary.read_bytes()).hexdigest(),flush=True)
requests=[]
class API(http.server.BaseHTTPRequestHandler):
 def log_message(self,*args):pass
 def do_GET(self):self.send_response(200);self.end_headers();self.wfile.write(b'{"data":[]}')
 def do_POST(self):
  raw=self.rfile.read(int(self.headers.get('Content-Length','0')))
  body=json.loads(raw);requests.append(body);n=len(requests)
  response={'id':'resp_probe_'+str(n),'object':'response','created_at':int(time.time()),'model':'fixture','status':'in_progress','output':[]}
  events=[('response.created',{'type':'response.created','response':dict(response)})]
  if n==1:
   name='mcp__recorder__publish';namespace=None
   for tool in body.get('tools',[]):
    if tool.get('type')=='namespace' and 'recorder' in tool.get('name',''):
     namespace=tool['name'];name=next((t['name'] for t in tool.get('tools',[]) if t.get('name')=='publish'),'publish')
    elif 'recorder' in tool.get('name',''):name=tool['name']
   args=json.dumps({'text':'Synthetic Codex phrase','turn_id':'MODEL_CLAIM_NOT_AUTHORITY','hook_stamp':'MODEL_SPOOF'})
   item={'type':'function_call','id':'fc_probe','call_id':'call_native_probe','name':name,'arguments':args,'status':'completed'}
   if namespace:item['namespace']=namespace
   start=dict(item);start['arguments']='';start['status']='in_progress'
   events.extend([('response.output_item.added',{'type':'response.output_item.added','output_index':0,'item':start}),('response.function_call_arguments.delta',{'type':'response.function_call_arguments.delta','item_id':item['id'],'output_index':0,'delta':args}),('response.function_call_arguments.done',{'type':'response.function_call_arguments.done','item_id':item['id'],'output_index':0,'arguments':args}),('response.output_item.done',{'type':'response.output_item.done','output_index':0,'item':item})])
  else:
   item={'type':'message','id':'msg_probe_'+str(n),'role':'assistant','content':[{'type':'output_text','text':'Synthetic tool complete.','annotations':[]}],'status':'completed'}
   events.extend([('response.output_item.added',{'type':'response.output_item.added','output_index':0,'item':item}),('response.output_text.delta',{'type':'response.output_text.delta','item_id':item['id'],'output_index':0,'content_index':0,'delta':'Synthetic tool complete.'}),('response.output_item.done',{'type':'response.output_item.done','output_index':0,'item':item})])
  response.update(status='completed',output=[item],usage={'input_tokens':10,'output_tokens':10,'total_tokens':20,'input_tokens_details':{'cached_tokens':0},'output_tokens_details':{'reasoning_tokens':0}})
  events.append(('response.completed',{'type':'response.completed','response':response}))
  self.send_response(200);self.send_header('Content-Type','text/event-stream');self.end_headers()
  try:
   for event,value in events:self.wfile.write(('event: '+event+'\ndata: '+json.dumps(value)+'\n\n').encode());self.wfile.flush()
  except (BrokenPipeError,ConnectionResetError):pass
  self.close_connection=True
api=http.server.ThreadingHTTPServer(('127.0.0.1',0),API);api.daemon_threads=True;threading.Thread(target=api.serve_forever,daemon=True).start()
mcplog=root/'mcp.jsonl';custody=root/'mcp-custody.json'
settings='model = "fixture"\nmodel_provider = "fixture"\napproval_policy = "never"\nsandbox_mode = "danger-full-access"\n[model_providers.fixture]\nname = "fixture"\nbase_url = "http://127.0.0.1:PORT/v1"\nwire_api = "responses"\nenv_key = "FIXTURE_API_KEY"\nrequires_openai_auth = false\nsupports_websockets = false\n[mcp_servers.recorder]\ncommand = PYTHON\nargs = ARGS\nrequired = true\n[mcp_servers.recorder.tools.publish]\napproval_mode = "approve"\n'
settings=settings.replace('PORT',str(api.server_port)).replace('PYTHON',json.dumps(sys.executable)).replace('ARGS',json.dumps([str(root/'recorder.py'),str(mcplog),str(custody)]))
(config/'config.toml').write_text(settings)
env={'PATH':'/usr/bin:/bin:/usr/sbin:/sbin','HOME':str(home),'CODEX_HOME':str(config),'TMPDIR':str(home/'tmp'),'FIXTURE_API_KEY':'synthetic-api-key','RUST_LOG':'warn','NO_PROXY':'127.0.0.1,localhost','HTTP_PROXY':'http://127.0.0.1:65501','HTTPS_PROXY':'http://127.0.0.1:65501','ALL_PROXY':'http://127.0.0.1:65501'}
check_guards(root,[api.server_port])
errfile=(root/'stderr.log').open('wb');proc=subprocess.Popen(guarded([str(binary),'app-server'],root,[api.server_port]),cwd=root,env=env,stdin=subprocess.PIPE,stdout=subprocess.PIPE,stderr=errfile,start_new_session=True)
frames=[];buffer=b''
def write(value):proc.stdin.write((json.dumps(value)+'\n').encode());proc.stdin.flush()
def read_until(predicate,timeout=15):
 global buffer
 deadline=time.monotonic()+timeout
 while time.monotonic()<deadline and proc.poll() is None:
  ready,_,_=select.select([proc.stdout],[],[],.1)
  if not ready:continue
  data=os.read(proc.stdout.fileno(),65536)
  if not data:break
  buffer+=data
  while b'\n' in buffer:
   line,buffer=buffer.split(b'\n',1)
   try:frame=json.loads(line)
   except ValueError:continue
   frames.append(frame)
   if 'method' in frame and 'id' in frame:write({'id':frame['id'],'result':{'decision':'accept'}})
   if predicate(frame):return frame
 raise RuntimeError('native RPC timeout/exit; '+(root/'stderr.log').read_text(errors='replace')[-1500:])
try:
 write({'id':1,'method':'initialize','params':{'clientInfo':{'name':'swarm_causal_probe','version':'0'},'capabilities':{'experimentalApi':True}}})
 init=read_until(lambda f:f.get('id')==1);assert 'result' in init,init
 write({'method':'initialized','params':{}})
 write({'id':2,'method':'thread/start','params':{'model':'fixture','modelProvider':'fixture','cwd':str(root),'approvalPolicy':'never','sandbox':'danger-full-access','ephemeral':True}})
 thread=read_until(lambda f:f.get('id')==2);assert 'result' in thread,thread;tid=thread['result']['thread']['id']
 write({'id':3,'method':'turn/start','params':{'threadId':tid,'input':[{'type':'text','text':'Call recorder publish once with a neutral synthetic phrase.'}]}})
 done=read_until(lambda f:f.get('method')=='turn/completed',25)
 calls=[json.loads(l)['request'] for l in mcplog.read_text().splitlines() if json.loads(l)['request'].get('method')=='tools/call'] if mcplog.exists() else []
 result={'native_version':'0.167.0','binary_sha256':hashlib.sha256(binary.read_bytes()).hexdigest(),'frames':frames,'mcp_calls':calls,'model_requests':requests,'real_account_used':False}
 (root/'result.json').write_text(json.dumps(result,indent=2))
 print(json.dumps({'mcp_calls':calls,'turn_complete':done,'thread_id':tid,'artifact':str(root)},indent=2),flush=True)
 assert calls,'no native MCP call recorded'
finally:
 (root/'partial-frames.json').write_text(json.dumps(frames,indent=2));(root/'partial-requests.json').write_text(json.dumps(requests,indent=2))
 try:os.killpg(proc.pid,signal.SIGTERM)
 except ProcessLookupError:pass
 try:proc.wait(timeout=2)
 except subprocess.TimeoutExpired:os.killpg(proc.pid,signal.SIGKILL);proc.wait(timeout=2)
 if custody.exists():
  owned=json.loads(custody.read_text());pid=owned['pid']
  if owned['birth'] is not None and birth(pid)==owned['birth']:
   os.kill(pid,signal.SIGTERM);time.sleep(.1)
   if birth(pid)==owned['birth']:os.kill(pid,signal.SIGKILL)
 api.shutdown();errfile.close()
