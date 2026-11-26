//! Offline-only adoption of retained Go connector evidence.
use super::config::{Config, Place, open_owner_file};
use crate::{checkout::state::receipts::open_bolt, ipc::field, supervisor::runtime};
use bbolt_rs::{BucketApi, BucketRwApi, DbApi, DbRwAPI, TxApi, TxRwApi, TxRwRefApi};
use serde::Serialize;
use serde_json::{Value, json};
use sha2::{Digest, Sha256};
use std::{
    collections::BTreeMap,
    fs,
    io::{self, Read},
    os::unix::fs::{MetadataExt, OpenOptionsExt},
    path::Path,
    time::{Duration, Instant},
};

const MARKER: &str = ".rust-go-discord-retained.json";
const KNOWN: &[&str] = &[
    "requests",
    "events",
    "operations",
    "publications",
    "optouts",
    "aliases",
    "publication_index",
    "teammate_ingress",
    "teammate_ingress_guard",
    "teammate_pending",
    "teammate_discussions",
    "teammate_active_discussions",
    "teammate_discussion_ids",
    "teammate_meta",
    "teammate_outbox",
    "teammate_receipts",
    "teammate_file_payloads",
    "teammate_call_admissions",
];

#[derive(Serialize)]
pub(super) struct Inventory {
    installation: String,
    mode: String,
    buckets: BTreeMap<String, usize>,
    settled: usize,
    pub(super) unresolved: usize,
    exclusions: usize,
    pub(super) issues: Vec<String>,
}

pub(super) fn file_digest(mut file: &fs::File, limit: u64, end: Instant) -> io::Result<String> {
    if file.metadata()?.len() > limit {
        return Err(io::Error::other("legacy file exceeds the inspection limit"));
    }
    let mut digest = Sha256::new();
    let mut buf = [0; 32768];
    let mut total = 0;
    loop {
        if Instant::now() >= end {
            return Err(io::ErrorKind::TimedOut.into());
        }
        let amount = file.read(&mut buf)?;
        if amount == 0 {
            break;
        }
        total += amount as u64;
        if total > limit {
            return Err(io::Error::other("legacy file exceeds the inspection limit"));
        }
        digest.update(&buf[..amount]);
    }
    Ok(format!("{:x}", digest.finalize()))
}

fn existing(path: &Path) -> io::Result<bool> {
    match fs::symlink_metadata(path) {
        Ok(_) => Ok(true),
        Err(e) if e.kind() == io::ErrorKind::NotFound => Ok(false),
        Err(e) => Err(e),
    }
}

pub(super) fn unchanged(file: &fs::File, path: &Path) -> io::Result<bool> {
    let a = file.metadata()?;
    let b = fs::symlink_metadata(path)?;
    Ok(a.dev() == b.dev()
        && a.ino() == b.ino()
        && a.len() == b.len()
        && a.modified()? == b.modified()?)
}

fn hash_field(digest: &mut Sha256, field: &[u8]) {
    digest.update((field.len() as u64).to_be_bytes());
    digest.update(field);
}

pub(super) fn inventory(cfg: &Config) -> Inventory {
    Inventory {
        installation: cfg.text("installation_id").into(),
        mode: cfg.text("mode").into(),
        buckets: BTreeMap::new(),
        settled: 0,
        unresolved: 0,
        exclusions: 0,
        issues: Vec::new(),
    }
}

