//! Deadline reads must drain a Unix peer's final buffered frame even after it exits.
use std::io::{self, Read};
use std::os::fd::AsRawFd;
use std::os::unix::net::UnixStream;
use std::time::Instant;

pub(crate) struct DeadlineReader<'a>(pub &'a UnixStream, pub Instant);

impl Read for DeadlineReader<'_> {
    fn read(&mut self, data: &mut [u8]) -> io::Result<usize> {
        if data.is_empty() {
            return Ok(0);
        }
        let mut fd = libc::pollfd {
            fd: self.0.as_raw_fd(),
            events: libc::POLLIN,
            revents: 0,
        };
        loop {
            let remaining = self.1.saturating_duration_since(Instant::now());
            if remaining.is_zero() {
                return Err(io::ErrorKind::TimedOut.into());
            }
            let millis = remaining.as_millis().clamp(1, i32::MAX as u128) as i32;
            // SAFETY: fd is live for this call; poll only writes into this entry.
            let count = unsafe { libc::poll(&mut fd, 1, millis) };
            if count == 0 {
                continue;
            }
            if count < 0 {
                let e = io::Error::last_os_error();
                if e.kind() == io::ErrorKind::Interrupted {
                    continue;
                }
                return Err(e);
            }
            // POLLHUP can accompany unread data. Avoid changing the socket's flags.
            // SAFETY: data is a writable slice and fd remains borrowed from the stream.
            let n = unsafe {
                libc::recv(
                    fd.fd,
                    data.as_mut_ptr().cast(),
                    data.len(),
                    libc::MSG_DONTWAIT,
                )
            };
            if n >= 0 {
                return Ok(n as usize);
            }
            let e = io::Error::last_os_error();
            if !matches!(
                e.kind(),
                io::ErrorKind::Interrupted | io::ErrorKind::WouldBlock
            ) {
                return Err(e);
            }
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use std::io::Write;
    use std::time::Duration;
    #[test]
    fn drains_closed_peer_and_observes_absolute_deadline() {
        let (a, mut b) = UnixStream::pair().unwrap();
        let mut r = DeadlineReader(&a, Instant::now() + Duration::from_millis(5));
        assert_eq!(
            r.read(&mut [0]).unwrap_err().kind(),
            io::ErrorKind::TimedOut
        );
        b.write_all(b"reply").unwrap();
        drop(b);
        let mut bytes = Vec::new();
        DeadlineReader(&a, Instant::now() + Duration::from_secs(1))
            .read_to_end(&mut bytes)
            .unwrap();
        assert_eq!(bytes, b"reply");
    }
}
