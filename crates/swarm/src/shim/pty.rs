pub(super) use crate::supervisor::runtime::child_exited;
use std::fs::File;
use std::io::{self, Write};
use std::os::fd::{AsRawFd, FromRawFd};
use std::os::unix::process::CommandExt;
use std::process::{Child, Command, Stdio};
use std::time::{Duration, Instant};

fn geometry(cols: u16, rows: u16) -> libc::winsize {
    libc::winsize {
        ws_col: cols,
        ws_row: rows,
        ws_xpixel: 0,
        ws_ypixel: 0,
    }
}

pub(super) fn start(mut command: Command, cols: u16, rows: u16) -> io::Result<(Child, File)> {
    let mut master = -1;
    let mut slave = -1;
    let mut size = geometry(cols, rows);
    // Both descriptor slots are written by openpty, and neither is used on failure.
    let rc = unsafe {
        libc::openpty(
            &mut master,
            &mut slave,
            std::ptr::null_mut(),
            std::ptr::null_mut(),
            std::ptr::addr_of_mut!(size),
        )
    };
    if rc == -1 {
        return Err(io::Error::last_os_error());
    }
    // The successful allocation transfers these unique descriptors to this process.
    let (master, slave) = unsafe { (File::from_raw_fd(master), File::from_raw_fd(slave)) };
    for fd in [&master, &slave] {
        // openpty has no CLOEXEC variant common to both supported OSes.
        if unsafe { libc::fcntl(fd.as_raw_fd(), libc::F_SETFD, libc::FD_CLOEXEC) } == -1 {
            return Err(io::Error::last_os_error());
        }
    }
    command
        .stdin(Stdio::from(slave.try_clone()?))
        .stdout(Stdio::from(slave.try_clone()?))
        .stderr(Stdio::from(slave));
    // Runs in the single-threaded child after stdio is remapped. Only async-signal-safe calls.
    unsafe {
        command.pre_exec(|| {
            if libc::setsid() == -1
                || libc::ioctl(libc::STDIN_FILENO, libc::TIOCSCTTY as _, 0) == -1
            {
                return Err(io::Error::last_os_error());
            }
            Ok(())
        });
    }
    // The master is nonblocking so a wedged slave cannot hold the input owner forever.
    if unsafe { libc::fcntl(master.as_raw_fd(), libc::F_SETFL, libc::O_NONBLOCK) } == -1 {
        return Err(io::Error::last_os_error());
    }
    Ok((command.spawn()?, master))
}

pub(super) fn resize(master: &File, cols: u16, rows: u16) -> io::Result<()> {
    let size = geometry(cols, rows);
    // The descriptor owns a live PTY master; TIOCSWINSZ reads the valid winsize.
    if unsafe { libc::ioctl(master.as_raw_fd(), libc::TIOCSWINSZ as _, &size) } == -1 {
        return Err(io::Error::last_os_error());
    }
    Ok(())
}

pub(super) fn wait(master: &File, events: libc::c_short, timeout: Duration) -> io::Result<bool> {
    let mut fd = libc::pollfd {
        fd: master.as_raw_fd(),
        events,
        revents: 0,
    };
    // A single initialized pollfd points to the owned master until poll returns.
    match unsafe { libc::poll(&mut fd, 1, timeout.as_millis().min(i32::MAX as u128) as i32) } {
        -1 => Err(io::Error::last_os_error()),
        0 => Ok(false),
        _ => Ok(true),
    }
}

pub(super) fn input(mut master: &File, mut bytes: &[u8]) -> io::Result<()> {
    let deadline = Instant::now() + Duration::from_millis(250);
    while !bytes.is_empty() {
        match master.write(bytes) {
            Ok(0) => return Err(io::ErrorKind::WriteZero.into()),
            Ok(written) => bytes = &bytes[written..],
            Err(err) if err.kind() == io::ErrorKind::Interrupted => continue,
            Err(err) if err.kind() == io::ErrorKind::WouldBlock => {
                let remaining = deadline.saturating_duration_since(Instant::now());
                if remaining.is_zero() || !wait(master, libc::POLLOUT, remaining)? {
                    return Err(io::ErrorKind::TimedOut.into());
                }
            }
            Err(err) => return Err(err),
        }
    }
    Ok(())
}

pub(super) fn signal_group(child: &Child, signal: i32) -> io::Result<()> {
    // The caller retains the unreaped Child. setsid in its pre-exec made its
    // PID the process group ID and the kernel cannot recycle it before wait().
    if unsafe { libc::kill(-(child.id() as i32), signal) } == -1 {
        let err = io::Error::last_os_error();
        if err.raw_os_error() != Some(libc::ESRCH) {
            #[cfg(target_os = "macos")]
            if err.raw_os_error() == Some(libc::EPERM) && zombie_only_group(child)? {
                return Ok(());
            }
            return Err(err);
        }
    }
    Ok(())
}

#[cfg(target_os = "macos")]
fn zombie_only_group(child: &Child) -> io::Result<bool> {
    if !child_exited(child)? {
        return Ok(false);
    }
    // Darwin returns EPERM when the only group member is the unreaped leader.
    let mut members = [0; 2];
    let count = unsafe {
        libc::proc_listpgrppids(
            child.id() as libc::pid_t,
            members.as_mut_ptr().cast(),
            std::mem::size_of_val(&members) as libc::c_int,
        )
    };
    Ok(count == 1 && members[0] == child.id() as libc::pid_t)
}

use std::sync::atomic::{AtomicBool, Ordering};

pub(super) static SHUTDOWN_SIGNAL: AtomicBool = AtomicBool::new(false);

extern "C" fn shutdown_handler(_: libc::c_int) {
    SHUTDOWN_SIGNAL.store(true, Ordering::Relaxed);
}

pub(super) fn arm_shutdown() -> io::Result<()> {
    // Install before the worker exists. Caught dispositions reset on its exec.
    let mut action: libc::sigaction = unsafe { std::mem::zeroed() };
    action.sa_sigaction = shutdown_handler as *const () as usize;
    unsafe {
        libc::sigemptyset(&mut action.sa_mask);
        for signal in [libc::SIGTERM, libc::SIGINT, libc::SIGHUP] {
            if libc::sigaction(signal, &action, std::ptr::null_mut()) == -1 {
                return Err(io::Error::last_os_error());
            }
        }
    }
    Ok(())
}
