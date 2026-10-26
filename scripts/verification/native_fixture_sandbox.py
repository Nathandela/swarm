"""OS guards for synthetic native-provider fixtures; never used by production sessions."""
import argparse
import json
import os
from pathlib import Path
import socket
import subprocess
import sys
import uuid


def profile(root, ports):
    root = str(Path(root).resolve())
    quote = json.dumps
    rules = [
        '(version 1)', '(deny default)',
        '(allow process*)', '(allow signal)', '(allow sysctl-read)',
        '(allow mach-lookup)', '(allow ipc-posix*)',
        '(allow file-read*)',
        '(allow file-write* (subpath ' + quote(root) + '))',
        '(allow file-write* (literal "/dev/null") (literal "/dev/tty") (regex #"^/dev/ttys[0-9]+$"))',
        '(allow file-ioctl)', '(allow pseudo-tty)', '(allow file-write* (literal "/dev/ptmx"))',
        '(allow network-bind (local ip "localhost:*"))',
        '(allow network-inbound (local ip "localhost:*"))',
        '(allow network-bind (local unix-socket (subpath ' + quote(root) + ')))',
        '(allow network-outbound (remote unix-socket (subpath ' + quote(root) + ')))',
        '(allow network-inbound (local unix-socket (subpath ' + quote(root) + ')))',
    ]
    for port in ports:
        port = int(port)
        if not 0 < port < 65536:
            raise ValueError('invalid fixture port')
        rules.append('(allow network-outbound (remote ip "localhost:' + str(port) + '"))')
    return '\n'.join(rules) + '\n'


def guarded(argv, root, ports):
    if sys.platform == 'darwin':
        path = Path(root) / 'fixture.sbpl'
        path.write_text(profile(root, ports))
        path.chmod(0o600)
        return ['/usr/bin/sandbox-exec', '-f', str(path), *argv]
    if sys.platform.startswith('linux') and os.environ.get('SWARM_NATIVE_FIXTURE_OS_ISOLATED') == '1':
        return argv
    raise RuntimeError('native fixture requires a scratch-only write guard and isolated network; on Linux run the entire test in a read-only mount/fresh network namespace')


def check_guards(root, ports):
    guarded([], root, ports)
    root = Path(root).resolve()
    boundary = root
    if sys.platform.startswith('linux'):
        boundary = Path(os.environ['SWARM_NATIVE_FIXTURE_WRITABLE_ROOT']).resolve()
        if not root.is_relative_to(boundary):
            raise AssertionError('fixture scratch root is outside its writable namespace mount')
        if os.stat('/proc/self/ns/net').st_ino == int(os.environ['SWARM_NATIVE_FIXTURE_HOST_NETNS']):
            raise AssertionError('fixture still uses the host network namespace')
        if {name for _, name in socket.if_nameindex()} != {'lo'}:
            raise AssertionError('fixture namespace has a non-loopback network interface')
    positive = root / 'fixture-allowed-write'
    write_ok = 'from pathlib import Path; Path(' + repr(str(positive)) + ').write_text("allowed")'
    result = subprocess.run(guarded([sys.executable, '-c', write_ok], root, ports), stdout=subprocess.DEVNULL, stderr=subprocess.PIPE, timeout=5)
    if result.returncode != 0 or not positive.exists():
        raise AssertionError('fixture sandbox could not perform an allowed scratch write: ' + result.stderr.decode(errors='replace')[:500])
    positive.unlink()
    pty_ok = 'import os,pty; a,b=pty.openpty(); os.close(a); os.close(b)'
    result = subprocess.run(guarded([sys.executable, '-c', pty_ok], root, ports), stdout=subprocess.DEVNULL, stderr=subprocess.PIPE, timeout=5)
    if result.returncode != 0:
        raise AssertionError('fixture sandbox could not create its own PTY: ' + result.stderr.decode(errors='replace')[:500])
    connect_ok = 'import socket; socket.create_connection(("127.0.0.1", ' + str(ports[0]) + '), timeout=1).close()'
    result = subprocess.run(guarded([sys.executable, '-c', connect_ok], root, ports), stdout=subprocess.DEVNULL, stderr=subprocess.PIPE, timeout=5)
    if result.returncode != 0:
        raise AssertionError('fixture sandbox could not reach its allowed fake provider: ' + result.stderr.decode(errors='replace')[:500])
    sentinel = boundary.parent / ('fixture-denied-' + uuid.uuid4().hex)
    write = 'from pathlib import Path; Path(' + repr(str(sentinel)) + ').write_text("guard failed")'
    result = subprocess.run(guarded([sys.executable, '-c', write], root, ports), stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL, timeout=5)
    if result.returncode == 0 or sentinel.exists():
        if sentinel.exists():
            sentinel.unlink()
        raise AssertionError('fixture write guard did not deny outside its scratch root')
    with socket.socket() as listener:
        listener.bind(('127.0.0.1', 0))
        listener.listen()
        port = listener.getsockname()[1]
        host = '127.0.0.1' if sys.platform == 'darwin' else '192.0.2.1'
        connect = 'import socket; socket.create_connection((' + repr(host) + ', ' + str(port) + '), timeout=1)'
        result = subprocess.run(guarded([sys.executable, '-c', connect], root, ports), stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL, timeout=5)
        if result.returncode == 0:
            raise AssertionError('fixture network guard allowed an unrelated local listener')


if __name__ == '__main__':
    parser = argparse.ArgumentParser()
    parser.add_argument('--root', required=True)
    parser.add_argument('--output', required=True)
    parser.add_argument('--port', type=int, action='append', default=[])
    args = parser.parse_args()
    Path(args.output).write_text(profile(args.root, args.port))
