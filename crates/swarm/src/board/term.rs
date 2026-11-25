use std::io::{self, Write};
use std::mem::MaybeUninit;
use std::os::fd::RawFd;
use std::sync::atomic::{AtomicBool, Ordering};
use std::time::Duration;

pub(super) const ENTER_BOARD: &str =
    "\x1b[?1049h\x1b[?25l\x1b[?2004h\x1b[?1002h\x1b[?1006h\x1b]2;swarm\x07";
pub(super) const RESTORE: &str = concat!(
    "\x1b[0m\x1b[r\x1b[<99u\x1b[?1049l\x1b[<99u\x1b[>4m\x1b>\x1b[?25h\x1b]2;\x07",
    "\x1b[?9l\x1b[?1001l\x1b[?1000l\x1b[?1002l\x1b[?1003l\x1b[?1005l\x1b[?1006l",
    "\x1b[?1015l\x1b[?1016l\x1b[?1004l\x1b[?1007l\x1b[?2031l\x1b[?2004l\x1b[?1l",
);

static STOP: AtomicBool = AtomicBool::new(false);

extern "C" fn stop(_: libc::c_int) {
    STOP.store(true, Ordering::Relaxed);
}

pub(super) struct Terminal {
    saved: libc::termios,
    signals: Vec<(libc::c_int, libc::sigaction)>,
}

impl Terminal {
    pub(super) fn raw() -> io::Result<Self> {
        STOP.store(false, Ordering::Relaxed);
        let mut saved = MaybeUninit::uninit();
        // SAFETY: on success tcgetattr initializes the supplied termios storage.
        syscall(unsafe { libc::tcgetattr(0, saved.as_mut_ptr()) })?;
        let saved = unsafe { saved.assume_init() };
        let mut terminal = Self {
            saved,
            signals: Vec::new(),
        };
        for signal in [libc::SIGINT, libc::SIGHUP, libc::SIGTERM] {
            // SAFETY: sigaction accepts zeroed flags and an initially empty signal set.
            let mut action: libc::sigaction = unsafe { std::mem::zeroed() };
            let mut previous = MaybeUninit::uninit();
            action.sa_sigaction = stop as *const () as usize;
            unsafe {
                libc::sigemptyset(&mut action.sa_mask);
                syscall(libc::sigaction(signal, &action, previous.as_mut_ptr()))?;
                terminal.signals.push((signal, previous.assume_init()));
            }
        }
        let mut raw = saved;
        // SAFETY: both functions operate on an initialized termios and stdin is the terminal.
        unsafe {
            libc::cfmakeraw(&mut raw);
            syscall(libc::tcsetattr(0, libc::TCSANOW, &raw))?;
        }
        Ok(terminal)
    }
}

impl Drop for Terminal {
    fn drop(&mut self) {
        let mut output = io::stdout().lock();
        let _ = output.write_all(RESTORE.as_bytes());
        let _ = output.flush();
        // SAFETY: restore only the terminal and signal dispositions previously saved here.
        unsafe {
            libc::tcsetattr(0, libc::TCSANOW, &self.saved);
            for (signal, previous) in &self.signals {
                libc::sigaction(*signal, previous, std::ptr::null_mut());
            }
        }
    }
}

pub(super) fn stopped() -> bool {
    STOP.load(Ordering::Relaxed)
}

pub(super) fn size() -> (u16, u16) {
    let mut dimensions = MaybeUninit::<libc::winsize>::uninit();
    // SAFETY: TIOCGWINSZ writes the complete winsize on success.
    if unsafe { libc::ioctl(0, libc::TIOCGWINSZ, dimensions.as_mut_ptr()) } == 0 {
        let dimensions = unsafe { dimensions.assume_init() };
        if dimensions.ws_col > 0 && dimensions.ws_row > 0 {
            return (dimensions.ws_col.min(1000), dimensions.ws_row.min(1000));
        }
    }
    (80, 24)
}

fn syscall(result: libc::c_int) -> io::Result<()> {
    if result == -1 {
        Err(io::Error::last_os_error())
    } else {
        Ok(())
    }
}

pub(super) fn ready(socket: Option<RawFd>, timeout: Duration) -> io::Result<(bool, bool)> {
    let mut fds = [
        libc::pollfd {
            fd: 0,
            events: libc::POLLIN,
            revents: 0,
        },
        libc::pollfd {
            fd: socket.unwrap_or(-1),
            events: libc::POLLIN,
            revents: 0,
        },
    ];
    // SAFETY: fds contains two initialized poll descriptors and remains alive during poll.
    let result = unsafe { libc::poll(fds.as_mut_ptr(), 2, timeout.as_millis() as libc::c_int) };
    if result == -1 {
        let error = io::Error::last_os_error();
        if error.kind() != io::ErrorKind::Interrupted {
            return Err(error);
        }
    }
    Ok((fds[0].revents != 0, fds[1].revents != 0))
}

pub(super) fn input(buffer: &mut [u8]) -> io::Result<usize> {
    // SAFETY: read writes up to the length of the supplied mutable buffer.
    let n = unsafe { libc::read(0, buffer.as_mut_ptr().cast(), buffer.len()) };
    if n >= 0 {
        return Ok(n as usize);
    }
    let error = io::Error::last_os_error();
    if error.kind() == io::ErrorKind::Interrupted {
        return Ok(0);
    }
    Err(error)
}
