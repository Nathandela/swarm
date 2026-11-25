#!/usr/bin/env python3
"""Local-only Swarm preview release workflow. Run with Python 3.11+."""
import argparse
import base64
import gzip
import hashlib
import io
import json
import os
from pathlib import Path, PurePosixPath
import re
import shutil
import stat
import subprocess
import tarfile
import tempfile
import tomllib

ROOT = Path(__file__).resolve().parent.parent
TOOLCHAIN = "stable"
RUSTC = "rustc 1.99.1 (58cf8f9c3 2026-10-13)"
NODE_VERSION = "24.17.0"
NODE_ARCHIVES = {
    "darwin-aarch64": ("darwin-arm64", "cf7e9152d7bd86c140f6eccf3577abfbaf8960be1ca49d9d900e8484984dcb9a"),
    "darwin-x86_64": ("darwin-x64", "fe50e386f6a5e0b29ce44b989e543da9fb9a80aed0b91a2f0cb19c55106921fc"),
    "linux-aarch64": ("linux-arm64", "67324b9e515e7d13da72571a5dd522bb23145a820f7dde15497897e466759ab3"),
    "linux-x86_64": ("linux-x64", "ab343a1b747c7cbf3630dfd7dbf818c5423fab2eb4f5ad1afc896f6bd121a917"),
}
GROUPS = {
    "darwin_all": ("darwin-aarch64", "darwin-x86_64"),
    "linux_arm64": ("linux-aarch64",),
    "linux_amd64": ("linux-x86_64",),
}
TARGETS = {
    "darwin-aarch64": "aarch64-apple-darwin",
    "darwin-x86_64": "x86_64-apple-darwin",
    "linux-aarch64": "aarch64-unknown-linux-musl",
    "linux-x86_64": "x86_64-unknown-linux-musl",
}
MEDIA = ("swarm-discord-voice", "media.mjs", "package.json", "package-lock.json")
VERSION = r"\d+\.\d+\.\d+(?:-[a-z0-9.]+)?"
ASSET = re.compile(rf"swarm-rust_({VERSION})_({'|'.join(GROUPS)})\.tar\.gz")
MAX_ARCHIVE = 512 * 1024 * 1024
MAX_CONTENT = 700 * 1024 * 1024


def require(ok, msg):
    if not ok:
        raise ValueError(msg)


def run(*cmd, **kwargs):
    return subprocess.run(cmd, check=True, **kwargs)


def output(*cmd):
    return run(*cmd, cwd=ROOT, stdout=subprocess.PIPE, text=True).stdout.strip()


def json_bytes(obj):
    return (json.dumps(obj, sort_keys=True, separators=(",", ":")) + "\n").encode()


def digest(data):
    return hashlib.sha256(data).hexdigest()


def file_hash(path):
    with path.open("rb") as handle:
        return hashlib.file_digest(handle, "sha256").hexdigest()


def host_platform():
    host = os.uname()
    arch = "aarch64" if host.machine == "arm64" else host.machine
    return f"{host.sysname.lower()}-{arch}"


def node_components(node_archives, platform, includes_npm=False):
    suffix, expected = NODE_ARCHIVES[platform]
    name = f"node-v{NODE_VERSION}-{suffix}"
    archive = node_archives / f"{name}.tar.xz"
    require(file_hash(archive) == expected, f"wrong upstream Node digest for {name}")
    with tarfile.open(archive, "r:xz") as node:
        files = {}
        for entry in node:
            relative = entry.name.removeprefix(name + "/")
            if relative in {"bin/node", "LICENSE"} or (
                includes_npm and relative.startswith("lib/node_modules/npm/") and entry.isfile()
            ):
                require(entry.isfile(), f"unexpected Node member: {relative}")
                files[relative] = node.extractfile(entry).read()
    require({"bin/node", "LICENSE"} <= files.keys(), f"incomplete Node archive {name}")
    return files


