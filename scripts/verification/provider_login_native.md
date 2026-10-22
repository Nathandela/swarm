# Provider-wide login native qualification

Run this only with owner-authorized fresh credentials on their original Linux
host. It uses a new daemon, state directory, home and two disposable projects.
Existing native processes and owner sessions are never signaled. Source homes
must have no native writers: their auth files are temporarily parked, then the
latest native authority is restored on that same host after writer exclusion.
The source homes are unavailable to other native clients during the run.

Build the current application and VM-local custody helper for the target host:

```sh
GOOS=linux GOARCH=amd64 go build -o /private/tmp/login-swarm ./cmd/swarm
GOOS=linux GOARCH=amd64 go build -o /private/tmp/login-seed scripts/verification/provider_login_native_seed.go
```

Transfer only those binaries and `provider_login_native.py` to a private directory
on the authorized host. Do not transfer credentials between hosts. Run once for
each provider, with explicit existing native binaries and credential source
homes, and a new short private run path:

```sh
python3 provider_login_native.py \
  --swarm /path/to/login-swarm --seed /path/to/login-seed \
  --native /path/to/native/codex --provider codex \
  --source-a /original/fresh/codex-a --source-b /original/fresh/codex-b \
  --run-dir /tmp/slq-codex-new
```

Use `--provider claude` with Claude source homes for the other provider. The
Codex canary requests `gpt-6-luna` with a read-only sandbox; Claude requests
`haiku`. Both ask for no tools. `--check-arguments` checks paths without creating
the run or acquiring credentials.

The helper seeds two distinct saved logins through the real store. Every
activation then uses the production owner-local login protocol. Each discussion
records one synthetic marker under A, recalls it under B, and recalls it again
under A. Assertions check the exact native conversation IDs, unchanged working
directories and native models, empty continuation prompts, no model override on
Codex resumes, exactly three canary submissions, no tool calls, and six Linux
native stopped-writer proofs. This qualifies continuity and absence of prompt
replay; it does not qualify an in-progress tool or approval interruption.

`result.json` contains only aggregate evidence, native conversation IDs, model
names and disposable paths. Native transcripts, credentials and logs stay in
the private VM run. If a run fails, it still attempts writer exclusion and
source restoration. A `restoration_failure` requires resolving the named
VM-local custody state before any other use of those source homes; do not copy
an older credential backup over a native refresh.

The custody helper's `-action diagnose -state /private/run/s -provider codex`
prints only predefined package-validation booleans for replacement launch
configs. It does not print arguments, identities, tokens or private log text.
