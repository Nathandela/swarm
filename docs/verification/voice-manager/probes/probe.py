#!/usr/bin/env python3
"""Isolated current-Claude native mod ingress and permission-separation probe."""
import sys
sys.path.insert(0, str(__import__("pathlib").Path(__import__("os").environ["SWARM_PROVENANCE_REPO"]) / "scripts/verification"))
from native_fixture_sandbox import guarded, check_guards
import fcntl, http.server, json, os, pathlib, pty, re, select, signal, socket, struct, subprocess, sys, tempfile, termios, threading, time, uuid

def probe():
 import http.client
 descriptor=json.loads(pathlib.Path(sys.argv[2]).read_text())
 plugin=pathlib.Path(sys.argv[3])
 binary=sys.argv[1]
 class UnixHTTP(http.client.HTTPConnection):
  def connect(self):
   self.sock=socket.socket(socket.AF_UNIX);self.sock.settimeout(3);self.sock.connect(descriptor["socket_path"])
 def broker(path,body):
  peer=UnixHTTP("localhost",timeout=3)
  try:
   body["generation"]=descriptor["generation"]
   peer.request("POST",path,json.dumps(body),{"Authorization":"Bearer "+descriptor["token"],"Content-Type":"application/json"})
   reply=peer.getresponse();raw=reply.read();assert reply.status==200,(reply.status,raw.decode());return json.loads(raw)
  finally:peer.close()
 root=pathlib.Path(tempfile.mkdtemp(prefix='swnative-',dir=str(pathlib.Path(sys.argv[2]).parent))).resolve();home=root/'home';cfg=home/'.claude';cfg.mkdir(parents=True);(home/'tmp').mkdir()
 child_spawned=False;phase='initial';queue_mode=os.environ.get('SWARM_CAUSAL_QUEUE_PROBE')=='1';causal_gate=threading.Event();causal_release=threading.Event()
 requests=[];native_events=[];done=threading.Event();pause_entered=threading.Event();pause_release=threading.Event();clear_completed=threading.Event();clear_native_ids=[];late_entered=threading.Event();late_release=threading.Event();interrupt_entered=threading.Event();interrupt_release=threading.Event()
 class API(http.server.BaseHTTPRequestHandler):
  def log_message(self,*args):pass
  def do_GET(self):self.send_response(404);self.end_headers()
  def do_POST(self):
   nonlocal child_spawned
   body=json.loads(self.rfile.read(int(self.headers.get('Content-Length','0'))))
   if self.path=='/lifecycle/event':
    native_events.append(body);self.send_response(200);self.end_headers();self.wfile.write(b'{}');return
   if self.path=='/lifecycle/late-gate':
    late_entered.set();late_release.wait(20);self.send_response(200);self.end_headers();self.wfile.write(b'{}');return
   if self.path=='/lifecycle/cleared':
    clear_native_ids.append(body['id']);clear_completed.set();self.send_response(200);self.end_headers();self.wfile.write(b'{}');return
   if self.path=='/lifecycle/pause':
    pause_entered.set();pause_release.wait(12);self.send_response(200);self.end_headers();self.wfile.write(b'{}');return
   if '/messages' not in self.path:self.send_response(404);self.end_headers();return
   primary=any('recorder' in t.get('name','') for t in body.get('tools',[]))
   text=json.dumps(body.get('messages',[]))
   requests.append({'primary':primary,'marker_present':'CHANNEL_NATIVE_MARKER' in text,'late_present':'LATE_OLD_RPC_MARKER' in text,'owner_present':'OWNER_REPLACEMENT_MARKER' in text,'permissions_result':'tool_result' in text})
   n=sum(r['primary'] for r in requests)
   last=body.get('messages',[{}])[-1].get('content',[])
   blocks=last if isinstance(last,list) else [{'type':'text','text':last}]
   result_input=any(v.get('type')=='tool_result' for v in blocks)
   wants_child=primary and not result_input and not child_spawned and any('Run the child provenance probe.' in v.get('text','') for v in blocks)
   latest_step=next((e for e in reversed(native_events) if e['event']=='turn.step'),{})
   wants_child=primary and phase=='child' and not child_spawned and latest_step.get('agentId') is None
   if wants_child:child_spawned=True
   tool=primary and latest_step.get('index')==0
   tool_name='Agent' if wants_child else next((t['name'] for t in body.get('tools',[]) if 'recorder' in t.get('name','')), 'mcp__recorder__publish')
   content={'type':'tool_use','id':'causal_tool_'+str(n),'name':tool_name,'input':{}} if tool else {'type':'text','text':''}
   delta={'type':'input_json_delta','partial_json':json.dumps({'description':'Child provenance probe','prompt':'CHILD_CAUSAL_PROBE '+envelope,'subagent_type':'general-purpose'} if wants_child else {'text':'Synthetic phrase '+str(n),'turn_id':'MODEL_CLAIM_NOT_AUTHORITY','hook_stamp':'MODEL_SPOOF'})} if tool else {'type':'text_delta','text':'Synthetic channel processing complete'}
   events=[('message_start',{'type':'message_start','message':{'id':'msg_probe','type':'message','role':'assistant','model':body.get('model'),'content':[],'stop_reason':None,'stop_sequence':None,'usage':{'input_tokens':1,'output_tokens':0}}}),('content_block_start',{'type':'content_block_start','index':0,'content_block':content}),('content_block_delta',{'type':'content_block_delta','index':0,'delta':delta}),('content_block_stop',{'type':'content_block_stop','index':0}),('message_delta',{'type':'message_delta','delta':{'stop_reason':'tool_use' if tool else 'end_turn','stop_sequence':None},'usage':{'output_tokens':1}}),('message_stop',{'type':'message_stop'})]
   if not body.get('stream'):
    data=json.dumps({'id':'msg_aux','type':'message','role':'assistant','model':body.get('model'),'content':[{'type':'text','text':'complete'}],'stop_reason':'end_turn','usage':{'input_tokens':1,'output_tokens':1}}).encode();self.send_response(200);self.send_header('Content-Type','application/json');self.end_headers();self.wfile.write(data);return
   self.send_response(200);self.send_header('Content-Type','text/event-stream');self.end_headers()
   if queue_mode and primary and n==1:causal_gate.set();causal_release.wait(8)
   if primary and 'INTERRUPT_NATIVE_MARKER' in text and not interrupt_entered.is_set():
    interrupt_entered.set();interrupt_release.wait(15)
   try:
    for event,obj in events:self.wfile.write(('event: '+event+'\ndata: '+json.dumps(obj)+'\n\n').encode());self.wfile.flush()
   except (BrokenPipeError,ConnectionResetError):pass
   self.close_connection=True
 class Deny(http.server.BaseHTTPRequestHandler):
  def log_message(self,*args):pass
  def do_CONNECT(self):self.send_error(403)
  def do_GET(self):self.send_error(403)
  def do_POST(self):self.send_error(403)
 api=http.server.ThreadingHTTPServer(('127.0.0.1',0),API);deny=http.server.ThreadingHTTPServer(('127.0.0.1',0),Deny)
 for s in [api,deny]:s.daemon_threads=True;threading.Thread(target=s.serve_forever,daemon=True).start()
 config={'hasCompletedOnboarding':True,'theme':'dark','lastOnboardingVersion':descriptor['provider_version'],'oauthAccount':{'accountUuid':'synthetic-account','emailAddress':'synthetic@example.invalid','organizationUuid':'synthetic-org'},'projects':{str(root):{'hasTrustDialogAccepted':True}}}
 (cfg/'.claude.json').write_text(json.dumps(config));(cfg/'.credentials.json').write_text(json.dumps({'claudeAiOauth':{'accessToken':'synthetic-token','refreshToken':'synthetic-refresh','expiresAt':int(time.time()*1000)+3600000,'scopes':['user:inference','user:profile'],'subscriptionType':'max','rateLimitTier':'default_claude_max_5x'}}))
 env={'PATH':'/usr/bin:/bin:/usr/sbin:/sbin','HOME':str(home),'CLAUDE_CONFIG_DIR':str(cfg),'TMPDIR':str(home/'tmp'),'TERM':'xterm-256color','ANTHROPIC_BASE_URL':'http://127.0.0.1:'+str(api.server_port),'CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC':'1','DISABLE_AUTOUPDATER':'1','CLAUDE_CODE_MAX_RETRIES':'0','HTTP_PROXY':'http://127.0.0.1:'+str(deny.server_port),'HTTPS_PROXY':'http://127.0.0.1:'+str(deny.server_port),'ALL_PROXY':'http://127.0.0.1:'+str(deny.server_port),'NO_PROXY':'127.0.0.1,localhost'}
 artifact=pathlib.Path(sys.argv[4]);recorder=sys.argv[5]
 mcp_log=root/'mcp.jsonl'
 mcp_custody=root/'mcp-custody.json'
 mcp=json.dumps({'mcpServers':{'recorder':{'command':sys.executable,'args':[recorder,str(mcp_log),str(mcp_custody)]}}})
 cmd=[binary,'--setting-sources','','--strict-mcp-config','--mcp-config',mcp,'--plugin-dir',str(plugin),'--plugin-dir',str(plugin.parent/'claude-input-guard-plugin'),'--system-prompt','Synthetic bounded protocol probe.','--tools','Agent','--allowedTools','Agent,mcp__recorder__publish','--permission-mode','default','--model','claude-sonnet-4-6','--debug-file',str(root/'debug.log'),'Begin the synthetic causal tool probe.']


 nonce='SWARM_INPUT_'+uuid.uuid4().hex
 envelope='['+nonce+'] Neutral machine-authenticated operation.'
 observer=root/'causal-observer'
 (observer/'.claude-plugin').mkdir(parents=True);(observer/'hooks').mkdir();(observer/'types').mkdir()
 (observer/'.claude-plugin/plugin.json').write_text(json.dumps({'name':'swarm-causal-observer','version':'0.0.1','types':'./types/index.d.ts'}))
 (observer/'hooks/hooks.json').write_text('{"modules":["./observe.js"]}')
 (observer/'types/index.d.ts').write_text('export interface PluginState {}')
 source=r"""
 const authorized = __ENVELOPE__;
 let admitted = false, consumed = false, retired = false, attestedText = '';
 const turnBindings = new Map(), toolBindings = new Map();
 const record = ($, body) => $.http.fetch(__URL__, {method:'POST',body:JSON.stringify(body)});
 export function register(on) {
   on('prompt.submit', async ($, e, next) => {
     await record($,{event:'prompt.submit',...e});
     if(e.text.includes('[SWARM_INPUT_')) {
       if(retired || e.origin.kind !== 'plugin' || e.origin.name.split('@')[0] !== 'swarm-native-input' || e.text !== authorized || admitted) {
         await record($,{event:'envelope.refused',origin:e.origin}); return {drop:'Unattested or repeated machine envelope'};
       }
       admitted=true;
     }
     const result=await next(e);await record($,{event:'prompt.result',result});return result;
   });
   on('session.append',async ($,e,n)=>{
     const result=await n(e);
     if(e.message.type==='user') {
       const message=result.message??e.message;
       await record($,{event:'session.append',uuid:e.uuid,origin:e.origin,door:e.door,agentId:e.agentId,content:message.content});
       if(e.door==='prompt' && e.agentId===undefined) {
         const text=message.content.filter(c=>c.type==='text').map(c=>c.text).join('\n');
         attestedText=e.origin.kind==='plugin' && e.origin.name.split('@')[0]==='swarm-native-input' && text.includes(authorized) && text.split('[SWARM_INPUT_').length===2 ? text : '';
       }
     }
     return result;
   });
   on('turn.start',async ($,e,n)=>{
     if(e.agentId===undefined && e.text===attestedText && attestedText!=='' && admitted && !consumed && !retired){consumed=true;turnBindings.set(e.turnId,'service-operation-1')}
     await record($,{event:'turn.start',...e,binding:turnBindings.get(e.turnId)});return n(e);
   });
   on('turn.step',async function* ($,e,n){
     await record($,{event:'turn.step',...e});
     const it=n(e);
     for(;;){const step=await it.next();if(step.done){await record($,{event:'step.result',turnId:e.turnId,agentId:e.agentId,result:step.value});return step.value}
       const c=step.value;
       if(c.kind==='tool'){toolBindings.set(c.id,{turnId:e.turnId,agentId:e.agentId});await record($,{event:'step.tool',turnId:e.turnId,agentId:e.agentId,chunk:c})}
       yield c;
     }
   });
   on('tool.call',async ($,e,n)=>{
     const native=toolBindings.get(e.tool_use_id);
     await record($,{event:'tool.call',...e,native,binding:native&&turnBindings.get(native.turnId)});
     if(e.tool.includes('recorder')) {
       if(!native || native.agentId!==e.agentId || retired)return {deny:'Missing exact native tool-call proof'};
       const stamped={...e,hook_stamp:JSON.stringify({toolUseID:e.tool_use_id,turnId:native.turnId,agentId:native.agentId,operation:turnBindings.get(native.turnId)})};
       return n(stamped);
     }
     return n(e);
   });
   on('turn.complete',async ($,e,n)=>{await record($,{event:'turn.complete',turnId:e.turnId,agentId:e.agentId,reason:e.reason});return n(e)});
   on('command.run',async ($,e,n)=>{if(['clear','resume','fork','branch','exit'].includes(e.command)){retired=true;await record($,{event:'generation.retired',command:e.command})}return n(e)});
 }
 """
 source=source.replace('__ENVELOPE__',json.dumps(envelope)).replace('__URL__',json.dumps('http://127.0.0.1:'+str(api.server_port)+'/lifecycle/event'))
 (observer/'hooks/observe.js').write_text(source)
 at=cmd.index('--plugin-dir');cmd[at:at]=['--plugin-dir',str(observer)]
 guard_root=pathlib.Path(sys.argv[2]).parent.resolve();check_guards(guard_root,[api.server_port])
 validation=subprocess.run(guarded([binary,'plugin','validate',str(observer),'--json'],guard_root,[api.server_port]),env=env,cwd=root,capture_output=True,text=True,timeout=8)
 assert validation.returncode==0,validation.stdout+validation.stderr
 master,slave=pty.openpty();fcntl.ioctl(slave,termios.TIOCSWINSZ,struct.pack('HHHH',45,160,0,0))
 proc=subprocess.Popen(guarded(cmd,guard_root,[api.server_port]),cwd=root,env=env,stdin=slave,stdout=slave,stderr=slave,start_new_session=True);os.close(slave)
 output=bytearray();deadline=time.monotonic()+55
 def drain():
  readable,_,_=select.select([master],[],[],.05)
  if readable:
   try:output.extend(os.read(master,65536))
   except OSError:pass
 def await_condition(check,seconds=10):
  end=min(deadline,time.monotonic()+seconds)
  while time.monotonic()<end and proc.poll() is None:
   drain()
   plain=re.sub(rb'\x1b\[[0-?]*[ -/]*[@-~]',b'',bytes(output)).decode(errors='replace')
   if not requests and ('New MCP server found' in plain or 'I am using this for local development' in plain):os.write(master,b'\r')
   if check():return
  raise AssertionError('condition timeout: '+re.sub(rb'\x1b\[[0-?]*[ -/]*[@-~]',b'',bytes(output))[-2000:].decode(errors='replace'))
 def completed():return [e for e in native_events if e['event']=='turn.complete' and 'agentId' not in e]
 try:
  await_condition(lambda:causal_gate.is_set() if queue_mode else len(completed())>=1,20)
  ready_until=time.monotonic()+4
  while True:
   try:worker=broker('/ready',{});break
   except AssertionError:
    if time.monotonic()>ready_until:raise
    drain()
  broker('/submit',{'worker':worker,'operation_id':'service-operation-1','text':envelope})
  if queue_mode:
   os.write(master,b'QUEUED_MANUAL_INPUT');time.sleep(.2);os.write(master,b'\r');time.sleep(.4);causal_release.set()
   await_condition(lambda:any(e['turnId']==x['turnId'] for e in completed() for x in native_events if x['event']=='turn.start' and x.get('binding')),15)
   os.write(master,b'\x15'+envelope.encode());time.sleep(.15);os.write(master,b'\r')
   await_condition(lambda:any(e['event']=='envelope.refused' and e['origin']['kind']=='composer' for e in native_events))
  else:
   os.write(master,envelope.encode());time.sleep(.15);os.write(master,b'\r')
   await_condition(lambda:len(completed())>=2)
  before=len(completed())
  broker('/submit',{'worker':worker,'operation_id':'duplicate-envelope-operation','text':envelope})
  await_condition(lambda:any(e['event']=='envelope.refused' and e['origin']['kind']=='plugin' for e in native_events))
  assert len(completed())==before,'duplicate machine envelope started a turn'
  phase='child'
  os.write(master,b'\x15Run the child provenance probe.');time.sleep(.15);os.write(master,b'\r')
  await_condition(lambda:any(e['event']=='tool.call' and e.get('agentId') for e in native_events),15)
  await_condition(lambda:len(completed())>before,15)
  os.write(master,b'\x15/clear');time.sleep(.15);os.write(master,b'\r')
  await_condition(lambda:any(e['event']=='generation.retired' for e in native_events))
  rows=[json.loads(line) for line in mcp_log.read_text().splitlines()] if mcp_log.exists() else []
  result={'version':descriptor['provider_version'],'requests':requests,'events':native_events,'mcp':rows,'envelope':envelope,'microphone_used':False,'real_account_used':False,'busy_queue_case':queue_mode}
  (artifact/'result.json').write_text(json.dumps(result,indent=2))
  types=observer/'.claude-plugin/types/claude-code/index.d.ts'
  if types.exists():(artifact/'installed-types.d.ts').write_text(types.read_text())
  (artifact/'terminal.log').write_bytes(output)
  bound=[e for e in native_events if e['event']=='turn.start' and e.get('binding')]
  assert len(bound)==1,bound
  joined=[e for e in native_events if e['event']=='tool.call' and e.get('binding')=='service-operation-1']
  assert len(joined)==1,joined
  children=[e for e in native_events if e['event']=='tool.call' and e.get('agentId')]
  assert children and all(not e.get('binding') for e in children),children
  assert any(e['event']=='envelope.refused' and e['origin']['kind']=='composer' for e in native_events),'identical composer envelope was not fenced'
  calls=[r['request'] for r in rows if r['request'].get('method')=='tools/call']
  print(json.dumps({'bound_native_turns':bound,'mcp_calls':calls,'tool_joins':[e for e in native_events if e['event']=='tool.call'],'artifact':str(artifact)},indent=2))
 finally:
  causal_release.set()
  (artifact/'partial-events.json').write_text(json.dumps({'events':native_events,'requests':requests,'envelope':envelope},indent=2))
  if mcp_log.exists():(artifact/'partial-mcp.jsonl').write_text(mcp_log.read_text())
  types=observer/'.claude-plugin/types/claude-code/index.d.ts'
  if types.exists():(artifact/'installed-types.d.ts').write_text(types.read_text())
  (artifact/'terminal.log').write_bytes(output)
  try:os.killpg(proc.pid,signal.SIGTERM)
  except ProcessLookupError:pass
  try:proc.wait(timeout=2)
  except subprocess.TimeoutExpired:
   os.killpg(proc.pid,signal.SIGKILL);proc.wait(timeout=2)
  os.close(master)
  if mcp_custody.exists():
   from custody import birth
   owned=json.loads(mcp_custody.read_text());pid=owned['pid']
   if owned['birth'] is not None and birth(pid)==owned['birth']:
    os.kill(pid,signal.SIGTERM);time.sleep(.1)
    if owned['birth'] is not None and birth(pid)==owned['birth']:os.kill(pid,signal.SIGKILL)
  api.shutdown();deny.shutdown()

if __name__=='__main__':probe()
