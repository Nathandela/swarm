import sys,os,json
sys.path.insert(0,os.path.dirname(__file__))
from custody import birth
identity=birth(os.getpid());assert identity is not None
with open(sys.argv[2],'w') as f:json.dump({'pid':os.getpid(),'birth':identity},f)
with open(sys.argv[1],'a',buffering=1) as log:
 for line in sys.stdin:
  try:r=json.loads(line)
  except ValueError:continue
  log.write(json.dumps({'pid':os.getpid(),'request':r})+'\n')
  if 'id' not in r:continue
  m=r.get('method')
  if m=='initialize':out={'protocolVersion':r['params']['protocolVersion'],'capabilities':{'tools':{}},'serverInfo':{'name':'causal-recorder','version':'0'}}
  elif m=='tools/list':out={'tools':[{'name':'publish','description':'Private neutral provenance fixture.','inputSchema':{'type':'object','properties':{'text':{'type':'string'},'turn_id':{'type':'string'},'hook_stamp':{'type':'string'}},'required':['text'],'additionalProperties':False}}]}
  elif m=='tools/call':out={'content':[{'type':'text','text':'Synthetic publication recorded.'}]}
  else:out={}
  print(json.dumps({'jsonrpc':'2.0','id':r['id'],'result':out}),flush=True)