pub(super) fn scan<'tx>(
    tx: &impl TxApi<'tx>,
    report: &mut Inventory,
    end: Instant,
    controls: bool,
) -> io::Result<String> {
    let mut count = 0;
    let mut effect_digest = Sha256::new();
    let mut expected_events = BTreeMap::new();
    let mut expected_operations = BTreeMap::new();
    let mut actual_events = BTreeMap::new();
    let mut actual_operations = BTreeMap::new();
    for (n, (name, bucket)) in tx.iter_buckets().enumerate() {
        let name = std::str::from_utf8(name).map_err(io::Error::other)?;
        if n >= 64 || Instant::now() >= end {
            return Err(io::Error::other(
                "legacy bucket inventory exceeded its bound",
            ));
        }
        if !KNOWN.contains(&name) {
            report.issues.push("unknown connector ledger bucket".into());
            continue;
        }
        if bucket.iter_buckets().next().is_some() {
            report.issues.push(format!(
                "unexpected nested connector ledger bucket in {name}"
            ));
        }
        if controls && name != "optouts" {
            effect_digest.update([0]);
            hash_field(&mut effect_digest, name.as_bytes());
        }
        let mut rows = 0;
        for (key, value) in bucket.iter_entries() {
            count += 1;
            rows += 1;
            if count > 32_768 || value.len() > 1 << 20 || Instant::now() >= end {
                return Err(io::Error::other(
                    "legacy connector record inventory exceeded its bound",
                ));
            }
            if controls && name != "optouts" {
                effect_digest.update([1]);
                hash_field(&mut effect_digest, key);
                hash_field(&mut effect_digest, value);
            }
            match name {
                "optouts" => {
                    let key = serde_json::from_slice::<String>(key)?;
                    if key.is_empty() || key.len() > 512 {
                        return Err(io::Error::other("invalid retained audience identity"));
                    }
                    if serde_json::from_slice::<bool>(value)? {
                        report.exclusions += 1;
                    }
                }
                "requests" => {
                    let value: Value = serde_json::from_slice(value)?;
                    let request = &value["request"];
                    let reference = field(request, "ref");
                    let event = field(request, "event_id");
                    let operation = field(request, "operation_id");
                    if reference.is_empty()
                        || reference.as_bytes() != key
                        || event.is_empty()
                        || operation.is_empty()
                        || expected_events
                            .insert(event.to_owned(), reference.to_owned())
                            .is_some()
                        || expected_operations
                            .insert(operation.to_owned(), reference.to_owned())
                            .is_some()
                    {
                        return Err(io::Error::other("unverified retained Go request identity"));
                    }
                    let state = field(request, "delivery_state");
                    if matches!(state, "sent" | "failed") {
                        report.settled += 1;
                    } else {
                        report.unresolved += 1;
                        if controls
                            && state != "uncertain"
                            && !report
                                .issues
                                .iter()
                                .any(|issue| issue == "request has an unqualified delivery phase")
                        {
                            report
                                .issues
                                .push("request has an unqualified delivery phase".into());
                        }
                    }
                }
                "events" | "operations" => {
                    let reference = serde_json::from_slice::<String>(value)?;
                    if name == "events" {
                        actual_events.insert(serde_json::from_slice::<String>(key)?, reference);
                    } else {
                        actual_operations.insert(
                            std::str::from_utf8(key)
                                .map_err(io::Error::other)?
                                .to_owned(),
                            reference,
                        );
                    }
                }
                "publications" => {
                    let value: Value = serde_json::from_slice(value)?;
                    let identity = serde_json::from_slice::<[String; 2]>(key)?;
                    if identity[0] != field(&value, "request_ref")
                        || identity[1] != field(&value, "id")
                    {
                        return Err(io::Error::other(
                            "unverified retained Go publication identity",
                        ));
                    }
                    if matches!(field(&value, "state"), "sent" | "failed") {
                        report.settled += 1;
                    } else {
                        report.unresolved += 1;
                        report
                            .issues
                            .push("publication has an unqualified delivery phase".into());
                    }
                }
                "teammate_discussions"
                | "teammate_active_discussions"
                | "teammate_discussion_ids"
                | "teammate_ingress_guard"
                | "teammate_meta"
                    if controls => {}
                _ => {
                    report.unresolved += 1;
                    if controls
                        && !report
                            .issues
                            .iter()
                            .any(|issue| issue == "unsupported active ledger linkage")
                    {
                        report
                            .issues
                            .push("unsupported active ledger linkage".into());
                    }
                }
            }
        }
        report.buckets.insert(name.to_owned(), rows);
    }
    if actual_events != expected_events || actual_operations != expected_operations {
        report
            .issues
            .push("retained request replay indexes disagree with their original receipts".into());
    }
    for known in KNOWN.iter().take(17) {
        if !report.buckets.contains_key(*known) {
            report
                .issues
                .push(format!("missing expected {known} ledger bucket"));
        }
    }
    if !controls && report.unresolved > 0 {
        report
            .issues
            .push("connector has unresolved effects or unsupported linkage".into());
    }
    Ok(format!("{:x}", effect_digest.finalize()))
}

