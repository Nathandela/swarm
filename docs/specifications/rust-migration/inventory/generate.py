#!/usr/bin/env python3
"""Generate the CLI flag catalog from the immutable M0 Go pin."""
import argparse
import re
import subprocess
from pathlib import Path

ROOT = Path(__file__).resolve().parents[4]
PIN = "f26648d69086e329132ac7849954d2f29a895387"
OUT = Path(__file__).with_name("flags.tsv")
METHODS = ("Bool", "BoolFunc", "Duration", "Float64", "Func", "Int", "Int64", "Text", "Uint", "Uint64", "String", "Var")
CALL = re.compile(r"\b(\w+)\.((?:" + "|".join(METHODS) + r")(?:Var)?)\s*\(")
QUOTED = re.compile(r'"((?:[^"\\]|\\.)*)"')
SET = re.compile(r"\b(\w+)\s*:?=\s*flag\.NewFlagSet\(\s*\"([^\"]+)\"")


def source_files():
    raw = subprocess.check_output(["git", "ls-tree", "-r", "--name-only", PIN, "--", "cmd"], cwd=ROOT, text=True)
    return [p for p in raw.splitlines() if p.endswith(".go") and not p.endswith("_test.go")]


def arg_strings(call):
    # Flag names are literal string arguments; registration calls do not use quoted strings in callbacks.
    return QUOTED.findall(call)


def entries():
    found = []
    for path in source_files():
        body = subprocess.check_output(["git", "show", f"{PIN}:{path}"], cwd=ROOT, text=True)
        lines = body.splitlines()
        setters = {}
        calls = []
        for n, line in enumerate(lines, 1):
            for match in SET.finditer(line):
                command = match.group(2).rstrip()
                if command in ("discord", "voice"):
                    command += " <dynamic subcommand>"
                setters[match.group(1)] = command
                calls.extend((command, name, path, n, "implicit Go help") for name in ("-h", "-help"))
            for match in CALL.finditer(line):
                recv, method = match.groups()
                if recv not in setters:
                    continue
                # Registrations may span lines; take through the call's matching close parenthesis.
                start = match.start()
                text = "\n".join(lines[n - 1:])
                open_at = text.find("(", match.start() - line.find(line.lstrip()))
                if open_at < 0:
                    continue
                depth, end = 0, open_at
                quoted, escaped = False, False
                for end, ch in enumerate(text[open_at:], open_at):
                    if quoted:
                        if escaped:
                            escaped = False
                        elif ch == "\\":
                            escaped = True
                        elif ch == '"':
                            quoted = False
                    elif ch == '"':
                        quoted = True
                    elif ch == "(":
                        depth += 1
                    elif ch == ")":
                        depth -= 1
                        if depth == 0:
                            break
                quoted_strings = arg_strings(text[match.start():end + 1])
                if not quoted_strings:
                    continue
                flag_name = quoted_strings[0]
                command = setters[recv]
                calls.append((command, flag_name, path, n, method))
        found.extend(calls)
    return sorted(found, key=lambda x: (x[0], x[2], x[3], x[1]))


