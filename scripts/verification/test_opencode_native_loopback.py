"""Preflight the complete endurance plan before starting a native worker."""

import pathlib
import subprocess
import sys
import tempfile
import unittest


class ArgumentPreflightTest(unittest.TestCase):
    def check_arguments(self, *extra):
        with tempfile.TemporaryDirectory(prefix="oc-preflight-", dir="/tmp") as directory:
            run = pathlib.Path(directory) / "run"
            result = subprocess.run(
                [
                    sys.executable,
                    str(pathlib.Path(__file__).with_name("opencode_native_loopback.py")),
                    "--swarm", sys.executable,
                    "--opencode", sys.executable,
                    "--source-head", "argument-preflight-only",
                    "--run-dir", str(run),
                    "--expected-opencode-version", "1.19.3",
                    "--check-arguments",
                    *extra,
                ],
                capture_output=True, text=True, timeout=10,
            )
            self.assertFalse(run.exists(), "argument checks must not start a worker")
            return result

    def test_every_planned_duration_passes_before_launch(self):
        for duration, interval, iterations in [(3600, 10, 500), (86400, 60, 2000), (259200, 60, 6000)]:
            with self.subTest(duration=duration):
                self.assertGreater(interval * iterations, duration)
                result = self.check_arguments("--duration-seconds", str(duration), "--interval-seconds", str(interval), "--max-iterations", str(iterations))
                self.assertEqual(result.returncode, 0, result.stderr)

    def test_original_invalid_queue_intervals_are_rejected_before_launch(self):
        for interval in (120, 180):
            with self.subTest(interval=interval):
                result = self.check_arguments("--interval-seconds", str(interval))
                self.assertEqual(result.returncode, 2, result.stderr)
                self.assertIn("invalid bounded fixture limits", result.stderr)

    def test_expected_version_is_stable_and_at_least_the_floor(self):
        for version in ("1.19.3", "1.19.4", "1.20.0", "2.0.0", "10.0.0"):
            with self.subTest(version=version):
                result = self.check_arguments("--expected-opencode-version", version)
                self.assertEqual(result.returncode, 0, result.stderr)
        for version in ("1.19.2", "1.9.99", "1.19", "v1.19.3", "01.19.3", "1.19.3-beta", "1.19.3+build", "1.19.3\n", ""):
            with self.subTest(version=version):
                result = self.check_arguments("--expected-opencode-version", version)
                self.assertEqual(result.returncode, 2, result.stderr)


if __name__ == "__main__":
    unittest.main()
