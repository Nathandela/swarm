//! Keep the original Go connector ledger while moving its default routing home.
use super::config::{Config, Place, open_owner_file};
use crate::{
    checkout::state::receipts::open_bolt,
    ipc,
    supervisor::{self, runtime},
};
use bbolt_rs::{DbApi, TxApi};
use serde_json::{Value, json};
use std::io::Read;
use std::os::{
    fd::AsRawFd,
    unix::fs::{FileTypeExt, MetadataExt},
};
use std::{
    fs, io,
    path::{Component, Path, PathBuf},
};

const SELECTOR: &str = "discord-active.json";

fn clean(path: &Path) -> bool {
    path.is_absolute() && !path.components().any(|c| c == Component::ParentDir)
}
fn path<'a>(value: &'a Value, key: &str) -> io::Result<&'a Path> {
    value[key]
        .as_str()
        .map(Path::new)
        .filter(|p| clean(p))
        .ok_or_else(|| io::Error::other("supervisor paths must be absolute and clean"))
}
fn real_endpoint(root: &Path) -> io::Result<String> {
    let canonical = fs::canonicalize(root)?;
    Ok(supervisor::endpoint(&canonical))
}
#[cfg(target_os = "linux")]
fn peer_executable(pid: u32) -> io::Result<PathBuf> {
    fs::read_link(format!("/proc/{pid}/exe"))
}
#[cfg(target_os = "macos")]
fn peer_executable(pid: u32) -> io::Result<PathBuf> {
    let mut bytes = [0u8; 4096];
    let count = unsafe {
        libc::proc_pidpath(
            pid as libc::c_int,
            bytes.as_mut_ptr().cast(),
            bytes.len() as u32,
        )
    };
    if count <= 0 {
        return Err(io::Error::last_os_error());
    }
    let n = bytes
        .iter()
        .position(|byte| *byte == 0)
        .unwrap_or(count as usize);
    Ok(PathBuf::from(
        std::str::from_utf8(&bytes[..n]).map_err(io::Error::other)?,
    ))
}
#[cfg(target_os = "linux")]
fn peer_holds_lock(pid: u32, _: &Path, lock: &fs::File) -> io::Result<bool> {
    let expected = lock.metadata()?;
    let proc = PathBuf::from(format!("/proc/{pid}"));
    for entry in fs::read_dir(proc.join("fdinfo"))?.take(4096) {
        let entry = entry?;
        let Ok(opened) = fs::metadata(proc.join("fd").join(entry.file_name())) else {
            continue;
        };
        if (opened.dev(), opened.ino()) != (expected.dev(), expected.ino()) {
            continue;
        }
        let mut info = String::new();
        fs::File::open(entry.path())?
            .take(8192)
            .read_to_string(&mut info)?;
        if info.lines().any(|line| {
            let mut fields = line.split_whitespace();
            fields.next() == Some("lock:")
                && fields.next().is_some()
                && fields.next() == Some("FLOCK")
                && fields.next() == Some("ADVISORY")
                && fields.next() == Some("WRITE")
                && fields.next().and_then(|value| value.parse().ok()) == Some(pid)
                && fields.next().is_some_and(|identity| {
                    let mut parts = identity.split(':');
                    parts.next().and_then(|n| u32::from_str_radix(n, 16).ok())
                        == Some(libc::major(expected.dev()))
                        && parts.next().and_then(|n| u32::from_str_radix(n, 16).ok())
                            == Some(libc::minor(expected.dev()))
                        && parts.next().and_then(|n| n.parse().ok()) == Some(expected.ino())
                })
                && fields.next() == Some("0")
                && fields.next() == Some("EOF")
        }) {
            return Ok(true);
        }
    }
    Ok(false)
}

