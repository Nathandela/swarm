# Ordinary Antigravity hands-off verification

The retained Go oracle at `f26648d6` reads the exact provider transcript at
`$HOME/.gemini/antigravity-cli/brain/<conversation>/.system_generated/logs/transcript.jsonl`.
It looks for a positive step in the first 16 complete records, each capped at 64 KiB.
A step has a nonnegative integer index and nonempty string type and creation time.
The Rust reader uses the same record bounds and rejects duplicate top-level keys.
It binds the accepted prefix digest and opened path identities and rechecks the
original source reservation, conversation, project, registered checkout if present,
and transcript before native successor execution. A later partial append preserves
already verified evidence.

| Vendor platform and version | Real board result |
| --- | --- |
| macOS arm64, 1.5.17 | A live ordinary agent produced a bound conversation ID and the corresponding provider transcript. The board launched a successor whose actual model request contained the original transcript and conversation pointers. |
| macOS arm64, installed 1.4.7 | A live ordinary agent produced the same transcript format before it exposed a usable conversation ID. The owner launch refused live hands-off. After confirming the scratch workspace and issuing `/exit`, the native exit footer supplied the exact UUID. The board launched a successor from the exited source. |
| Linux x86_64, 1.5.17 | The live source and real board witness used the same direct transcript and conversation UUID. |

The real-board results above use the runtime at `e10bf55a`.

Both vendor releases materialized completed `USER_INPUT` and `PLANNER_RESPONSE` records
without a status-line fixture. The version difference is when the session can prove
that it owns the transcript. The test delivered the successor through the public
board and inspected the actual synthetic model request with the exact source
pointer. The target ran with a different scratch `HOME` to prove that it could not
select the source's storage root. No real credentials or model account were used.

The Linux fixture's synthetic setup inherited a group-friendly umask, yielding
group-writable bootstrap directories `.gemini` and `antigravity-cli` beneath the
owner-only home. The official binary created its brain and logs directories with
mode 0700 and the regular file with mode 0600. The verifier accepts the directory
layout behind an owner-only ancestor and rejects a group-writable directory reachable
from a traversable home. It refuses an alias out of HOME, symlinks below the
one retained absolute in-HOME `.gemini` alias, hard links, oversized and
unterminated evidence, and changed transcript, home, workspace or source reservation
identity. No test modified a production transcript or used a production workspace.

Run the same fixture on macOS with the guarded command.

```sh
python3 -u scripts/verification/rust_agy_handoff.py /absolute/path/to/swarm /absolute/path/to/agy
```

For the Linux disposable namespace, stage `swarm`, `rust_agy_handoff.py`,
`rust_agy_schedule.py`, `native_fixture_sandbox.py`, and the
`parity/test_board.py` and `parity/swarm-parity` helpers under the fixture directory.
The namespace launcher restricts the binary to the disposable filesystem and
loopback model.

```sh
scripts/verification/provider_native_linux.sh FIXTURE_DIR VENDOR_AGY rust_agy_handoff.py
```

Run the focused Rust and pinned Go comparison tests from their respective checkouts.

```sh
TMPDIR=/private/tmp cargo test agy::handoff
go test ./internal/skeleton -run 'TestLocateTranscript_Agy|TestHandsOff_ComposesForAgySource' -count=1
```
