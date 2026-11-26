use super::{Process, read_at};
use crate::{ipc::field, providers::codex_receipt::canonical_thread, supervisor::runtime};
use serde_json::{Value, json};
use sha2::{Digest, Sha256};
use std::{
    fs::{self, File},
    io,
    os::unix::{
        ffi::OsStrExt,
        fs::{FileTypeExt, MetadataExt},
        net::UnixStream,
    },
    path::{Path, PathBuf},
    time::Instant,
};
use tungstenite::{Message, WebSocket, client::client_with_config, protocol::WebSocketConfig};

pub(super) fn owned_backend(
    path: &Path,
    directory: &File,
    launch: &Value,
    native: &Value,
) -> io::Result<Process> {
    let backend = read_at(directory, c"backend.json")?;
    let expected = path.join("codex.sock");
    if Path::new(field(launch, "backend_socket_path")) != expected
        || Path::new(field(&backend, "socket_path")) != expected
        || backend["pid"] != backend["pgid"]
        || backend["pid"] != native["backend_pid"]
        || backend["started_at_ms"] != native["backend_start_time"]
        || backend["pid"] == native["pid"]
        || backend["pid"] == native["shim_pid"]
        || native["runtime"] != json!({"provider":"","epoch":0,"identity":"","home":""})
    {
        return Err(io::Error::other("Go Codex backend custody disagrees"));
    }
    Process::new(&backend["pid"], &backend["started_at_ms"])
}

pub(super) fn verify_thread(dir: &Path, backend: &Process, meta: &Value) -> io::Result<()> {
    let thread_id = field(meta, "conversation_id");
    if !canonical_thread(thread_id) {
        return Err(io::Error::other("Go Codex conversation is unproved"));
    }
    let endpoint = dir.join("codex.sock");
    let uid = dir.metadata()?.uid();
    let version = field(&meta["cli_identity"], "version");
    let (path, socket) = socket_target(&endpoint, uid, version)?;
    backend.matches()?;
    let stream = UnixStream::connect(&path)?;
    let (confirmed_path, confirmed_socket) = socket_target(&endpoint, uid, version)?;
    if confirmed_path != path
        || (confirmed_socket.dev(), confirmed_socket.ino()) != (socket.dev(), socket.ino())
    {
        return Err(io::Error::other(
            "Go Codex backend socket changed during verification",
        ));
    }
    stream.set_read_timeout(Some(runtime::IO_TIMEOUT))?;
    stream.set_write_timeout(Some(runtime::IO_TIMEOUT))?;
    if runtime::peer_pid(&stream)? != backend.pid {
        return Err(io::Error::other("Go Codex backend peer identity mismatch"));
    }
    backend.matches()?;
    let config = WebSocketConfig::default()
        .max_message_size(Some(1024 * 1024))
        .max_frame_size(Some(1024 * 1024));
    let (mut wire, _) = client_with_config("ws://localhost/rpc", stream, Some(config))
        .map_err(|_| io::Error::other("Go Codex backend WebSocket handshake failed"))?;
    let initialized = request(
        &mut wire,
        1,
        "initialize",
        json!({"clientInfo":{"name":"swarm","title":"Swarm","version":"1"},
            "capabilities":{"experimentalApi":true,"requestAttestation":false}}),
    )?;
    if !initialized.is_object() {
        return Err(io::Error::other(
            "Go Codex backend initialization was invalid",
        ));
    }
    send(&mut wire, json!({"method":"initialized","params":{}}))?;
    let loaded = request(&mut wire, 2, "thread/loaded/list", json!({}))?;
    // The app-server can also have guardian and subagent threads. Match the
    // persisted root exactly once, like Go's same-instance rejoin.
    if !loaded["data"].as_array().is_some_and(|ids| {
        ids.iter().filter(|id| *id == thread_id).count() == 1
            && ids
                .iter()
                .all(|id| id.as_str().is_some_and(canonical_thread))
    }) {
        return Err(io::Error::other("Go Codex loaded conversation disagrees"));
    }
    let read = request(
        &mut wire,
        3,
        "thread/read",
        json!({"threadId":thread_id,"includeTurns":false}),
    )?;
    if read["thread"]["id"] != thread_id || read["thread"]["cwd"] != meta["cwd"] {
        return Err(io::Error::other("Go Codex conversation checkout disagrees"));
    }
    backend.matches()
}

fn socket_target(endpoint: &Path, uid: u32, version: &str) -> io::Result<(PathBuf, fs::Metadata)> {
    let invalid = || io::Error::other("unproven Go Codex backend socket");
    let original = endpoint.symlink_metadata()?;
    if original.uid() != uid {
        return Err(invalid());
    }
    if original.file_type().is_socket() {
        // The stock direct listener is already inside the validated private session directory.
        return Ok((endpoint.to_owned(), original));
    }
    if !original.is_symlink() || version != "0.172.0" {
        return Err(invalid());
    }
    // Codex 0.172.0 hashes the exact requested pathname and puts a mode-0600
    // listener into its per-UID private directory. Never connect via a mutable alias.
    let expected = short_socket(endpoint, uid)?;
    let root = expected.parent().expect("Codex socket parent");
    let directory = root.symlink_metadata()?;
    if fs::read_link(endpoint)? != expected
        || !directory.is_dir()
        || directory.uid() != uid
        || directory.mode() & 0o777 != 0o700
    {
        return Err(invalid());
    }
    let socket = expected.symlink_metadata()?;
    if !socket.file_type().is_socket() || socket.uid() != uid || socket.mode() & 0o777 != 0o600 {
        return Err(invalid());
    }
    Ok((expected, socket))
}

