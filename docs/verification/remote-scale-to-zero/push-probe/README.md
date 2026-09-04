# Push Firestore transaction probe

This local-only probe uses Firebase project `demo-swarm-push-probe`; Firebase CLI blocks
non-emulated service access for demo projects. FCM and attestation are fakes and no
production credentials or endpoints are used.

Run `sh run-local.sh`. It installs the exact versions in `package-lock.json` with a
separate 120-second process-group deadline, then starts
Firestore emulator v1.22.0 through pinned `firebase-tools` 15.29.0. The runner bounds the
probe/emulator phase to 150 seconds. `emulators:exec` normally shuts the emulator down;
the deadline signals the entire owned process group and escalates to SIGKILL after five
seconds, preventing an orphaned JVM. Java is discovered from `JAVA_HOME` or `PATH`, with
the known Homebrew Java 21 path used only when it exists.

Run `node test-run-bounded.mjs` for a short lifecycle negative test. It verifies both a
deadline and external SIGTERM kill an owned grandchild that ignores graceful signals.

The induced transaction retry can take about 60 seconds under the emulator's pessimistic
locking. It proves callbacks may execute twice and provider I/O must not occur inside one.
Moving FCM outside the transaction prevents retry-callback duplication but cannot provide
exactly-once delivery: a crash between provider acceptance and durable completion remains
ambiguous and requires an explicit at-most-once versus at-least-once policy.
