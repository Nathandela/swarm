#!/usr/bin/env python3
"""Guarded shared-owner Hermes qualification with synthetic provider/auth only."""
import argparse
import hashlib
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
import json
import os
from pathlib import Path
import shlex
import signal
import socket
import subprocess
import threading
import time

import psutil

from native_fixture_sandbox import check_guards, guarded


def run(args):
    root = Path(args.root).resolve()
    root.mkdir(mode=0o700, parents=True, exist_ok=True)
    root.chmod(0o700)
    source = Path(args.source).resolve()
    pin = subprocess.check_output(['git', '-C', str(source), 'rev-parse', 'HEAD'], text=True).strip()
    assert pin == 'bf0203a794a07aac3ff764972ab9087baa4e4f4e', pin
    original = Path(args.hermes).resolve()
    original_hash = hashlib.sha256(original.read_bytes()).hexdigest()
    bindir = root / 'bin'
    bindir.mkdir(exist_ok=True)
    private = bindir / 'hermes'
    # A private physical console-script copy selects the pinned source just as
    # its isolated editable install would. Native implementation files stay read-only.
    body = original.read_text()
    first, rest = body.split('\n', 1)
    runtime = {'HERMES_NODE': str(Path(args.node).resolve()), 'HERMES_TUI_DIR': str(source / 'ui-tui'),
               'HERMES_PYTHON_SRC_ROOT': str(source), 'HERMES_PYTHON': first[2:]}
    private.write_text(first + '\nimport os,sys\nsys.path.insert(0, ' + repr(str(source)) + ')\n'
                       + 'os.environ.update(' + repr(runtime) + ')\n' + rest)
    private.chmod(0o700)
    sleeper = bindir / 'fixture-sleeper'
    sleeper.write_text(first + '\nimport os,sys,time\nfrom pathlib import Path\n'
                       'Path(sys.argv[1]).write_text(str(os.getpid()))\ntime.sleep(300)\n')
    sleeper.chmod(0o700)
    home = root / 'home'
    home.mkdir(mode=0o700, exist_ok=True)
    home.chmod(0o700)
    (root / 'tmp').mkdir(exist_ok=True)
    (root / 'user').mkdir(exist_ok=True)
    (root / 'protected').write_text('native permission must remain pending')
    requests = []
    lock = threading.Lock()

    def command(name, background):
        target = root / name
        return {'command': shlex.quote(str(sleeper)) + ' ' + shlex.quote(str(target)),
                'background': background, 'timeout': 330}

    class Provider(BaseHTTPRequestHandler):
        def log_message(self, *unused):
            pass

        def do_GET(self):
            if self.path == '/fixture/stats':
                with lock:
                    value = {'main_calls': sum(bool(r.get('tools')) for r in requests)}
            else:
                value = {'data': [{'id': 'swarm-native-fixture', 'object': 'model'}]}
            raw = json.dumps(value).encode()
            self.send_response(200)
            self.send_header('Content-Type', 'application/json')
            self.send_header('Content-Length', str(len(raw)))
            self.end_headers()
            self.wfile.write(raw)

        def do_POST(self):
            request = json.loads(self.rfile.read(int(self.headers['Content-Length'])))
            with lock:
                requests.append(request)
            messages = request.get('messages', [])
            users = [(i, m.get('content')) for i, m in enumerate(messages) if m.get('role') == 'user']
            index, content = users[-1] if users else (-1, '')
            text = content if isinstance(content, str) else json.dumps(content)
            answered = any(m.get('role') == 'tool' for m in messages[index + 1:])
            tool, arguments = None, None
            if request.get('tools') and not answered:
                if text.strip() == 'SWARM_CHILD_A_WAIT':
                    tool, arguments = 'terminal', command('child-a.pid', False)
                elif 'SWARM_BACKGROUND_A' in text:
                    tool, arguments = 'terminal', command('a.pid', True)
                elif 'SWARM_BACKGROUND_B' in text:
                    tool, arguments = 'terminal', command('b.pid', True)
                elif 'SWARM_DELEGATE_A' in text:
                    tool, arguments = 'delegate_task', {'tasks': [{'goal': 'SWARM_CHILD_A_WAIT'}]}
                elif 'SWARM_PERMISSION_A' in text:
                    tool, arguments = 'terminal', {'command': 'rm -f ' + str(root / 'protected')}
            message = {'role': 'assistant', 'content': 'SYNTHETIC_NATIVE_ACK'}
            finish = 'stop'
            if tool:
                message = {'role': 'assistant', 'content': None, 'tool_calls': [{
                    'id': 'fixture-' + tool, 'type': 'function',
                    'function': {'name': tool, 'arguments': json.dumps(arguments)}}]}
                finish = 'tool_calls'
            streaming = request.get('stream') is True
            self.send_response(200)
            self.send_header('Content-Type', 'text/event-stream' if streaming else 'application/json')
            self.end_headers()
            try:
                if streaming:
                    if tool:
                        message['tool_calls'][0]['index'] = 0
                    for delta, reason in [(message, None), ({}, finish)]:
                        frame = {'id': 'fixture', 'object': 'chat.completion.chunk', 'created': 1,
                                 'model': 'swarm-native-fixture', 'choices': [
                                     {'index': 0, 'delta': delta, 'finish_reason': reason}]}
                        self.wfile.write(('data: ' + json.dumps(frame) + '\n\n').encode())
                    self.wfile.write(b'data: [DONE]\n\n')
                else:
                    self.wfile.write(json.dumps({'id': 'fixture', 'object': 'chat.completion', 'created': 1,
                        'model': 'swarm-native-fixture', 'choices': [
                            {'index': 0, 'message': message, 'finish_reason': finish}],
                        'usage': {'prompt_tokens': 10, 'completion_tokens': 5, 'total_tokens': 15}}).encode())
                self.wfile.flush()
            except (BrokenPipeError, ConnectionResetError):
                pass

    server = ThreadingHTTPServer(('127.0.0.1', 0), Provider)
    server.daemon_threads = True
    threading.Thread(target=server.serve_forever, daemon=True).start()
    port = server.server_port
    check_guards(root, [port])
    model_url = f'http://127.0.0.1:{port}/v1'
    config = {
        'model': {'provider': 'custom', 'default': 'swarm-native-fixture', 'base_url': model_url},
        'approvals': {'mode': 'manual', 'timeout': 180},
        'auxiliary': {'title_generation': {'enabled': False}},
        'telemetry': {'shared_metrics': {'enabled': False, 'send': False, 'offer_version': 2}},
    }
    (home / 'config.yaml').write_text(json.dumps(config))
    (home / 'config.yaml').chmod(0o600)
    env = {'PATH': str(bindir) + ':' + str(Path(args.node).resolve().parent) + ':/usr/bin:/bin:/usr/sbin:/sbin',
           'HOME': str(root / 'user'),
           'HERMES_HOME': str(home), 'HERMES_GATEWAY_LOCK_DIR': str(root / 'locks'),
           'XDG_STATE_HOME': str(root / 'xdg'), 'TMPDIR': str(root / 'tmp'),
           'OPENAI_API_KEY': 'loopback-only', 'OPENAI_BASE_URL': model_url,
           'TERM': 'xterm-256color', 'LANG': 'C.UTF-8', 'PYTHONUNBUFFERED': '1'}
    owner_log = (root / 'owner.log').open('w')
    owner = subprocess.Popen(guarded([str(private), 'gateway', 'run', '--no-supervise', '--force'], root, [port]),
                             cwd=root, env=env, stdin=subprocess.DEVNULL, stdout=owner_log,
                             stderr=subprocess.STDOUT, start_new_session=True)
    probe = None
    witnessed = {}

    def capture_jobs():
        for name in ('a.pid', 'b.pid', 'child-a.pid'):
            if name in witnessed:
                continue
            try:
                process = psutil.Process(int((root / name).read_text().strip()))
                assert owner.pid in [p.pid for p in process.parents()], (name, process.pid)
                witnessed[name] = process
            except (FileNotFoundError, ValueError, psutil.NoSuchProcess):
                pass

    try:
        deadline = time.monotonic() + 60
        descriptor = {}
        while owner.poll() is None and time.monotonic() < deadline:
            direct = home / 'gateway.sock'
            try:
                with socket.socket(socket.AF_UNIX) as peer:
                    peer.settimeout(2)
                    peer.connect(str(direct))
                    peer.sendall(b'{"protocol":1,"id":1,"verb":"identify","params":{}}\n')
                    with peer.makefile('rb') as stream:
                        reply = json.loads(stream.readline(65537))
                descriptor = reply.get('result', {})
                if descriptor.get('state') == 'ready':
                    break
            except (OSError, ValueError):
                pass
            time.sleep(.1)
        assert descriptor.get('state') == 'ready', owner_log.name
        assert descriptor['pid'] == owner.pid, descriptor
        api_port = int(descriptor['api_origin'].rsplit(':', 1)[1])
        check_guards(root, [port, api_port])
        with (root / 'probe.log').open('w') as log:
            probe = subprocess.Popen(guarded([str(Path(args.probe).resolve()), '--root', str(root),
                '--swarm', str(Path(args.swarm).resolve()), '--stats', f'http://127.0.0.1:{port}/fixture/stats'],
                root, [port, api_port]), cwd=root, env=env, stdin=subprocess.DEVNULL,
                stdout=log, stderr=subprocess.STDOUT, start_new_session=True)
            deadline = time.monotonic() + 180
            while probe.poll() is None and time.monotonic() < deadline:
                capture_jobs()
                time.sleep(.1)
            assert probe.poll() == 0, (probe.returncode, str(root / 'probe.log'))
        result = json.loads((root / 'result.json').read_text())
        result.update(source_pin=pin, provider_version='0.22.5', platform=os.uname().sysname,
                      os_write_guard=True, os_network_guard=True, no_production_auth=True)
        (root / 'result.json').write_text(json.dumps(result, indent=2) + '\n')
        print(json.dumps(result, indent=2))
    finally:
        if probe is not None and probe.poll() is None:
            os.killpg(probe.pid, signal.SIGTERM)
            try:
                probe.wait(timeout=8)
            except subprocess.TimeoutExpired:
                os.killpg(probe.pid, signal.SIGKILL)
                probe.wait(timeout=5)
        capture_jobs()
        if owner.poll() is None:
            os.killpg(owner.pid, signal.SIGTERM)
            try:
                owner.wait(timeout=10)
            except subprocess.TimeoutExpired:
                os.killpg(owner.pid, signal.SIGKILL)
                owner.wait(timeout=5)
        for process in witnessed.values():
            try:
                if process.is_running() and process.status() != psutil.STATUS_ZOMBIE:
                    process.kill()  # psutil verifies the captured process incarnation.
                    process.wait(timeout=5)
            except psutil.NoSuchProcess:
                pass
        with lock:
            (root / 'provider-requests.json').write_text(json.dumps(requests))
        assert hashlib.sha256(original.read_bytes()).hexdigest() == original_hash
        server.shutdown()
        server.server_close()
        owner_log.close()


if __name__ == '__main__':
    parser = argparse.ArgumentParser()
    for name in ('root', 'source', 'hermes', 'node', 'swarm', 'probe'):
        parser.add_argument('--' + name, required=True)
    run(parser.parse_args())