fn inspect(
    place: &Place,
    controls: bool,
    change: impl FnOnce(&Config, &Inventory, Value) -> io::Result<Value>,
) -> io::Result<Value> {
    place.verify_selection()?;
    let end = Instant::now() + Duration::from_secs(10);
    let config_path = place.dir.join("config.json");
    let conf = open_owner_file(&config_path)?;
    let config_hash = file_digest(&conf, 1 << 20, end)?;
    let cfg = place.load()?;
    // Reopen the config and compare bytes in addition to pinning its filesystem identity.
    if !unchanged(&conf, &config_path)?
        || file_digest(&open_owner_file(&config_path)?, 1 << 20, end)? != config_hash
    {
        return Err(io::Error::other(
            "connector configuration changed during inventory",
        ));
    }
    let mut report = inventory(&cfg);
    if !matches!(report.mode.as_str(), "personal" | "teammate") {
        report
            .issues
            .push("connector mode is outside the retained adoption scope".into());
    }
    if cfg.enabled() && !controls {
        report
            .issues
            .push("legacy connector must be disabled before dormant adoption".into());
    }
    if controls {
        verify_controls_place(place, &cfg)?;
    }
    if cfg.text("project_access") == "vm" {
        report
            .issues
            .push("legacy VM project access requires independent proof".into());
    }
    if !cfg.0["previous_supervisor"].is_null() || !cfg.0["calls"].is_null() {
        report.issues.push(
            "retained previous-supervisor or calls authority requires separate qualification"
                .into(),
        );
    }
    for path in [
        "bridge.sock",
        "rust-text-v1.json",
        ".rust-offline-owner.json",
    ] {
        if existing(&place.dir.join(path))? {
            report
                .issues
                .push(format!("existing {path} requires separate custody proof"));
        }
    }
    let discussions = place.dir.join("discussions");
    if existing(&discussions)?
        && (!fs::symlink_metadata(&discussions)?.is_dir()
            || fs::read_dir(&discussions)?.next().is_some())
    {
        report.issues.push(
            "retained manager and worker discussion ledgers require separate custody proof".into(),
        );
    }
    let path = place.dir.join("discord.db");
    let (pin, db) = open_bolt(&path, 64 << 20)?;
    let tx = db.begin().map_err(io::Error::other)?;

    let effects = scan(&tx, &mut report, end, controls)?;
    if controls
        && let Err(error) = super::legacy_discussions::verify(&tx, &cfg, cfg.text("daemon_socket"))
    {
        report.issues.push(error.to_string());
    }
    let digest = if controls {
        effects
    } else {
        file_digest(&pin, 64 << 20, end)?
    };
    let dir = fs::symlink_metadata(&place.dir)?;
    if !unchanged(&conf, &config_path)? || !unchanged(&pin, &path)? {
        return Err(io::Error::other(
            "connector evidence changed during inventory",
        ));
    }
    let mut anchor = json!({"schema":1,"installation":report.installation,"mode":report.mode,
        "directory_device":dir.dev(),"directory_inode":dir.ino(),"config_sha256":config_hash,"database_sha256":digest});
    if controls {
        let db = pin.metadata()?;
        anchor = json!({"schema":2,"installation":report.installation,"mode":report.mode,
            "directory_device":dir.dev(),"directory_inode":dir.ino(),"config_sha256":config_hash,
            "database_device":db.dev(),"database_inode":db.ino(),"effects_sha256":digest});
    }
    change(&cfg, &report, anchor)
}