def stage_media(scratch, node_archives):
    host_node = scratch / "npm-tool"
    for relative, data in node_components(node_archives, host_platform(), True).items():
        path = host_node / relative
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_bytes(data)
    node = host_node / "bin/node"
    node.chmod(0o755)
    npm = host_node / "lib/node_modules/npm/bin/npm-cli.js"
    pins = {}
    for platform, (suffix, _) in NODE_ARCHIVES.items():
        bundle = scratch / "media" / platform
        bundle.mkdir(parents=True)
        for name in MEDIA:
            shutil.copyfile(ROOT / "services/discord-voice" / name, bundle / name)
        os_name, arch = suffix.split("-")
        run(node, npm, "ci", "--omit=dev", "--ignore-scripts", "--no-bin-links", "--no-audit",
            "--no-fund", f"--userconfig={scratch / 'npm-user-config'}",
            f"--globalconfig={scratch / 'npm-global-config'}", f"--os={os_name}",
            f"--cpu={arch}", "--libc=glibc", "--registry=https://registry.npmjs.org",
            f"--cache={scratch / 'npm-cache'}", cwd=bundle, env={"PATH": "/usr/bin:/bin"})
        modules = bundle / "node_modules"
        (modules / ".package-lock.json").unlink()
        target_dep = f"@snazzah/davey-{suffix}" + ("-gnu" if os_name == "linux" else "")
        if os_name == "linux":
            # The addon publishes glibc/musl packages without npm libc metadata.
            shutil.rmtree(modules / f"@snazzah/davey-{suffix}-musl", ignore_errors=True)
        installed = {p.relative_to(modules).as_posix() for p in (modules / "@snazzah").glob("davey-*")}
        require(installed == {target_dep}, f"wrong DAVE native module tree for {platform}: {installed}")
        upstream = node_components(node_archives, platform)
        (bundle / "node").write_bytes(upstream["bin/node"])
        (bundle / "NODE-LICENSE").write_bytes(upstream["LICENSE"])
        pins[platform] = {}
        for path in sorted(bundle.rglob("*")):
            require(not path.is_symlink(), f"symlink in media dependency tree: {path}")
            if path.is_file():
                pins[platform][path.relative_to(bundle).as_posix()] = file_hash(path)
    return pins


def member_mode(name):
    return 0o755 if name == "swarm" or re.fullmatch(
        r"media/[^/]+/(node|swarm-discord-voice)", name
    ) else 0o644


def archive(path, files):
    with path.open("xb") as handle, gzip.GzipFile(filename="", mode="wb", fileobj=handle, mtime=0) as gz:
        with tarfile.open(mode="w|", fileobj=gz, format=tarfile.GNU_FORMAT) as tar:
            for name, data in sorted(files.items()):
                member = tarfile.TarInfo(name)
                member.size = len(data)
                member.mode = member_mode(name)
                tar.addfile(member, io.BytesIO(data))


def build(args):
    require(output("git", "status", "--porcelain", "--untracked-files=all") == "", "dirty release source")
    source = output("git", "rev-parse", "HEAD")
    require(source == args.source, "source commit differs from the requested freeze")
    manifest = tomllib.loads((ROOT / "crates/swarm/Cargo.toml").read_text())
    version = manifest["package"]["version"]
    require(args.version == version and re.fullmatch(VERSION, version), "crate version differs from release version")
    require(output("rustup", "run", TOOLCHAIN, "rustc", "--version") == RUSTC, "wrong Rust toolchain")
    require(host_platform().startswith("darwin-"), "Darwin SDK and lipo required for complete release build")
    require(ROOT not in args.output.parents and args.output != ROOT, "release output must be outside checkout")
    with tempfile.TemporaryDirectory(prefix="swarm-release-") as tmp:
        scratch = Path(tmp)
        pins = stage_media(scratch, args.node_archives)
        pins_file = scratch / "release-media.json"
        pins_file.write_bytes(json_bytes(pins))
        env = {
            **os.environ, "CARGO_TARGET_DIR": str(args.target_dir), "SWARM_BUILD_VERSION": version,
            "SWARM_BUILD_COMMIT": source, "SWARM_BUILD_MEDIA_PINS": str(pins_file),
            "SOURCE_DATE_EPOCH": output("git", "show", "-s", "--format=%ct", source),
            "MACOSX_DEPLOYMENT_TARGET": "12.0",
        }
        require(not any(key in env for key in ("RUSTFLAGS", "CARGO_ENCODED_RUSTFLAGS", "CARGO_BUILD_RUSTFLAGS")),
                "unset externally supplied Rust flags for release builds")
        env["RUSTFLAGS"] = f"--remap-path-prefix={ROOT}=/swarm-source"
        for target in TARGETS.values():
            run("rustup", "run", TOOLCHAIN, "cargo", "build", "--frozen", "--release", "--bin", "swarm",
                "--target", target, cwd=ROOT, env=env)
        bins = {group: args.target_dir / TARGETS[platforms[0]] / "release/swarm"
                for group, platforms in GROUPS.items()}
        bins["darwin_all"] = scratch / "swarm"
        run("lipo", "-create", *(args.target_dir / TARGETS[p] / "release/swarm"
                                for p in GROUPS["darwin_all"]), "-output", bins["darwin_all"])
        run("lipo", "-verify_arch", "arm64", "x86_64", bins["darwin_all"])
        # The resulting universal CLI must have a valid ad hoc Mach-O signature.
        run("codesign", "--force", "--sign", "-", bins["darwin_all"])
        compat = json.loads(output(bins["darwin_all"], "release-info"))
        require(compat["source"] == source and compat["version"] == version
                and compat["media_pins_sha256"] == file_hash(pins_file), "built CLI freeze differs")
        args.output.mkdir()
        checksums = {}
        for group, platforms in GROUPS.items():
            files = {
                "swarm": bins[group].read_bytes(),
                "compat.json": json_bytes(compat),
                "media-pins.json": pins_file.read_bytes(),
                "LICENSE": (ROOT / "LICENSE").read_bytes(),
            }
            for platform in platforms:
                for relative in pins[platform]:
                    name = f"media/{platform}/{relative}"
                    files[name] = (scratch / name).read_bytes()
            files["files.json"] = json_bytes({name: digest(data) for name, data in files.items()})
            path = args.output / f"swarm-rust_{version}_{group}.tar.gz"
            archive(path, files)
            checksums[path.name] = file_hash(path)
        for name, source_file in (
            ("swarm-rust-install.py", Path(__file__)),
            ("swarm-rust-install.md", ROOT / "docs/operations/rust-preview-release.md"),
        ):
            path = args.output / name
            shutil.copyfile(source_file, path)
            checksums[name] = file_hash(path)
        (args.output / "checksums.txt").write_bytes(
            "".join(f"{value}  {name}\n" for name, value in sorted(checksums.items())).encode()
        )


