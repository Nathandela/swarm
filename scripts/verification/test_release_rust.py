#!/usr/bin/env python3
"""Checks release archive allowlists and digest pins without production assets."""
import importlib.util
from pathlib import Path
import tempfile
import unittest

spec = importlib.util.spec_from_file_location("release", Path(__file__).parents[1] / "release-rust.py")
release = importlib.util.module_from_spec(spec)
spec.loader.exec_module(release)
NAME = "swarm-rust_0.1.0_linux_amd64.tar.gz"


class ArchiveTest(unittest.TestCase):
    def setUp(self):
        native = "node_modules/@snazzah/davey-linux-x64-gnu/package.json"
        media = {name: name.encode() for name in (*release.MEDIA, "node", "NODE-LICENSE", native)}
        pins = {p: {} for p in release.TARGETS}
        pins["linux-x86_64"] = {name: release.digest(data) for name, data in media.items()}
        pins = release.json_bytes(pins)
        self.files = {f"media/linux-x86_64/{name}": data for name, data in media.items()}
        self.files.update({
            "swarm": b"fixture",
            "LICENSE": b"fixture",
            "media-pins.json": pins,
            "compat.json": release.json_bytes({
                "format": 1, "channel": "rust-preview", "version": "0.1.0", "source": "a" * 40,
                "media_pins_sha256": release.digest(pins),
            }),
        })

    def inspect(self, original_manifest=None):
        manifest = original_manifest or {name: release.digest(data) for name, data in self.files.items()}
        with tempfile.TemporaryDirectory() as root:
            path = Path(root) / NAME
            release.archive(path, dict(self.files, **{"files.json": release.json_bytes(manifest)}))
            return release.inspect_archive(path.read_bytes(), release.ASSET.fullmatch(NAME))

    def test_content_and_tampering(self):
        self.assertEqual(self.inspect()[0], self.files)
        path = "media/linux-x86_64/swarm-discord-voice"
        self.files[path] = b"wrong blob"
        with self.assertRaisesRegex(ValueError, "media pin differs"):
            self.inspect()
        del self.files[path]
        with self.assertRaisesRegex(ValueError, "media pin differs"):
            self.inspect()

    def test_retired_member_and_traversal(self):
        self.files["swarm-remote"] = b"fixture"
        with self.assertRaisesRegex(ValueError, "retired members"):
            self.inspect()
        self.files["../outside"] = b"fixture"
        with self.assertRaisesRegex(ValueError, "invalid archive member"):
            self.inspect()

    def test_swapped_card(self):
        manifest = {name: release.digest(data) for name, data in self.files.items()}
        self.files["compat.json"] = self.files["compat.json"].replace(b"0.1.0", b"0.0.0")
        with self.assertRaisesRegex(ValueError, "member digest mismatch"):
            self.inspect(manifest)
        with self.assertRaisesRegex(ValueError, "invalid compatibility card"):
            self.inspect()


if __name__ == "__main__":
    unittest.main()
