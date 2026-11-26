//! Resolve the installed teammate supervisor for the local dashboard.
use super::config::{Place, invalid};
use crate::{ipc, supervisor};
use std::fs;
use std::io;
use std::os::unix::fs::{FileTypeExt, MetadataExt};
use std::path::{Path, PathBuf};
use std::time::{Duration, Instant};

#[derive(Clone, Debug, PartialEq, Eq)]
pub(crate) struct Binding {
    pub(crate) socket: PathBuf,
    pub(crate) endpoint: String,
    place: PathBuf,
    installation: String,
    socket_identity: (u64, u64),
}

fn slot() -> io::Result<PathBuf> {
    Ok(ipc::user_config_dir()?.join("swarm/installed-discord"))
}

pub(super) fn record(place: &Path) -> io::Result<()> {
    let slot = slot()?;
    supervisor::runtime::private_dir(&slot)?;
    match Place::read_selection(&slot) {
        Ok(selected) if selected == place => return Ok(()),
        Err(error) if error.kind() != io::ErrorKind::NotFound => return Err(error),
        _ => {}
    }
    supervisor::runtime::write_json(
        &slot.join(super::config::SELECTION),
        &serde_json::json!(place),
    )
}

fn endpoint(path: &Path) -> String {
    supervisor::endpoint(&fs::canonicalize(path).unwrap_or_else(|_| path.to_owned()))
}

fn legacy_endpoint(place: &Path, socket: &Path) -> io::Result<String> {
    let mut selected = None;
    for candidate in [place.parent(), socket.parent()].into_iter().flatten() {
        let parent = fs::metadata(candidate)?;
        if !parent.is_dir()
            || parent.uid() != supervisor::runtime::uid()
            || parent.mode() & 0o022 != 0
        {
            continue;
        }
        let recorded = match Place::read_selection(candidate) {
            Ok(path) => path,
            Err(error) if error.kind() == io::ErrorKind::NotFound => continue,
            Err(error) => return Err(error),
        };
        if recorded.canonicalize()? != place.canonicalize()? {
            continue;
        }
        let id = endpoint(candidate);
        if selected.as_ref().is_some_and(|prior| *prior != id) {
            return Err(invalid("ambiguous original dispatcher supervisor"));
        }
        selected = Some(id);
    }
    selected.ok_or_else(|| invalid("installed dispatcher has no verified supervisor state"))
}

pub(crate) fn resolve(
    local: &Path,
    local_endpoint: &str,
    caps: &[&str],
) -> io::Result<Option<(Binding, ipc::Client)>> {
    resolve_slot(&slot()?, local, local_endpoint, caps)
}

