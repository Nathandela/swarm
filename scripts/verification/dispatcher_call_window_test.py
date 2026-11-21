import json
from pathlib import Path
import subprocess
import tempfile
from unittest.mock import patch
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
        tmp = tempfile.TemporaryDirectory()
        self.addCleanup(tmp.cleanup)
        root = Path(tmp.name)
        config = {"application_id": "1", "calls": {"guild_id": "2", "room_id": "3", "discussion_id": "4"}}
        (root / "config.json").write_text(json.dumps(config))
        binary = Path("/test-reader")
        response = subprocess.CompletedProcess([], 0, stdout=json.dumps(state))
        with patch("dispatcher_call_window.subprocess.run", return_value=response) as run:
            observed = sample(binary, root, expected)
            self.assertEqual(run.call_args.args[0], ["/test-reader", "discord", "status", "--state-dir", str(root)])
            self.assertEqual(observed["metrics"], {"accepted_turns": 2})
            self.assertNotIn("private", json.dumps(observed))
            for key in expected:
                with self.subTest(key=key), self.assertRaises(ValueError):
                    sample(binary, root, dict(expected, **{key: "wrong"}))
            previous = run.call_count
            with self.assertRaises(ValueError):
                sample(binary, root, dict(expected, room_id="production"))
            self.assertEqual(run.call_count, previous)
            state["connected"] = False
            response.stdout = json.dumps(state)
            with self.assertRaises(ValueError):
                sample(binary, root, expected)


if __name__ == "__main__":
    unittest.main()
