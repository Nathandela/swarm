#!/usr/bin/env python3
"""Build the two pinned, unsigned Go CLI references in a new output directory.

Only manifest.json marks a complete bundle. If publication is interrupted,
retry with a fresh directory. Existing outputs are never overwritten.
"""

import argparse
import hashlib
import json
import os
from pathlib import Path
import subprocess
import tempfile


ROOT = Path(__file__).resolve().parents[2]
PINS = {
    "main": "a716dd50a5faf746ce6f0831fc31d0d73b06ffeb",
    "calls": "da7b9f2dfa7e2c6960bd860a72459c0448845c82",
}


def build(label, commit, stage, env):
    with tempfile.TemporaryDirectory(prefix="swarm-source-") as scratch:
        source = Path(scratch) / "source"
        subprocess.run(["git", "clone", "--quiet", "--shared", "--no-checkout",
                        str(ROOT), str(source)], check=True)
        git = ["git", "-C", str(source), "-c", "core.hooksPath=/dev/null"]
        subprocess.run([*git, "checkout", "--quiet", "--detach", commit], check=True)
        head = subprocess.check_output([*git, "rev-parse", "HEAD"], text=True).strip()
        if head != commit:
            raise ValueError(f"{label}: expected {commit}, checked out {head}")
        binary = stage / f"swarm-{label}"
        subprocess.run(["go", "build", "-trimpath", "-buildvcs=false", "-mod=readonly",
                        "-o", str(binary), "./cmd/swarm"], cwd=source, env=env, check=True)

    version = subprocess.check_output([binary, "version"], text=True, timeout=15).strip()
    help_text = subprocess.check_output([binary, "discord", "--help"], text=True, timeout=15)
    if not version.startswith("swarm dev (") or "swarm discord connect|" not in help_text:
        raise ValueError(f"{label}: unexpected version or Discord command surface")
    if ("|stop-call|" in help_text) != (label == "calls"):
        raise ValueError(f"{label}: unexpected Discord stop-call availability")
    with binary.open("rb") as stream:
        digest = hashlib.sha256()
        for block in iter(lambda: stream.read(1024 * 1024), b""):
            digest.update(block)
    info = json.loads(subprocess.check_output(["go", "version", "-m", "-json", binary], env=env))
    return {"binary": binary.name, "build_settings": info["Settings"], "git_commit": commit, "sha256": digest.hexdigest(),
            "reported_version": version, "discord_help": help_text.strip()}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("output", type=Path, help="new directory under an existing parent")
    output = parser.parse_args().output.absolute()
    if os.path.lexists(output):
        parser.error(f"output already exists: {output}")
    env = dict(os.environ, GOFLAGS="", GOWORK="off", GOTOOLCHAIN="local", CGO_ENABLED="0")
    with tempfile.TemporaryDirectory(dir=output.parent, prefix=".swarm-reference-") as temp:
        stage = Path(temp)
        target = json.loads(subprocess.check_output(
            ["go", "env", "-json", "GOVERSION", "GOOS", "GOARCH"], env=env, cwd=ROOT))
        refs = {label: build(label, commit, stage, env) for label, commit in PINS.items()}
        manifest = {"kind": "unsigned-local-go-cli-builds", "go": target, "references": refs}
        receipt = stage / "manifest.json"
        receipt.write_text(json.dumps(manifest, indent=2) + "\n", encoding="utf-8")
        output.mkdir(mode=0o700)
        for ref in refs.values():
            (stage / ref["binary"]).rename(output / ref["binary"])
        receipt.rename(output / receipt.name)
    print(output / "manifest.json")


if __name__ == "__main__":
    main()
