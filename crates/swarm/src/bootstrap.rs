//! Interactive board auto-start. Scripting commands only dial an existing daemon.
use crate::{ipc, supervisor::runtime};
use std::env;
use std::fs::OpenOptions;
use std::io;
use std::os::unix::fs::{MetadataExt, OpenOptionsExt};
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
    let dial = || ipc::Client::dial(&socket, &[]);
    match dial() {
        Ok(_) => return Ok(()),
        Err(e)
            if matches!(
                e.kind(),
                io::ErrorKind::NotFound | io::ErrorKind::ConnectionRefused
            ) => {}
        Err(e) => return Err(e),
    }
    let root = ipc::state_path()?;
    let saved = if unattended {
        Some(crate::environment::restore(&root)?)
    } else {
        None
    };
    let child = command(&socket, &root, saved)?.spawn()?;
    wait_for_start(child, || ipc::Client::dial(&socket, &[]).is_ok())
}

fn command(socket: &Path, root: &Path, saved: Option<Vec<String>>) -> io::Result<Command> {
    runtime::private_dir(root)?;
    let log_path = ipc::env_path("SWARM_DAEMON_LOG").unwrap_or_else(|| root.join("daemon.log"));
    let log = OpenOptions::new()
        .append(true)
        .create(true)
        .mode(0o600)
        .custom_flags(libc::O_NOFOLLOW | libc::O_NONBLOCK)
        .open(log_path)?;
    let meta = log.metadata()?;
    // SAFETY: geteuid has no arguments or preconditions.
    if !meta.is_file()
        || meta.mode() & 0o077 != 0
        || meta.uid() != unsafe { libc::geteuid() }
        || meta.nlink() != 1
    {
        return Err(io::Error::other(
            "daemon log must be a private regular file owned by you",
        ));
    }
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
    cmd.arg("daemon")
        .env("SWARM_DAEMON_STATE", root)
        .stdin(Stdio::null())
        .stdout(log.try_clone()?)
        .stderr(log);
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
    if !client.supports(RESTART) {
        return Err(io::Error::other(
            "running daemon requires its matching restart command",
        ));
    }
    let dir = std::fs::canonicalize(ipc::state_path()?)?;
    client.request_body(
        serde_json::json!({"op":"owner_daemon_restart","state_dir":dir}),
        "ok",
    )?;
    wait_for_exit(&mut client)?;
    ensure_daemon()
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
    if !runtime::lock_held(&lock)? {
        return Ok((
            0,
            "converged: no daemon holds the lock, nothing to converge".into(),
        ));
    }
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
        serde_json::json!({"op":"owner_idle_restart","state_dir":root,"commit":false}),
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
    let mut child = command(&socket, &root, Some(saved))?
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

pub(crate) fn replace_idle(pid: u32, birth: u64) -> io::Result<bool> {
    let root = ipc::state_path()?.canonicalize()?;
    let mut client = ipc::Client::dial(&ipc::socket_path()?, &[UNATTENDED])?;
    if !client.supports(UNATTENDED)
        || client.endpoint() != crate::supervisor::endpoint(&root)
        || runtime::peer_pid(client.stream())? != pid
        || runtime::birth(pid)? != birth
    {
        return Ok(false);
    }
    let reply = client.request_body(
        serde_json::json!({"op":"owner_idle_restart","state_dir":root,"commit":true}),
        "owner_idle_restart",
    )?;
    match ipc::field(&reply, "result") {
        "deferred" => Ok(false),
        "stopping" => {
            wait_for_exit(&mut client)?;
            let lock =
                ipc::env_path("SWARM_DAEMON_LOCK").unwrap_or_else(|| root.join("daemon.lock"));
            let deadline = Instant::now() + Duration::from_secs(5);
            while runtime::lock_held(&lock)? {
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