#[cfg(target_os = "macos")]
fn peer_holds_lock(pid: u32, path: &Path, lock: &fs::File) -> io::Result<bool> {
    use std::os::unix::ffi::OsStrExt;
    unsafe extern "C" {
        fn proc_listpidspath(
            kind: u32,
            kind_info: u32,
            path: *const libc::c_char,
            path_flags: u32,
            buffer: *mut libc::c_void,
            size: libc::c_int,
        ) -> libc::c_int;
    }
    const PROC_ALL_PIDS: u32 = 1;
    let path = std::ffi::CString::new(path.as_os_str().as_bytes())?;
    // XNU exposes flock through F_GETLK as an anonymous file-description lock.
    // Require a whole-file writer and a single possible owner besides this probe.
    let mut conflict = libc::flock {
        l_start: 0,
        l_len: 0,
        l_pid: 0,
        l_type: libc::F_RDLCK,
        l_whence: libc::SEEK_SET as i16,
    };
    if unsafe { libc::fcntl(lock.as_raw_fd(), libc::F_GETLK, &mut conflict) } == -1 {
        return Err(io::Error::last_os_error());
    }
    if conflict.l_type != libc::F_WRLCK
        || conflict.l_start != 0
        || conflict.l_len != 0
        || conflict.l_pid != -1
    {
        return Ok(false);
    }
    let mut holders = [0i32; 4096];
    let size = size_of_val(&holders);
    let count = unsafe {
        proc_listpidspath(
            PROC_ALL_PIDS,
            0,
            path.as_ptr(),
            0,
            holders.as_mut_ptr().cast(),
            size as i32,
        )
    };
    let Ok(bytes) = usize::try_from(count) else {
        return Err(io::Error::last_os_error());
    };
    if bytes >= size || bytes % size_of::<i32>() != 0 {
        return Ok(false);
    }
    let ours = std::process::id() as i32;
    let holders = &holders[..bytes / size_of::<i32>()];
    Ok(holders.contains(&ours)
        && holders.contains(&(pid as i32))
        && holders
            .iter()
            .all(|&holder| holder == ours || holder == pid as i32))
}

pub(super) fn live(root: &Path, socket: &Path, lock: &Path) -> io::Result<(String, Vec<Value>)> {
    let (client, rows) = verified_owner(root, socket, lock, &[])?;
    Ok((client.endpoint().to_owned(), rows))
}

pub(super) fn verified_owner(
    root: &Path,
    socket: &Path,
    lock: &Path,
    capabilities: &[&str],
) -> io::Result<(ipc::Client, Vec<Value>)> {
    let endpoint = real_endpoint(root)?;
    let identity = fs::symlink_metadata(socket)?;
    if !identity.file_type().is_socket()
        || identity.uid() != unsafe { libc::geteuid() }
        || identity.mode() & 0o077 != 0
    {
        return Err(io::Error::other(
            "supervisor socket has no private owner proof",
        ));
    }
    let mut client = ipc::Client::dial(socket, capabilities)?;
    if client.endpoint() != endpoint {
        return Err(io::Error::other(
            "socket answered for a different supervisor",
        ));
    }
    let pid = runtime::peer_pid(client.stream())?;
    let birth = runtime::birth(pid)?;
    match open_owner_file(&root.join("daemon.pid")) {
        Ok(file) => {
            let mut body = String::new();
            file.take(128).read_to_string(&mut body)?;
            let parts: Vec<_> = body.split_whitespace().collect();
            if !matches!(parts.as_slice(),[p,b] if p.parse::<u32>()==Ok(pid)&&b.parse::<u64>()==Ok(birth))
            {
                return Err(io::Error::other(
                    "Go daemon PID proof disagrees with the supervisor socket",
                ));
            }
        }
        Err(e) if e.kind() == io::ErrorKind::NotFound => {
            let own = fs::metadata(std::env::current_exe()?)?;
            let peer = fs::metadata(peer_executable(pid)?)?;
            if (own.dev(), own.ino()) != (peer.dev(), peer.ino()) {
                return Err(io::Error::other(
                    "supervisor executable identity is unverified",
                ));
            }
        }
        Err(e) => return Err(e),
    }
    let lock_file = open_owner_file(lock)?;
    let expected_lock = lock_file.metadata()?;
    let rows = client.sessions()?;
    if !peer_holds_lock(pid, lock, &lock_file)? {
        return Err(io::Error::other(
            "supervisor socket peer does not hold the configured owner lock",
        ));
    }
    let current_lock = fs::symlink_metadata(lock)?;
    let after = fs::symlink_metadata(socket)?;
    if runtime::birth(pid)? != birth
        || (after.dev(), after.ino()) != (identity.dev(), identity.ino())
        || (current_lock.dev(), current_lock.ino()) != (expected_lock.dev(), expected_lock.ino())
    {
        return Err(io::Error::other(
            "supervisor owner changed during verification",
        ));
    }
    Ok((client, rows))
}

