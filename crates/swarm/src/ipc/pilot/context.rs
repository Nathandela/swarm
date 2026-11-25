use crate::ipc::{Client, socket_path};
use crate::supervisor::runtime;
use serde_json::{Value, json};
use std::fs::{self, DirBuilder, File, OpenOptions};
use std::io;
use std::os::fd::AsRawFd;
use std::os::unix::fs::{DirBuilderExt, FileTypeExt, MetadataExt, OpenOptionsExt};
use std::os::unix::net::UnixStream;
use std::path::{Path, PathBuf};
use time::{Duration, OffsetDateTime, format_description::well_known::Rfc3339};

pub(super) struct Context {
    pub path: PathBuf,
    pub socket: PathBuf,
    pub expires: OffsetDateTime,
    endpoint: String,
    dev: u64,
    ino: u64,
    record_id: (u64, u64),
}

fn unsafe_context() -> io::Error {
    io::Error::other("context is missing or unsafe; run swarm --pilot again")
}

fn validate_dir(path: &Path) -> io::Result<()> {
    let temp = fs::canonicalize("/tmp")?;
    if !path.is_absolute()
        || !path
            .parent()
            .and_then(|p| fs::canonicalize(p).ok())
            .is_some_and(|p| p == temp)
        || !path.file_name().and_then(|p| p.to_str()).is_some_and(|p| {
            p.strip_prefix("swarm-pilot-")
                .is_some_and(|id| !id.is_empty() && id.bytes().all(|b| b.is_ascii_alphanumeric()))
        })
    {
        return Err(io::Error::other("invalid context path"));
    }
    let info = fs::symlink_metadata(path).map_err(|_| unsafe_context())?;
    // SAFETY: effective UID lookup has no memory or process side effects.
    if !info.is_dir() || info.mode() & 0o777 != 0o700 || info.uid() != unsafe { libc::geteuid() } {
        return Err(unsafe_context());
    }
    Ok(())
}

fn file(path: &Path) -> io::Result<File> {
    validate_dir(path)?;
    let file = OpenOptions::new()
        .read(true)
        .custom_flags(libc::O_NOFOLLOW | libc::O_NONBLOCK)
        .open(path.join("context.json"))
        .map_err(|_| unsafe_context())?;
    let info = file.metadata()?;
    // SAFETY: effective UID lookup has no memory or process side effects.
    if !info.is_file()
        || info.mode() & 0o777 != 0o600
        || info.uid() != unsafe { libc::geteuid() }
        || info.nlink() != 1
    {
        return Err(unsafe_context());
    }
    Ok(file)
}

fn absolute_socket() -> io::Result<PathBuf> {
    crate::ipc::spawn::absolute(&socket_path()?)
}

fn identity(path: &Path) -> io::Result<(u64, u64)> {
    let meta = fs::symlink_metadata(path)?;
    if !meta.file_type().is_socket() || meta.uid() != unsafe { libc::geteuid() } {
        return Err(io::Error::other("context daemon socket is unsafe"));
    }
    Ok((meta.dev(), meta.ino()))
}

pub(super) fn lock(path: &Path) -> io::Result<File> {
    let file = file(path)?;
    // SAFETY: the lock belongs to this live own-file descriptor until drop.
    if unsafe { libc::flock(file.as_raw_fd(), libc::LOCK_EX) } != 0 {
        return Err(io::Error::last_os_error());
    }
    let current = fs::symlink_metadata(path.join("context.json")).map_err(|_| unsafe_context())?;
    let original = file.metadata()?;
    if (current.dev(), current.ino()) != (original.dev(), original.ino()) {
        return Err(unsafe_context());
    }
    Ok(file)
}