def openssl(*cmd, **kwargs):
    return run(os.environ.get("SWARM_OPENSSL", "openssl"), *cmd, **kwargs)


def artifact_set(dist):
    checksums = (dist / "checksums.txt").read_bytes()
    entries = {}
    for line in checksums.decode("ascii").splitlines():
        match = re.fullmatch(r"([0-9a-f]{64})  ([a-zA-Z0-9._-]+)", line)
        require(match is not None, "invalid checksum line")
        value, name = match.groups()
        require(name not in entries, f"duplicate checksum name {name}")
        entries[name] = value
    assets = [ASSET.fullmatch(name) for name in entries]
    archives = [match.groups() for match in assets if match]
    require(len(entries) == 5 and {"swarm-rust-install.py", "swarm-rust-install.md"} <= entries.keys()
            and {g for _, g in archives} == GROUPS.keys() and len({v for v, _ in archives}) == 1,
            "checksums do not cover exactly one complete preview release")
    return checksums, entries


def sign(args):
    checksums, entries = artifact_set(args.dist)
    for name, expected in entries.items():
        require(file_hash(args.dist / name) == expected, f"wrong digest for {name}")
    with tempfile.TemporaryDirectory(prefix="swarm-sign-") as tmp:
        data = Path(tmp) / "checksums"
        data.write_bytes(checksums)
        signature = run(os.environ.get("SWARM_OPENSSL", "openssl"), "pkeyutl", "-sign", "-rawin",
                        "-inkey", args.private_key, "-in", data, stdout=subprocess.PIPE).stdout
    require(len(signature) == 64, "an Ed25519 signing key is required")
    with (args.dist / "checksums.txt.sig").open("xb") as handle:
        handle.write(base64.b64encode(signature) + b"\n")