struct Plan {
    cfg: Config,
    home: PathBuf,
    original: PathBuf,
}

fn selection(root: &Path) -> io::Result<Option<PathBuf>> {
    match runtime::read_json(&root.join(SELECTOR)) {
        Ok(value) => value
            .as_str()
            .map(PathBuf::from)
            .filter(|path| clean(path))
            .map(Some)
            .ok_or_else(|| io::Error::other("malformed original connector selector")),
        Err(e) if e.kind() == io::ErrorKind::NotFound => Ok(None),
        Err(e) => Err(e),
    }
}
fn missing(path: &Path) -> io::Result<bool> {
    match fs::symlink_metadata(path) {
        Ok(_) => Ok(false),
        Err(e) if e.kind() == io::ErrorKind::NotFound => Ok(true),
        Err(e) => Err(e),
    }
}
fn selected_place(root: &Path, place: &Path) -> io::Result<bool> {
    match selection(root)? {
        Some(selected) => Ok(fs::canonicalize(selected)? == place),
        None => Ok(false),
    }
}

fn plan(place: &Place, explicit: [Option<&str>; 4]) -> io::Result<Plan> {
    let mut cfg = place.load()?;
    if !cfg.enabled() || !cfg.managed() {
        return Err(io::Error::other(
            "migration requires an enabled teammate or personal connector",
        ));
    }
    if missing(&place.dir.join("discord.db"))?
        || !missing(&place.dir.join("rust-text-v1.json"))?
        || !missing(&place.dir.join(".rust-offline-owner.json"))?
    {
        return Err(io::Error::other(
            "native Rust discussion routing requires a separate migration",
        ));
    }
    let directory = fs::canonicalize(&place.dir)?;
    let home = fs::canonicalize(ipc::state_path()?)?;
    let socket = ipc::socket_path()?;
    let lock = ipc::env_path("SWARM_DAEMON_LOCK").unwrap_or_else(|| home.join("daemon.lock"));
    let log = ipc::env_path("SWARM_DAEMON_LOG").unwrap_or_else(|| home.join("daemon.log"));
    if [&socket, &lock, &log].iter().any(|p| !clean(p)) {
        return Err(io::Error::other(
            "home supervisor paths must be absolute and clean",
        ));
    }
    let destination = json!({"state_dir":home,"lock":lock,"log":log});
    if !selected_place(&home, &directory)?
        && (selection(&home)?.is_some()
            || directory != home.join("discord") && !missing(&home.join("discord/config.json"))?)
    {
        return Err(io::Error::other(
            "home already selects a different connector",
        ));
    }
    let supplied = if explicit.iter().any(Option::is_some) {
        let [Some(root), Some(socket), Some(lock), Some(log)] = explicit else {
            return Err(io::Error::other("supply all four --from supervisor paths"));
        };
        let root = Path::new(root);
        if [root, Path::new(socket), Path::new(lock), Path::new(log)]
            .iter()
            .any(|p| !clean(p))
        {
            return Err(io::Error::other(
                "supply all four absolute --from supervisor paths",
            ));
        }
        Some(
            json!({"state_dir":root,"socket":socket,"lock":lock,"log":log,"endpoint":real_endpoint(root)?}),
        )
    } else {
        None
    };
    let saved_prior = &cfg.0["previous_supervisor"];
    let previous = if !saved_prior.is_null() {
        if supplied
            .as_ref()
            .is_some_and(|supplied| supplied != saved_prior)
        {
            return Err(io::Error::other(
                "supplied original supervisor differs from the saved migration",
            ));
        }
        if cfg.0["supervisor"] != destination || cfg.0["daemon_socket"] != json!(socket) {
            return Err(io::Error::other(
                "this connector already retains a previous supervisor",
            ));
        }
        saved_prior.clone()
    } else if let Some(supplied) = supplied {
        if supplied["socket"] != cfg.0["daemon_socket"]
            || !cfg.0["supervisor"].is_null()
                && ["state_dir", "lock", "log"]
                    .iter()
                    .any(|key| supplied[*key] != cfg.0["supervisor"][*key])
        {
            return Err(io::Error::other(
                "supplied original supervisor differs from enrollment",
            ));
        }
        supplied
    } else {
        let stored = &cfg.0["supervisor"];
        if stored.is_null() {
            return Err(io::Error::other(
                "earlier enrollment requires all four --from supervisor paths",
            ));
        }
        json!({"state_dir":stored["state_dir"],"socket":cfg.0["daemon_socket"],"lock":stored["lock"],
            "log":stored["log"],"endpoint":real_endpoint(path(stored,"state_dir")?)?})
    };
    let original = fs::canonicalize(path(&previous, "state_dir")?)?;
    if !selected_place(&original, &directory)? {
        return Err(io::Error::other(
            "original supervisor does not select this connector",
        ));
    }
    let old_socket = path(&previous, "socket")?;
    let (actual_old, _) = live(&original, old_socket, path(&previous, "lock")?)?;
    let (actual_new, _) = live(&home, &socket, &lock)?;
    if previous["endpoint"] != actual_old {
        return Err(io::Error::other(
            "saved original supervisor endpoint disagrees with its owner",
        ));
    }
    if actual_old == actual_new || old_socket == socket {
        if original == home
            && cfg.0["supervisor"] == destination
            && cfg.0["daemon_socket"] == json!(socket)
            && saved_prior.is_null()
        {
            return Ok(Plan {
                cfg,
                home,
                original,
            });
        }
        return Err(io::Error::other(
            "conflicting supervisor socket or state identity",
        ));
    }
    cfg.0["previous_supervisor"] = previous;
    cfg.0["supervisor"] = destination;
    cfg.0["daemon_socket"] = json!(socket);
    cfg.validate()?;
    Ok(Plan {
        cfg,
        home,
        original,
    })
}

