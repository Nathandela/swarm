#!/usr/bin/env python3
"""Actual copied Claude TUI + production manager mods + synthetic model/account."""
from native_fixture_sandbox import guarded, check_guards
import fcntl, hashlib, http.client, http.server, json, os, pathlib, pty, re, select, signal, socket, struct, subprocess, sys, tempfile, termios, threading, time, uuid

def probe():
 binary, descriptor_path, plugin = sys.argv[1], pathlib.Path(sys.argv[2]), pathlib.Path(sys.argv[3])
 descriptor = json.loads(descriptor_path.read_text())
 root = pathlib.Path(tempfile.mkdtemp(prefix='manager-',dir=str(descriptor_path.parent))).resolve()
 home=root/'home';config=home/'.claude';config.mkdir(parents=True);(home/'tmp').mkdir()
 requests=[];calls=[];trace=[];tool_inputs=set();held_inputs=set();phase='normal';hold=threading.Event();release=threading.Event();mcp_seen=threading.Event();mcp_release=threading.Event();mcp_release.set();worker=None;binding=None
 class UnixHTTP(http.client.HTTPConnection):
  def connect(self):
   self.sock=socket.socket(socket.AF_UNIX);self.sock.settimeout(3);self.sock.connect(descriptor['socket_path'])
 def broker(path,body,expected=200):
  peer=UnixHTTP('localhost',timeout=3)
  try:
   peer.request('POST',path,json.dumps({'generation':descriptor['generation'],**body}),{'Authorization':'Bearer '+descriptor['token'],'Content-Type':'application/json'})
   response=peer.getresponse();raw=response.read();assert response.status==expected,(path,response.status,raw.decode());return json.loads(raw) if expected==200 else None
  finally:peer.close()
 class API(http.server.BaseHTTPRequestHandler):
  def log_message(self,*args):pass
  def do_GET(self):self.send_error(404)
  def do_POST(self):
   body=json.loads(self.rfile.read(int(self.headers.get('Content-Length','0'))))
   if self.path=='/trace':
    trace.append(body);self.send_response(200);self.end_headers();self.wfile.write(b'{}');return
   if self.path=='/mcp':
    args=body.get('arguments',{});meta=body.get('_meta',{});digest=hashlib.sha256(json.dumps(args,sort_keys=True,separators=(',',':'),ensure_ascii=False).encode()).hexdigest()
    proof=broker('/manager-resolve',{'worker':worker,'proof_request':{'binding':binding,'tool_name':body['name'],'arguments_hash':digest,'host_meta':meta}})
    calls.append({'meta':meta,'arguments_hash':digest,'proof':proof,'args':args});mcp_seen.set();mcp_release.wait(12)
    result={'content':[{'type':'text','text':'Neutral synthetic publication observed.'}]};self.send_response(200);self.end_headers();self.wfile.write(json.dumps(result).encode());return
   if '/messages' not in self.path:self.send_error(404);return
   primary=any(t.get('name')=='mcp__swarm_voice__publish' for t in body.get('tools',[]))
   last=body.get('messages',[{}])[-1].get('content',[]);blocks=last if isinstance(last,list) else [{'type':'text','text':last}]
   tool_result=any(x.get('type')=='tool_result' for x in blocks) and not any(x.get('type')=='text' and '[SWARM_MANAGER_INPUT_V1:' in x.get('text','') for x in blocks)
   text=' '.join(x.get('text','') for x in blocks)
   requests.append({'primary':primary,'phase':phase,'tool_result':tool_result})
   nonces=re.findall(r'\[SWARM_MANAGER_INPUT_V1:([a-f0-9]{64})\]',json.dumps(body.get('messages',[])))
   input_nonce=nonces[-1] if nonces else ''
   if primary and phase=='error':
    self.send_response(400);self.send_header('Content-Type','application/json');self.end_headers();self.wfile.write(b'{"type":"error","error":{"type":"invalid_request_error","message":"Synthetic bounded model error"}}');return
   use_tool=primary and phase in ['publish','modal'] and input_nonce not in tool_inputs
   if use_tool:tool_inputs.add(input_nonce)
   content={'type':'tool_use','id':'native_call_'+str(len(requests)),'name':'mcp__swarm_voice__'+('ask_permission' if phase=='modal' else 'publish'),'input':{}} if use_tool else {'type':'text','text':''}
   delta={'type':'input_json_delta','partial_json':json.dumps({'text':'Neutral phrase.','turn_id':'MODEL_CLAIM_NOT_AUTHORITY'})} if use_tool else {'type':'text_delta','text':'Neutral synthetic response.'}
   stop='tool_use' if use_tool else ('refusal' if primary and phase=='refusal' else 'end_turn')
   events=[('message_start',{'type':'message_start','message':{'id':'msg_synthetic','type':'message','role':'assistant','model':body.get('model'),'content':[],'stop_reason':None,'stop_sequence':None,'usage':{'input_tokens':1,'output_tokens':0}}}),('content_block_start',{'type':'content_block_start','index':0,'content_block':content}),('content_block_delta',{'type':'content_block_delta','index':0,'delta':delta}),('content_block_stop',{'type':'content_block_stop','index':0}),('message_delta',{'type':'message_delta','delta':{'stop_reason':stop,'stop_sequence':None},'usage':{'output_tokens':1}}),('message_stop',{'type':'message_stop'})]
   if not body.get('stream'):
    self.send_response(200);self.send_header('Content-Type','application/json');self.end_headers();self.wfile.write(json.dumps({'id':'msg_aux','type':'message','role':'assistant','model':body.get('model'),'content':[{'type':'text','text':'neutral'}],'stop_reason':'end_turn','usage':{'input_tokens':1,'output_tokens':1}}).encode());return
   self.send_response(200);self.send_header('Content-Type','text/event-stream');self.end_headers()
   if primary and phase=='hold' and input_nonce not in held_inputs:held_inputs.add(input_nonce);hold.set();release.wait(12)
   try:
    for name,event in events:self.wfile.write(('event: '+name+'\ndata: '+json.dumps(event)+'\n\n').encode());self.wfile.flush()
   except (BrokenPipeError,ConnectionResetError):pass
   self.close_connection=True
 class Deny(http.server.BaseHTTPRequestHandler):
  def log_message(self,*args):pass
  def do_CONNECT(self):self.send_error(403)
  def do_GET(self):self.send_error(403)
  def do_POST(self):self.send_error(403)
 api=http.server.ThreadingHTTPServer(('127.0.0.1',0),API);deny=http.server.ThreadingHTTPServer(('127.0.0.1',0),Deny)
 for server in [api,deny]:server.daemon_threads=True;threading.Thread(target=server.serve_forever,daemon=True).start()
 (config/'.claude.json').write_text(json.dumps({'hasCompletedOnboarding':True,'theme':'dark','lastOnboardingVersion':descriptor['provider_version'],'oauthAccount':{'accountUuid':'synthetic-account','emailAddress':'synthetic@example.invalid','organizationUuid':'synthetic-org'},'projects':{str(root):{'hasTrustDialogAccepted':True}}}))
 (config/'.credentials.json').write_text(json.dumps({'claudeAiOauth':{'accessToken':'synthetic-token','refreshToken':'synthetic-refresh','expiresAt':int(time.time()*1000)+3600000,'scopes':['user:inference','user:profile'],'subscriptionType':'max','rateLimitTier':'default_claude_max_5x'}}))
 env={'PATH':'/usr/bin:/bin:/usr/sbin:/sbin','HOME':str(home),'CLAUDE_CONFIG_DIR':str(config),'CLAUDE_SECURESTORAGE_CONFIG_DIR':str(config),'TMPDIR':str(home/'tmp'),'TERM':'xterm-256color','ANTHROPIC_BASE_URL':'http://127.0.0.1:'+str(api.server_port),'CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC':'1','DISABLE_AUTOUPDATER':'1','CLAUDE_CODE_MAX_RETRIES':'0','PYTHONDONTWRITEBYTECODE':'1','HTTP_PROXY':'http://127.0.0.1:'+str(deny.server_port),'HTTPS_PROXY':'http://127.0.0.1:'+str(deny.server_port),'ALL_PROXY':'http://127.0.0.1:'+str(deny.server_port),'NO_PROXY':'127.0.0.1,localhost'}
 custody=root/'mcp-custody.json';repo=pathlib.Path(__file__).resolve().parents[2];recorder=pathlib.Path(__file__).with_name('claude_manager_recorder.py')
 mcp={'mcpServers':{'swarm_voice':{'command':sys.executable,'args':[str(recorder),'http://127.0.0.1:'+str(api.server_port)+'/mcp',str(repo/'docs/verification/voice-manager/probes'),str(custody)]}}}
 cmd=[binary,'--setting-sources','','--strict-mcp-config','--mcp-config',json.dumps(mcp),'--plugin-dir',str(plugin),'--plugin-dir',str(plugin.parent/'claude-input-guard-plugin'),'--system-prompt','Neutral bounded provenance fixture.','--tools','','--allowedTools','mcp__swarm_voice__publish','--permission-mode','default','--model','claude-sonnet-4-6','--debug-file',str(root/'debug.log')]
 observer=root/'observer';(observer/'.claude-plugin').mkdir(parents=True);(observer/'hooks').mkdir();(observer/'types').mkdir()
 (observer/'.claude-plugin/plugin.json').write_text(json.dumps({'name':'manager-proof-observer','version':'0','types':'./types/index.d.ts'}))
 (observer/'types/index.d.ts').write_text('export interface PluginState {}');(observer/'hooks/hooks.json').write_text('{"modules":["./observe.js"]}')
 source="const trace=($,body)=>$.http.fetch('http://127.0.0.1:"+str(api.server_port)+"/trace',{method:'POST',body:JSON.stringify(body)});export function register(on){on('turn.start',async($,e,n)=>{await trace($,{event:'start',turnId:e.turnId,agentId:e.agentId});return n(e)});on('turn.complete',async($,e,n)=>{await trace($,{event:'complete',turnId:e.turnId,agentId:e.agentId,reason:e.reason});return n(e)});on('classic.PermissionRequest',async($,e,n)=>{await trace($,{event:'permission',tool:e.tool_name});return n(e)});on('turn.step',async function*($,e,n){const it=n(e);for(;;){const s=await it.next();if(s.done)return s.value;if(s.value.kind==='tool')await trace($,{event:'step',turnId:e.turnId,agentId:e.agentId,chunk:s.value});yield s.value}});on('tool.call',async($,e,n)=>{await trace($,{event:'call',tool:e.tool,id:e.tool_use_id,agentId:e.agentId,keys:Object.keys(e)});return n(e)}); }"
 (observer/'hooks/observe.js').write_text(source)
 cmd[1:1]=['--plugin-dir',str(observer)]
 guard_root=descriptor_path.parent.resolve();check_guards(guard_root,[api.server_port])
 for path in [plugin,plugin.parent/'claude-input-guard-plugin']:
  validation=subprocess.run(guarded([binary,'plugin','validate',str(path),'--json'],guard_root,[api.server_port]),cwd=root,env=env,capture_output=True,text=True,timeout=8)
  assert validation.returncode==0,validation.stdout+validation.stderr
 master,slave=pty.openpty();fcntl.ioctl(slave,termios.TIOCSWINSZ,struct.pack('HHHH',45,160,0,0));process=subprocess.Popen(guarded(cmd,guard_root,[api.server_port]),cwd=root,env=env,stdin=slave,stdout=slave,stderr=slave,start_new_session=True);os.close(slave)
 terminal=bytearray();startup_dialogs=[];deadline=time.monotonic()+85
 def drain():
  readable,_,_=select.select([master],[],[],.05)
  if readable:
   try:terminal.extend(os.read(master,65536))
   except OSError:pass
  plain=re.sub(rb'\x1b\[[0-?]*[ -/]*[@-~]',b'',bytes(terminal)).decode(errors='replace')
  for gate in ['New MCP server found','I am using this for local development']:
   if gate in plain and gate not in startup_dialogs:startup_dialogs.append(gate);os.write(master,b'\r')
 def wait(check,seconds=8):
  until=min(deadline,time.monotonic()+seconds)
  while time.monotonic()<until and process.poll() is None:
   drain()
   try:
    found=check()
    if found:return found
   except (AssertionError,OSError):pass
  raise AssertionError('native fixture condition timeout; '+re.sub(rb'\x1b\[[0-?]*[ -/]*[@-~]',b'',bytes(terminal))[-1800:].decode(errors='replace'))
 def turn():return broker('/turn-status',{'worker':worker})
 def outcome(nonce):return broker('/manager-input-status',{'worker':worker,'binding':binding,'native_input_id':nonce})
 def submit(text):
  snapshot=turn();nonce=uuid.uuid4().hex+uuid.uuid4().hex
  broker('/manager-submit',{'worker':worker,'operation_id':'op-'+nonce,'text':text,'admission':{'binding':binding,'nonce':nonce,'expected_external_input':snapshot['external_input']}})
  return nonce
 results=[]
 try:
  worker=wait(lambda:broker('/ready',{}),20)
  binding={'session':descriptor['session_id'],'session_instance':'synthetic-instance','conversation_id':descriptor['manager_conversation_id'],'provider':'claude','native_session_id':worker['native_session_id'],'native_generation':'synthetic-native-process','observation_generation':broker('/manager-observation',{'worker':worker})['generation'],'login_epoch':'synthetic-account-identity'}
  broker('/manager-bind',{'worker':worker,'binding':binding})
  wait(lambda:turn().get('ready_for_input'))
  results.append({'case':'startup','ready':True,'startup_dialogs':startup_dialogs})
  phase='publish';nonce=submit('Publish a neutral phrase.')
  wait(lambda:any(c['proof']['native_input_id']==nonce for c in calls));wait(lambda:outcome(nonce)['state']=='completed');wait(lambda:turn().get('ready_for_input'))
  original=next(c for c in calls if c['proof']['native_input_id']==nonce)
  assert original['proof']['main'] and original['proof']['native_turn_id']!=original['args']['turn_id']
  results.append({'case':'native-call','proof':original['proof'],'meta':original['meta'],'completed':outcome(nonce)})
  phase='hold';hold.clear();release.clear();nonce=submit('Hold this synthetic model turn.')
  wait(hold.is_set);active=turn();assert active['state']=='running' and not active['ready_for_input']
  broker('/interrupt',{'worker':worker,'operation_id':'exact-native-stop','turn_id':active['turn_id']})
  wait(lambda:outcome(nonce).get('reason')=='aborted');release.set();wait(lambda:turn().get('ready_for_input'))
  phase='normal';fresh=submit('Fresh human input after exact native abort.');wait(lambda:outcome(fresh)['state']=='completed')
  results.append({'case':'abort-recovery','old':outcome(nonce),'fresh':outcome(fresh)})
  for failure in ['error','refusal']:
   phase=failure;nonce=submit('Exercise synthetic '+failure+'.');terminal_outcome=wait(lambda:outcome(nonce) if outcome(nonce)['state']=='completed' else None)
   assert terminal_outcome['reason']==('error' if failure=='error' else 'refusal'),terminal_outcome
   wait(lambda:turn().get('ready_for_input'));phase='normal';fresh=submit('Fresh human input after synthetic '+failure+'.');wait(lambda:outcome(fresh)['state']=='completed')
   results.append({'case':failure+'-recovery','old':terminal_outcome,'fresh':outcome(fresh)})
  phase='modal';nonce=submit('Exercise a neutral native permission modal.')
  wait(lambda:any(e['event']=='permission' and e.get('tool')=='mcp__swarm_voice__ask_permission' for e in trace))
  modal=turn();assert modal['state']=='running' and not modal['ready_for_input']
  # Stay beyond the broker liveness horizon: native timers must keep custody live.
  modal_until=time.monotonic()+3.5
  while time.monotonic()<modal_until:
   drain();assert not turn()['ready_for_input']
  assert not any(c['proof']['native_input_id']==nonce for c in calls)
  os.write(master,b'\x1b');wait(lambda:outcome(nonce)['state']=='completed');wait(lambda:turn().get('ready_for_input'))
  results.append({'case':'permission-modal','ready':modal['ready_for_input'],'held_seconds':3.5,'no_host_call':True,'outcome':outcome(nonce)})
  phase='publish';mcp_seen.clear();mcp_release.clear();nonce=submit('Hold a publication for manual native steering.')
  wait(mcp_seen.is_set);before=turn();assert not before['ready_for_input']
  os.write(master,b'Manual native steering.');time.sleep(.15);os.write(master,b'\r')
  wait(lambda:turn()['external_input']>before['external_input'])
  call=next(c for c in reversed(calls) if c['proof']['native_input_id']==nonce)
  broker('/manager-resolve',{'worker':worker,'proof_request':{'binding':binding,'tool_name':'publish','arguments_hash':call['arguments_hash'],'host_meta':call['meta']}},409)
  phase='normal';mcp_release.set();results.append({'case':'manual-steer','before':before,'after':turn(),'proof_refused':True})
  # The exact old nonce outcome remains separate from whichever manual turn is current.
  wait(lambda:outcome(nonce)['state'] in ['completed','unknown']);results.append({'case':'historical-input','outcome':outcome(nonce)})
  result={'version':descriptor['provider_version'],'sender_name':descriptor['sender_name'],'real_account_used':False,'production_sessions_used':False,'microphone_used':False,'production_guard_sha256':hashlib.sha256((plugin.parent/'claude-input-guard-plugin'/'hooks/input.js').read_bytes()).hexdigest(),'results':results}
  destination=os.environ.get('SWARM_NATIVE_MANAGER_EVIDENCE')
  if destination:pathlib.Path(destination).write_text(json.dumps(result,indent=2)+'\n')
  print(json.dumps(result,indent=2))
 finally:
  print(json.dumps({'native_trace':trace,'request_phases':requests,'completed_cases':results},indent=2))
  try:print(json.dumps({'last_native_turn':turn(),'last_input_outcome':outcome(nonce)}))
  except Exception as failure:print(str(failure))
  release.set();mcp_release.set()
  try:os.killpg(process.pid,signal.SIGTERM)
  except ProcessLookupError:pass
  try:process.wait(timeout=2)
  except subprocess.TimeoutExpired:os.killpg(process.pid,signal.SIGKILL);process.wait(timeout=2)
  os.close(master)
  if custody.exists():
   sys.path.insert(0,str(repo/'docs/verification/voice-manager/probes'));from custody import birth
   owned=json.loads(custody.read_text());pid=owned['pid']
   if owned['birth'] is not None and birth(pid)==owned['birth']:
    os.kill(pid,signal.SIGTERM);time.sleep(.1)
    if birth(pid)==owned['birth']:os.kill(pid,signal.SIGKILL)
  api.shutdown();deny.shutdown()
if __name__=='__main__':probe()
