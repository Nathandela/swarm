#!/usr/bin/env python3
"""Checks release archive allowlists and digest pins without production assets."""
import importlib.util
import os

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

    def test_media_directories_stay_private_with_a_group_writable_umask(self):
        with tempfile.TemporaryDirectory() as directory:
            old_mask = os.umask(0o002)
            try:
                release.install_members(Path(directory), {"media/p/node_modules/pkg/test.js": b"fixture"})
            finally:
                os.umask(old_mask)
            target = Path(directory) / "media/p/node_modules/pkg/test.js"
            self.assertEqual(target.read_bytes(), b"fixture")
            for parent in target.parents:
                if parent == Path(directory):
                    break
                self.assertEqual(parent.stat().st_mode & 0o777, 0o700)

    def test_swapped_card(self):
        manifest = {name: release.digest(data) for name, data in self.files.items()}
        self.files["compat.json"] = self.files["compat.json"].replace(b"0.1.0", b"0.0.0")
        with self.assertRaisesRegex(ValueError, "member digest mismatch"):
            self.inspect(manifest)
        with self.assertRaisesRegex(ValueError, "invalid compatibility card"):
            self.inspect()

class UpgradeTest(unittest.TestCase):
    def setUp(self):
        import types
        scratch = tempfile.TemporaryDirectory()
        self.addCleanup(scratch.cleanup)
        self.root = Path(scratch.name).resolve() / "sw"
        self.root.mkdir(mode=0o700)
        self.args = types.SimpleNamespace(prefix=self.root, caller=None, step="status",
                                          dist=None, check_media=False)
        self.old = self.card("0.1.0", "a")
        self.new = self.card("0.2.0", "b")
        release.managed_install(self.root, b"fixture", self.members(self.old), self.old, False)
        self.args.caller = release.release_id(self.old)
        self.new_slot = release.populate(self.root, self.members(self.new), self.new, False)
        release.replace_record(self.root / "staged.json",
                               release.json_bytes({"from": self.args.caller, "to": self.new_slot}))

    def card(self, version, source):
        return {"version": version, "source": source * 40, "managed_format": 1,
                "state_abi": 1, "channel": "rust-preview", "protocol": 1, "shimwire": 1,
                "media_protocol": 1}

    def members(self, card):
        encoded = release.json_bytes(card)
        return {"compat.json": encoded, "swarm": b"#!/bin/sh\ncat <<'EOF'\n" + encoded + b"EOF\n"}

    def test_cold_activate_and_rollback(self):
        self.args.step = "activate"
        with release.owner_lock(self.root / "writers.lock"):
            with self.assertRaises(BlockingIOError):
                release.upgrade(self.args)
        release.upgrade(self.args)
        self.assertEqual(release.route(self.root)["active"], self.new_slot)
        self.args.step = "rollback"
        with self.assertRaisesRegex(ValueError, "current managed launcher"):
            release.upgrade(self.args)
        self.args.caller = self.new_slot
        release.upgrade(self.args)
        self.assertEqual(release.route(self.root)["active"], release.release_id(self.old))

    def test_possible_effect_blocks_rollback_and_preserves_state(self):
        self.args.step = "activate"
        release.upgrade(self.args)
        self.args.caller = self.new_slot
        self.args.step = "rollback"
        (self.root / "effects" / self.new_slot).write_bytes(b"")
        (self.root / "user-work").write_text("written after cutover")
        with self.assertRaisesRegex(ValueError, "may have written"):
            release.upgrade(self.args)
        self.assertEqual(release.route(self.root)["active"], self.new_slot)
        self.assertEqual((self.root / "user-work").read_text(), "written after cutover")

    def test_tamper_never_switches_pointer(self):
        self.args.step = "activate"
        (release.release_path(self.root, self.new_slot) / "compat.json").write_text("{}")
        with self.assertRaisesRegex(ValueError, "modified release member"):
            release.upgrade(self.args)
        self.assertEqual(release.route(self.root)["active"], self.args.caller)


if __name__ == "__main__":
    unittest.main()
