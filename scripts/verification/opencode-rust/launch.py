import hashlib, json, os, socket, struct, sys
client=socket.socket(socket.AF_UNIX)
client.settimeout(60)
client.connect(os.environ['SWARM_DAEMON_SOCK'])
f=client.makefile('rwb')
def exchange(req):
  data=b'\x01'+json.dumps(req).encode()
  f.write(struct.pack('>I',len(data))+data); f.flush()
  return json.loads(f.read(struct.unpack('>I',f.read(4))[0])[1:])
hello=exchange(dict(op='hello',protocol_version=1,build_version='dev'))
assert hello['op']=='hello', hello
assert hello['endpoint_id']=='ep-'+hashlib.sha256(os.path.realpath(os.environ['SWARM_DAEMON_STATE']).encode()).hexdigest()[:8], hello
if len(sys.argv) == 1:
  sys.exit(0)
result=exchange(dict(op='launch',endpoint_id=hello['endpoint_id'],launch=dict(agent='opencode',cwd=sys.argv[1],env=[f'{k}={v}' for k,v in os.environ.items()])))
print(json.dumps(result))
assert result['op']!='error', result