impl Context {
    pub fn entry() -> io::Result<(Self, Client)> {
        let socket = absolute_socket()?;
        let (dev, ino) = identity(&socket)?;
        let client = Client::dial(&socket, &["journal"])?;
        if identity(&socket)? != (dev, ino) {
            return Err(io::Error::other("daemon socket changed during pilot entry"));
        }
        let path = Path::new("/tmp").join(format!("swarm-pilot-{}", runtime::random_id()?));
        DirBuilder::new().mode(0o700).create(&path)?;
        let expires = OffsetDateTime::now_utc() + Duration::hours(24);
        let endpoint = client.endpoint().to_owned();
        let result = runtime::write_json(
            &path.join("context.json"),
            &json!({
                "socket":socket,"endpoint":endpoint,"device":dev,"inode":ino,
                "expires":expires.format(&Rfc3339).map_err(io::Error::other)?,
            }),
        );
        if let Err(err) = result {
            let _ = fs::remove_dir_all(&path);
            return Err(err);
        }
        Ok((Self::read(&path)?, client))
    }

    fn read(path: &Path) -> io::Result<Self> {
        let metadata = file(path)?.metadata()?;
        let record_id = (metadata.dev(), metadata.ino());
        let raw = runtime::read_json(&path.join("context.json"))?;
        let endpoint = raw["endpoint"]
            .as_str()
            .filter(|p| !p.is_empty())
            .ok_or_else(unsafe_context)?
            .to_owned();
        let socket = PathBuf::from(
            raw["socket"]
                .as_str()
                .filter(|p| !p.is_empty())
                .ok_or_else(unsafe_context)?,
        );
        let expires = OffsetDateTime::parse(
            raw["expires"].as_str().ok_or_else(unsafe_context)?,
            &Rfc3339,
        )
        .map_err(|_| unsafe_context())?;
        let dev = raw["device"].as_u64().ok_or_else(unsafe_context)?;
        let ino = raw["inode"]
            .as_u64()
            .filter(|n| *n > 0)
            .ok_or_else(unsafe_context)?;
        Ok(Self {
            path: path.to_owned(),
            socket,
            endpoint,
            dev,
            ino,
            record_id,
            expires,
        })
    }

    pub(super) fn connect(path: &Path, caps: &[&str]) -> io::Result<(Self, Client)> {
        let ctx = Self::read(path)?;
        if absolute_socket()? != ctx.socket {
            return Err(io::Error::other("context is bound to another Swarm daemon"));
        }
        ctx.check()?;
        let client = Client::dial(&ctx.socket, caps)?;
        ctx.check()?;
        if client.endpoint() != ctx.endpoint {
            return Err(io::Error::other(
                "context daemon identity changed; run swarm --pilot again",
            ));
        }
        Ok((ctx, client))
    }

    pub(super) fn check(&self) -> io::Result<()> {
        let meta = file(&self.path)?.metadata()?;
        if (meta.dev(), meta.ino()) != self.record_id {
            return Err(unsafe_context());
        }
        if self.expires <= OffsetDateTime::now_utc() {
            return Err(io::Error::other("context expired; run swarm --pilot again"));
        }
        match fs::symlink_metadata(self.path.join("exiting")) {
            Ok(_) => return Err(io::Error::other("context exited; run swarm --pilot again")),
            Err(err) if err.kind() == io::ErrorKind::NotFound => {}
            Err(err) => return Err(err),
        }
        if identity(&self.socket).ok() != Some((self.dev, self.ino)) {
            return Err(io::Error::other(
                "context daemon socket changed; run swarm --pilot again",
            ));
        }
        Ok(())
    }

    pub(super) fn exit(path: &Path) -> io::Result<()> {
        Self::read(path)?;
        runtime::write_json(&path.join("exiting"), &Value::Bool(true))?;
        for item in fs::read_dir(path)? {
            let item = item?;
            if item.file_name().to_string_lossy().starts_with("wait-")
                && item.file_type()?.is_socket()
            {
                let _ = UnixStream::connect(item.path());
            }
        }
        let _lock = lock(path)?;
        fs::remove_dir_all(path)
    }
}
