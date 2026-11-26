import argparse
import errno
import hashlib
import http.client
import http.server
import json
import os
from pathlib import Path
import re
import socket
import subprocess
import sys
import threading
import time

assert __debug__, "assertions enforce the qualification gates"
parser = argparse.ArgumentParser(description='Isolated installed OpenCode and Rust Swarm probe')
parser.add_argument('root', type=Path)
parser.add_argument('swarm', type=Path)
parser.add_argument('--duration', type=int)
parser.add_argument('--interval', type=int, choices=range(1, 61), default=10, metavar='1..60')
parser.add_argument('--restart-every', type=int, default=20)
parser.add_argument('--source')
parser.add_argument('--native', type=Path)
parser.add_argument('--check-arguments', action='store_true')
args = parser.parse_args()
if args.duration is not None and (
    not 1 <= args.duration <= 259200 or args.restart_every < 1
    or not re.fullmatch('[a-f0-9]{40}', args.source or '')
):
    parser.error('soak requires 1..259200 seconds, a positive restart cadence and the full source SHA')
root, swarm = args.root, args.swarm
assert root.is_absolute() and len(os.fsencode(root)) <= 35, 'short absolute scratch path required'
assert swarm.is_absolute() and os.access(swarm, os.X_OK), 'absolute executable required'
if args.check_arguments:
    sys.exit(0)
assert root.is_dir() and root.stat().st_uid == os.getuid() and root.stat().st_mode & 0o077 == 0
assert not any(root.iterdir()), 'fresh private scratch required'
if sys.platform.startswith('linux'):
    assert [name for _,name in socket.if_nameindex()] == ['lo'], socket.if_nameindex()
for target in ['192.0.2.1', '2001:db8::1']:
    family = socket.AF_INET6 if ':' in target else socket.AF_INET
    with socket.socket(family, socket.SOCK_STREAM) as check:
        check.settimeout(0.2)
        try:
            check.connect((target, 80))
        except OSError as err:
            assert err.errno in (errno.EPERM, errno.EACCES, errno.ENETUNREACH, errno.EHOSTUNREACH), (target, err)
        else:
            raise AssertionError('guard allowed external network')
for name in ['home','state','project','xdg-state/opencode','config/opencode','data','cache']:
    (root / name).mkdir(parents=True, mode=0o700)
env = dict(PATH=f'{root.parent}/bin:/usr/local/bin:/opt/homebrew/bin:/usr/bin:/bin', HOME=str(root/'home'), TMPDIR=str(root),
    TERM='xterm-256color', SWARM_DAEMON_STATE=str(root/'state'), SWARM_DAEMON_SOCK=str(root/'daemon.sock'),
    SWARM_DAEMON_LOCK=str(root/'daemon.lock'), OPENCODE_DISABLE_MODELS_FETCH='true',
    OPENCODE_DISABLE_AUTOUPDATE='true', OPENCODE_DISABLE_DEFAULT_PLUGINS='true',
    OPENCODE_DISABLE_PROJECT_CONFIG='true', XDG_CONFIG_HOME=str(root/'config'), XDG_DATA_HOME=str(root/'data'),
    XDG_STATE_HOME=str(root/'xdg-state'), XDG_CACHE_HOME=str(root/'cache'))
