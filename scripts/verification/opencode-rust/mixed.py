"""Long-lived operator PTY, scheduled work and a private Discord control receiver."""

import hashlib
import json
import os
from pathlib import Path
import shutil
import socket
import socketserver
import threading
import time

from launch import owner_request, owner_stream, read_control, read_frame, send_control, write_frame
from processes import alive, birth, sample

SCHEDULE_PROMPT = 'M11 scheduled fixture. Report the synthetic completion.'
DISCORD_BODY = 'Public test fixture reply.\n'
DISCUSSION = 'fixture-discussion'


def fixture_call(path, route, method='GET'):
    with socket.socket(socket.AF_UNIX) as connection:
        connection.settimeout(3)
        connection.connect(str(path))
        connection.sendall(f'{method} {route} HTTP/1.0\r\nHost: fixture\r\n\r\n'.encode())
        with connection.makefile('rb') as response:
            header, body = response.read(65537).split(b'\r\n\r\n', 1)
    assert b'200 OK' in header and len(body) < 65536, header
    return body


class DiscordReceiver(socketserver.StreamRequestHandler):
    def handle(self):
        self.connection.settimeout(4)
        route = self.rfile.readline(1024)
        assert route == self.server.route
        fields = {}
        for _ in range(32):
            line = self.rfile.readline(1024)
            if line == b'\r\n':
                break
            key, value = line.decode().split(':', 1)
            fields[key.lower()] = value.strip()
        size = int(fields['content-length'])
        assert 0 < size <= 4096
        payload = json.loads(self.rfile.read(size))
        assert payload['ref'] == DISCUSSION and payload['body'] == DISCORD_BODY
        publication = payload['id']
        assert publication.startswith('m11-')
        response = self.server.effects.setdefault(publication, {'message_id': str(len(self.server.effects) + 1000)})
        body = json.dumps(response).encode()
        self.wfile.write(f'HTTP/1.0 200 OK\r\nContent-Length: {len(body)}\r\n\r\n'.encode() + body)


