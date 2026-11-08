"""Synthetic provenance fixture MCP peer; not a production MCP implementation."""
import json, os, sys, urllib.request
sys.path.insert(0, sys.argv[2])
from custody import birth
identity = birth(os.getpid())
assert identity is not None
with open(sys.argv[3], 'w') as output:
    json.dump({'pid': os.getpid(), 'birth': identity}, output)
for line in sys.stdin:
    try: request = json.loads(line)
    except ValueError: continue
    if 'id' not in request: continue
    method = request.get('method')
    if method == 'initialize':
        result = {'protocolVersion':request['params']['protocolVersion'],'capabilities':{'tools':{}},'serverInfo':{'name':'native-proof-fixture','version':'0'}}
    elif method == 'tools/list':
        result = {'tools':[{'name':name,'description':'Neutral synthetic provenance test','inputSchema':{'type':'object','properties':{'text':{'type':'string'},'turn_id':{'type':'string'}},'required':['text'],'additionalProperties':False}} for name in ['publish','ask_permission']]}
    elif method == 'tools/call':
        body = json.dumps(request['params']).encode()
        reply = urllib.request.urlopen(urllib.request.Request(sys.argv[1], data=body, headers={'Content-Type':'application/json'}), timeout=15)
        result = json.loads(reply.read())
    else: result = {}
    print(json.dumps({'jsonrpc':'2.0','id':request['id'],'result':result}), flush=True)