fn resolve_slot(
    slot: &Path,
    local: &Path,
    local_endpoint: &str,
    caps: &[&str],
) -> io::Result<Option<(Binding, ipc::Client)>> {
    match fs::symlink_metadata(slot) {
        Ok(meta)
            if meta.is_dir()
                && meta.uid() == supervisor::runtime::uid()
                && meta.mode() & 0o077 == 0 => {}
        Ok(_) => {
            return Err(invalid(
                "installed dispatcher slot must be a private owner directory",
            ));
        }
        Err(error) if error.kind() == io::ErrorKind::NotFound => return Ok(None),
        Err(error) => return Err(error),
    }
    let place = match Place::read_selection(slot) {
        Ok(place) => place,
        Err(error) if error.kind() == io::ErrorKind::NotFound => return Ok(None),
        Err(error) => return Err(error),
    };
    let cfg = Place::resolve(Some(
        place
            .to_str()
            .ok_or_else(|| invalid("invalid installed place"))?,
    ))?
    .load()?;
    if !cfg.enabled() || !cfg.managed() {
        return Ok(None);
    }
    let mut socket = PathBuf::from(cfg.text("daemon_socket"));
    let mut expected = match cfg.0["supervisor"]["state_dir"].as_str() {
        Some(state) => endpoint(Path::new(state)),
        None => legacy_endpoint(&place, &socket)?,
    };
    let previous = &cfg.0["previous_supervisor"];
    if !previous.is_null() {
        if expected != local_endpoint {
            return Ok(None);
        }
        socket = PathBuf::from(ipc::field(previous, "socket"));
        let old = endpoint(Path::new(ipc::field(previous, "state_dir")));
        if previous["endpoint"] != old {
            return Err(invalid("previous dispatcher supervisor identity changed"));
        }
        expected = old;
    }
    if socket == local {
        return Ok(None);
    }
    let remote = fs::metadata(&socket)?;
    if !remote.file_type().is_socket() || remote.uid() != supervisor::runtime::uid() {
        return Err(invalid("invalid dispatcher supervisor socket owner"));
    }
    let identity = (remote.dev(), remote.ino());
    if fs::metadata(local).is_ok_and(|m| (m.dev(), m.ino()) == identity) {
        return Ok(None);
    }
    let client = ipc::Client::dial_before(&socket, caps, Instant::now() + Duration::from_secs(2))?;
    if expected != client.endpoint() {
        return Err(invalid("dispatcher supervisor identity changed"));
    }
    if client.endpoint() == local_endpoint {
        return Ok(None);
    }
    let observed = fs::metadata(&socket)?;
    if (observed.dev(), observed.ino()) != identity {
        return Err(invalid(
            "dispatcher supervisor socket changed during handshake",
        ));
    }
    Ok(Some((
        Binding {
            socket,
            endpoint: client.endpoint().to_owned(),
            place,
            installation: cfg.text("installation_id").to_owned(),
            socket_identity: identity,
        },
        client,
    )))
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::supervisor::runtime;
    use serde_json::json;
    use std::os::unix::fs::{PermissionsExt, symlink};
    use std::os::unix::net::UnixListener;
    use std::thread;

    fn handshake(listener: &UnixListener, endpoint: &str) -> thread::JoinHandle<()> {
        let listener = listener.try_clone().unwrap();
        let endpoint = endpoint.to_owned();
        thread::spawn(move || {
            let (mut conn, _) = listener.accept().unwrap();
            let hello = runtime::read_control(&mut conn).unwrap();
            assert_eq!(hello["op"], "hello");
            runtime::write_control(&mut conn, &json!({
                "op":"hello","protocol_version":1,"endpoint_id":endpoint,"capabilities":hello["capabilities"]})).unwrap();
        })
    }

    #[test]
    fn selects_the_current_or_proven_previous_owner_and_ignores_local_aliases() {
        let root = std::env::temp_dir().join(format!("lb-{}", &runtime::random_id().unwrap()[..9]));
        let slot = root.join("installed");
        let place = root.join("place");
        let current = root.join("current");
        let old = root.join("old");
        for dir in [&root, &slot, &place, &current, &old] {
            runtime::private_dir(dir).unwrap();
        }
        let socket = current.join("s");
        let old_socket = old.join("s");
        let listener = runtime::bind(&socket).unwrap();
        let old_listener = runtime::bind(&old_socket).unwrap();
        let local = root.join("board.sock");
        let mut cfg = json!({"version":1,"enabled":true,"mode":"personal","name":"fixture","installation_id":"fixture",
            "owner_id":"300","application_id":"200","dm_channel_id":"400","manager_cli":"codex","work_dir":root,
            "daemon_socket":socket,"token_file":root.join("token"),
            "supervisor":{"state_dir":current,"lock":current.join("lock"),"log":current.join("log")}});
        runtime::write_json(&place.join("config.json"), &cfg).unwrap();
        runtime::write_json(&slot.join(super::super::config::SELECTION), &json!(place)).unwrap();
        let server = handshake(&listener, &endpoint(&current));
        let (route, _) = resolve_slot(&slot, &local, "different", &["attach"])
            .unwrap()
            .unwrap();
        assert_eq!(route.endpoint, endpoint(&current));
        server.join().unwrap();

        let complete = cfg.clone();
        cfg.as_object_mut().unwrap().shift_remove("supervisor");
        runtime::write_json(&place.join("config.json"), &cfg).unwrap();
        assert!(resolve_slot(&slot, &local, "different", &[]).is_err());
        runtime::write_json(
            &current.join(super::super::config::SELECTION),
            &json!(place),
        )
        .unwrap();
        let legacy = handshake(&listener, &endpoint(&current));
        assert_eq!(
            resolve_slot(&slot, &local, "different", &[])
                .unwrap()
                .unwrap()
                .0
                .endpoint,
            endpoint(&current)
        );
        legacy.join().unwrap();
        let wrong_legacy = handshake(&listener, "wrong-legacy-owner");
        assert!(resolve_slot(&slot, &local, "different", &[]).is_err());
        wrong_legacy.join().unwrap();
        cfg = complete;
        runtime::write_json(&place.join("config.json"), &cfg).unwrap();
        let liar = handshake(&listener, "wrong-owner");
        assert!(resolve_slot(&slot, &local, "different", &[]).is_err());
        liar.join().unwrap();
        symlink(&socket, &local).unwrap();
        assert!(
            resolve_slot(&slot, &local, "different", &[])
                .unwrap()
                .is_none()
        );
        fs::remove_file(&local).unwrap();

        cfg["previous_supervisor"] = json!({"state_dir":old,"socket":old_socket,"endpoint":endpoint(&old),
            "lock":old.join("lock"),"log":old.join("log")});
        runtime::write_json(&place.join("config.json"), &cfg).unwrap();
        assert!(
            resolve_slot(&slot, &local, "different", &[])
                .unwrap()
                .is_none()
        );
        let previous = handshake(&old_listener, &endpoint(&old));
        let (route, _) = resolve_slot(&slot, &socket, &endpoint(&current), &[])
            .unwrap()
            .unwrap();
        assert_eq!(route.socket, old_socket);
        previous.join().unwrap();
        cfg["previous_supervisor"]["endpoint"] = json!("ep-12345678");
        runtime::write_json(&place.join("config.json"), &cfg).unwrap();
        assert!(resolve_slot(&slot, &socket, &endpoint(&current), &[]).is_err());
        fs::set_permissions(&slot, fs::Permissions::from_mode(0o755)).unwrap();
        assert!(resolve_slot(&slot, &socket, &endpoint(&current), &[]).is_err());
        fs::remove_dir_all(root).unwrap();
    }
}