fn short_socket(endpoint: &Path, uid: u32) -> io::Result<PathBuf> {
    let root = Path::new("/tmp")
        .canonicalize()?
        .join(format!("codex-daemon-{uid}"));
    let name = format!("{:x}", Sha256::digest(endpoint.as_os_str().as_bytes()));
    Ok(root.join(name))
}

pub(in crate::supervisor::stopped_go) fn verify_stopped_socket(
    directory: &File,
    launch: &Value,
    version: &str,
) -> io::Result<Option<File>> {
    use rustix::fs::{AtFlags, FileType, OFlags, fstat, open, readlinkat, statat};
    let flags = AtFlags::SYMLINK_NOFOLLOW;
    let alias = match statat(directory, "codex.sock", flags) {
        Err(rustix::io::Errno::NOENT) => return Ok(None),
        Err(error) => return Err(error.into()),
        Ok(alias) => alias,
    };
    let uid = directory.metadata()?.uid();
    if version != "0.172.0"
        || FileType::from_raw_mode(alias.st_mode) != FileType::Symlink
        || alias.st_uid != uid
    {
        return Err(io::Error::other("Go Codex listener custody remains"));
    }
    let endpoint = Path::new(field(launch, "session_dir")).join("codex.sock");
    let expected = short_socket(&endpoint, uid)?;
    let alias_matches = || -> io::Result<bool> {
        Ok(same_entry(&alias, &statat(directory, "codex.sock", flags)?)
            && readlinkat(directory, "codex.sock", Vec::new())?.to_bytes()
                == expected.as_os_str().as_bytes())
    };
    if !alias_matches()? {
        return Err(io::Error::other(
            "Go Codex stopped socket alias is unproved",
        ));
    }
    let root = expected.parent().expect("Codex socket parent");
    // Search the exact coordinates without opening the directory for enumeration.
    #[cfg(target_os = "macos")]
    let search = OFlags::from_bits_retain(libc::O_SEARCH as _);
    #[cfg(target_os = "linux")]
    let search = OFlags::PATH | OFlags::DIRECTORY;
    let handle = match open(
        root,
        search | OFlags::NOFOLLOW | OFlags::CLOEXEC,
        rustix::fs::Mode::empty(),
    ) {
        Err(rustix::io::Errno::NOENT) => return Ok(None),
        Err(error) => return Err(error.into()),
        Ok(handle) => handle,
    };
    let parent = fstat(&handle)?;
    if parent.st_uid != uid || parent.st_mode & 0o777 != 0o700 {
        return Err(io::Error::other(
            "Go Codex stopped socket directory is unproved",
        ));
    }
    let name = expected.file_name().expect("Codex socket name");
    let listener = match statat(&handle, name, flags) {
        Err(rustix::io::Errno::NOENT) => None,
        Err(error) => return Err(error.into()),
        Ok(listener) => Some(listener),
    };
    let lock = prove_inert_listener(&handle, &expected, listener.as_ref(), uid)?;
    if !same_entry(&parent, &statat(rustix::fs::CWD, root, flags)?) || !alias_matches()? {
        return Err(io::Error::other(
            "Go Codex stopped socket alias or directory changed",
        ));
    }
    Ok(lock)
}

fn same_entry(before: &rustix::fs::Stat, after: &rustix::fs::Stat) -> bool {
    before.st_dev == after.st_dev
        && before.st_ino == after.st_ino
        && before.st_mode == after.st_mode
        && before.st_uid == after.st_uid
        && before.st_nlink == after.st_nlink
        && before.st_ctime == after.st_ctime
        && before.st_ctime_nsec == after.st_ctime_nsec
}