request_log=[]
class Fixture(http.server.BaseHTTPRequestHandler):
    def log_message(self, *_):
        pass
    def do_POST(self):
        assert self.path == '/v1/chat/completions'
        length = int(self.headers['Content-Length'])
        assert 0 < length <= 16 * 1024 * 1024, 'model input ceiling exceeded'
        incoming = json.loads(self.rfile.read(length))
        assert incoming['model'] == 'fixture'
        messages = incoming['messages']
        latest = max((i for i,m in enumerate(messages) if m.get('role')=='user'),default=-1)
        after = messages[latest+1:]
        prompt = str(messages[latest].get('content')) if latest>=0 else ''
        names = [tool['function']['name'] for tool in incoming.get('tools',[]) if tool.get('type')=='function']
        actions = []
        if names and 'RUST_SEMANTIC' in prompt and any(m['role']=='tool' for m in after):
            time.sleep(0.35)
        if names and 'RUST_SEMANTIC' in prompt and not any(m['role']=='tool' for m in after):
            if 'question' in names:
                actions.append(('question', {'questions':[{
                  'question':'Pick a color','header':'Color',
                  'options':[{'label':'blue','description':'Blue'}, {'label':'red','description':'Red'}]
                }]}))
            if 'bash' in names:
                actions.append(('bash', {'command':"printf 'fixture permission answered\\n'", 'description':'Print fixture response'}))
        assert len(request_log) < (20000 if args.duration else 16), 'fixture call ceiling exceeded'
        marker = re.search(r'RUST_SOAK_[0-9]{6}', prompt)
        request_log.append({'names':len(names),'actions':[name for name,_ in actions],
                            'marker': marker.group() if marker else None,
                            'primary': bool(names) and not any(m['role']=='tool' for m in after)})
        now=int(time.time())
        if incoming.get('stream'):
            self.send_response(200)
            self.send_header('Content-Type','text/event-stream')
            self.end_headers()
            def event(delta, finish=None):
                val=dict(id=f'fixture-{len(request_log)}',object='chat.completion.chunk',created=now,model='fixture',
                    choices=[dict(index=0,delta=delta,finish_reason=finish)])
                self.wfile.write(('data: '+json.dumps(val)+'\n\n').encode()); self.wfile.flush()
            event(dict(role='assistant',content=''))
            if actions:
                for index,(name,params) in enumerate(actions):
                    event(dict(tool_calls=[dict(index=index,id=f'call-fixture-{index}',type='function',
                        function=dict(name=name,arguments=json.dumps(params)))]))
                event({},'stop')
            else:
                event(dict(content='Deterministic OpenCode fixture response.'))
                event({},'stop')
            self.wfile.write(b'data: [DONE]\n\n')
            self.close_connection=True
        else:
            val=dict(id='fixture-aux',object='chat.completion',created=now,model='fixture',choices=[
              dict(index=0,message=dict(role='assistant',content='OpenCode fixture'),finish_reason='stop')])
            data=json.dumps(val).encode()
            self.send_response(200)
            self.send_header('Content-Type','application/json')
            self.send_header('Content-Length',str(len(data))); self.end_headers()
            self.wfile.write(data)

class LoopbackServer(http.server.ThreadingHTTPServer):
    def server_bind(self):
        import socketserver
        socketserver.TCPServer.server_bind(self)
        self.server_name, self.server_port = self.server_address
provider=LoopbackServer(('127.0.0.1',0),Fixture)
threading.Thread(target=provider.serve_forever,daemon=True).start()
port=provider.server_address[1]
with socket.create_connection(('127.0.0.1',port),timeout=1):
    pass
print('loopback guard verified',flush=True)
(root/'config/opencode/opencode.json').write_text(json.dumps({
    '$schema':'https://opencode.ai/config.json','autoupdate':False,'enabled_providers':['fixture'],'model':'fixture/fixture','small_model':'fixture/fixture',
    'default_agent':'build','compaction':{'auto':False},'agent':{'build':{'model':'fixture/fixture'}},
    'permission':{'*':'deny','bash':'ask','question':'allow'},
    'provider':{'fixture':{'npm':'@ai-sdk/openai-compatible','name':'Local fixture',
    'options':{'baseURL':f'http://127.0.0.1:{port}/v1','apiKey':'local-fixture'},
    'models':{'fixture':{'name':'Local Fixture','tool_call':True,'limit':{'context':64000,'output':8192}}}}}
}))
(root/'xdg-state/opencode/model.json').write_text(json.dumps({'recent':[{'providerID':'fixture','modelID':'fixture'}], 'favorite':[], 'variant':{}}))
from launch import owner_request, history_page, resume_request

def owner(request=None):
    return owner_request(env, request)

def wait(check, timeout=25):
    limit=time.monotonic()+timeout
    while time.monotonic()<limit:
        try:
            val=check()
        except (OSError, subprocess.CalledProcessError):
            val=None
        if val:
            return val
        time.sleep(0.15)
    raise AssertionError('condition timed out')
def cli(*args, check=True):
    owner()
    result=subprocess.run([swarm,*args], env=env,capture_output=True,text=True,timeout=60)
    if check:
        assert result.returncode == 0, (args,result.stdout,result.stderr)
    return result
def output(*args):
    return json.loads(cli(*args).stdout)
def start():
    with (root/'daemon.log').open('ab') as log:
        daemon=subprocess.Popen([swarm,'daemon'],env=env,stdout=log,stderr=log)
    wait(owner,5)
    return daemon
def stop(daemon):
    daemon.terminate()
    try:
        daemon.wait(5)
    except subprocess.TimeoutExpired:
        daemon.kill()
        daemon.wait(5)
version=subprocess.check_output(['opencode','--version'], env=env, text=True, timeout=15).strip()
assert version=='1.19.3',version
print('installed native version verified', flush=True)
if args.duration is not None:
    from soak import run
    run(args, env, cli, output, wait, start, stop, provider, request_log)
    sys.exit(0)

