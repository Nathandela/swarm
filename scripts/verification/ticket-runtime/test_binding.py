import json
from pathlib import Path
import tempfile
import time
import unittest

from binding import session_binding


def record(path, data):
    path.write_text(json.dumps(data))
    path.chmod(0o600)


class BindingTest(unittest.TestCase):
    def setUp(self):
        scratch = tempfile.TemporaryDirectory()
        self.addCleanup(scratch.cleanup)
        self.root = Path(scratch.name)
        self.sessions = self.root / "sessions"
        self.profile = self.root / "profile"
        self.work = self.root / "workspaces" / "assigned"
        self.sid = "aaaaaaaaaaaaaaaa"
        for directory in (
            self.sessions,
            self.work.parent,
            self.sessions / self.sid,
            self.profile,
            self.work,
        ):
            directory.mkdir(parents=True, mode=0o700)
        self.config = {"generation": "fixture-1", "workspaces": str(self.work.parent)}
        self.active = {
            "assignment": "ticket-1",
            "generation": "fixture-1",
            "supervisor": str(self.sessions),
            "workspace": str(self.work),
            "conversation": "discord-teammate-" + "a" * 64,
            "bridge_socket": str(self.root / "control/bridge.sock"),
            "auth_file": str(self.root / "control/auth"),
            "provider_sockets": {
                "dispatcher": str(self.root / "p.sock"),
                "worker": str(self.root / "q.sock"),
            },
            "expires_at": time.time() + 60,
        }
        self.meta = {
            "id": self.sid,
            "agent_type": "codex",
            "role": "manager",
            "manager_conversation_id": self.active["conversation"],
            "manager_bridge_socket": self.active["bridge_socket"],
            "manager_auth_file": self.active["auth_file"],
        }
        self.shim = {
            "session_id": self.sid,
            "session_dir": str(self.sessions / self.sid),
            "native_provider": "codex",
            "cwd": str(self.work),
            "backend_required": True,
            "backend_program": str(self.profile / "codex"),
            "argv": [str(self.profile / "codex")],
            "backend_socket_path": str(self.sessions / self.sid / "codex.sock"),
            "env": [],
        }

    def run_binding(self):
        record(self.profile / "assignment.json", self.active)
        record(self.sessions / self.sid / "meta.json", self.meta)
        record(self.sessions / self.sid / "shim-launch.json", self.shim)
        return session_binding(
            self.config, self.profile, self.sessions, self.sid, self.work
        )

    def test_typed_manager(self):
        self.assertEqual(self.run_binding()[1:], ("dispatcher", None))

    def test_refusals(self):
        for source, field, bad in [
            (self.active, "expires_at", 0),
            (self.active, "generation", "other"),
            (self.shim, "backend_required", False),
            (self.shim, "backend_program", "/usr/bin/codex"),
            (self.meta, "manager_native_cli", True),
            (self.meta, "manager_conversation_id", "other"),
            (self.meta, "role", ""),
            (self.active, "workspace", str(self.work.parent)),
        ]:
            with self.subTest(field=field):
                backup = source.copy()
                source[field] = bad
                with self.assertRaises(ValueError):
                    self.run_binding()
                source.clear()
                source.update(backup)


if __name__ == "__main__":
    unittest.main()
