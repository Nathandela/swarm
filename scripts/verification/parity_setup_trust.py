#!/usr/bin/env python3
"""Verify that both executables can read, replace and revoke each other's consent."""
import argparse
import os
from pathlib import Path
import subprocess
import tempfile


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("reference", type=Path)
    parser.add_argument("candidate", type=Path)
    args = parser.parse_args()
    programs = [p.resolve(strict=True) for p in (args.reference, args.candidate)]

    with tempfile.TemporaryDirectory(prefix="trust-") as tmp:
        root = Path(tmp).resolve()
        workspace = root / "project"
        workspace.mkdir()
        alias = root / "link"
        alias.symlink_to(workspace)
        env = {"HOME": str(root), "XDG_CONFIG_HOME": str(root / "config"), "PATH": os.defpath}

        def run(program, *argv):
            result = subprocess.run([program, *argv], env=env, cwd=root, stdin=subprocess.DEVNULL,
                                    capture_output=True, timeout=10)
            return result.returncode, result.stdout, result.stderr

        def compare(*argv):
            expected, actual = (run(program, *argv) for program in programs)
            if actual != expected:
                raise AssertionError(f"{argv!r}: oracle={expected!r}, candidate={actual!r}")
            return actual

        for option in ([], ["--skill"], ["--help"], ["--bad"]):
            compare("dispatcher-guide", *option)
        compare("setup-trust", "off", "relative")
        compare("setup-trust", "status", workspace)
        compare("setup-trust", "status", os.fsencode(workspace) + b"-\xff")
        for writer, reader in (programs, reversed(programs)):
            result = run(writer, "setup-trust", "on", alias, "--profile-hooks")
            if result[0] or b"new or changed user and plugin hooks" not in result[1]:
                raise AssertionError(f"enrollment failed: {result!r}")
            observed = compare("setup-trust", "status", workspace)
            if observed != result:
                raise AssertionError(f"reader lost physical enrollment: {observed!r}")
            workspace.rename(root / "moved")
            workspace.mkdir()
            observed = compare("setup-trust", "status", alias)
            if b"is off for " not in observed[1]:
                raise AssertionError("replacement directory inherited consent")
            workspace.rmdir()
            (root / "moved").rename(workspace)
            if run(reader, "setup-trust", "off", alias)[0]:
                raise AssertionError("could not revoke foreign enrollment")
            if b"is off for " not in compare("setup-trust", "status", workspace)[1]:
                raise AssertionError("original writer retained revoked consent")
    print("Guide bytes, refusal and both directions of workspace consent passed.")


if __name__ == "__main__":
    main()
