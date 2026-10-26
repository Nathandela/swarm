#!/usr/bin/env python3
from native_fixture_sandbox import guarded, check_guards
import fcntl,http.server,json,os,pathlib,pty,re,select,signal,socket,struct,subprocess,sys,tempfile,termios,threading,time

def probe():
 import http.client
 descriptor=json.loads(pathlib.Path(sys.argv[2]).read_text())
 root=pathlib.Path(descriptor['cwd'])
 class UnixHTTP(http.client.HTTPConnection):
  def connect(self):
   self.sock=socket.socket(socket.AF_UNIX);self.sock.settimeout(7);self.sock.connect(descriptor['socket_path'])
 def broker(path,body):
  peer=UnixHTTP('localhost',timeout=7)
  try:
   body['generation']=descriptor['generation'];peer.request('POST',path,json.dumps(body),{'Authorization':'Bearer '+descriptor['token']})
   reply=peer.getresponse();raw=reply.read();assert reply.status==200,(reply.status,raw.decode());return json.loads(raw)
  finally:peer.close()
 home=root/'home';cfg=home/'.gemini/antigravity-cli';cfg.mkdir(parents=True)
 (cfg/'settings.json').write_text(json.dumps({'modelProvider':'gemini','telemetry':{'enabled':False}}))
 (cfg/'cache').mkdir()
 (cfg/'cache/onboarding.json').write_text(json.dumps({'consumerOnboardingComplete':True,'enterpriseOnboardingComplete':False,'onboardingComplete':True}))
 (cfg/'jetski_state.pbtxt').write_text('post_onboarding { completed_steps: POST_ONBOARDING_STEP_TYPE_MANAGER_WELCOME completed_steps: POST_ONBOARDING_STEP_TYPE_USAGE_MODE completed_steps: POST_ONBOARDING_STEP_TYPE_AGENT_CONFIGURATION completed_steps: POST_ONBOARDING_STEP_TYPE_ADD_WORKSPACE }\ninstallation_uuid: "11111111-1111-4111-8111-111111111111"\n')
 requests=[]
 class API(http.server.BaseHTTPRequestHandler):
  def log_message(self,*args):pass
  def do_GET(self):self.send_response(404);self.end_headers()
  def do_POST(self):
   size=int(self.headers.get('Content-Length','0'));body=json.loads(self.rfile.read(size));requests.append({'path':self.path,'keys':list(body),'marker_present':'AGY_NATIVE_MESSAGE_MARKER' in json.dumps(body.get('contents',[])),'responses':[p.get('functionResponse') for c in body.get('contents',[]) for p in c.get('parts',[]) if p.get('functionResponse')],'tools':[x.get('name') for t in body.get('tools',[]) for x in t.get('functionDeclarations',[])]})
   for group in body.get('tools',[]):
    for tool in group.get('functionDeclarations',[]):
     if tool.get('name')=='run_command':(root/'run-command-schema.json').write_text(json.dumps(tool,indent=2))
   main=bool(body.get('tools'))
   parts=[{'text':'Synthetic native API response.'}]
   if main and sum(bool(r['tools']) for r in requests)==1:
    parts=[{'functionCall':{'name':'run_command','args':{'CommandLine':descriptor['bootstrap_command'],'Cwd':str(root),'toolSummary':'Register own native binding','toolAction':'execute','WaitMsBeforeAsync':1000}}}]
   if main and sum(bool(r['tools']) for r in requests)==2:
    parts=[{'functionCall':{'name':'run_command','args':{'CommandLine':'touch '+str(root/'must-not-run'),'Cwd':str(root),'toolSummary':'Permission separation test','toolAction':'Testing permission separation','WaitMsBeforeAsync':1000}}}]
   if main:
    supported={name for group in body.get('tools',[]) for tool in group.get('functionDeclarations',[]) if tool.get('name')=='run_command' for name in tool.get('parametersJsonSchema',{}).get('properties',{})}
    for part in parts:
     if 'functionCall' in part:part['functionCall']['args']={key:value for key,value in part['functionCall']['args'].items() if key in supported}
   reply={'candidates':[{'content':{'role':'model','parts':parts},'finishReason':'STOP','index':0}],'usageMetadata':{'promptTokenCount':1,'candidatesTokenCount':1,'totalTokenCount':2}}
   data=('data: '+json.dumps(reply)+'\n\n').encode() if 'streamGenerateContent' in self.path else json.dumps(reply).encode()
   self.send_response(200);self.send_header('Content-Type','text/event-stream' if 'streamGenerateContent' in self.path else 'application/json');self.end_headers();self.wfile.write(data);self.close_connection=True
 api=http.server.ThreadingHTTPServer(('127.0.0.1',0),API);api.daemon_threads=True;threading.Thread(target=api.serve_forever,daemon=True).start()
 env={'PATH':'/usr/bin:/bin:/usr/sbin:/sbin','HOME':str(home),'TMPDIR':str(root),'TERM':'xterm-256color','SWARM_SESSION_ID':descriptor['session_id'],'GEMINI_API_KEY':'synthetic-key','GOOGLE_GEMINI_BASE_URL':'http://127.0.0.1:'+str(api.server_port)}
 master,slave=pty.openpty();fcntl.ioctl(slave,termios.TIOCSWINSZ,struct.pack('HHHH',40,140,0,0))
 check_guards(root,[api.server_port])
 proc=subprocess.Popen(guarded([sys.argv[1],'--log-file',str(root/'native.log'),'--prompt-interactive','Register the managed native input, then run the synthetic permission probe.'],root,[api.server_port]),cwd=root,env=env,stdin=slave,stdout=slave,stderr=slave,start_new_session=True);os.close(slave)
 output=bytearray();deadline=time.monotonic()+35;trust=False
 try:
  while time.monotonic()<deadline and proc.poll() is None:
   ready,_,_=select.select([master],[],[],.1)
   if ready:
    try:output.extend(os.read(master,65536))
    except OSError:break
    (root/'terminal.log').write_bytes(output)
   plain=re.sub(rb'\x1b\[[0-?]*[ -/]*[@-~]',b'',bytes(output)).decode(errors='replace')
   if not trust and ('[Next]' in plain or ('trust' in plain.lower() and ('Yes' in plain or 'Trust' in plain))):
    os.write(master,b'\r');trust=True
   if 'Run this command?' in plain and not locals().get('bootstrap_approved'):
    os.write(master,b'\r');bootstrap_approved=True
   if sum(bool(r['tools']) for r in requests)>=2 and 'must-not-run' in plain and 'Run this command?' in plain and not locals().get('message_sent'):
    try:proof=broker('/ready',{})
    except AssertionError:
     print(json.dumps({'native_registration_results':[r['responses'] for r in requests if r['responses']]},indent=2));raise
    receipt=broker('/submit',{'proof':proof,'operation_id':'native-message','text':'AGY_NATIVE_MESSAGE_MARKER: trusted teammate context.\n第二行 <exact>; do not approve the pending command.'})
    message_sent=True;sent_at=time.monotonic()
   if locals().get('message_sent') and time.monotonic()-sent_at>3 and not locals().get('denied'):
    assert not (root/'must-not-run').exists(),'native message approved tool'
    choices=re.findall(r'([1-9])\.\s*No,\s*cancel',plain)
    if not choices:raise AssertionError('native refusal option was not identified')
    os.write(master,(choices[-1]+'\r').encode());denied=True;denied_at=time.monotonic()
   if locals().get('denied') and time.monotonic()-denied_at>3 and not locals().get('continued'):
    os.write(master,b'Continue the isolated test.');time.sleep(.2);os.write(master,b'\r');continued=True
   if any(r.get('marker_present') for r in requests):break
  result={'version':descriptor['version'],'message_submitted':locals().get('message_sent',False),'receipt':locals().get('receipt'),'tool_ran':(root/'must-not-run').exists(),'marker_in_model_context':any(r.get('marker_present') for r in requests),'bootstrap_approved_once':locals().get('bootstrap_approved',False)}
  (root/'result.json').write_text(json.dumps(result,indent=2));print(json.dumps(result,indent=2))
  if not result['message_submitted']:
   print(json.dumps({'native_tool_results':[r['responses'] for r in requests if r['responses']], 'tool_schema':json.loads((root/'run-command-schema.json').read_text()) if (root/'run-command-schema.json').exists() else None,'startup_tail':re.sub(r'https?://\S+|[A-Za-z0-9._%+-]+@[A-Za-z0-9.-]+','<redacted>',plain[-2400:])},indent=2))
  assert result['message_submitted'] and result['receipt']['state']=='submitted' and result['marker_in_model_context'] and not result['tool_ran']
 finally:
  try:os.killpg(proc.pid,signal.SIGTERM)
  except ProcessLookupError:pass
  try:proc.wait(timeout=2)
  except subprocess.TimeoutExpired:os.killpg(proc.pid,signal.SIGKILL);proc.wait()
  os.close(master);api.shutdown()
if __name__=='__main__':probe()
