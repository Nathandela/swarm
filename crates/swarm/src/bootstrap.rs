//! Interactive board auto-start. Scripting commands only dial an existing daemon.
use crate::{ipc, supervisor::runtime};
use std::env;
use std::fs::File;
use std::io;
use std::os::fd::AsFd;
use std::os::unix::fs::MetadataExt;
use std::os::unix::process::CommandExt;
use std::path::Path;
use std::process::{Command, Stdio};
use std::thread;
use std::time::{Duration, Instant};

pub(crate) fn ensure_daemon() -> io::Result<()> {
    ensure(false)
}

pub(crate) fn ensure(unattended: bool) -> io::Result<()> {
    let socket = ipc::socket_path()?;
    if reachable(&socket, None)? {
        return Ok(());
    }
    cold_start(&socket, &ipc::state_path()?, unattended, None)
}

/// Service startup uses the enrolled daemon's paths even when launchd or systemd supplies a different environment.
pub(crate) fn ensure_bound(
    socket: &Path,
    root: &Path,
    lock: &Path,
    log: &Path,
    unattended: bool,
) -> io::Result<()> {
    let expected =
        crate::supervisor::endpoint(&root.canonicalize().unwrap_or_else(|_| root.to_owned()));
    if reachable(socket, Some(&expected))? {
        return Ok(());
    }
    cold_start(socket, root, unattended, Some((lock, log)))
}

fn reachable(socket: &Path, expected: Option<&str>) -> io::Result<bool> {
    match ipc::Client::dial(socket, &[]) {
        Ok(client) if expected.is_none_or(|id| client.endpoint() == id) => Ok(true),
        Ok(_) => Err(io::Error::other(
            "configured socket answered for a different supervisor",
        )),
        Err(error)
            if matches!(
                error.kind(),
                io::ErrorKind::NotFound | io::ErrorKind::ConnectionRefused
            ) =>
        {
            Ok(false)
        }
        Err(error) => Err(error),
    }
}

fn cold_start(
    socket: &Path,
    root: &Path,
    unattended: bool,
    bound: Option<(&Path, &Path)>,
) -> io::Result<()> {
    let saved = if unattended {
        Some(crate::environment::restore(root)?)
    } else {
        None
    };
    let expected = bound.map(|_| {
        crate::supervisor::endpoint(&root.canonicalize().unwrap_or_else(|_| root.to_owned()))
    });
    let child = command(socket, root, saved, bound)?.spawn()?;
    wait_for_start(child, || {
        reachable(socket, expected.as_deref()).unwrap_or(false)
    })
}

fn command(
    socket: &Path,
    root: &Path,
    saved: Option<Vec<String>>,
    bound: Option<(&Path, &Path)>,
) -> io::Result<Command> {
    runtime::private_dir(root)?;
    let log_path = bound
        .map(|(_, log)| log.to_owned())
        .unwrap_or_else(|| crate::clean::DiagnosticLog::path(root));
    let log = crate::clean::DiagnosticLog::open_owner(&log_path)?;
    let mut cmd = Command::new(env::current_exe()?);
    if let Some(entries) = saved {
        cmd.env_clear();
        for kv in &entries {
            let (key, value) = kv.split_once('=').expect("validated restart entry");
            cmd.env(key, value);
        }
        cmd.env("SWARM_DAEMON_SOCK", socket);
        for key in ["SWARM_DAEMON_LOCK", "SWARM_DAEMON_LOG"] {
            if let Some(path) = ipc::env_path(key) {
                cmd.env(key, path);
            }
        }
    }
    if let Some((lock, log)) = bound {
        cmd.env("SWARM_DAEMON_SOCK", socket)
            .env("SWARM_DAEMON_LOCK", lock)
            .env("SWARM_DAEMON_LOG", log);
    }
    cmd.arg("daemon")
        .env("SWARM_DAEMON_STATE", root)
        .stdin(Stdio::null())
        .stdout(log.output()?)
        .stderr(log.output()?);
    // SAFETY: setsid is async-signal-safe and uses no inherited Rust state.
    unsafe {
        cmd.pre_exec(|| {
            if libc::setsid() == -1 {
                Err(io::Error::last_os_error())
            } else {
                Ok(())
            }
        });
    }
    Ok(cmd)
}

fn wait_for_start(mut child: std::process::Child, ready: impl Fn() -> bool) -> io::Result<()> {
    let deadline = Instant::now() + Duration::from_secs(5);
    loop {
        if ready() {
            // A long-running board must reap its own daemon if it exits first.
            thread::spawn(move || {
                let _ = child.wait();
            });
            return Ok(());
        }
        if let Some(status) = child.try_wait()? {
            // Another concurrent board can win the daemon lock before it binds.
            while Instant::now() < deadline {
                if ready() {
                    return Ok(());
                }
                thread::sleep(Duration::from_millis(50));
            }
            return Err(io::Error::other(format!(
                "daemon exited {status}; inspect its log"
            )));
        }
        if Instant::now() >= deadline {
            thread::spawn(move || {
                let _ = child.wait();
            });
            return Err(io::Error::other("daemon did not become ready within 5s"));
        }
        thread::sleep(Duration::from_millis(50));
    }
}