class Mixed:
    def __init__(self, root, env, cli, output, wait, owned):
        self.root, self.env, self.cli, self.output, self.wait = root, env, cli, output, wait
        self.owned = owned
        self.operator = self.job = self.discord = None
        self.stats = {'operator_attaches': 0, 'schedule_ticks': 0, 'scheduled_completions': 0,
                      'discord_publications': 0, 'scheduled_peaks': {}}
        self.attempts = set()
        self.last_minute = None

    def start(self):
        for name in ('claude', 'codex'):
            fixture = self.root / 'bin' / name
            assert Path(shutil.which(name, path=self.env['PATH'])) == fixture, f'private {name} fixture required'
        self.operator = self.cli('spawn', '--cli', 'claude', '--dir', str(self.root / 'project'),
                                 '--name', 'M11 operator', '--prompt', 'offline fixture').stdout.strip()
        stage = self.root / 'state/rust-v1' / self.operator.split('/')[-1]
        owner = json.loads((stage / 'owner.json').read_text())
        self.owned['operator_shim'] = (owner['pid'], owner['birth'])
        self.owned['operator_worker'] = (owner['worker_pid'], birth(owner['worker_pid']))
        # Fixtures use the shared bin directory but keep their observed effects under this stage.
        self.effects = self.root / 'effects'
        self.wait(lambda: all((self.effects / name).is_file() for name in ('size', 'input')), 8)
        self.operator_input = (self.effects / 'input').read_bytes()
        prompt = self.root / 'schedule-prompt'
        prompt.write_text(SCHEDULE_PROMPT)
        workspace = self.root / 'schedule-project'
        workspace.mkdir(mode=0o700)
        self.job = self.output('schedule', 'create', '--name', 'M11 fixture',
                               '--dir', str(workspace), '--agents', 'codex',
                               '--authority', 'workspace-write', '--workspace', 'directory', '--cron', '* * * * *',
                               '--timezone', 'UTC', '--catch-up', '2m', '--prompt-file', str(prompt))['id']
        self.last_minute = int(time.time()) // 60

        token = self.root / 'discord-token'
        token.write_text('test-only-token\n')
        token.chmod(0o600)
        place = self.root / 'discord'
        enrollment = self.output('discord', 'connect', '--state-dir', str(place), '--name', 'M11 fixture',
                                 '--application-id', '123', '--owner-id', '456', '--token-file', str(token),
                                 '--workdir', str(self.root / 'project'))
        bridge = place / 'bridge.sock'
        self.discord = socketserver.UnixStreamServer(str(bridge), DiscordReceiver)
        bridge.chmod(0o600)
        digest = hashlib.sha256(enrollment['installation_id'].encode()).hexdigest()
        self.discord.route = f'POST /installation-v1/{digest}/reply HTTP/1.0\r\n'.encode()
        self.discord.effects = {}
        threading.Thread(target=self.discord.serve_forever, daemon=True).start()
        self.body = self.root / 'discord-body'
        self.body.write_text(DISCORD_BODY)

    def interaction(self, ordinal):
        marker = f'm11-operator-{ordinal:06}'.encode()
        geometry = 90 + (ordinal % 3), 26 + (ordinal % 2)
        with owner_stream(self.env, ('attach',)) as (stream, hello):
            fields = dict(endpoint_id=hello['endpoint_id'], session_id=self.operator)
            send_control(stream, dict(op='attach', **fields))
            lease = read_control(stream)
            assert lease['op'] == 'lease', lease
            fields['generation'] = lease['generation']
            snapshot = bytearray()
            while len(snapshot) < lease['snapshot_len']:
                tag, body = read_frame(stream)
                assert tag == 4
                snapshot.extend(body)
            assert len(snapshot) == lease['snapshot_len']
            assert json.loads(snapshot)['lines']
            send_control(stream, dict(op='resize', cols=geometry[0], rows=geometry[1], **fields))
            self.wait(lambda: (self.effects / 'size').read_text() == f'{geometry[0]}x{geometry[1]}\n', 5)
            write_frame(stream, 3, marker + b'\r')
            terminal = bytearray()
            while marker not in terminal:
                tag, body = read_frame(stream)
                assert tag == 2, tag
                terminal.extend(body)
                assert len(terminal) <= 32768
            send_control(stream, dict(op='detach', **fields))
            assert read_control(stream)['op'] == 'detach'
        self.operator_input += marker + b'\r'
        assert (self.effects / 'input').read_bytes() == self.operator_input
        self.stats['operator_attaches'] += 1

        publication = f'm11-{ordinal:06}'
        args = ('discord', 'reply', DISCUSSION, '--body-file', str(self.body), '--publication-id', publication)
        first = self.output(*args)
        assert self.output(*args) == first == self.discord.effects[publication]
        self.stats['discord_publications'] += 1
        assert len(self.discord.effects) == self.stats['discord_publications']

    def tick(self):
        minute = int(time.time()) // 60
        if self.last_minute is None or minute == self.last_minute:
            return
        assert minute == self.last_minute + 1, 'fixture missed a real minute tick'
        self.last_minute = minute

        def runs():
            return self.output('schedule', 'history', self.job) or []

        self.cli('schedule', 'tick')
        self.stats['schedule_ticks'] += 1
        def started():
            for run in runs():
                attempts = run.get('attempts') or []
                if run['id'] not in self.attempts and attempts and attempts[-1].get('session_id'):
                    return run
            return None

        try:
            run = self.wait(started, 15)
        except AssertionError as exc:
            raise AssertionError([(r['state'], r.get('detail')) for r in runs()]) from exc
        assert not run['manual'] and len(run['attempts']) == 1, run
        ident = run['attempts'][0]['session_id']
        stage = self.root / 'state/rust-v1' / ident.split('/')[-1]
        app = stage / 'b'

        def native_ready():
            observation = json.loads((stage / 'observation.json').read_text())
            return observation.get('schedule_ready')

        self.wait(native_ready, 12)
        self.cli('schedule', 'tick')
        turn = self.wait(lambda: (p if (p := json.loads(fixture_call(app, '/probe')))['receipt'] else None), 12)
        assert turn['turnStarts'] == 1 and SCHEDULE_PROMPT in turn['lastText']
        assert turn['scheduleProof'] and not turn['scheduleOwner'] and not turn['openai'] and not turn['anthropic']
        owner = json.loads((stage / 'owner.json').read_text())
        for role, pid in (('scheduled_shim', owner['pid']), ('scheduled_worker', owner['worker_pid'])):
            observed = sample(pid)
            if role == 'scheduled_shim':
                assert observed['rss_bytes'] < 256 * 1024 * 1024 and observed['fds'] < 128
            maximum = self.stats['scheduled_peaks'].setdefault(role, {})
            for key, value in observed.items():
                maximum[key] = max(value, maximum.get(key, 0))
        fixture_call(app, '/fixture/schedule-finish', 'POST')
        self.wait(lambda: (stage / 'schedule-finish.json').exists(), 8)
        self.cli('schedule', 'tick')
        result = next(r for r in runs() if r['id'] == run['id'])
        assert result['state'] == 'ready' and result['completion']['summary'] == 'Fixture assistant result'
        assert result['attempts'][0]['id'] == run['attempts'][0]['id']
        assert json.loads(fixture_call(app, '/probe'))['turnStarts'] == 1
        self.cli('kill', ident)
        self.cli('watch', ident, '--until', 'completed', '--timeout', '8s')

        def released():
            self.cli('schedule', 'tick')
            return next(r for r in runs() if r['id'] == run['id'])['released_at'] != '0001-01-01T00:00:00Z'

        self.wait(released, 8)
        # Retain the schedule ledger while deleting this proved stopped fixture session.
        deleted = owner_request(self.env, {'op': 'delete', 'session_id': ident})
        assert deleted['op'] == 'ok' and not stage.exists(), deleted
        self.attempts.add(run['id'])
        self.stats['scheduled_completions'] += 1

    def rejoined(self):
        assert all(alive(*self.owned[role]) for role in ('operator_shim', 'operator_worker'))
        assert (self.effects / 'input').read_bytes() == self.operator_input

    def close(self):
        if self.job is not None:
            self.cli('schedule', 'pause', self.job)
        if self.operator is not None:
            self.cli('kill', self.operator)
            self.cli('watch', self.operator, '--until', 'completed', '--timeout', '8s')
        if self.discord is not None:
            self.discord.shutdown()
            self.discord.server_close()
            self.cli('discord', 'disconnect')
