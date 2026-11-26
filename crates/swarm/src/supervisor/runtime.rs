mod operator;

use crate::ipc::wire;
use serde_json::Value;
use std::fs::{self, DirBuilder, File, OpenOptions};
use std::io::{self, Read, Write};
use std::os::fd::AsRawFd;
use std::os::unix::fs::{DirBuilderExt, FileTypeExt, MetadataExt, OpenOptionsExt, PermissionsExt};
use std::os::unix::net::{UnixListener, UnixStream};
use std::path::Path;
use std::time::Duration;

pub(crate) fn timestamp_now() -> String {
    time::OffsetDateTime::now_utc()
        .format(&time::format_description::well_known::Rfc3339)
        .expect("UTC timestamp")
}

pub(crate) const IO_TIMEOUT: Duration = Duration::from_secs(2);
pub(crate) const SHIM_VERSION: u64 = 1;
pub(crate) const DATA_OUT: u8 = 2;
pub(crate) const DATA_IN: u8 = 3;
pub(crate) const SNAPSHOT: u8 = 4;

pub(crate) fn private_dir(path: &Path) -> io::Result<()> {
    DirBuilder::new().mode(0o700).recursive(true).create(path)?;
    let meta = fs::symlink_metadata(path)?;
    if !meta.is_dir() || meta.mode() & 0o077 != 0 || meta.uid() != uid() {
        return Err(io::Error::other(format!(
            "{} is not a private owner directory",
            path.display()
        )));
    }
    Ok(())
}

pub(crate) fn bind(path: &Path) -> io::Result<UnixListener> {
    let listener = UnixListener::bind(path)?;
    fs::set_permissions(path, fs::Permissions::from_mode(0o600))?;
    Ok(listener)
}

pub(crate) fn reclaim_socket(path: &Path) -> io::Result<()> {
    let meta = match fs::symlink_metadata(path) {
        Ok(meta) => meta,
        Err(err) if err.kind() == io::ErrorKind::NotFound => return Ok(()),
        Err(err) => return Err(err),
    };
    if !meta.file_type().is_socket() || meta.uid() != uid() {
        return Err(io::Error::other(format!(
            "refusing foreign socket path {}",
            path.display()
        )));
    }
    match UnixStream::connect(path) {
        Err(err) if err.kind() == io::ErrorKind::ConnectionRefused => fs::remove_file(path),
        Err(err) => Err(err),
        Ok(_) => Err(io::Error::new(
            io::ErrorKind::AddrInUse,
            "owner socket already has a live listener",
        )),
    }
}

pub(crate) fn lock(path: &Path) -> io::Result<File> {
    lock_for(path, Duration::ZERO)
}

pub(crate) fn lock_for(path: &Path, timeout: Duration) -> io::Result<File> {
    let file = OpenOptions::new()
        .read(true)
        .write(true)
        .create(true)
        .mode(0o600)
        .custom_flags(libc::O_NOFOLLOW | libc::O_NONBLOCK)
        .open(path)?;
    validate_file(&file)?;
    let deadline = std::time::Instant::now() + timeout;
    loop {
        // SAFETY: flock borrows this live descriptor and never blocks.
        if unsafe { libc::flock(file.as_raw_fd(), libc::LOCK_EX | libc::LOCK_NB) } == 0 {
            return Ok(file);
        }
        let error = io::Error::last_os_error();
        if error.kind() != io::ErrorKind::WouldBlock {
            return Err(error);
        }
        let remaining = deadline.saturating_duration_since(std::time::Instant::now());
        if remaining.is_zero() {
            return Err(io::Error::new(
                error.kind(),
                "daemon: another instance is already running",
            ));
        }
        std::thread::sleep(Duration::from_millis(10).min(remaining));
    }
}

pub(crate) fn held_lock_identity(path: &Path) -> io::Result<Option<[u64; 2]>> {
    let file = match OpenOptions::new()
        .read(true)
        .custom_flags(libc::O_NOFOLLOW | libc::O_NONBLOCK)
        .open(path)
    {
        Ok(file) => file,
        Err(error) if error.kind() == io::ErrorKind::NotFound => return Ok(None),
        Err(error) => return Err(error),
    };
    validate_file(&file)?;
    // SAFETY: the live descriptor is released at the end of this read-only probe.
    if unsafe { libc::flock(file.as_raw_fd(), libc::LOCK_EX | libc::LOCK_NB) } == 0 {
        return Ok(None);
    }
    let error = io::Error::last_os_error();
    if error.kind() == io::ErrorKind::WouldBlock {
        let meta = file.metadata()?;
        Ok(Some([meta.dev(), meta.ino()]))
    } else {
        Err(error)
    }
}