pub(crate) const RESTART: &str = "owner-daemon-restart";

/// A connected supervisor retires itself after its short transactions settle.
/// Holding the connected socket avoids signalling a recycled PID.
pub(crate) fn restart() -> io::Result<()> {
    let socket = ipc::socket_path()?;
    let mut client = match ipc::Client::dial(&socket, &[RESTART]) {
        Ok(c) => c,
        Err(e)
            if matches!(
                e.kind(),
                io::ErrorKind::NotFound | io::ErrorKind::ConnectionRefused
            ) =>
        {
            return ensure_daemon();
        }
        Err(e) => return Err(e),
    };
    request_restart(&mut client, &std::fs::canonicalize(ipc::state_path()?)?)?;
    ensure_daemon()
}

/// The board may restart only the owner and build it originally displayed.
pub(crate) fn restart_board(
    socket: &Path,
    endpoint: &str,
    version: &str,
    birth: (u64, u64),
) -> io::Result<()> {
    let dir = std::fs::canonicalize(ipc::state_path()?)?;
    let unchanged =
        || std::fs::metadata(socket).is_ok_and(|meta| (meta.dev(), meta.ino()) == birth);
    if crate::supervisor::endpoint(&dir) != endpoint || !unchanged() {
        return Err(io::Error::other(
            "selected supervisor is not the configured local owner",
        ));
    }
    let mut client = ipc::Client::dial(socket, &[RESTART])?;
    if client.endpoint() != endpoint || client.build_version() != version || !unchanged() {
        return Err(io::Error::other(
            "selected supervisor changed before restart",
        ));
    }
    request_restart(&mut client, &dir)?;
    if !reachable(socket, Some(endpoint))? {
        cold_start(socket, &dir, false, None)?;
    }
    let replacement = ipc::Client::dial(socket, &[])?;
    if replacement.endpoint() != endpoint
        || replacement.build_version() != option_env!("SWARM_BUILD_VERSION").unwrap_or("dev")
    {
        return Err(io::Error::other(
            "replacement supervisor did not report the selected build",
        ));
    }
    Ok(())
}

fn request_restart(client: &mut ipc::Client, dir: &Path) -> io::Result<()> {
    if !client.supports(RESTART) {
        return Err(io::Error::other(
            "running daemon requires its matching restart command",
        ));
    }
    client.request_body(
        serde_json::json!({"op":"owner_daemon_restart","state_dir":dir}),
        "ok",
    )?;
    wait_for_exit(client)
}

fn wait_for_exit(client: &mut ipc::Client) -> io::Result<()> {
    use std::io::Read;
    let mut reader =
        crate::socket::DeadlineReader(client.stream(), Instant::now() + Duration::from_secs(5));
    if reader.read(&mut [0])? != 0 {
        return Err(io::Error::other(
            "unexpected data while waiting for daemon exit",
        ));
    }
    Ok(())
}

pub(crate) const UNATTENDED: &str = "owner-idle-restart-v1";

pub(crate) fn unattended() -> u8 {
    let outcome = converge();
    let (code, reason) = outcome.unwrap_or_else(|e| (1, format!("failed: {e}")));
    eprintln!("{reason}");
    code
}

