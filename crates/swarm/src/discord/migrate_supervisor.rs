//! Keep the original Go connector ledger while moving its default routing home.
use super::config::{Config, Place};
use crate::{
    checkout::state::receipts::open_bolt,
    ipc,
    supervisor::{self, runtime},
};
use bbolt_rs::{DbApi, TxApi};
use serde_json::{Value, json};
use std::os::fd::AsRawFd;
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
fn live(root: &Path, socket: &Path) -> io::Result<String> {
    let endpoint = real_endpoint(root)?;
    let mut client = ipc::Client::dial(socket, &[])?;
    if client.endpoint() != endpoint {
        return Err(io::Error::other(
            "socket answered for a different supervisor",
        ));
    }
    client.sessions()?;
    Ok(endpoint)
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
    let actual_old = live(&original, old_socket)?;
    let actual_new = live(&home, &socket)?;
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
    runtime::write_json(&place.dir.join("config.json"), &next.cfg.0)?;
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
