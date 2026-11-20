# Dispatcher call native admission and publication

The local fixtures exercise the existing native discussion ledger with typed call inputs, the Discord connector against a mock REST service, and the VM manager prompt. They ran with Go 1.26.5 for darwin/amd64 on an arm64 macOS host.

- The personal fixture resolves the established owner DM, pins its historical supervisor, retries the same turn with calls disabled, rejects a changed body or generation, and leaves the Discord text spool empty.
- The team fixture checks the configured participant list, the linked discussion's effective channel permissions, archived-thread behavior, and loss of private-thread membership.
- The native fixture checks that an ended call cannot produce a queued native write. A known no-effect draft refusal requires authority again, including when the refusal is reconciled after a lost acknowledgement. Uncertainty preserves the sealed operation and the existing text path preserves its FIFO behavior.
- Canonical publication fixtures check the exact body digest and attempt. Two independent attempts resolve to the correct admissions when their completions arrive in reverse order. The speech path refuses an attempt with conflicting later steering.

Existing VM shell delegation uses a process-scoped dispatcher credential. That credential proves the parent manager and worker attempt, but carries no native turn identity. A later shell worker result with only that proof stays eligible for the canonical written outbox. Calls direct new delegation through the existing native manager tool, which can record the exact launch or resume grant against the input. Neither observing a currently active native turn nor matching a job ID establishes that a prior shell command came from that turn. Attributing shell-launched workers to live calls remains open until the launch carries an independently verified native call coordinate.

These fixtures use synthetic input and no Discord or speech credentials. Actual personal and team calls, provider tool adoption, audible stop, and human end-to-end acceptance remain open.
