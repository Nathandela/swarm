#!/usr/bin/env python3
"""An explicitly authorized, tiny native-account turn without credential copying."""
import json
import os
from pathlib import Path
import pwd
import signal
import subprocess
import sys
import uuid

from native_fixture_sandbox import profile


def probe():
    binary, root = sys.argv[1], Path(sys.argv[2]).resolve()
    assert sys.platform == 'darwin', 'native Keychain fixture is macOS only'
    home = root / 'account-home'
    config = home / '.claude'
    config.mkdir(parents=True)
    (home / 'tmp').mkdir()
    (config / '.claude.json').write_text(json.dumps({'hasCompletedOnboarding': True}))
    sandbox = root / 'native-account.sbpl'
    sandbox.write_text(profile(root, []) + '\n(allow network-outbound)\n')
    env = {
        'PATH': '/usr/bin:/bin:/usr/sbin:/sbin', 'HOME': os.environ['HOME'],
        'USER': pwd.getpwuid(os.getuid()).pw_name, 'TMPDIR': str(home / 'tmp'),
        'CLAUDE_CONFIG_DIR': str(config),
        # Empty selects the existing default Keychain service even with scratch config.
        'CLAUDE_SECURESTORAGE_CONFIG_DIR': os.environ.get('CLAUDE_SECURESTORAGE_CONFIG_DIR', os.environ.get('CLAUDE_CONFIG_DIR', '')),
        'CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC': '1', 'DISABLE_AUTOUPDATER': '1',
        'CLAUDE_CODE_MAX_RETRIES': '0',
    }
    sentinel = root.parent / ('native-account-denied-' + uuid.uuid4().hex)
    attempt = 'from pathlib import Path; Path(' + repr(str(sentinel)) + ').write_text("denied")'
    check = subprocess.run(['/usr/bin/sandbox-exec', '-f', str(sandbox), sys.executable, '-c', attempt],
                           env=env, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL, timeout=5)
    if sentinel.exists():
        sentinel.unlink()
        raise AssertionError('native-account sandbox permitted an outside write')
    assert check.returncode != 0, 'native-account outside-write guard was ineffective'
    def run(args):
        process = subprocess.Popen(['/usr/bin/sandbox-exec', '-f', str(sandbox), binary, *args],
                                   cwd=root, env=env, stdin=subprocess.DEVNULL,
                                   stdout=subprocess.PIPE, stderr=subprocess.PIPE, start_new_session=True)
        try:
            output, _ = process.communicate(timeout=22)
            try:
                return process.returncode, json.loads(output)
            except (ValueError, UnicodeDecodeError):
                return process.returncode, {}
        finally:
            try:
                os.killpg(process.pid, signal.SIGTERM)
            except ProcessLookupError:
                pass
            try:
                process.wait(timeout=1)
            except subprocess.TimeoutExpired:
                os.killpg(process.pid, signal.SIGKILL)
                process.wait()
    code, account = run(['auth', 'status', '--json'])
    logged_in = code == 0 and account.get('loggedIn') is True
    native_account = logged_in and account.get('authMethod') == 'claude.ai'
    result = {'logged_in': logged_in, 'native_account': native_account, 'real_model_turn': False,
              'auth_status_exit_code':code,
              'credentials_copied': False, 'global_config_writes_permitted': False}
    if native_account:
        code, response = run(['-p', '--no-session-persistence', '--setting-sources', '',
                              '--strict-mcp-config', '--mcp-config', '{"mcpServers":{}}',
                              '--tools', '', '--model', 'haiku', '--output-format', 'json',
                              '--system-prompt', 'Reply exactly NATIVE_ACCOUNT_OK.', 'Say hello.'])
        result['real_model_turn'] = code == 0 and not response.get('is_error', True) and response.get('result', '').strip() == 'NATIVE_ACCOUNT_OK'
        result['native_response_received'] = response.get('type') == 'result'
    print(json.dumps(result, sort_keys=True))
    assert result['native_account'] and result['real_model_turn'], 'native account or real model turn not qualified'


if __name__ == '__main__':
    probe()
