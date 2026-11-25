//! Machine-local startup consent, bound to the physical workspace and its birth time.
use sha2::{Digest, Sha256};
use std::env;
use std::ffi::{OsStr, OsString};
use std::fs::{self, DirBuilder, OpenOptions};
use std::io::{self, Read, Write};
use std::os::unix::ffi::OsStrExt;
use std::os::unix::fs::{DirBuilderExt, MetadataExt, OpenOptionsExt};
use std::path::{Component, Path, PathBuf};
use std::time::{SystemTime, UNIX_EPOCH};

const USAGE: &str = "usage: swarm setup-trust on /absolute/workspace [--profile-hooks] | off|status /absolute/workspace";
const PRIVATE: &str = "setup trust: config directory must be private and owned by you";
const IDENTITY: &str =
    "setup trust: workspace must be a directory on a filesystem that records its creation time";

fn user_config() -> io::Result<PathBuf> {
    #[cfg(target_os = "linux")]
    if let Some(xdg) = env::var_os("XDG_CONFIG_HOME").filter(|s| !s.is_empty()) {
        let dir = PathBuf::from(xdg);
        if !dir.is_absolute() {
            return Err(io::Error::other("path in $XDG_CONFIG_HOME is relative"));
        }
        return Ok(dir);
    }
    let home = env::var_os("HOME")
        .filter(|s| !s.is_empty())
        .ok_or_else(|| {
            io::Error::other(if cfg!(target_os = "macos") {
                "$HOME is not defined"
            } else {
                "neither $XDG_CONFIG_HOME nor $HOME are defined"
            })
        })?;
    Ok(PathBuf::from(home).join(if cfg!(target_os = "macos") {
        "Library/Application Support"
    } else {
        ".config"
    }))
}

fn private(meta: &fs::Metadata) -> bool {
    // SAFETY: geteuid has no arguments or preconditions.
    meta.mode() & 0o077 == 0 && meta.uid() == unsafe { libc::geteuid() }
}

#[cfg(target_os = "macos")]
fn identity(workspace: &Path) -> io::Result<String> {
    let meta = workspace.symlink_metadata()?;
    let born = meta
        .created()
        .ok()
        .and_then(|t| t.duration_since(UNIX_EPOCH).ok())
        .filter(|t| meta.is_dir() && t.as_secs() != 0)
        .ok_or_else(|| io::Error::other(IDENTITY))?;
    let dev = meta.dev() as i32;
    Ok(format!(
        "{dev}:{}:{}:{}",
        meta.ino(),
        born.as_secs(),
        born.subsec_nanos()
    ))
}

#[cfg(target_os = "linux")]
fn identity(workspace: &Path) -> io::Result<String> {
    use std::ffi::CString;
    use std::mem::MaybeUninit;
    let name = CString::new(workspace.as_os_str().as_bytes())?;
    let mask = libc::STATX_TYPE | libc::STATX_INO | libc::STATX_BTIME;
    let mut stat = MaybeUninit::<libc::statx>::uninit();
    // SAFETY: name is NUL-terminated and stat points to a full writable statx buffer.
    let result = unsafe {
        libc::syscall(
            libc::SYS_statx,
            libc::AT_FDCWD,
            name.as_ptr(),
            libc::AT_SYMLINK_NOFOLLOW,
            mask,
            stat.as_mut_ptr(),
        )
    };
    if result != 0 {
        return Err(io::Error::last_os_error());
    }
    // SAFETY: a successful statx fills the structure; mask gates field availability.
    let stat = unsafe { stat.assume_init() };
    if stat.stx_mask & mask != mask
        || stat.stx_mode as u32 & libc::S_IFMT != libc::S_IFDIR
        || stat.stx_btime.tv_sec == 0
    {
        return Err(io::Error::other(IDENTITY));
    }
    Ok(format!(
        "{}:{}:{}:{}:{}",
        stat.stx_dev_major,
        stat.stx_dev_minor,
        stat.stx_ino,
        stat.stx_btime.tv_sec,
        stat.stx_btime.tv_nsec
    ))
}

