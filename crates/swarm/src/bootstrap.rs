//! Interactive board auto-start. Scripting commands only dial an existing daemon.
use crate::{ipc, supervisor::runtime};
use std::env;
use std::fs::OpenOptions;
use std::io;
use std::os::unix::fs::{MetadataExt, OpenOptionsExt};
use std::os::unix::process::CommandExt;
use std::process::{Command, Stdio};
use std::thread;
use std::time::{Duration, Instant};

pub(crate) fn ensure_daemon() -> io::Result<()> {
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
    runtime::private_dir(&root)?;
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
    cmd.arg("daemon")
        .env("SWARM_DAEMON_STATE", &root)
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
    let mut child = cmd.spawn()?;
    let deadline = Instant::now() + Duration::from_secs(5);
    loop {
        if dial().is_ok() {
            // A long-running board must reap its own daemon if it exits first.
            thread::spawn(move || {
                let _ = child.wait();
            });
            return Ok(());
        }
        if let Some(status) = child.try_wait()? {
            // Another concurrent board can win the daemon lock before it binds.
            while Instant::now() < deadline {
                if dial().is_ok() {
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
    use std::io::Read;
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
    let mut reader =
        crate::socket::DeadlineReader(client.stream(), Instant::now() + Duration::from_secs(5));
    if reader.read(&mut [0])? != 0 {
        return Err(io::Error::other(
            "unexpected data while waiting for daemon exit",
        ));
    }
    ensure_daemon()
}