fn converge() -> io::Result<(u8, String)> {
    let root = ipc::state_path()?;
    let socket = ipc::socket_path()?;
    let lock = ipc::env_path("SWARM_DAEMON_LOCK").unwrap_or_else(|| root.join("daemon.lock"));
    let Some(lock_identity) = runtime::held_lock_identity(&lock)? else {
        return Ok((
            0,
            "converged: no daemon holds the lock, nothing to converge".into(),
        ));
    };
    let mut client = match ipc::Client::dial(&socket, &[UNATTENDED]) {
        Ok(client) => client,
        Err(error) => {
            return Ok((
                2,
                format!(
                    "deferred: the daemon holds the lock but did not answer the hello: {error}"
                ),
            ));
        }
    };
    let root = root.canonicalize()?;
    if client.endpoint() != crate::supervisor::endpoint(&root) {
        return Ok((
            2,
            "deferred: configured supervisor and state directory differ".into(),
        ));
    }
    let version = option_env!("SWARM_BUILD_VERSION").unwrap_or("dev");
    if client.build_version() == version {
        return Ok((
            0,
            format!("converged: the daemon already runs this build ({version}), nothing to do"),
        ));
    }
    if !client.supports(UNATTENDED) {
        return Ok((
            2,
            "deferred: the running supervisor cannot confirm idle restart custody".into(),
        ));
    }
    let response = client.request_body(
        serde_json::json!({"op":"owner_idle_restart","state_dir":root,"lock_identity":lock_identity,"commit":false}),
        "owner_idle_restart",
    )?;
    match ipc::field(&response, "result") {
        "deferred" => return Ok((2, format!("deferred: {}", ipc::field(&response, "reason")))),
        "ready" => {}
        _ => return Err(io::Error::other("unexpected idle restart response")),
    }
    let saved = match crate::environment::restore(&root) {
        Ok(saved) => saved,
        Err(error)
            if matches!(
                error.kind(),
                io::ErrorKind::NotFound | io::ErrorKind::UnexpectedEof
            ) =>
        {
            return Ok((3, format!("refused: {error}")));
        }
        Err(error) => return Err(error),
    };
    let pid = runtime::peer_pid(client.stream())?;
    let birth = runtime::birth(pid)?;
    // The replacement opens its log and execs successfully before it asks the old daemon to retire.
    let mut child = command(&socket, &root, Some(saved), None)?
        .args(["--replace-idle", &pid.to_string(), &birth.to_string()])
        .spawn()?;
    let deadline = Instant::now() + Duration::from_secs(12);
    loop {
        if let Ok(ready) = ipc::Client::dial(&socket, &[])
            && ready.endpoint() == client.endpoint()
            && ready.build_version() == version
            && runtime::peer_pid(ready.stream())? == child.id()
        {
            thread::spawn(move || {
                let _ = child.wait();
            });
            return Ok((
                0,
                format!("converged: daemon restarted from the saved environment ({version})"),
            ));
        }
        if let Some(status) = child.try_wait()? {
            if status.code() == Some(2) {
                return Ok((
                    2,
                    "deferred: supervisor or session custody changed before restart".into(),
                ));
            }
            return Err(io::Error::other(format!(
                "replacement exited {status}; inspect the daemon log"
            )));
        }
        if Instant::now() >= deadline {
            thread::spawn(move || {
                let _ = child.wait();
            });
            return Err(io::Error::other("replacement readiness was not confirmed"));
        }
        thread::sleep(Duration::from_millis(50));
    }
}

fn prepared_environment(root: &Path) -> io::Result<bool> {
    use std::collections::BTreeMap;
    use std::ffi::OsString;
    let saved = crate::environment::restore(root)?;
    let expected: BTreeMap<_, _> = saved
        .iter()
        .map(|entry| {
            let (key, value) = entry.split_once('=').expect("validated environment");
            (OsString::from(key), OsString::from(value))
        })
        .collect();
    let actual: BTreeMap<_, _> = env::vars_os()
        .filter(|(key, _)| {
            !matches!(
                key.to_str(),
                Some(
                    "SWARM_DAEMON_STATE"
                        | "SWARM_DAEMON_SOCK"
                        | "SWARM_DAEMON_LOCK"
                        | "SWARM_DAEMON_LOG"
                )
            )
        })
        .collect();
    if actual != expected {
        return Ok(false);
    }
    let path = ipc::env_path("SWARM_DAEMON_LOG").unwrap_or_else(|| root.join("daemon.log"));
    let log = std::fs::symlink_metadata(path)?;
    if !log.is_file() || log.nlink() != 1 || log.mode() & 0o077 != 0 || log.uid() != runtime::uid()
    {
        return Ok(false);
    }
    for fd in [io::stdout().as_fd(), io::stderr().as_fd()] {
        let open = File::from(fd.try_clone_to_owned()?).metadata()?;
        if (open.dev(), open.ino()) != (log.dev(), log.ino()) {
            return Ok(false);
        }
    }
    Ok(true)
}

pub(crate) fn replace_idle(pid: u32, birth: u64) -> io::Result<bool> {
    let root = ipc::state_path()?.canonicalize()?;
    if !prepared_environment(&root)? {
        return Ok(false);
    }
    let mut client = ipc::Client::dial(&ipc::socket_path()?, &[UNATTENDED])?;
    if client.build_version() == option_env!("SWARM_BUILD_VERSION").unwrap_or("dev")
        || !client.supports(UNATTENDED)
        || client.endpoint() != crate::supervisor::endpoint(&root)
        || runtime::peer_pid(client.stream())? != pid
        || runtime::birth(pid)? != birth
    {
        return Ok(false);
    }
    let lock = ipc::env_path("SWARM_DAEMON_LOCK").unwrap_or_else(|| root.join("daemon.lock"));
    let Some(lock_identity) = runtime::held_lock_identity(&lock)? else {
        return Ok(false);
    };
    let reply = client.request_body(
        serde_json::json!({"op":"owner_idle_restart","state_dir":root,"lock_identity":lock_identity,"commit":true}),
        "owner_idle_restart",
    )?;
    match ipc::field(&reply, "result") {
        "deferred" => Ok(false),
        "stopping" => {
            wait_for_exit(&mut client)?;
            let deadline = Instant::now() + Duration::from_secs(5);
            while runtime::held_lock_identity(&lock)?.is_some() {
                if Instant::now() >= deadline {
                    return Err(io::Error::other("old daemon lock is still held"));
                }
                thread::sleep(Duration::from_millis(20));
            }
            Ok(true)
        }
        _ => Err(io::Error::other("unexpected idle restart response")),
    }
}