pub(super) fn migrate(
    place: &Place,
    dry_run: bool,
    explicit: [Option<&str>; 4],
) -> io::Result<bool> {
    let first = plan(place, explicit)?;
    let database = place.dir.join("discord.db");
    let (_pin, db) = open_bolt(&database, 64 << 20)?;
    let tx = db.begin().map_err(io::Error::other)?;
    for name in [
        b"requests".as_slice(),
        b"teammate_discussions",
        b"teammate_receipts",
    ] {
        if tx.bucket(name).is_none() {
            return Err(io::Error::other("unqualified original connector ledger"));
        }
    }
    if dry_run {
        println!("Verified home and original supervisor identities and connector selection.");
        return Ok(true);
    }
    let _config = place.lock()?;
    let _prior_fence =
        if first.original != first.home && first.original != fs::canonicalize(&place.dir)? {
            let file = fs::File::open(&first.original)?;
            if unsafe { libc::flock(file.as_raw_fd(), libc::LOCK_EX | libc::LOCK_NB) } != 0 {
                return Err(io::Error::other("original supervisor selection is locked"));
            }
            Some(file)
        } else {
            None
        };
    let _bridge = runtime::lock(&place.dir.join("bridge.lock"))?;
    if !missing(&place.dir.join("bridge.sock"))? {
        return Err(io::Error::other(
            "stop the original connector before changing its supervisor",
        ));
    }
    let next = plan(place, explicit)?;
    if first.cfg.0 != next.cfg.0 || first.home != next.home || first.original != next.original {
        return Err(io::Error::other("supervisor migration plan changed"));
    }
    if place.load()?.0 != next.cfg.0 {
        runtime::write_json(&place.dir.join("config.json"), &next.cfg.0)?;
    }
    fs::File::open(&place.dir)?.sync_all()?;
    // Pinned Go keeps old unset-route records on the previous supervisor.
    // Configuration first preserves that fence if selector publication is interrupted.
    runtime::write_json(
        &next.home.join(SELECTOR),
        &json!(fs::canonicalize(&place.dir)?),
    )?;
    println!(
        "New discussions will use the selected home supervisor when the connector next starts. Existing discussions keep their supervisor."
    );
    Ok(true)
}
