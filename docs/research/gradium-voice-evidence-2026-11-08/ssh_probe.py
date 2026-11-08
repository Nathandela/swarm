"""Research only: fake-mic page through SSH -L; no persistent VM files."""
import json
import secrets
import selectors
import shlex
import socket
import subprocess
import time
import urllib.request
from browser_probe import CHECK, CAPTURE, WORKLET
from playwright.sync_api import sync_playwright

def stop(process):
    if process and process.poll() is None:
        process.terminate()
        try:
            process.wait(timeout=3)
        except subprocess.TimeoutExpired:
            process.kill()
            process.wait(timeout=3)

def main():
    nonce = secrets.token_hex(16)
    code = '''import http.server, time
worklet=WORKLET
class Handler(http.server.BaseHTTPRequestHandler):
 def do_GET(self):
  body=NONCE if self.path == '/probe' else worklet if self.path == '/worklet.js' else '<!doctype html><title>SSH fake audio probe</title>'
  self.send_response(200)
  self.send_header('Content-Type', 'text/javascript' if self.path == '/worklet.js' else 'text/html')
  self.end_headers(); self.wfile.write(body.encode())
  if self.path == '/done': self.server.done=True
 def log_message(self,*args): pass
server=http.server.HTTPServer(("127.0.0.1",0),Handler)
server.timeout=1
server.done=False
deadline=time.monotonic()+30
print(server.server_port,flush=True)
while not server.done and time.monotonic()<deadline: server.handle_request()
server.server_close()
'''.replace('NONCE', repr(nonce)).replace('WORKLET', repr(WORKLET))
    options = ['ssh', '-o', 'BatchMode=yes', '-o', 'ConnectTimeout=10']
    remote = tunnel = None
    try:
        remote = subprocess.Popen(options + ['tf_nathan', 'python3 -u -c ' + shlex.quote(code)], stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True)
        with selectors.DefaultSelector() as selector:
            selector.register(remote.stdout, selectors.EVENT_READ)
            assert selector.select(15), 'remote readiness timeout'
            line = remote.stdout.readline().strip()
        assert line.isdigit(), 'remote fixture did not return its port'
        remote_port = int(line)
        with socket.socket() as temporary:
            temporary.bind(('127.0.0.1', 0))
            local_port = temporary.getsockname()[1]
        start = time.monotonic()
        tunnel = subprocess.Popen(options + ['-N', '-T', '-o', 'ExitOnForwardFailure=yes', '-L', f'127.0.0.1:{local_port}:127.0.0.1:{remote_port}', 'tf_nathan'], stdout=subprocess.DEVNULL, stderr=subprocess.PIPE, text=True)
        while True:
            try:
                with urllib.request.urlopen(f'http://127.0.0.1:{local_port}/probe', timeout=3) as response:
                    body = response.read().decode()
                break
            except OSError:
                assert tunnel.poll() is None, 'SSH tunnel exited'
                assert time.monotonic() - start < 10, 'forwarding timeout'
                time.sleep(.1)
        assert body == nonce, 'forwarded response mismatch'
        forwarding_ms = round((time.monotonic()-start)*1000, 1)
        with sync_playwright() as pw:
            browser = pw.chromium.launch(args=['--use-fake-device-for-media-stream', '--use-fake-ui-for-media-stream'])
            try:
                page = browser.new_page()
                page.goto(f'http://127.0.0.1:{local_port}/')
                capabilities = page.evaluate(CHECK)
                assert all(capabilities.values()), capabilities
                audio = page.evaluate(CAPTURE)
                assert audio['contextRate'] == audio['workletRate'] == 24000
            finally:
                browser.close()
        with urllib.request.urlopen(f'http://127.0.0.1:{local_port}/done', timeout=3) as response:
            response.read()
        remote.wait(timeout=5)
        assert remote.returncode == 0, 'remote fixture failed'
        print(json.dumps({'kind':'actual-ssh-local-forward', 'host_alias':'tf_nathan',
            'local_bind':'127.0.0.1', 'remote_bind':'127.0.0.1', 'nonce_match':True,
            'remote_fixture_exit':remote.returncode, 'startup_and_request_ms':forwarding_ms,
            'browser_through_forward':capabilities, 'browser_fake_audio':audio,
            'audio_transported':False, 'real_microphone':False,
            'persistent_remote_files':False}, indent=2))
    finally:
        stop(tunnel)
        stop(remote)

if __name__ == '__main__':
    main()
