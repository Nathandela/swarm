# Saved Claude Code and Codex logins

This page documents released rotation behavior. The [native handover
redesign](../specifications/native-login-handover.md) is being qualified on a
feature branch and has no supported upgrade procedure yet.

Press `o`, select **Provider logins →** in Options, and press Enter. Add a Claude
or Codex login and finish the native sign-in. Codex displays its device URL and
code. Claude opens its native sign-in terminal when you press Enter. Credentials
are stored on this machine; the phone and remote protocol cannot manage them.

Sign-in requires a provider executable owned by you or root, without group or
other write permissions. If Swarm reports unsafe CLI permissions, repair the
installation or reinstall the provider CLI, then retry.

Select a saved login and make it active. Every Swarm discussion for that provider
uses the same selection. Saving a login alone does not activate it. With no
selection, new discussions use ordinary native authentication.

The active login shows indicative remaining quota reported by the native client:
Claude's 5-hour and weekly windows, and Codex's weekly window only. An em dash
means a window is absent, reset, or stale; these readings are not predictive and
do not affect login rotation.

Readings expire after 15 minutes without a native report and start unknown after
a daemon restart or account switch. Claude starts reporting from discussions
launched or resumed with this version; its common custom status line is preserved.
Project-specific or managed status-line overrides can leave Claude usage unknown.
Additional model-specific limits can still apply.

When a confirmed subscription limit is reached, Swarm selects another usable
login. It waits for current work to settle and for attached terminals to detach,
then restarts the provider's discussions in their exact native conversations.
Answer any pending permission request normally. A failed prompt is not replayed;
send your next instruction after continuation is ready.

**Saved** means native credentials were collected successfully, not that every
future server request is guaranteed to succeed. **Needs sign-in** means the native
client rejected authentication. Use **Sign in again**, including for the selected
login. If no other login is available, Swarm keeps the pending restart visible.
Use **Cancel switch** on that provider row to stop the pending switch. Limited
logins become eligible again after a known native reset time.
Credit/spending rejections and unknown resets require an explicit sign-in after
the provider issue is resolved.

Native clients renew their own credentials. Claude keeps each saved login in its
own secure-storage directory (Keychain on macOS). Codex's current auth file lives
in the common native home while selected; Swarm captures its latest state before
switching away. Do not run external clients in Swarm's private credential homes.

Managed native configuration and history live under:

```
<swarm-state>/logins/provider/claude/native
<swarm-state>/logins/provider/codex/native
```

Initial activation imports the required native context once. Stop external native
clients if Swarm reports that this import is waiting for a writer. After import,
edit managed native settings in these directories. Changing the ordinary owner
home does not continually overwrite the managed context. Project settings still
apply; competing API-key/auth routing is refused.

Removing a parked login erases its saved authentication. It preserves discussion
history and does not revoke the account at the provider. Switch away before
removing the selected login.

## Upgrade and recovery

Upgrading from v0.20 changes the shim wire contract. The updater waits for the old
daemon and native sessions to stop before activating the replacement. **Before activation by
any install path, including the updater, finish or cancel every old sign-in or
account check and wait for its worker to exit.** Then save current work, end the
old discussions, and stop the old daemon.

The v0.20 updater does not check detached worker liveness. Activating while one
is still running is unsupported: the new daemon refuses migration before changing
data. Let that worker finish, or return to v0.20 to cancel/resolve it, then retry.
When conversion has not created new login state, the new rollback guard permits
that compatible return to v0.20. Do not manually delete worker or recovery files.

At startup, the one-time importer validates old state, preserves credentials and
referenced conversation history, and converts metadata before normal loading.
Stopped v0.20 account checks can leave empty private lock files; these are retained
and accepted only when unlocked. Claude's global `CLAUDE.md` and `rules/` are
preserved with its native settings and history.
The importer also preserves global preferences reached through a symlinked
default Claude home.
After import, open Logins and activate one saved login for each provider before
resuming migrated discussions.
An old login with cleared credentials remains **Needs sign-in** under its recorded
identity. Stale cached account details cannot reactivate it; use the normal native
sign-in flow to restore access.
An unresolved recovery, live writer, conflicting history, or malformed private
record stops conversion with an actionable error. Resolve it using the previous
installation; do not delete credentials or journals to bypass the check.
Native source files and directories must not be group- or world-writable;
tighten the affected permissions before retrying.

Compressed legacy Codex rollouts require an executable `zstd` on `PATH` during
this one-time import. Without it, Swarm refuses conversion before changing data.
Native clients handle their own compressed history after import.

After conversion, old binaries cannot read the new login/switch state. The new
upgrader refuses such a downgrade even when there are no discussions. Keep a
normal machine backup before upgrading. Never paste credential files into an
issue; report the fixed error and native CLI version instead.

Architecture: [ADR-034](../adr/ADR-034-provider-wide-native-logins.md).
Validation scope: [provider login evidence](../verification/provider-logins.md).