def inspect_archive(data, match):
    version, group = match.groups()
    files = {}
    total = 0
    with tarfile.open(fileobj=io.BytesIO(data), mode="r:gz") as tar:
        for member in tar:
            name = member.name
            parts = PurePosixPath(name).parts
            require(parts and all(part not in ("..", "/") for part in parts)
                    and "/".join(parts) == name and "\\" not in name and name not in files,
                    f"invalid archive member {name}")
            total += member.size
            require(member.isfile() and member.mode == member_mode(name)
                    and 0 <= member.size and total <= MAX_CONTENT and len(files) < 16384,
                    f"invalid archive member metadata for {name}")
            files[name] = tar.extractfile(member).read()
    manifest = json.loads(files.pop("files.json"))
    require(manifest == {name: digest(content) for name, content in files.items()}, "archive member digest mismatch")
    compat = json.loads(files["compat.json"])
    require(compat["format"] == 1 and compat["channel"] == "rust-preview" and compat["version"] == version
            and re.fullmatch(r"[0-9a-f]{40}", compat["source"])
            and compat["media_pins_sha256"] == digest(files["media-pins.json"]), "invalid compatibility card")
    pins = json.loads(files["media-pins.json"])
    require(pins.keys() == TARGETS.keys(), "media target set differs")
    expected = {"swarm", "compat.json", "media-pins.json", "LICENSE"}
    for platform in GROUPS[group]:
        media = pins[platform]
        suffix = NODE_ARCHIVES[platform][0]
        native = suffix + ("-gnu" if platform.startswith("linux-") else "")
        required = {*MEDIA, "node", "NODE-LICENSE", f"node_modules/@snazzah/davey-{native}/package.json"}
        require(required <= media.keys(), f"missing helper dependencies for {platform}")
        for relative, sha in media.items():
            require(relative in required or relative.startswith("node_modules/"), "unexpected media member")
            name = f"media/{platform}/{relative}"
            require(name in files and sha == digest(files[name]), f"media pin differs for {name}")
            expected.add(name)
    require(files.keys() == expected, "archive contains unlisted or retired members")
    return files, compat


def verify(args):
    checksums, entries = artifact_set(args.dist)
    sig = base64.b64decode((args.dist / "checksums.txt.sig").read_bytes().strip(), validate=True)
    require(len(sig) == 64, "invalid Ed25519 signature length")
    with tempfile.TemporaryDirectory(prefix="swarm-verify-") as tmp:
        data, signature = Path(tmp) / "checksums", Path(tmp) / "sig"
        data.write_bytes(checksums)
        signature.write_bytes(sig)
        openssl("pkeyutl", "-verify", "-rawin", "-pubin", "-inkey", args.public_key,
                "-in", data, "-sigfile", signature, stdout=subprocess.DEVNULL)
    match = ASSET.fullmatch(args.archive)
    require(match and args.archive in entries, "archive is not signed for this preview")
    with (args.dist / args.archive).open("rb") as handle:
        data = handle.read(MAX_ARCHIVE + 1)
    require(len(data) <= MAX_ARCHIVE and digest(data) == entries[args.archive], "archive checksum differs")
    for name in ("swarm-rust-install.py", "swarm-rust-install.md"):
        require(file_hash(args.dist / name) == entries[name], f"installer content differs for {name}")
    files, compat = inspect_archive(data, match)
    if args.prefix:
        require(host_platform() in GROUPS[match[2]], "archive architecture does not match this host")
        require(args.prefix.parent.is_dir(), "create the install destination's parent first")
        args.prefix.mkdir(mode=0o700)
        try:
            for name, content in files.items():
                path = args.prefix / name
                path.parent.mkdir(exist_ok=True, parents=True)
                with path.open("xb") as handle:
                    handle.write(content)
                path.chmod(member_mode(name))
            exe = args.prefix / "swarm"
            require(json.loads(output(exe, "release-info")) == compat, "installed CLI differs from its compatibility card")
            if args.check_media:
                run(exe, "release-check-media")
        except BaseException:
            shutil.rmtree(args.prefix)
            raise
    print(f"verified {args.archive} {entries[args.archive]}")


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    commands = parser.add_subparsers(dest="command", required=True)
    p = commands.add_parser("build")
    p.add_argument("--source", required=True)
    p.add_argument("--version", required=True)
    p.add_argument("--target-dir", type=Path, required=True)
    p.add_argument("--node-archives", type=Path, required=True)
    p.add_argument("--output", type=Path, required=True)
    p = commands.add_parser("sign")
    p.add_argument("dist", type=Path)
    p.add_argument("--private-key", type=Path, required=True)
    p = commands.add_parser("verify")
    p.add_argument("dist", type=Path)
    p.add_argument("archive")
    p.add_argument("--public-key", type=Path, required=True)
    p.add_argument("--prefix", type=Path)
    p.add_argument("--check-media", action="store_true")
    args = parser.parse_args()
    for key, value in vars(args).items():
        if isinstance(value, Path):
            # Keep the requested basename without resolving an existing destination symlink.
            setattr(args, key, value.absolute())
    try:
        require(not getattr(args, "check_media", False) or args.prefix is not None, "--check-media requires --prefix")
        {"build": build, "sign": sign, "verify": verify}[args.command](args)
    except (ValueError, OSError, KeyError, tarfile.TarError, subprocess.CalledProcessError) as exc:
        parser.exit(1, f"swarm release: {exc}\n")


if __name__ == "__main__":
    main()
