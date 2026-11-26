use super::{Item, LOG_LIMIT, files::Directory, parent};
use crate::ipc;
use rustix::fs::{Mode, OFlags, openat};
use std::ffi::OsString;
use std::fs::File;
use std::io;
use std::os::fd::AsFd;
use std::os::unix::fs::{MetadataExt, PermissionsExt};
use std::path::{Path, PathBuf};
use std::time::{Duration, Instant};

pub(crate) struct DiagnosticLog {
    dir: Directory,
    name: OsString,
    file: File,
    next: Instant,
}
impl DiagnosticLog {
    pub(crate) fn path(root: &Path) -> PathBuf {
        ipc::env_path("SWARM_DAEMON_LOG").unwrap_or_else(|| root.join("daemon.log"))
    }
    pub(crate) fn open_owner(path: &Path) -> io::Result<Self> {
        let (dir, name) = parent(path)?;
        let flags = OFlags::WRONLY | OFlags::APPEND;
        let file = match dir.open_file(name, flags) {
            Ok(file) => file,
            Err(e) if e.kind() == io::ErrorKind::NotFound => {
                match openat(
                    &dir.file,
                    name,
                    flags
                        | OFlags::CREATE
                        | OFlags::EXCL
                        | OFlags::CLOEXEC
                        | OFlags::NOFOLLOW
                        | OFlags::NONBLOCK,
                    Mode::RUSR | Mode::WUSR,
                ) {
                    Ok(fd) => File::from(fd),
                    Err(rustix::io::Errno::EXIST) => dir.open_file(name, flags)?,
                    Err(e) => return Err(e.into()),
                }
            }
            Err(e) => return Err(e),
        };
        let log = Self::from_parts(dir, name.into(), file);
        let meta = log.dir.verify_file(&log.name, &log.file)?;
        if meta.mode() & 0o077 != 0 {
            // The Go opener hardens a proven owned log on startup. Preview never changes its mode.
            log.file
                .set_permissions(std::fs::Permissions::from_mode(0o600))?;
        }
        Ok(log)
    }
    fn from_parts(dir: Directory, name: OsString, file: File) -> Self {
        Self {
            dir,
            name,
            file,
            next: Instant::now(),
        }
    }
    pub(crate) fn from_stderr(path: &Path) -> io::Result<Self> {
        let (dir, name) = parent(path)?;
        let file = File::from(io::stderr().as_fd().try_clone_to_owned()?);
        dir.verify_file(name, &file)?;
        Ok(Self::from_parts(dir, name.into(), file))
    }
    pub(crate) fn output(&self) -> io::Result<File> {
        self.file.try_clone()
    }
    fn trim(&self) -> io::Result<u64> {
        let meta = self.dir.verify_file(&self.name, &self.file)?;
        if meta.len() > LOG_LIMIT {
            self.file.set_len(0)?;
            Ok(meta.len())
        } else {
            Ok(0)
        }
    }
    pub(crate) fn tick(&mut self) {
        let now = Instant::now();
        if now >= self.next {
            if let Err(e) = self.trim() {
                eprintln!("diagnostic log retention: {e}");
            }
            self.next = now + Duration::from_secs(5);
        }
    }
    pub(super) fn inspect(path: &Path, apply: bool, item: &mut Item) -> io::Result<()> {
        let (dir, name) = parent(path)?;
        let file = dir.open_file(
            name,
            if apply {
                OFlags::WRONLY | OFlags::APPEND
            } else {
                OFlags::RDONLY
            },
        )?;
        let log = Self::from_parts(dir, name.into(), file);
        let before = log.dir.verify_file(&log.name, &log.file)?;
        item.before = Some((&before).into());
        if before.len() > LOG_LIMIT {
            let bytes = if apply { log.trim()? } else { before.len() };
            item.eligible(bytes);
            if apply && bytes > 0 {
                item.changed();
            }
        }
        item.after = Some((&log.dir.verify_file(&log.name, &log.file)?).into());
        Ok(())
    }
}
