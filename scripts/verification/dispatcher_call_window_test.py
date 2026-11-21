import json
from pathlib import Path
import tempfile
import unittest

from dispatcher_call_window import sample


class CallWindowTest(unittest.TestCase):
    def test_accepts_only_matching_connected_server_and_selects_metrics(self):
        expected = dict(commit="candidate", application_id="1", guild_id="2",
                        room_id="3", discussion_id="4")
        runtime = dict(expected, pid=300, version="trial")
        state = dict(connected=True, calls="active", call_runtime=runtime,
                     call_metrics=dict(accepted_turns=2, secret="private"),
                     transcript="private")
        with tempfile.TemporaryDirectory() as root:
            root = Path(root)
            binary = root / "reader"
            binary.write_text('#!/bin/sh\nexec /bin/cat "$4/status.json"\n')
            binary.chmod(0o700)
            snapshot = root / "status.json"
            snapshot.write_text(json.dumps(state))
            observed = sample(binary, root, expected)
            self.assertEqual(observed["metrics"], {"accepted_turns": 2})
            self.assertNotIn("private", json.dumps(observed))
            for key in expected:
                with self.subTest(key=key), self.assertRaises(ValueError):
                    sample(binary, root, dict(expected, **{key: "wrong"}))
            state["connected"] = False
            snapshot.write_text(json.dumps(state))
            with self.assertRaises(ValueError):
                sample(binary, root, expected)


if __name__ == "__main__":
    unittest.main()