daemon=start()
try:
    assert cli('spawn','--cli','opencode','--dir',str(root/'project'),'--model','fixture/fixture',
               '--prompt','RUST_INITIAL. Reply fixture only.').returncode==0
    print('initial prompt admitted',flush=True)
    row=output('ls','--json')[0]['id']
    def history(identifier=row, limit=15, before=''):
        return history_page(env, identifier, limit, before)
    def ready():
        return next((item for item in output('ls','--json') if item['id']==row and item['status']['Turn']=='idle'),None)
    wait(ready)
    before=history()
    assert any(m['info']['role']=='user' for m in before['history']), before
    assert resume_request(env, row, root/'project')['op'] == 'error'
    operation=f'semantic:{int(time.time()*1000)}:00112233445566778899aabbccddeeff'
    message=output('message',row,'--operation-id',operation,'--text','RUST_SEMANTIC. Reply fixture only.')
    assert message['state'] in ('native_accepted','completed'),message
    print('semantic input admitted',flush=True)
    cards={}
    stop(daemon); daemon=start()
    for _ in range(2):
        requests=wait(lambda:output('requests',row).get('requests'))
        request=requests[0]
        ident=request['request_id']; target=request['native_session_id']
        query=['--request-id',ident,'--native-session-id',target]
        card=output('requests',row,*query)['request']
        assert cli('decide',row,*query,'--request-hash','0'*64,'--reply','reject',check=False).returncode!=0
        if card['kind']=='question':
            binding_path=root/'state'/'rust-v1'/row.split('/')[-1]/'opencode-binding.json'
            binding=json.loads(binding_path.read_text())
            def digest(body, method="POST", route="/dispatchDigest"):
                class PrivateBridge(http.client.HTTPConnection):
                    def connect(self):
                        self.sock=socket.socket(socket.AF_UNIX)
                        self.sock.settimeout(5)
                        self.sock.connect(binding['socketPath'])
                with_bridge=PrivateBridge('localhost')
                try:
                    with_bridge.request(method,route,json.dumps(body) if method=='POST' else None,{
                        'Authorization':f"Bearer {binding['token']}",'Content-Type':'application/json'})
                    reply=with_bridge.getresponse()
                    return reply.status,json.loads(reply.read())
                finally:
                    with_bridge.close()
            scope={key:binding[key] for key in ('version','sessionInstance','backendGeneration','rootID')}
            request_body=dict(scope,kind='question_reply',targetID=target,requestID=ident,
                operationID='verify-content-digest',answers=[['blue']],expectedRequest={'2':1e-8,'10':1e20},
                requestExpiresAtMS=1)
            status, exact=digest(request_body)
            assert status==200 and len(exact['requestHash'])==64
            assert digest(dict(request_body,answers=[['red']]))[1]['requestHash']!=exact['requestHash']
            assert digest(dict(request_body,expectedRequest={'10':1e20,'2':1e-8}))[1]['requestHash']==exact['requestHash']
            assert digest(dict(request_body,extra='invalid'))[1]['requestHash']!=exact['requestHash']
            assert digest(dict(request_body,sessionInstance='bad'))[0]==422
            assert digest(None)[0]==422
            assert digest(None,"GET","/receipt?operationID=verify-content-digest")==(200,None)
            assert output('requests',row,*query)['request']['request_hash']==card['request_hash']
            invalid=output('decide',row,*query,'--request-hash',card['request_hash'],
                           '--reply','answer','--answers','[["blue"],["red"]]')
            assert invalid['state']=='rejected' and invalid['native_effect']=='none',invalid
            assert output('decide',row,'--status','--operation-id',invalid['operation_id'])==invalid
            assert output('requests',row,*query)['request']['request_hash']==card['request_hash']
        choice=['--reply','once'] if card['kind']=='permission' else ['--reply','answer','--answers','[["blue"]]']
        receipt=output('decide',row,*query,'--request-hash',card['request_hash'],*choice)
        assert receipt['state']=='admitted',receipt
        assert output('decide',row,'--status','--operation-id',receipt['operation_id'])['state']=='admitted'
        cards[card['kind']]=receipt['state']
        print(f'confirmed {card["kind"]} decision',flush=True)
    def completed():
        current=output('message',row,'--status','--operation-id',operation)
        return current if current['state']=='completed' else None
    persisted=root/'state'/'rust-v1'/row.split('/')[-1]/'opencode-receipts'/(hashlib.sha256(operation.encode()).hexdigest()+'.json')
    def checkpointed():
        receipt=json.loads(persisted.read_text())['receipt']
        return receipt if receipt['state']=='completed' else None
    durable=wait(checkpointed)
    assert durable['outcome_source']=='reconciled_message',durable
    done=wait(completed)
    assert done==durable
    assert output('message',row,'--operation-id',operation,'--text','RUST_SEMANTIC. Reply fixture only.')==done
    prior=history()
    first=history(row, 1)
    assert first['next_cursor']
    earlier=history(row, 1, first['next_cursor'])
    assert earlier['history'][0]['info']['id']!=first['history'][0]['info']['id']
    cli('send',row,'--text','RUST_DRAFT_DO_NOT_SUBMIT','--no-submit')
    time.sleep(0.3)
    draft_op=f'semantic:{int(time.time()*1000)}:102132435465768798a9bacbdcedfe0f'
    refused=cli('message',row,'--operation-id',draft_op,'--text','RUST_DRAFT_GUARDED',check=False)
    assert refused.returncode!=0,(refused.stdout,refused.stderr)
    assert output('message',row,'--status','--operation-id',draft_op)['state']=='rejected'
    assert [m['info']['id'] for m in history()['history']]==[m['info']['id'] for m in prior['history']]
    print('existing native draft fenced semantic dispatch',flush=True)
    cli('kill',row)
    wait(lambda:all(item['id']!=row or item['status']['Process']=='exited' for item in output('ls','--json')))
    def check_initial_receipt(identifier,state):
        session_dir=root/'state'/'rust-v1'/identifier.split('/')[-1]
        receipt_file=session_dir/'opencode-initial.json'
        original=receipt_file.read_text()
        proof=json.loads(original)
        planned=json.loads((session_dir/'session.json').read_text())['opencode_initial']
        assert proof['state']==state and proof['intent']==planned
        assert proof['bridge_receipt']['intentHash']==planned['intentHash']
        receipt_file.write_text(json.dumps({'token':proof['token'],'state':state}))
        try:
            refused=resume_request(env, identifier, root/'project')
            assert refused['op']=='error' and 'initial input remains unverified' in refused['error']
        finally:
            receipt_file.write_text(original)
        return proof['bridge_receipt']
    admission=check_initial_receipt(row,'native_accepted')
    assert any(row['info']['id']==admission['messageID'] and row['info']['role']=='user'
               for row in prior['history'])
    for bad_id in ('must-use-reliable-protocol', 42):
        assert resume_request(env, row, root/'project', operation_id=bad_id)['op'] == 'error'
    resumed=resume_request(env, row, root/'project')['session']
    assert resumed['resumed_from'] == row
    after=history(resumed['id'])
    assert prior['native_session_id']==after['native_session_id']
    assert prior['native_created_at_ms']==after['native_created_at_ms']
    assert prior['session_instance']!=after['session_instance']
    assert [m['info']['id'] for m in prior['history']]==[m['info']['id'] for m in after['history']]
    assert output('message',row,'--status','--operation-id',operation)==done
    previous_ids={item['id'] for item in output('ls','--json')}
    rejected=cli('spawn','--cli','opencode','--dir',str(root/'project'),'--model','fixture/fixture',
                 '--prompt','/fixture-no-native-effect',check=False)
    assert rejected.returncode!=0 and 'initial_command_unsupported' in rejected.stderr,rejected.stderr
    rejected_id=next(item['id'] for item in output('ls','--json') if item['id'] not in previous_ids)
    cli('kill',rejected_id)
    wait(lambda:any(item['id']==rejected_id and item['status']['Process']=='exited' for item in output('ls','--json')))
    witness=check_initial_receipt(rejected_id,'rejected')
    assert witness['nativeEffect']=='none' and 'messageID' not in witness
    rejected_resume=resume_request(env, rejected_id, root/'project')['session']
    assert history(rejected_resume['id'])['history']==[]
    print('admission and rejection resume require their saved initial bridge proof',flush=True)
    print(json.dumps({'version':'1.19.3','guard':'IPv4 and IPv6 external egress rejected; loopback only',
       'local_provider_calls':len(request_log),'cards':cards,'semantic_state':done['state'],
       'history_pagination':True,'daemon_restart':True,'confirmed_resume':True,'duplicate_exact':True,'draft_fence':True,'canonical_decision_digest':True,'initial_witness':True,'durable_terminal_event':True}))
finally:
    try:
        for item in output('ls','--json'):
            if item['status']['Process']=='running':
                cli('kill',item['id'],check=False)
        time.sleep(2.2)
    finally:
        stop(daemon)
        provider.shutdown()