pub(crate) fn uid() -> u32 {
    // geteuid requires no pointers and has no process side effects.
    unsafe { libc::geteuid() }
}

pub(super) fn validate_file(file: &File) -> io::Result<()> {
    let meta = file.metadata()?;
    if !meta.is_file() || meta.mode() & 0o077 != 0 || meta.uid() != uid() || meta.nlink() != 1 {
        return Err(io::Error::other(
            "state file is not a private regular owner file",
        ));
    }
    Ok(())
}

pub(crate) fn read_json(path: &Path) -> io::Result<Value> {
    decode_json(open_private(path)?)
}

fn open_private(path: &Path) -> io::Result<File> {
    OpenOptions::new()
        .read(true)
        .custom_flags(libc::O_NOFOLLOW | libc::O_NONBLOCK)
        .open(path)
}

pub(super) fn read_private(path: &Path, max: usize) -> io::Result<Vec<u8>> {
    read_private_file(open_private(path)?, max)
}

pub(super) fn decode_json(file: File) -> io::Result<Value> {
    Ok(serde_json::from_slice(&read_private_file(file, 1 << 20)?)?)
}

pub(super) fn read_private_file(file: File, max: usize) -> io::Result<Vec<u8>> {
    validate_file(&file)?;
    let mut payload = Vec::new();
    file.take(max as u64 + 1).read_to_end(&mut payload)?;
    if payload.len() > max {
        return Err(io::Error::other("state file exceeds size limit"));
    }
    Ok(payload)
}

pub(crate) fn write_json(path: &Path, data: &Value) -> io::Result<()> {
    write_atomic(path, |file| {
        serde_json::to_writer(&mut *file, data)?;
        file.write_all(b"\n")
    })
}

pub(crate) fn write_atomic(
    path: &Path,
    write: impl FnOnce(&mut File) -> io::Result<()>,
) -> io::Result<()> {
    let parent = path
        .parent()
        .ok_or_else(|| io::Error::other("state path missing parent"))?;
    let temp = parent.join(format!(".save-{}", random_id()?));
    let result = (|| {
        let mut file = OpenOptions::new()
            .create_new(true)
            .write(true)
            .mode(0o600)
            .open(&temp)?;
        write(&mut file)?;
        file.sync_all()?;
        fs::rename(&temp, path)?;
        File::open(parent)?.sync_all()
    })();
    if result.is_err() {
        let _ = fs::remove_file(temp);
    }
    result
}

pub(crate) fn random_id() -> io::Result<String> {
    let mut bytes = [0; 12];
    File::open("/dev/urandom")?.read_exact(&mut bytes)?;
    Ok(bytes.iter().map(|byte| format!("{byte:02x}")).collect())
}

pub(crate) fn valid_id(id: &str) -> bool {
    id.len() == 24
        && id
            .bytes()
            .all(|byte| byte.is_ascii_digit() || (b'a'..=b'f').contains(&byte))
}

pub(crate) fn read_control(reader: &mut impl Read) -> io::Result<Value> {
    let (kind, bytes) = wire::read_frame(reader)?;
    if kind != wire::CONTROL {
        return Err(io::Error::other("expected a control frame"));
    }
    Ok(serde_json::from_slice(&bytes)?)
}

pub(crate) fn write_control(writer: &mut impl Write, control: &Value) -> io::Result<()> {
    wire::write_frame(writer, wire::CONTROL, &serde_json::to_vec(control)?)
}

pub(crate) fn dims(value: &Value) -> (u16, u16) {
    let dimension = |key, default| match value[key].as_u64() {
        Some(n) if n > 0 => n.min(300) as u16,
        _ => default,
    };
    (dimension("cols", 80), dimension("rows", 24))
}

#[cfg(target_os = "macos")]
pub(crate) fn birth(pid: u32) -> io::Result<u64> {
    let size = size_of::<libc::proc_bsdinfo>();
    let mut info = std::mem::MaybeUninit::<libc::proc_bsdinfo>::uninit();
    // proc_pidinfo fills precisely the provided native structure on success.
    let received = unsafe {
        libc::proc_pidinfo(
            pid as i32,
            libc::PROC_PIDTBSDINFO,
            0,
            info.as_mut_ptr().cast(),
            size as i32,
        )
    };
    if received != size as i32 {
        return Err(io::Error::last_os_error());
    }
    let info = unsafe { info.assume_init() };
    Ok(info.pbi_start_tvsec * 1_000_000 + info.pbi_start_tvusec)
}

