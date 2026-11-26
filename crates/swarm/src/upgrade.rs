//! A managed installation holds a shared lease before any command can reach a writer.
mod archive;
mod transaction;
use std::fs::{self, File, OpenOptions};
use std::io::{self, Read, Write};
use std::os::fd::AsRawFd;
use std::os::unix::fs::{MetadataExt, OpenOptionsExt};
use std::path::{Path, PathBuf};
#[cfg(target_os = "linux")]
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
        Err(error) if error.kind() == io::ErrorKind::NotFound => {
            // A damaged managed prefix must never turn an inactive slot into an unmanaged writer.
            for name in [
                "writers.lock",
                "admin.lock",
                "routes",
                "inflight",
                "effects",
            ] {
                match fs::symlink_metadata(root.join(name)) {
                    Ok(_) => return Err(invalid("managed installation marker is missing")),
                    Err(error) if error.kind() == io::ErrorKind::NotFound => {}
                    Err(error) => return Err(error),
                }
            }
            return Ok(None);
        }
        Err(error) => return Err(error),
        Ok(_) => {
            let mut version = Vec::new();
            owner_file(&marker, false)?
                .take(3)
                .read_to_end(&mut version)?;
            if version != b"2\n" {
                return Err(invalid("invalid managed installation marker"));
            }
        }
    }
    // Darwin can report the launch symlink. A switch between exec and
    // canonicalization must never grant the old image the new writer lease.
    let build = format!(
        "{}-{}",
        option_env!("SWARM_BUILD_VERSION").unwrap_or("dev"),
        option_env!("SWARM_BUILD_COMMIT").unwrap_or("dev")
    );
    if generation
        .file_name()
        .is_none_or(|name| name != build.as_str())
    {
        return Err(invalid("managed slot does not match the running build"));
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

pub(crate) struct WriterLease {
    _lock: File,
    unfinished: Option<PathBuf>,
}

impl Drop for WriterLease {
    fn drop(&mut self) {
        if std::thread::panicking() {
            return;
        }
        if let Some(marker) = self.unfinished.take() {
            let _ = fs::remove_file(&marker);
            let _ = File::open(marker.parent().unwrap()).and_then(|dir| dir.sync_all());
        }
    }
}

pub(crate) fn writer(args: &[std::ffi::OsString]) -> io::Result<Option<WriterLease>> {
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
    let role = args.first().and_then(|arg| arg.to_str());
    let second = args.get(1).and_then(|arg| arg.to_str());
    let supervised_effect = matches!(
        role,
        Some("shim" | "schedule-receipt-bridge" | "schedule-agy-worker")
    ) || matches!(
        (role, second),
        (Some("discord"), Some("run")) | (Some("schedule"), Some("tick"))
    );
    let unfinished = if supervised_effect {
        let pid = std::process::id();
        let birth = crate::supervisor::runtime::birth(pid)?;
        let path = root.join("inflight").join(format!("{pid}-{birth}"));
        let mut file = OpenOptions::new()
            .write(true)
            .create_new(true)
            .mode(0o600)
            .open(&path)?;
        file.write_all(generation.as_encoded_bytes())?;
        file.sync_all()?;
        File::open(path.parent().unwrap())?.sync_all()?;
        Some(path)
    } else {
        None
    };
    Ok(Some(WriterLease {
        _lock: lock,
        unfinished,
    }))
}

pub(crate) fn launcher() -> io::Result<PathBuf> {
    match identify()? {
        Some((root, _)) => Ok(root.join("swarm")),
        None => std::env::current_exe(),
    }
}

const USAGE: &str = "usage: swarm upgrade [--stage|--activate|--rollback|--unattended] [--allow-downgrade] [--json] [--dist DIRECTORY]";

fn external_owner() -> (&'static str, &'static str) {
    let exe = std::env::current_exe()
        .ok()
        .and_then(|path| fs::canonicalize(path).ok());
    let Some(exe) = exe else {
        return ("unknown", "installation owner is unresolved");
    };
    let path = exe.to_string_lossy();
    if cfg!(target_os = "macos")
        && ["/Caskroom/", "/Cellar/", "/homebrew/"]
            .iter()
            .any(|part| path.contains(part))
    {
        return ("brew", "brew upgrade --cask swarm owns this installation");
    }
    for (key, suffix, owner, detail) in [
        ("CARGO_HOME", "bin", "cargo", "cargo owns this installation"),
        ("GOBIN", "", "go", "go install owns this installation"),
        ("GOPATH", "bin", "go", "go install owns this installation"),
    ] {
        if std::env::var_os(key)
            .map(PathBuf::from)
            .and_then(|path| path.join(suffix).canonicalize().ok())
            .as_deref()
            == exe.parent()
        {
            return (owner, detail);
        }
    }
    if let Some(home) = std::env::var_os("HOME").map(PathBuf::from) {
        for (relative, owner, detail) in [
            (".cargo/bin", "cargo", "cargo owns this installation"),
            ("go/bin", "go", "go install owns this installation"),
        ] {
            if home.join(relative).canonicalize().ok().as_deref() == exe.parent() {
                return (owner, detail);
            }
        }
    }
    #[cfg(target_os = "linux")]
    for (tool, flag, owner, detail) in [
        ("/usr/bin/dpkg", "-S", "dpkg", "apt owns this installation"),
        ("/usr/bin/rpm", "-qf", "rpm", "dnf owns this installation"),
    ] {
        if Command::new(tool)
            .arg(flag)
            .arg(&exe)
            .stdout(std::process::Stdio::null())
            .stderr(std::process::Stdio::null())
            .status()
            .is_ok_and(|status| status.success())
        {
            return (owner, detail);
        }
    }
    (
        "unknown",
        "use the installer, package manager, or Cargo that owns this binary",
    )
}

pub(crate) fn run(args: &[std::ffi::OsString]) -> u8 {
    const BOOL: &[&str] = &[
        "stage",
        "activate",
        "rollback",
        "unattended",
        "allow-downgrade",
        "json",
        "check-media",
    ];
    let Some(args) = args
        .iter()
        .map(|arg| arg.to_str().map(str::to_owned))
        .collect::<Option<Vec<_>>>()
    else {
        eprintln!("{USAGE}");
        return 2;
    };
    let operation = args
        .first()
        .map(String::as_str)
        .filter(|name| matches!(*name, "status" | "stage" | "activate" | "rollback"));
    let args = if operation.is_some() {
        &args[1..]
    } else {
        &args[..]
    };
    let mut set = std::collections::BTreeSet::new();
    let mut dist = crate::ipc::env_path("SWARM_UPGRADE_DIST");
    let names = [BOOL, &["dist"]].concat();
    let parsed = crate::flags::options(args, &names, BOOL, |name, value| {
        if name == "dist" {
            dist = Some(PathBuf::from(value));
        } else if crate::flags::parse_bool(name, value)? {
            set.insert(name.to_owned());
        } else {
            set.remove(name);
        }
        Ok(())
    });
    match parsed {
        Ok([]) => {}
        Err(problem) => {
            if !problem.is_empty() {
                eprintln!("{problem}");
            }
            eprintln!("{USAGE}");
            return 2;
        }
        _ => {
            eprintln!("{USAGE}");
            return 2;
        }
    }
    let step = ["rollback", "activate", "unattended", "stage"]
        .into_iter()
        .find(|name| set.contains(*name))
        .or(operation)
        .unwrap_or("check");
    let result = (|| -> io::Result<u8> {
        if std::env::var_os("SWARM_UPGRADE_HANDOFF").is_some_and(|value| !value.is_empty()) {
            return Err(invalid("refusing to run inside an activation handoff"));
        }
        let Some((root, exe)) = identify()? else {
            let (owner, detail) = external_owner();
            let version = option_env!("SWARM_BUILD_VERSION").unwrap_or("dev");
            let outcome = if version == "dev" {
                "refused-dev"
            } else {
                "refused-owner"
            };
            if set.contains("json") {
                let value = if step == "check" {
                    serde_json::json!({"Action":"refuse","Reason":detail,"Latest":"","Owner":owner,"RefuseOutcome":outcome})
                } else {
                    serde_json::json!({"installed":version,"owner":owner,"outcome":outcome,"detail":detail})
                };
                println!("{value}");
            } else if step == "check" {
                println!("installed {version}, latest (unresolved): would refuse ({detail})");
            } else {
                println!("{outcome}: {detail}");
            }
            return Ok(0);
        };
        transaction::run(
            &root,
            &exe,
            transaction::Options {
                step,
                dist,
                json: set.contains("json"),
                allow_downgrade: set.contains("allow-downgrade"),
                check_media: set.contains("check-media"),
            },
        )
    })();
    result.unwrap_or_else(|error| {
        eprintln!("swarm upgrade: {error}");
        1
    })
}
