#!/usr/bin/env python3
"""Isolated current-Claude native mod ingress and permission-separation probe."""
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
 requests=[];done=threading.Event()
 class API(http.server.BaseHTTPRequestHandler):
  def log_message(self,*args):pass
  def do_GET(self):self.send_response(404);self.end_headers()
  def do_POST(self):
   body=json.loads(self.rfile.read(int(self.headers.get('Content-Length','0'))))
   if '/messages' not in self.path:self.send_response(404);self.end_headers();return
   primary=any(t.get('name')=='Bash' for t in body.get('tools',[]))
   text=json.dumps(body.get('messages',[]))
   requests.append({'primary':primary,'marker_present':'CHANNEL_NATIVE_MARKER' in text,'permissions_result':'tool_result' in text})
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
 config={'hasCompletedOnboarding':True,'theme':'dark','lastOnboardingVersion':'2.1.296','oauthAccount':{'accountUuid':'synthetic-account','emailAddress':'synthetic@example.invalid','organizationUuid':'synthetic-org'},'projects':{str(root):{'hasTrustDialogAccepted':True}}}
 (cfg/'.claude.json').write_text(json.dumps(config));(cfg/'.credentials.json').write_text(json.dumps({'claudeAiOauth':{'accessToken':'synthetic-token','refreshToken':'synthetic-refresh','expiresAt':int(time.time()*1000)+3600000,'scopes':['user:inference','user:profile'],'subscriptionType':'max','rateLimitTier':'default_claude_max_5x'}}))
 env={'PATH':'/usr/bin:/bin:/usr/sbin:/sbin','HOME':str(home),'CLAUDE_CONFIG_DIR':str(cfg),'TMPDIR':str(home/'tmp'),'TERM':'xterm-256color','ANTHROPIC_BASE_URL':'http://127.0.0.1:'+str(api.server_port),'CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC':'1','DISABLE_AUTOUPDATER':'1','CLAUDE_CODE_MAX_RETRIES':'0','HTTP_PROXY':'http://127.0.0.1:'+str(deny.server_port),'HTTPS_PROXY':'http://127.0.0.1:'+str(deny.server_port),'ALL_PROXY':'http://127.0.0.1:'+str(deny.server_port),'NO_PROXY':'127.0.0.1,localhost'}
 mcp={'mcpServers':{'swarm-native':{'command':sys.executable,'args':[str(pathlib.Path(__file__).resolve()),'channel',str(root)]}}}
 cmd=[binary,'--setting-sources','','--strict-mcp-config','--mcp-config','{"mcpServers":{}}','--plugin-dir',str(plugin),'--system-prompt','Synthetic bounded protocol probe.','--tools','Bash,SendMessage','--settings',json.dumps({'permissions':{'ask':['Bash']}}),'--permission-mode','default','--model','claude-sonnet-4-6','--debug-file',str(root/'debug.log'),'Begin the synthetic permission probe.']

 master,slave=pty.openpty();fcntl.ioctl(slave,termios.TIOCSWINSZ,struct.pack('HHHH',45,160,0,0));proc=subprocess.Popen(cmd,cwd=root,env=env,stdin=slave,stdout=slave,stderr=slave,start_new_session=True);os.close(slave)
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
      if time.monotonic()>=ready_deadline:raise
      time.sleep(.05)
    broker('/submit',{'worker':worker,'operation_id':'native-message','text':'CHANNEL_NATIVE_MARKER: trusted teammate context, do not approve pending tool'})
    channel_sent=True;sent_at=time.monotonic()
   if channel_sent and time.monotonic()-sent_at>3:
    assert not (root/'must-not-run').exists(),'channel accidentally approved pending tool'
    if not locals().get('denied'):
     os.write(master,b'2');denied=True
   if any(r['primary'] and r['marker_present'] for r in requests):break
  result={'version':'2.1.296','native_permission_dialog_seen':dialog,'broker_submitted':channel_sent,'tool_ran':(root/'must-not-run').exists(),'native_receipt':broker('/operation',{'worker':worker,'operation_id':'native-message'}) if channel_sent else None,'message_in_model_context':any(r['primary'] and r['marker_present'] for r in requests),'requests':requests,'root':str(root)}
  (root/'result.json').write_text(json.dumps(result,indent=2));print(json.dumps(result,indent=2))
  assert dialog and channel_sent and result['message_in_model_context'] and not result['tool_ran']
 finally:
  try:os.killpg(proc.pid,signal.SIGTERM)
  except ProcessLookupError:pass
  try:proc.wait(timeout=2)
  except subprocess.TimeoutExpired:os.killpg(proc.pid,signal.SIGKILL);proc.wait()
  os.close(master);api.shutdown();deny.shutdown()
if __name__=='__main__':probe()
