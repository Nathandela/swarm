#!/usr/bin/env python3
"""Unit checks and optional guarded Go/Go qualification."""
from concurrent.futures import ThreadPoolExecutor
from importlib.machinery import SourceFileLoader
from importlib.util import module_from_spec, spec_from_loader
import json
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest
from unittest.mock import patch

HERE = Path(__file__).resolve().parent
DRIVER = HERE / "swarm-parity"
loader = SourceFileLoader("parity", str(DRIVER))
spec = spec_from_loader(loader.name, loader)
parity = module_from_spec(spec)
loader.exec_module(parity)


class ParityTests(unittest.TestCase):
    def test_first_divergence_keeps_prefix_and_missing_values(self):
        ok = {"name": "spawn", "value": 0}
        echo = {"name": "echo", "value": "0d0a"}
        altered = {"name": "echo", "value": "0d0d0a"}
        ref = {"observations": [ok, echo]}
        self.assertIsNone(parity.compare(ref, ref))
        for actual, right in [(altered, [ok, altered]), (None, [ok])]:
            self.assertEqual(parity.compare(ref, {"observations": right}), {
                "index": 1, "last_good": "spawn", "expected": echo, "actual": actual,
            })

    def test_bystander_with_arm_path_is_excluded(self):
        root = Path("/tmp/swarm-parity-owner-test")
        rows = {
            100: (1, "", "S", "python swarm-parity _drive /tmp/swarm-parity-owner-test"),
            101: (1, "", "S", f"{root}/bin/swarm shim"),
            102: (1, "", "S", f"/usr/bin/tail -f {root}/daemon.log"),
            103: (1, "", "S", f"{root}/bin/swarm shim"),
            104: (100, "", "S", "/usr/bin/sleep 2"),
        }
        executable = {100: "/usr/bin/python3", 101: str(root / "bin/swarm"),
                      102: "/usr/bin/tail", 103: "/usr/bin/tail", 104: "/usr/bin/sleep"}
        with (patch.object(parity, "table", return_value=rows),
              patch.object(parity, "generation", side_effect=lambda pid: pid + 10),
              patch.object(parity, "executable", side_effect=executable.__getitem__)):
            identities = {102: 1}  # PID was recycled after registration.
            self.assertEqual(parity.owned(root, 100, identities), {100: 110, 101: 111, 104: 114})


def qualify(binary):
    with tempfile.TemporaryDirectory(prefix="swarm-parity-test-") as scratch:
        def run(control, replay=None):
            output = Path(scratch) / f"{control}.json"
            args = (["replay", "--bundle", str(replay)] if replay else [
                "run", "--reference", binary, "--candidate", binary,
                "--negative-control", control, "--timeout", "20",
            ])
            proc = subprocess.run([DRIVER, *args, "--output", output], timeout=80)
            return proc.returncode, json.loads(output.read_text()), output

        with ThreadPoolExecutor(max_workers=4) as pool:
            modes = dict(zip(("none", "echo", "timeout", "driver-kill"),
                             pool.map(run, ("none", "echo", "timeout", "driver-kill"))))
        status, receipt, _ = modes["none"]
        assert status == 0 and receipt["result"] == "passed", receipt
        status, receipt, bundle = modes["echo"]
        assert status == 1 and receipt["first_divergence"]["expected"]["name"] == "worker-echo", receipt
        first = receipt["first_divergence"]
        assert bytes.fromhex(first["expected"]["value"]).replace(b"\r\n", b"\r\r\n") == bytes.fromhex(
            first["actual"]["value"]), receipt
        for control, failure in [("timeout", "driver-timeout"), ("driver-kill", "driver-exit")]:
            status, receipt, _ = modes[control]
            assert status == 1 and receipt["candidate"]["failure"]["stage"] == failure, receipt
            assert receipt["candidate"]["cleanup"]["reaped"] >= 3, receipt
            assert receipt["candidate"]["cleanup"]["remaining"] == [], receipt
        status, receipt, _ = run("replayed", bundle)
        assert status == 1 and receipt["first_divergence"] == first, receipt
    print("Go/Go, echo negative control and replay, timeout and driver SIGKILL passed")


if __name__ == "__main__":
    if len(sys.argv) == 2:
        qualify(str(Path(sys.argv[1]).resolve()))
    else:
        unittest.main()
