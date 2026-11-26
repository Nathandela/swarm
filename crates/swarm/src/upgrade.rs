//! A managed installation holds a shared lease before any command can reach a writer.
use std::fs::{self, File, OpenOptions};
use std::io;
use std::os::fd::AsRawFd;
use std::os::unix::fs::{MetadataExt, OpenOptionsExt};
use std::path::{Path, PathBuf};
use std::process::Command;

fn invalid(reason: &str) -> io::Error {
    io::Error::other(reason)
}

pub(crate) fn identify() -> io::Result<Option<(PathBuf, PathBuf)>> {
    let exe = fs::canonicalize(std::env::current_exe()?)?;
    let Some(generation) = exe.parent() else {
        return Ok(None);
    };
    let Some(releases) = generation.parent() else {
        return Ok(None);
    };
    if releases.file_name().is_none_or(|name| name != "releases") {
        return Ok(None);
    }
    let root = releases
        .parent()
        .ok_or_else(|| invalid("invalid release root"))?;
    let marker = root.join(".swarm-managed");
    match fs::symlink_metadata(&marker) {
        Err(error) if error.kind() == io::ErrorKind::NotFound => return Ok(None),
        Err(error) => return Err(error),
        Ok(_) => drop(owner_file(&marker, false)?),
    }
    for dir in [root, releases, generation] {
        let meta = fs::symlink_metadata(dir)?;
        if !meta.is_dir()
            || meta.uid() != crate::supervisor::runtime::uid()
            || meta.mode() & 0o077 != 0
        {
            return Err(invalid(
                "managed release directories must belong to this user and be private",
            ));
        }
    }
    Ok(Some((root.into(), exe)))
}

fn owner_file(path: &Path, create: bool) -> io::Result<File> {
    let file = OpenOptions::new()
        .read(true)
        .write(create)
        .create(create)
        .mode(0o600)
        .custom_flags(libc::O_NOFOLLOW | libc::O_NONBLOCK)
        .open(path)?;
    let meta = file.metadata()?;
    if !meta.is_file()
        || meta.uid() != crate::supervisor::runtime::uid()
        || meta.mode() & 0o077 != 0
        || meta.nlink() != 1
    {
        return Err(invalid("invalid managed installation owner file"));
    }
    Ok(file)
}

pub(crate) fn writer() -> io::Result<Option<File>> {
    let Some((root, exe)) = identify()? else {
        return Ok(None);
    };
    let lock_path = root.join("writers.lock");
    let lock = owner_file(&lock_path, false)?;
    // SAFETY: flock uses the live file descriptor retained for this command's lifetime.
    if unsafe { libc::flock(lock.as_raw_fd(), libc::LOCK_SH | libc::LOCK_NB) } != 0 {
        return Err(invalid(
            "installation is changing, retry after upgrade finishes",
        ));
    }
    let disk = fs::symlink_metadata(lock_path)?;
    let held = lock.metadata()?;
    if disk.dev() != held.dev() || disk.ino() != held.ino() {
        return Err(invalid("managed installation lease was replaced"));
    }
    if fs::canonicalize(root.join("swarm"))? != exe {
        return Err(invalid(
            "release is no longer active; use the managed swarm launcher",
        ));
    }
    let generation = exe.parent().unwrap().file_name().unwrap();
    let marker = root.join("effects").join(generation);
    owner_file(&marker, true)?.sync_all()?;
    File::open(marker.parent().unwrap())?.sync_all()?;
    Ok(Some(lock))
}

pub(crate) fn launcher() -> io::Result<PathBuf> {
    match identify()? {
        Some((root, _)) => Ok(root.join("swarm")),
        None => std::env::current_exe(),
    }
}

pub(crate) fn run(args: &[std::ffi::OsString]) -> u8 {
    let result = (|| -> io::Result<u8> {
        if args
            .first()
            .is_some_and(|arg| arg == "--help" || arg == "-h")
        {
            println!("swarm upgrade status|stage --dist DIRECTORY|activate|rollback");
            return Ok(0);
        }
        let Some((root, exe)) = identify()? else {
            if args.is_empty() || (args.len() == 1 && args[0] == "status") {
                println!(
                    "owner: external; use the installer, package manager, or Cargo that owns this binary"
                );
                return Ok(0);
            }
            return Err(invalid("this installation belongs to an external owner"));
        };
        let admin = exe.with_file_name("swarm-rust-install.py");
        let status = Command::new("python3")
            .arg(admin)
            .arg("upgrade")
            .arg(root)
            .arg("--caller")
            .arg(exe.parent().unwrap().file_name().unwrap())
            .args(args)
            .status()?;
        Ok(status
            .code()
            .and_then(|code| u8::try_from(code).ok())
            .unwrap_or(1))
    })();
    result.unwrap_or_else(|error| {
        eprintln!("swarm upgrade: {error}");
        1
    })
}