pub(super) fn migrate(
    place: &Place,
    dry_run: bool,
    rollback: bool,
    controls: bool,
) -> io::Result<bool> {
    if dry_run && rollback {
        return Err(io::Error::other("select one migration operation"));
    }
    let marker = place.dir.join(MARKER);
    let existing_marker = || -> io::Result<Option<Value>> {
        match runtime::read_json(&marker) {
            Ok(value) => Ok(Some(value)),
            Err(err) if err.kind() == io::ErrorKind::NotFound => Ok(None),
            Err(err) => Err(err),
        }
    };
    let retained = if controls {
        "retained_controls"
    } else {
        "retained"
    };
    let describe = |report: &Inventory, state: &str| json!({"state":state,"eligible":report.issues.is_empty(),"gateway_authority":false,"inventory":report});
    let preflight = inspect(place, controls, |_, report, anchor| {
        let state = match existing_marker()? {
            Some(value) if value["anchor"] == anchor && value["state"] == retained => retained,
            Some(value) if value["anchor"] == anchor && value["state"] == "rolled_back" => {
                "rolled_back"
            }
            Some(_) => "changed",
            None => "legacy",
        };
        Ok(describe(report, state))
    })?;
    if dry_run || preflight["eligible"] != true || preflight["state"] == "changed" {
        println!("{}", serde_json::to_string_pretty(&preflight)?);
        return Ok(preflight["eligible"] == true && preflight["state"] != "changed");
    }
    let _locks = place.lock()?;
    let _bridge = runtime::lock(&place.dir.join("bridge.lock"))?;
    let result = inspect(place, controls, |_, report, anchor| {
        if !report.issues.is_empty() {
            return Ok(describe(report, "blocked"));
        }
        let prior = existing_marker()?;
        if prior.as_ref().is_some_and(|v| {
            v["anchor"] != anchor || !["rolled_back", retained].contains(&field(v, "state"))
        }) {
            return Ok(describe(report, "changed"));
        }
        if rollback && prior.is_none() {
            return Ok(describe(report, "legacy"));
        }
        let state = if rollback { "rolled_back" } else { retained };
        let next = json!({"anchor":anchor,"state":state});
        if prior.as_ref() != Some(&next) {
            runtime::write_json(&marker, &next)?;
        }
        fs::File::open(&place.dir)?.sync_all()?;
        Ok(describe(report, state))
    })?;
    println!("{}", serde_json::to_string_pretty(&result)?);
    Ok(result["eligible"] == true && result["state"] != "changed")
}

pub(super) fn verify_controls_place(place: &Place, cfg: &Config) -> io::Result<()> {
    let root = fs::canonicalize(place.supervisor_root())?;
    if !cfg.enabled()
        || !cfg.managed()
        || cfg.0["supervisor"]["state_dir"] != json!(root)
        || fs::canonicalize(Place::for_supervisor(&root)?.dir)? != fs::canonicalize(&place.dir)?
    {
        return Err(io::Error::other(
            "retained controls require the enabled original selected place",
        ));
    }
    Ok(())
}

pub(super) fn writable_pin(path: &Path) -> io::Result<fs::File> {
    let file = fs::OpenOptions::new()
        .read(true)
        .write(true)
        .custom_flags(libc::O_NOFOLLOW | libc::O_NONBLOCK)
        .open(path)?;
    let info = file.metadata()?;
    if !info.is_file()
        || info.nlink() != 1
        || info.mode() & 0o077 != 0
        || info.uid() != unsafe { libc::geteuid() }
        || !(4096..=64 << 20).contains(&info.len())
    {
        return Err(io::Error::other(
            "expected a bounded private original database",
        ));
    }
    Ok(file)
}

