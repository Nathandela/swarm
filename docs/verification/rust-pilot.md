# Rust M6 local Pilot slice

The [Pilot operator contract](../operations/pilot.md) and [workflow matrix](../specifications/rust-migration/workflows.md) define the retained behavior.

| Test | Observation |
|---|---|
| Public daemon and worker fixture | On macOS, pinned Go `f26648d69086e329132ac7849954d2f29a895387` and Rust each launched a disposable synthetic Claude worker through Pilot. Entry, create, roster and literal structured history agreed. An authenticated Stop reply arrived before a new watch, and cursor replay recovered it in `worker_data`. |
| Atomic watch and context lifecycle | An omitted cursor returned a snapshot at the head, and an explicit cursor replayed after the lower bound. Idle timeout returned exit 2 and a safe checkpoint. Context exit interrupted the active watch, and a replacement daemon socket refused the old context. The Rust journal retained the reply across the replacement and replayed the worker's exit when it happened while the supervisor was down. Independent tests recover a label update, a new worker, a proven deletion, and an exit after an interrupted journal commit. An older journal without a committed roster, or an unexplained disappearance, requires explicit full resync. |
| Lost acknowledgements | A synthetic owner peer dropped one launch response and one semantic response. Exact retries reused the saved operation and payload. Changed work was refused while pending. Await returned `delivery_uncertain` until the original native receipt could be reconciled. The pre-send cursor replayed a fast reply. |
| Custody | Context files were private and inaccessible after their permission fence was weakened. The launch request excluded the unrelated environment canary. The worker received only the task prompt or exact message. |

```sh
cargo build --locked -p swarm
python3 scripts/verification/parity_pilot.py \
  --reference /absolute/path/to/pinned/swarm-main \
  --candidate "$PWD/target/debug/swarm"
```

Live structured capture in the Rust journal currently covers bounded authenticated Claude Stop replies and lifecycle transitions. Other providers' full interaction streams, request history and multi-hour retention remain open. Native semantic send and resume delegate to the owner's negotiated provider capabilities. The lost-acknowledgement arm is a protocol fixture. No real credential use, remote SSH connection, or hosted wake service was tested.
