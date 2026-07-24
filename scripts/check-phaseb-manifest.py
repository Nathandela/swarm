#!/usr/bin/env python3
"""PB-DOC-7: enforce the Phase B requirement-ownership manifest.

Rounds 2, 3 and 4 of the audit committee each found requirements that were written
into the spec but never wired into a slice (homeless PB-KEY-2, then PB-STATE-10 and
PB-SAS-2, then PB-GW-7/8, PB-KEY-8, PB-PUSH-10). Ownership lived in prose, so the
error was only ever caught by a human reading carefully. This makes it mechanical.

Fails if a requirement defined in the spec is unowned, owned more than once, or if
the manifest names an id the spec does not define.
"""
import re, sys, pathlib

ROOT = pathlib.Path(__file__).resolve().parents[1]
SPEC = ROOT / "docs/specifications/remote-phaseB-requirements.md"
MANIFEST = ROOT / "docs/specifications/remote-phaseB-manifest.tsv"

# NOTE: [A-Z0-9]+ not [A-Z]+ -- the PB-E2E-* family contains a digit, and a narrower
# pattern silently drops the entire family (which is how it was first missed).
ID = re.compile(r"^\|\s*(~~)?\*{0,2}(PB-[A-Z0-9]+-\d+)\*{0,2}(~~)?\s*\|")

defined, withdrawn = set(), set()
for line in SPEC.read_text().splitlines():
    m = ID.match(line)
    if m:
        (withdrawn if m.group(1) else defined).add(m.group(2))
defined -= withdrawn

owned = {}
for raw in MANIFEST.read_text().splitlines():
    if not raw.strip() or raw.startswith("#"):
        continue
    rid, slice_ = raw.split("\t")
    owned.setdefault(rid, []).append(slice_)

errors  = [f"UNOWNED   {r}" for r in sorted(defined) if r not in owned]
errors += [f"MULTIOWN  {r} -> {v}" for r, v in sorted(owned.items()) if len(v) > 1]
errors += [f"PHANTOM   {r} (in manifest, not defined in the spec)" for r in sorted(set(owned) - defined)]
errors += [f"WITHDRAWN {r} (withdrawn in the spec but still owned)" for r in sorted(set(owned) & withdrawn)]

print(f"spec: {len(defined)} active requirements ({len(withdrawn)} withdrawn) | manifest: {len(owned)} owned")
if errors:
    print("\n".join(errors)); sys.exit(1)
print("manifest OK: every requirement owned exactly once")
