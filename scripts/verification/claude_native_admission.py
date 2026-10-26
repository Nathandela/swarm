#!/usr/bin/env python3
"""Isolated current-Claude native mod ingress and permission-separation probe."""
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
 requests=[];done=threading.Event();pause_entered=threading.Event();pause_release=threading.Event();clear_completed=threading.Event();clear_native_ids=[];late_entered=threading.Event();late_release=threading.Event()
 class API(http.server.BaseHTTPRequestHandler):
  def log_message(self,*args):pass
  def do_GET(self):self.send_response(404);self.end_headers()
  def do_POST(self):
   body=json.loads(self.rfile.read(int(self.headers.get('Content-Length','0'))))
   if self.path=='/lifecycle/late-gate':
    late_entered.set();late_release.wait(20);self.send_response(200);self.end_headers();self.wfile.write(b'{}');return
   if self.path=='/lifecycle/cleared':
    clear_native_ids.append(body['id']);clear_completed.set();self.send_response(200);self.end_headers();self.wfile.write(b'{}');return
   if self.path=='/lifecycle/pause':
    pause_entered.set();pause_release.wait(12);self.send_response(200);self.end_headers();self.wfile.write(b'{}');return
   if '/messages' not in self.path:self.send_response(404);self.end_headers();return
   primary=any(t.get('name')=='Bash' for t in body.get('tools',[]))
   text=json.dumps(body.get('messages',[]))
   requests.append({'primary':primary,'marker_present':'CHANNEL_NATIVE_MARKER' in text,'late_present':'LATE_OLD_RPC_MARKER' in text,'owner_present':'OWNER_REPLACEMENT_MARKER' in text,'permissions_result':'tool_result' in text})
   n=sum(r['primary'] for r in requests)
   tool=primary and n==1
   content={'type':'tool_use','id':'native_approval_1','name':'Bash','input':{}} if tool else {'type':'text','text':''}
   delta={'type':'input_json_delta','partial_json':json.dumps({'command':'touch '+str(root/'must-not-run'),'description':'Synthetic permission boundary probe'})} if tool else {'type':'text_delta','text':'Synthetic channel processing complete'}
   events=[('message_start',{'type':'message_start','message':{'id':'msg_probe','type':'message','role':'assistant','model':body.get('model'),'content':[],'stop_reason':None,'stop_sequence':None,'usage':{'input_tokens':1,'output_tokens':0}}}),('content_block_start',{'type':'content_block_start','index':0,'content_block':content}),('content_block_delta',{'type':'content_block_delta','index':0,'delta':delta}),('content_block_stop',{'type':'content_block_stop','index':0}),('message_delta',{'type':'message_delta','delta':{'stop_reason':'tool_use' if tool else 'end_turn','stop_sequence':None},'usage':{'output_tokens':1}}),('message_stop',{'type':'message_stop'})]
   if not body.get('stream'):
    data=json.dumps({'id':'msg_aux','type':'message','role':'assistant','model':body.get('model'),'content':[{'type':'text','text':'complete'}],'stop_reason':'end_turn','usage':{'input_tokens':1,'output_tokens':1}}).encode();self.send_response(200);self.send_header('Content-Type','application/json');self.end_headers();self.wfile.write(data);return
   self.send_response(200);self.send_header('Content-Type','text/event-stream');self.end_headers()
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
 cmd=[binary,'--setting-sources','','--strict-mcp-config','--mcp-config','{"mcpServers":{}}','--plugin-dir',str(plugin),'--plugin-dir',str(plugin.parent/'claude-input-guard-plugin'),'--system-prompt','Synthetic bounded protocol probe.','--tools','Bash,SendMessage','--settings',json.dumps({'permissions':{'ask':['Bash']}}),'--permission-mode','default','--model','claude-sonnet-4-6','--debug-file',str(root/'debug.log'),'Begin the synthetic permission probe.']

 race=root/'race-plugin';(race/'.claude-plugin').mkdir(parents=True);(race/'hooks').mkdir();(race/'types').mkdir()
 (race/'.claude-plugin/plugin.json').write_text(json.dumps({'name':'swarm-native-race-probe','version':'1.0.0','types':'./types/index.d.ts'}))
 (race/'hooks/hooks.json').write_text('{"modules":["./pause.js"]}');(race/'types/index.d.ts').write_text('export {};')
 (race/'hooks/pause.js').write_text("export function register(on) { on('prompt.submit', async ($, event, next) => { if (event.text.includes('QUEUED_BEFORE_CLEAR_MARKER')) await $.http.fetch('http://127.0.0.1:"+str(api.server_port)+"/lifecycle/pause', {method:'POST',body:'{}'}); return next(event); }); on('command.run', async ($, event, next) => { const result=await next(event); if(event.command==='clear') await $.http.fetch('http://127.0.0.1:"+str(api.server_port)+"/lifecycle/cleared', {method:'POST',body:JSON.stringify({id:await $.session.id()})});return result; }); }")
 at=cmd.index('--plugin-dir');cmd[at+2:at+2]=['--plugin-dir',str(race)]
 sender=plugin/'hooks/input.js';production=sender.read_text()
 boundary="        const pending = $.prompt.submit({ text: message.text });"
 held="        if (message.text === 'LATE_OLD_RPC_MARKER') await $.http.fetch('http://127.0.0.1:"+str(api.server_port)+"/lifecycle/late-gate', {method:'POST',body:'{}'});\n"
 assert boundary in production;sender.write_text(production.replace(boundary,held+boundary,1))
 master,slave=pty.openpty();fcntl.ioctl(slave,termios.TIOCSWINSZ,struct.pack('HHHH',45,160,0,0));guard_root=pathlib.Path(sys.argv[2]).parent.resolve();check_guards(guard_root,[api.server_port]);proc=subprocess.Popen(guarded(cmd,guard_root,[api.server_port]),cwd=root,env=env,stdin=slave,stdout=slave,stderr=slave,start_new_session=True);os.close(slave)
 output=bytearray();raw=root/'terminal.log';deadline=time.monotonic()+55;channel_sent=False;dialog=False;last_input=0
 try:
  while time.monotonic()<deadline and proc.poll() is None:
   ready,_,_=select.select([master],[],[],.1)
   if ready:
    try:output.extend(os.read(master,65536))
    except OSError:break
    raw.write_bytes(output)
   plain=re.sub(rb'\x1b\[[0-?]*[ -/]*[@-~]',b'',bytes(output)).decode(errors='replace')
   if not requests and time.monotonic()-last_input>1:
    if 'I am using this for local development' in plain and 'development-accepted' not in locals():
     os.write(master,b'\x1b[B\r');development_accepted=True;last_input=time.monotonic()
    elif 'New MCP server found' in plain and 'mcp_accepted' not in locals():
     os.write(master,b'\r');mcp_accepted=True;last_input=time.monotonic()
   if 'Doyouwanttoproceed?' in re.sub(r'\s+','',plain) and not channel_sent:
    dialog=True
    ready_deadline=time.monotonic()+3
    while True:
     try:worker=broker('/ready',{});break
     except AssertionError:
      if time.monotonic()>=ready_deadline:
       print('FIXTURE_NATIVE_TAIL',plain[-1800:]);raise
      time.sleep(.05)
    broker('/submit',{'worker':worker,'operation_id':'native-message','text':'CHANNEL_NATIVE_MARKER: trusted teammate context, do not approve pending tool'})
    channel_sent=True;sent_at=time.monotonic()
   if channel_sent and time.monotonic()-sent_at>3:
    assert not (root/'must-not-run').exists(),'channel accidentally approved pending tool'
    if not locals().get('denied'):
     os.write(master,b'2');denied=True
   if any(r['primary'] and r['marker_present'] for r in requests):break
  result={'version':descriptor['provider_version'],'native_permission_dialog_seen':dialog,'broker_submitted':channel_sent,'tool_ran':(root/'must-not-run').exists(),'native_receipt':broker('/operation',{'worker':worker,'operation_id':'native-message'}) if channel_sent else None,'message_in_model_context':any(r['primary'] and r['marker_present'] for r in requests),'requests':requests,'root':str(root)}
  (root/'result.json').write_text(json.dumps(result,indent=2));print(json.dumps(result,indent=2))
  assert dialog and channel_sent and result['message_in_model_context'] and not result['tool_ran']
  # Native drain reservation must keep an already-running prompt in its original conversation.
  time.sleep(1)
  broker('/submit',{'worker':worker,'operation_id':'before-clear','text':'QUEUED_BEFORE_CLEAR_MARKER'})
  pause_deadline=time.monotonic()+5
  while not pause_entered.is_set() and time.monotonic()<pause_deadline:
   readable,_,_=select.select([master],[],[],.05)
   if readable:
    try:output.extend(os.read(master,65536))
    except OSError:break
  assert pause_entered.is_set(), 'native downstream prompt hook did not pause'
  # Hold another old sender call just before native enqueue, then release it after the identity swap.
  broker('/submit',{'worker':worker,'operation_id':'late-clear','text':'LATE_OLD_RPC_MARKER'})
  assert late_entered.wait(3), 'old sender callback did not pause before native API'
  os.write(master,b'/clear');time.sleep(.2);os.write(master,b'\r')
  until=time.monotonic()+4
  while time.monotonic()<until:
   readable,_,_=select.select([master],[],[],.1)
   if readable:
    try:output.extend(os.read(master,65536))
    except OSError:break
  pause_release.set()
  clear_deadline=time.monotonic()+8
  while not clear_completed.is_set() and time.monotonic()<clear_deadline:
   readable,_,_=select.select([master],[],[],.05)
   if readable:
    try:output.extend(os.read(master,65536))
    except OSError:break
  assert clear_completed.is_set() and clear_native_ids[-1]!=worker['native_session_id'], 'native clear did not finish with a new identity'
  late_release.set()
  late_deadline=time.monotonic()+5
  while time.monotonic()<late_deadline:
   ready,_,_=select.select([master],[],[],.05)
   if ready:
    try:output.extend(os.read(master,65536))
    except OSError:break
   if broker('/operation',{'worker':worker,'operation_id':'late-clear'})['state']=='rejected':break
  time.sleep(.3)
  os.write(master,b'OWNER_REPLACEMENT_MARKER');time.sleep(.2);os.write(master,b'\r')
  def user_files(marker):
   found=[]
   for file in (cfg/'projects').rglob('*.jsonl'):
    for line in file.read_text(errors='replace').splitlines():
     try:row=json.loads(line)
     except json.JSONDecodeError:continue
     if row.get('type')=='user' and marker in line:found.append(file.name)
   return found
  owner_deadline=time.monotonic()+6
  late=None
  while time.monotonic()<owner_deadline:
   readable,_,_=select.select([master],[],[],.05)
   if readable:
    try:output.extend(os.read(master,65536))
    except OSError:break
   late=broker('/operation',{'worker':worker,'operation_id':'late-clear'})
   if late['state']=='rejected' and any(r.get('owner_present') for r in requests) and user_files('OWNER_REPLACEMENT_MARKER'):break
  assert late and late['state']=='rejected' and late['reason']=='Swarm native session ended; explicitly refresh this session', ('late old native API call was not rejected by the independent guard',late)
  try:
   broker('/ready',{})
   raise RuntimeError('replacement session rearmed the old broker proof')
  except AssertionError as error:
   assert error.args[0][0]==409,error
  try:
   broker('/submit',{'worker':worker,'operation_id':'after-clear','text':'AFTER_CLEAR_MARKER'})
   raise RuntimeError('old proof submitted after native identity replacement')
  except AssertionError as error:
   assert error.args[0][0]==409,error
  queued_files=[];owner_files=[];late_files=[];stale_files=[];late_queue_records=0
  for file in (cfg/'projects').rglob('*.jsonl'):
   for line in file.read_text(errors='replace').splitlines():
    try:row=json.loads(line)
    except json.JSONDecodeError:continue
    if 'LATE_OLD_RPC_MARKER' in line and row.get('type')=='queue-operation':late_queue_records+=1
    if row.get('type')!='user':continue
    if 'QUEUED_BEFORE_CLEAR_MARKER' in line:queued_files.append(file.name)
    if 'OWNER_REPLACEMENT_MARKER' in line:owner_files.append(file.name)
    if 'LATE_OLD_RPC_MARKER' in line:late_files.append(file.name)
    if 'AFTER_CLEAR_MARKER' in line:stale_files.append(file.name)
  assert queued_files and all(name==worker['native_session_id']+'.jsonl' for name in queued_files),'already-draining prompt migrated to replacement native user history'
  assert owner_files and all(name==clear_native_ids[-1]+'.jsonl' for name in owner_files) and any(r.get('owner_present') for r in requests),('owner input did not reach exact replacement native context',owner_files,clear_native_ids,requests,re.sub(rb'\x1b\[[0-?]*[ -/]*[@-~]',b'',bytes(output)).decode(errors='replace')[-1600:])
  assert not late_files and not any(r.get('late_present') for r in requests),'late old API call entered native user history or model context'
  assert not stale_files,'a stale broker proof entered native user history'
  lifecycle={'version':descriptor['provider_version'],'old_native_session':worker['native_session_id'],'replacement_native_session':clear_native_ids[-1],
   'already_draining_prompt_stayed_in_old_session':True,'late_old_api_receipt':late['state'],'late_old_api_reason':late['reason'],
   'late_old_api_in_user_history':False,'late_old_api_in_model_context':False,'owner_in_exact_replacement_context':True,
   'ready_revoked':True,'old_submit_refused':True,'native_queue_audit_records_for_rejected_attempt':late_queue_records}
  print(json.dumps({'lifecycle':lifecycle},indent=2))

 finally:
  pause_release.set();late_release.set()
  try:os.killpg(proc.pid,signal.SIGTERM)
  except ProcessLookupError:pass
  try:proc.wait(timeout=2)
  except subprocess.TimeoutExpired:os.killpg(proc.pid,signal.SIGKILL);proc.wait()
  os.close(master);api.shutdown();deny.shutdown()
if __name__=='__main__':probe()