#[cfg(target_os = "linux")]
pub(crate) fn birth(pid: u32) -> io::Result<u64> {
    let stat = fs::read_to_string(format!("/proc/{pid}/stat"))?;
    stat.rsplit_once(") ")
        .and_then(|(_, fields)| fields.split_whitespace().nth(19))
        .and_then(|start| start.parse().ok())
        .ok_or_else(|| io::Error::other("could not read process birth ticks"))
}

pub(crate) fn terminal_peer(socket: &UnixStream, route: &str, observe: bool) -> io::Result<Value> {
    let pid = peer_pid(socket)?;
    let born = birth(pid)?;
    let (parent, group, tty, foreground) = job_control(pid)?;
    let session = unsafe { libc::getsid(pid as i32) };
    if session <= 0
        || parent <= 1
        || birth(pid)? != born
        || !matches!(route, "pipe" | "tty")
        || route == "tty" && (group != pid || foreground != pid || tty == 0 || tty == u32::MAX)
    {
        return Err(io::Error::other("permission operator context is unproved"));
    }
    let output = observe
        .then(|| operator::stdout(pid, route, tty))
        .transpose()?;
    let parent_birth = birth(parent)?;
    if !operator::matches_image(pid)? || birth(pid)? != born {
        return Err(io::Error::other(
            "permission operator executable is unproved",
        ));
    }
    Ok(
        serde_json::json!({"scope":[parent,parent_birth,session,route,if route == "tty" {tty} else {0}],"born":born,"pid":pid,"output":output}),
    )
}

#[cfg(target_os = "macos")]
pub(crate) fn birth_clock() -> io::Result<u64> {
    u64::try_from(
        std::time::SystemTime::now()
            .duration_since(std::time::UNIX_EPOCH)
            .map_err(io::Error::other)?
            .as_micros(),
    )
    .map_err(io::Error::other)
}

#[cfg(target_os = "linux")]
pub(crate) fn birth_clock() -> io::Result<u64> {
    let mut since_boot = libc::timespec {
        tv_sec: 0,
        tv_nsec: 0,
    };
    let ticks = unsafe { libc::sysconf(libc::_SC_CLK_TCK) };
    if ticks <= 0 || unsafe { libc::clock_gettime(libc::CLOCK_BOOTTIME, &mut since_boot) } != 0 {
        return Err(io::Error::other("could not read process birth clock"));
    }
    Ok(since_boot.tv_sec as u64 * ticks as u64
        + since_boot.tv_nsec as u64 * ticks as u64 / 1_000_000_000)
}

#[cfg(target_os = "macos")]
pub(crate) fn job_control(pid: u32) -> io::Result<(u32, u32, u32, u32)> {
    let size = size_of::<libc::proc_bsdinfo>();
    let mut info = std::mem::MaybeUninit::<libc::proc_bsdinfo>::uninit();
    let received = unsafe {
        libc::proc_pidinfo(
            pid as i32,
            libc::PROC_PIDTBSDINFO,
            0,
            info.as_mut_ptr().cast(),
            size as i32,
        )
    };
    if received != size as i32 {
        return Err(io::Error::last_os_error());
    }
    let info = unsafe { info.assume_init() };
    Ok((info.pbi_ppid, info.pbi_pgid, info.e_tdev, info.e_tpgid))
}

#[cfg(target_os = "linux")]
pub(crate) fn job_control(pid: u32) -> io::Result<(u32, u32, u32, u32)> {
    let stat = fs::read_to_string(format!("/proc/{pid}/stat"))?;
    let fields: Vec<_> = stat
        .rsplit_once(") ")
        .map(|(_, fields)| fields.split_whitespace().collect())
        .unwrap_or_default();
    let field = |index: usize| -> io::Result<i64> {
        fields
            .get(index)
            .and_then(|field| field.parse().ok())
            .ok_or_else(|| io::Error::other("could not read process terminal context"))
    };
    Ok((
        field(1)? as u32,
        field(2)? as u32,
        field(4)? as u32,
        field(5)? as u32,
    ))
}

pub(crate) fn child_exited(child: &std::process::Child) -> io::Result<bool> {
    // waitid with WNOWAIT observes without reaping, so group ownership cannot
    // transfer via PID recycling before the final group signal.
    let mut status: libc::siginfo_t = unsafe { std::mem::zeroed() };
    let rc = unsafe {
        libc::waitid(
            libc::P_PID,
            child.id() as libc::id_t,
            &mut status,
            libc::WEXITED | libc::WNOHANG | libc::WNOWAIT,
        )
    };
    if rc == -1 {
        let err = io::Error::last_os_error();
        if err.kind() == io::ErrorKind::Interrupted {
            return Ok(false);
        }
        return Err(err);
    }
    // Darwin reports a stopped child even under WEXITED.
    Ok(unsafe { status.si_pid() } != 0
        && matches!(
            status.si_code,
            libc::CLD_EXITED | libc::CLD_KILLED | libc::CLD_DUMPED
        ))
}

