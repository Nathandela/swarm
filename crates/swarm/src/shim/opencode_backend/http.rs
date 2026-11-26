use crate::supervisor::runtime;
use std::io::{self, Read, Write};
use std::os::unix::fs::{FileTypeExt, MetadataExt};
use std::os::unix::net::UnixStream;
use std::path::PathBuf;
use std::time::Duration;
use ureq::unversioned::{
    resolver::DefaultResolver,
    transport::{Buffers, ConnectionDetails, Connector, LazyBuffers, NextTimeout, Transport},
};

#[derive(Debug, Clone)]
pub(crate) struct LocalSocket {
    pub path: PathBuf,
    pub worker: u32,
    pub birth: u64,
}

impl LocalSocket {
    pub(crate) fn agent(&self, seconds: u64) -> ureq::Agent {
        let config = ureq::config::Config::builder()
            .proxy(None)
            .max_redirects(0)
            .http_status_as_error(false)
            .timeout_global(Some(Duration::from_secs(seconds)))
            .build();
        ureq::Agent::with_parts(config, self.clone(), DefaultResolver::default())
    }
}

impl Connector for LocalSocket {
    type Out = LocalTransport;

    fn connect(
        &self,
        details: &ConnectionDetails,
        _: Option<()>,
    ) -> Result<Option<Self::Out>, ureq::Error> {
        let metadata = self.path.symlink_metadata()?;
        if !metadata.file_type().is_socket()
            || metadata.uid() != unsafe { libc::geteuid() }
            || metadata.mode() & 0o777 != 0o600
        {
            return Err(io::Error::other("OpenCode bridge socket is not private").into());
        }
        let socket = UnixStream::connect(&self.path)?;
        if runtime::peer_pid(&socket)? != self.worker || runtime::birth(self.worker)? != self.birth
        {
            return Err(io::Error::other("OpenCode bridge peer is not the owned TUI").into());
        }
        Ok(Some(LocalTransport {
            socket,
            buffers: LazyBuffers::new(
                details.config.input_buffer_size(),
                details.config.output_buffer_size(),
            ),
        }))
    }
}

#[derive(Debug)]
pub(crate) struct LocalTransport {
    socket: UnixStream,
    buffers: LazyBuffers,
}

impl Transport for LocalTransport {
    fn buffers(&mut self) -> &mut dyn Buffers {
        &mut self.buffers
    }

    fn transmit_output(&mut self, amount: usize, timeout: NextTimeout) -> Result<(), ureq::Error> {
        let timeout = checked(timeout.for_write())?;
        self.socket
            .set_write_timeout(timeout.not_zero().map(|duration| *duration))?;
        self.socket
            .write_all(&self.buffers.output()[..amount])
            .map_err(|e| deadline(e, timeout))
    }

    fn await_input(&mut self, timeout: NextTimeout) -> Result<bool, ureq::Error> {
        let timeout = checked(timeout.for_read())?;
        self.socket
            .set_read_timeout(timeout.not_zero().map(|duration| *duration))?;
        let amount = self
            .socket
            .read(self.buffers.input_append_buf())
            .map_err(|e| deadline(e, timeout))?;
        self.buffers.input_appended(amount);
        Ok(amount != 0)
    }

    fn is_open(&mut self) -> bool {
        // Recheck peer ownership on every request.
        false
    }
}

fn deadline(error: io::Error, timeout: NextTimeout) -> ureq::Error {
    match error.kind() {
        io::ErrorKind::TimedOut | io::ErrorKind::WouldBlock => ureq::Error::Timeout(timeout.reason),
        _ => error.into(),
    }
}

fn checked(timeout: NextTimeout) -> Result<NextTimeout, ureq::Error> {
    if timeout.after.is_zero() {
        return Err(ureq::Error::Timeout(timeout.reason));
    }
    Ok(timeout)
}