fn prove_inert_listener(
    directory: &std::os::fd::OwnedFd,
    expected: &Path,
    listener: Option<&rustix::fs::Stat>,
    uid: u32,
) -> io::Result<Option<File>> {
    use rustix::fs::{
        AtFlags, FileType, FlockOperation, Mode, OFlags, flock, fstat, openat, statat,
    };
    use rustix::net::{AddressFamily, SocketAddrUnix, SocketType, connect, socket};
    let unproved = || io::Error::other("Go Codex short listener custody remains");
    let name = expected.file_name().expect("Codex socket name");
    let lock_name = Path::new(name).with_extension("lock");
    let flags = AtFlags::SYMLINK_NOFOLLOW;
    if listener.is_some_and(|entry| {
        FileType::from_raw_mode(entry.st_mode) != FileType::Socket
            || entry.st_uid != uid
            || entry.st_mode & 0o777 != 0o600
            || entry.st_nlink != 1
    }) {
        return Err(unproved());
    }
    let lock = match openat(
        directory,
        &lock_name,
        OFlags::RDONLY | OFlags::NOFOLLOW | OFlags::NONBLOCK | OFlags::CLOEXEC,
        Mode::empty(),
    ) {
        Err(rustix::io::Errno::NOENT) if listener.is_none() => return Ok(None),
        Err(_) => return Err(unproved()),
        Ok(lock) => File::from(lock),
    };
    runtime::validate_file(&lock).map_err(|_| unproved())?;
    let held = fstat(&lock)?;
    flock(&lock, FlockOperation::NonBlockingLockExclusive)
        .map_err(|_| io::Error::other("Go Codex short listener lock is held or unproved"))?;
    let unchanged = || -> io::Result<bool> {
        let socket_stable = match (listener, statat(directory, name, flags)) {
            (Some(before), Ok(after)) => same_entry(before, &after),
            (None, Err(rustix::io::Errno::NOENT)) => true,
            _ => false,
        };
        Ok(socket_stable && same_entry(&held, &statat(directory, &lock_name, flags)?))
    };
    if !unchanged()? {
        return Err(unproved());
    }
    if listener.is_some() {
        let probe = socket(AddressFamily::UNIX, SocketType::STREAM, None)?;
        rustix::io::fcntl_setfd(&probe, rustix::io::FdFlags::CLOEXEC)?;
        rustix::io::ioctl_fionbio(&probe, true)?;
        if connect(&probe, &SocketAddrUnix::new(expected)?) != Err(rustix::io::Errno::CONNREFUSED)
            || !unchanged()?
        {
            return Err(unproved());
        }
    }
    // The new backend uses a separate socket. Holding the vendor lock proves
    // this stale coordinate without making any change to the old files.
    Ok(Some(lock))
}

fn send(wire: &mut WebSocket<UnixStream>, request: Value) -> io::Result<()> {
    wire.send(Message::Text(request.to_string().into()))
        .map_err(|_| io::Error::other("Go Codex backend probe write failed"))
}

fn request(
    wire: &mut WebSocket<UnixStream>,
    id: u64,
    method: &str,
    params: Value,
) -> io::Result<Value> {
    send(wire, json!({"id":id,"method":method,"params":params}))?;
    let deadline = Instant::now() + runtime::IO_TIMEOUT;
    for _ in 0..256 {
        if Instant::now() >= deadline {
            break;
        }
        let frame = wire
            .read()
            .map_err(|_| io::Error::other("Go Codex backend probe read failed"))?;
        if let Message::Text(text) = frame {
            let reply: Value = serde_json::from_str(&text)?;
            if reply["id"] == id && reply.get("method").is_none() {
                return reply
                    .get("result")
                    .filter(|_| reply.get("error").is_none())
                    .cloned()
                    .ok_or_else(|| io::Error::other("Go Codex backend probe was refused"));
            }
        }
    }
    Err(io::Error::other("Go Codex backend probe timed out"))
}

#[cfg(test)]
mod tests {
    use super::*;
    use rustix::fs::{AtFlags, FlockOperation, Mode, OFlags, flock, open, statat};
    use std::os::unix::{fs::PermissionsExt, net::UnixListener};

    #[test]
    fn exact_inert_listener_requires_an_unchanged_owner_socket_and_free_lock() -> io::Result<()> {
        let root = crate::test_temp::root().join(format!("go-inert-{}", runtime::random_id()?));
        runtime::private_dir(&root)?;
        let path = root.join("listener");
        let lock_path = path.with_extension("lock");
        runtime::write_json(&lock_path, &json!({}))?;
        let directory = open(&root, OFlags::RDONLY | OFlags::DIRECTORY, Mode::empty())?;
        let flags = AtFlags::SYMLINK_NOFOLLOW;
        let candidate = UnixListener::bind(&path)?;
        fs::set_permissions(&path, fs::Permissions::from_mode(0o600))?;
        let original = statat(&directory, "listener", flags)?;
        let prove = || prove_inert_listener(&directory, &path, Some(&original), runtime::uid());
        assert!(prove().is_err());
        drop(candidate);

        let held = prove()?.expect("stale listener lock");
        let other = File::open(&lock_path)?;
        assert!(flock(&other, FlockOperation::NonBlockingLockExclusive).is_err());
        assert!(prove().is_err());
        drop(held);

        fs::rename(&path, root.join("old"))?;
        let replacement = UnixListener::bind(&path)?;
        fs::set_permissions(&path, fs::Permissions::from_mode(0o600))?;
        drop(replacement);
        assert!(prove().is_err());
        let current = statat(&directory, "listener", flags)?;
        assert!(prove_inert_listener(&directory, &path, Some(&current), runtime::uid()).is_ok());
        fs::remove_file(&path)?;
        flock(&other, FlockOperation::NonBlockingLockExclusive)?;
        assert!(prove_inert_listener(&directory, &path, None, runtime::uid()).is_err());
        drop(other);
        assert!(prove_inert_listener(&directory, &path, None, runtime::uid())?.is_some());
        fs::remove_dir_all(root)?;
        Ok(())
    }
}