pub(super) use runtime::bounded_bolt_writer as bounded_writer;

pub(super) fn audience(
    place: &Place,
    cfg: &Config,
    id: &str,
    exclude: bool,
) -> io::Result<Option<Value>> {
    let marker = place.dir.join(MARKER);
    if !existing(&place.dir.join("discord.db"))? || !existing(&marker)? {
        return Ok(None);
    }
    let prior = runtime::read_json(&marker)?;
    if prior["state"] != "retained_controls" {
        return Ok(None);
    }
    let _place = place.lock()?;
    let _bridge = runtime::lock(&place.dir.join("bridge.lock"))?;
    if runtime::read_json(&marker)? != prior || place.load()?.0 != cfg.0 {
        return Err(io::Error::other("retained connector ownership changed"));
    }
    let anchor = inspect(place, true, |_, report, anchor| {
        if !report.issues.is_empty() || anchor != prior["anchor"] {
            return Err(io::Error::other("original connector evidence changed"));
        }
        Ok(anchor)
    })?;
    let root = place.supervisor_root();
    let supervisor = &cfg.0["supervisor"];
    let socket = Path::new(cfg.text("daemon_socket"));
    let lock = Path::new(
        supervisor["lock"]
            .as_str()
            .ok_or_else(|| io::Error::other("missing supervisor owner lock"))?,
    );
    let (endpoint, rows) = super::migrate_supervisor::live(root, socket, lock)?;
    if !id
        .strip_prefix(&format!("{endpoint}/"))
        .is_some_and(|suffix| {
            !suffix.is_empty()
                && suffix.len() <= 128
                && suffix
                    .bytes()
                    .all(|byte| byte.is_ascii_alphanumeric() || byte == b'-')
        })
    {
        return Err(io::Error::other(
            "invalid original supervisor worker target",
        ));
    }
    if rows
        .iter()
        .filter(|row| {
            row["id"] == id
                && row["roster_hidden"] != true
                && field(row, "superseded_by").is_empty()
        })
        .count()
        != 1
    {
        return Err(io::Error::other(
            "worker target is missing or ambiguous on the original supervisor",
        ));
    }
    let path = place.dir.join("discord.db");
    let file = writable_pin(&path)?;
    let pinned = file.metadata()?;
    if anchor["database_device"] != pinned.dev() || anchor["database_inode"] != pinned.ino() {
        return Err(io::Error::other("original connector database lost custody"));
    }
    let (pin, mut db) = bounded_writer(file)?;
    let mut tx = db.begin_rw().map_err(io::Error::other)?;
    let mut report = inventory(cfg);
    let digest = scan(
        &tx,
        &mut report,
        Instant::now() + Duration::from_secs(10),
        true,
    )?;
    super::legacy_discussions::verify(&tx, cfg, cfg.text("daemon_socket"))?;
    if !report.issues.is_empty() || anchor["effects_sha256"] != digest || !unchanged(&pin, &path)? {
        return Err(io::Error::other("original connector effects changed"));
    }
    {
        let mut bucket = tx
            .bucket_mut(b"optouts")
            .ok_or_else(|| io::Error::other("missing original audience bucket"))?;
        let key = serde_json::to_vec(id)?;
        let desired = if exclude {
            b"true".as_slice()
        } else {
            b"false".as_slice()
        };
        if bucket.get(&key) != Some(desired) {
            if bucket.get(&key).is_none() && report.buckets["optouts"] >= 2048 {
                return Err(io::Error::other("original audience bucket is full"));
            }
            bucket.put(&key, desired).map_err(io::Error::other)?;
        } else {
            return Ok(Some(cfg.status()));
        }
    }
    tx.commit().map_err(io::Error::other)?;
    Ok(Some(cfg.status()))
}