fn socket_option<T>(socket: &UnixStream, level: i32, name: i32) -> io::Result<T> {
    let mut value = std::mem::MaybeUninit::<T>::uninit();
    let mut length = size_of::<T>() as libc::socklen_t;
    // Callers choose an option whose C value has exactly type T.
    let rc = unsafe {
        libc::getsockopt(
            socket.as_raw_fd(),
            level,
            name,
            value.as_mut_ptr().cast(),
            &mut length,
        )
    };
    if rc == -1 {
        return Err(io::Error::last_os_error());
    }
    if length as usize != size_of::<T>() {
        return Err(io::Error::other("unexpected Unix peer credential length"));
    }
    Ok(unsafe { value.assume_init() })
}

#[cfg(target_os = "macos")]
pub(crate) fn peer_pid(socket: &UnixStream) -> io::Result<u32> {
    let pid = socket_option::<libc::pid_t>(socket, libc::SOL_LOCAL, libc::LOCAL_PEERPID)?;
    Ok(pid as u32)
}

#[cfg(target_os = "linux")]
pub(crate) fn peer_pid(socket: &UnixStream) -> io::Result<u32> {
    let credentials = socket_option::<libc::ucred>(socket, libc::SOL_SOCKET, libc::SO_PEERCRED)?;
    Ok(credentials.pid as u32)
}

pub(crate) fn launch_env(supplied: &Value) -> Vec<(String, String)> {
    let allowed = |key: &str| !key.contains('\0') && crate::environment::child_key(key);
    if let Some(items) = supplied.as_array() {
        return items
            .iter()
            .filter_map(Value::as_str)
            .filter_map(|item| item.split_once('='))
            .filter(|(key, value)| allowed(key) && !value.contains('\0'))
            .map(|(key, value)| (key.to_owned(), value.to_owned()))
            .collect();
    }
    if supplied.is_null() {
        return std::env::vars_os()
            .filter_map(|(key, value)| Some((key.into_string().ok()?, value.into_string().ok()?)))
            .filter(|(key, _)| allowed(key))
            .collect();
    }
    Vec::new()
}

#[cfg(test)]
mod tests {
    use super::*;
    use serde_json::json;

    #[test]
    fn paused_child_remains_owned_until_it_exits() {
        use std::process::Command;
        use std::time::{Duration, Instant};

        let mut child = Command::new("/bin/sh")
            .args(["-c", "kill -STOP $$; exit 17"])
            .spawn()
            .expect("start paused fixture child");
        let pid = child.id();
        let deadline = Instant::now() + Duration::from_secs(2);
        let mut paused = false;
        while Instant::now() < deadline {
            let mut status: libc::siginfo_t = unsafe { std::mem::zeroed() };
            let rc = unsafe {
                libc::waitid(
                    libc::P_PID,
                    pid as libc::id_t,
                    &mut status,
                    libc::WSTOPPED | libc::WNOHANG | libc::WNOWAIT,
                )
            };
            if rc == 0 && status.si_code == libc::CLD_STOPPED {
                paused = true;
                break;
            }
            std::thread::sleep(Duration::from_millis(5));
        }
        let before = super::child_exited(&child);
        let resumed = unsafe { libc::kill(pid as libc::pid_t, libc::SIGCONT) } == 0;
        if !paused || !resumed {
            let _ = child.kill();
        }
        let status = child.wait().expect("reap fixture child");
        assert!(paused && resumed);
        assert!(!before.expect("observe paused fixture"));
        assert_eq!(status.code(), Some(17));
    }

    #[test]
    fn filters_the_launch_boundary() {
        let result = launch_env(&json!([
            "PATH=/bin",
            "LC_ALL=C",
            "ANTHROPIC_API_KEY=value=with=equals",
            "AWS_SECRET_ACCESS_KEY=private",
            "LD_PRELOAD=lib.bad",
            "CLAUDE_CODE_REMOTE=1"
        ]));
        assert_eq!(
            result,
            vec![
                ("PATH".to_owned(), "/bin".to_owned()),
                ("LC_ALL".to_owned(), "C".to_owned()),
                (
                    "ANTHROPIC_API_KEY".to_owned(),
                    "value=with=equals".to_owned()
                ),
            ]
        );
    }
}