fn consent(workspace: &Path, dir: &Path, marker: &Path) -> Option<bool> {
    let identity = identity(workspace).ok()?;
    let meta = dir.symlink_metadata().ok()?;
    if !meta.is_dir() || !private(&meta) {
        return None;
    }
    let file = OpenOptions::new()
        .read(true)
        .custom_flags(libc::O_NOFOLLOW | libc::O_NONBLOCK)
        .open(marker)
        .ok()?;
    let meta = file.metadata().ok()?;
    if !meta.is_file() || !private(&meta) {
        return None;
    }
    let mut data = Vec::new();
    file.take(256).read_to_end(&mut data).ok()?;
    if data == format!("{identity} workspace\n").as_bytes() {
        Some(false)
    } else if data == format!("{identity} profile\n").as_bytes() {
        Some(true)
    } else {
        None
    }
}

fn set(dir: &Path, marker: &Path, workspace: &Path, scope: Option<&str>) -> io::Result<()> {
    let contents = if let Some(scope) = scope {
        let id = identity(workspace)?;
        DirBuilder::new().recursive(true).mode(0o700).create(dir)?;
        Some(format!("{id} {scope}\n"))
    } else {
        None
    };
    match dir.symlink_metadata() {
        Ok(meta) if meta.is_dir() && private(&meta) => {}
        Err(e) if e.kind() == io::ErrorKind::NotFound && contents.is_none() => return Ok(()),
        _ => return Err(io::Error::other(PRIVATE)),
    }
    if let Some(data) = contents {
        let stamp = SystemTime::now()
            .duration_since(UNIX_EPOCH)
            .map_err(io::Error::other)?
            .as_nanos();
        let tmp = dir.join(format!(".enroll-{}-{stamp}", std::process::id()));
        let mut file = OpenOptions::new()
            .create_new(true)
            .write(true)
            .mode(0o600)
            .open(&tmp)?;
        let result = file
            .write_all(data.as_bytes())
            .and_then(|()| file.sync_all())
            .and_then(|()| fs::rename(&tmp, marker));
        if result.is_err() {
            let _ = fs::remove_file(&tmp);
        }
        result?;
    } else if let Err(e) = fs::remove_file(marker)
        && e.kind() != io::ErrorKind::NotFound
    {
        return Err(e);
    }
    Ok(())
}

fn physical(path: &Path) -> io::Result<PathBuf> {
    match path.canonicalize() {
        Err(e) if e.kind() == io::ErrorKind::NotFound => {
            let mut clean = PathBuf::from("/");
            for part in path.components() {
                match part {
                    Component::ParentDir => {
                        clean.pop();
                    }
                    Component::Normal(name) => clean.push(name),
                    _ => {}
                }
            }
            Ok(clean)
        }
        result => result,
    }
}

fn report(path: &Path, state: &str, suffix: &str) -> io::Result<()> {
    let mut out = io::stdout().lock();
    write!(out, "Automatic startup trust is {state} for ")?;
    out.write_all(path.as_os_str().as_bytes())?;
    writeln!(out, ".{suffix}")
}

fn execute(action: &OsStr, path: &Path, profile: bool) -> io::Result<()> {
    let workspace = physical(path)?;
    let dir = user_config()?.join("swarm/setup-trust");
    let marker = dir.join(format!(
        "{:x}",
        Sha256::digest(workspace.as_os_str().as_bytes())
    ));
    if action == "off" {
        set(&dir, &marker, &workspace, None)?;
        return report(
            &workspace,
            "off",
            " Earlier approvals remain in the provider.",
        );
    }
    if action == "on" {
        set(
            &dir,
            &marker,
            &workspace,
            Some(if profile { "profile" } else { "workspace" }),
        )?;
    }
    if let Some(profile) = consent(&workspace, &dir, &marker) {
        report(
            &workspace,
            "on",
            " Codex saves folder and project hook approvals in its profile.",
        )?;
        if profile {
            println!(
                "Codex also saves approvals for new or changed user and plugin hooks; they apply wherever that profile loads them."
            );
        }
    } else {
        report(path, "off", "")?;
    }
    Ok(())
}

pub fn run(args: &[OsString]) -> u8 {
    let profile = matches!(args, [on, _, flag] if on == "on" && flag == "--profile-hooks");
    if args.len() != 2 && !profile
        || !matches!(args[0].to_str(), Some("status" | "on" | "off"))
        || !Path::new(&args[1]).is_absolute()
    {
        eprintln!("{USAGE}");
        return 2;
    }
    if let Err(e) = execute(&args[0], Path::new(&args[1]), profile) {
        if args[0] == "status" {
            return u8::from(report(Path::new(&args[1]), "off", "").is_err());
        }
        eprintln!("{e}");
        return 1;
    }
    0
}