def classify(command, path):
    command = command.lower()
    if path.startswith("cmd/swarm-publish/") or path.startswith("cmd/swarm-pushgw/"):
        return "retire standalone publication/gateway binary", "M10 retirement"
    if path.startswith("cmd/swarm-char/"):
        return "retain as local-only characterization tool", "M1/M10"
    if command.startswith(("remote", "relay")):
        return "retire mobile/pairing entry point", "M10 retirement"
    if command.startswith(("internal", "shim", "native-input", "schedule-agy-worker", "schedule-receipt-bridge")):
        return "internal contract; preserve only installed consumer requirements", "M2/M5/M7"
    if command.startswith("voice") and path.endswith(("voice_device.go", "voice_ssh.go", "voice_panel.go", "voice_unit.go")):
        return "retire standalone device/SSH/panel control", "M10 retirement"
    if command.startswith(("voice panel", "voice --host", "voice devices", "voice enroll", "voice revoke", "voice capture", "voice start", "voice stop")):
        return "retire standalone device surface", "M9/M10"
    if "voice" in command:
        return "split: preserve configured Discord speech; retire device control", "M9/M10"
    if command.startswith("discord"):
        return "retain text/discussion; calls use call pin", "M8/M9"
    if command.startswith("upgrade"):
        return "split; retain verified local upgrade contract", "M10"
    if command.startswith("relogin"):
        return "split; retain native re-login recovery", "M7/M10"
    if command.startswith("doctor"):
        return "split; retain local diagnostics, remove retired sections", "M7/M10"
    owners = {
        "ls": ("read-only session roster", "M4"), "watch": ("session status observer", "M4/M5"),
        "kill": ("supervisor lifecycle", "M3/M5"), "send": ("supervisor submit boundary", "M3/M5"),
        "peek": ("shim screen snapshot", "M2"), "message": ("provider operation driver", "M5"),
        "requests": ("provider native-request observer", "M5"), "decide": ("provider native decision", "M5"),
        "spawn": ("supervisor launch", "M6"), "handoff": ("supervisor handoff", "M6"),
        "reattach": ("provider history import", "M6"), "pilot": ("discussion workflow", "M6/M8"),
        "teammate message": ("discussion worker return", "M6/M8"), "teammate reply": ("discussion worker return", "M6/M8"),
        "teammate receipt": ("discussion receipt observer", "M6/M8"), "schedule create": ("scheduler catalog", "M7"),
        "upgrade": ("local installer and rollback", "M10"), "refresh": ("provider CLI maintenance", "M7"),
        "clean": ("safe local storage cleanup", "M7"), "checkout inventory": ("workspace custody inventory", "M6"),
    }
    owner, slice_ = owners.get(command, ("CLI operation; see manual-operations.tsv", "M0 capture"))
    return "retain; verify defaults, validation and refusal", f"{owner} ({slice_})"


def rows():
    result = ["command\tflag\tsource\tregistration\tdisposition\tevidence_owner"]
    for command, name, path, line, method in entries():
        disposition, owner = classify(command, path)
        rendered = name if name.startswith("-") else "--" + name
        result.append(f"{command}\t{rendered}\t{path}#{line}\tflag.{method}\t{disposition}\t{owner}")
    return result


def check_ledgers():
    for ledger in OUT.parent.glob("*.tsv"):
        data = [row.split("\t") for row in ledger.read_text().splitlines()]
        if not data or any(len(row) != len(data[0]) for row in data[1:]):
            raise SystemExit(f"malformed TSV: {ledger.name}")
        if ledger == OUT:
            continue
        for row in data[1:]:
            for cell in row:
                for ref in cell.split(";"):
                    ref = ref.strip()
                    if ref.startswith(("cmd/", "internal/", "docs/")):
                        source_ref = ref.split(" ", 1)[0].rstrip(".,)")
                        source = source_ref.split("#", 1)[0].split(":", 1)[0]
                        if not (ROOT / source).exists():
                            raise SystemExit(f"missing source in {ledger.name}: {source}")
                        range_match = re.match(r"^([^:]+):(\d+)-(\d+)$", source_ref)
                        scenario_match = re.search(r"\(scenario row (\d+)\)", ref)
                        if range_match and scenario_match:
                            _, first, last = range_match.groups()
                            lines = (ROOT / source).read_text().splitlines()
                            first, last, row_id = int(first), int(last), int(scenario_match.group(1))
                            if last > len(lines) or not any(re.search(r"\|\s*" + str(row_id) + r"\s*\|", line) for line in lines[first - 1:last]):
                                raise SystemExit(f"invalid scenario line reference in {ledger.name}: {source_ref}, row {row_id}")
                        if "#" in source_ref and source.endswith(".go"):
                            symbol = source_ref.split("#", 1)[1]
                            if not symbol.isdigit():
                                body = subprocess.check_output(["git", "show", f"{PIN}:{source}"], cwd=ROOT, text=True)
                                if not re.search(r"\b" + re.escape(symbol) + r"\b", body):
                                    raise SystemExit(f"missing Go symbol in {ledger.name}: {source}#{symbol}")


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--check", action="store_true")
    args = parser.parse_args()
    expected = "\n".join(rows()) + "\n"
    if args.check:
        if not OUT.exists() or OUT.read_text() != expected:
            raise SystemExit("flags.tsv stale against pinned Go source; regenerate")
        check_ledgers()
    else:
        OUT.write_text(expected)


if __name__ == "__main__":
    main()
