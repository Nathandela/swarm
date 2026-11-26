//! Cold install transactions with one durable route switch.
use super::{archive, invalid, owner_file};
use crate::supervisor::runtime;
use serde::{Deserialize, Serialize};
use serde_json::{Value, json};
use std::{
    cmp::Ordering,
    collections::{BTreeMap, BTreeSet},
    fs::{self, File, OpenOptions},
    io::{self, Write},
    os::fd::AsRawFd,
    os::unix::fs::{DirBuilderExt, MetadataExt, OpenOptionsExt, PermissionsExt, symlink},
    path::{Path, PathBuf},
    process::{Command, Stdio},
    time::Duration,
};

const CHANNEL_BASE: &str =
    "https://github.com/Nathandela/swarm/releases/download/rust-native-preview";
const ADMIN: [&str; 2] = ["swarm-rust-install.py", "swarm-rust-install.md"];
const GROUPS: [&str; 3] = ["darwin_all", "linux_arm64", "linux_amd64"];
const METADATA_LIMIT: usize = 8 * 1024 * 1024;

pub(super) struct Options<'a> {
    pub step: &'a str,
    pub dist: Option<PathBuf>,
    pub json: bool,
    pub allow_downgrade: bool,
    pub check_media: bool,
}

#[derive(Serialize, Deserialize)]
struct Route {
    active: String,
    previous: Option<String>,
    held: Option<String>,
}
#[derive(Serialize, Deserialize)]
struct Stage {
    from: String,
    to: String,
    downgrade: bool,
}

fn read(path: &Path, limit: usize) -> io::Result<Vec<u8>> {
    let file = OpenOptions::new()
        .read(true)
        .custom_flags(libc::O_NOFOLLOW | libc::O_NONBLOCK)
        .open(path)?;
    if !file.metadata()?.is_file() {
        return Err(invalid("expected a regular release file"));
    }
    archive::read_bounded(file, limit)
}

fn value(path: &Path) -> io::Result<Value> {
    Ok(serde_json::from_slice(&read(path, METADATA_LIMIT)?)?)
}

fn required<'a>(value: &'a Value, key: &str) -> io::Result<&'a str> {
    value[key]
        .as_str()
        .ok_or_else(|| invalid("release metadata field is absent"))
}

fn durable(path: &Path, bytes: &[u8], mode: u32) -> io::Result<()> {
    let mut file = OpenOptions::new()
        .write(true)
        .create_new(true)
        .mode(mode)
        .open(path)?;
    file.write_all(bytes)?;
    file.set_permissions(fs::Permissions::from_mode(mode))?;
    file.sync_all()
}

fn dir(path: &Path) -> io::Result<()> {
    let mut builder = fs::DirBuilder::new();
    builder.mode(0o700).create(path)
}

fn save(path: &Path, record: &impl Serialize) -> io::Result<()> {
    let parent = path.parent().unwrap();
    let tmp = parent.join(format!(".swarm-record-{}", runtime::random_id()?));
    durable(&tmp, &serde_json::to_vec(record)?, 0o600)?;
    fs::rename(tmp, path)?;
    File::open(parent)?.sync_all()
}

fn secure_lock(path: &Path) -> io::Result<File> {
    let lock = owner_file(path, false)?;
    // SAFETY: flock borrows the open descriptor.
    if unsafe { libc::flock(lock.as_raw_fd(), libc::LOCK_EX | libc::LOCK_NB) } != 0 {
        return Err(io::Error::last_os_error());
    }
    let (held, disk) = (lock.metadata()?, fs::symlink_metadata(path)?);
    if (held.dev(), held.ino()) != (disk.dev(), disk.ino()) {
        return Err(invalid("upgrade lease was replaced"));
    }
    Ok(lock)
}

#[derive(PartialEq, Eq, PartialOrd, Ord)]
enum Prefix {
    Number(u64),
    String(String),
}
#[derive(PartialEq, Eq, PartialOrd, Ord)]
struct Version([u64; 3], bool, Vec<Prefix>);
fn version(src: &str) -> io::Result<Version> {
    let (base, tail) = src
        .split_once('-')
        .map_or((src, None), |(base, tail)| (base, Some(tail)));
    let parts = base
        .split('.')
        .map(|part| {
            if part.is_empty()
                || (part.len() > 1 && part.starts_with('0'))
                || !part.bytes().all(|b| b.is_ascii_digit())
            {
                return Err(invalid("invalid release version"));
            }
            part.parse::<u64>().map_err(io::Error::other)
        })
        .collect::<io::Result<Vec<_>>>()?
        .try_into()
        .map_err(|_| invalid("invalid release version"))?;
    let mut prerelease = Vec::new();
    if let Some(tail) = tail {
        for part in tail.split('.') {
            if part.is_empty()
                || !part
                    .bytes()
                    .all(|b| b.is_ascii_digit() || b.is_ascii_lowercase())
            {
                return Err(invalid("invalid preview prerelease version"));
            }
            prerelease.push(match part.parse() {
                Ok(n) => Prefix::Number(n),
                Err(_) => Prefix::String(part.into()),
            });
        }
    }
    Ok(Version(parts, tail.is_none(), prerelease))
}

fn release_path(root: &Path, slot: &str) -> io::Result<PathBuf> {
    let (ver, source) = slot
        .rsplit_once('-')
        .ok_or_else(|| invalid("invalid release slot"))?;
    version(ver)?;
    if !archive::hex(source, 40) {
        return Err(invalid("invalid release source identity"));
    }
    Ok(root.join("releases").join(slot))
}

fn card_id(card: &Value) -> io::Result<String> {
    if card["format"] != 1
        || card["managed_format"] != 1
        || card["upgrade_runtime"] != 2
        || card["channel"] != "rust-native-preview"
        || card["state_abi"].as_u64().is_none_or(|abi| abi == 0)
    {
        return Err(invalid(
            "release does not support this native managed channel",
        ));
    }
    Ok(format!(
        "{}-{}",
        required(card, "version")?,
        required(card, "source")?
    ))
}

fn route(root: &Path) -> io::Result<Route> {
    let target = fs::read_link(root.join("current"))?;
    let name = target
        .file_name()
        .and_then(|name| name.to_str())
        .unwrap_or_default();
    if target.parent() != Some(Path::new("routes"))
        || !name.starts_with("route-")
        || !name
            .bytes()
            .all(|b| b.is_ascii_alphanumeric() || matches!(b, b'_' | b'-'))
    {
        return Err(invalid("invalid current release route"));
    }
    let route: Route = serde_json::from_value(value(&root.join(target).join("record.json"))?)?;
    let active = release_path(root, &route.active)?;
    if fs::canonicalize(root.join("swarm"))? != active.join("swarm") {
        return Err(invalid("launcher does not match active release route"));
    }
    if let Some(previous) = &route.previous {
        release_path(root, previous)?;
    }
    Ok(route)
}

fn point(root: &Path, route: Route) -> io::Result<()> {
    let name = format!("route-{}", runtime::random_id()?);
    let relative = Path::new("routes").join(name);
    let destination = root.join(&relative);
    dir(&destination)?;
    durable(
        &destination.join("record.json"),
        &serde_json::to_vec(&route)?,
        0o600,
    )?;
    symlink(
        Path::new("../..")
            .join("releases")
            .join(&route.active)
            .join("swarm"),
        destination.join("swarm"),
    )?;
    File::open(&destination)?.sync_all()?;
    File::open(root.join("routes"))?.sync_all()?;
    let tmp = root.join(format!(".swarm-switch-{}", runtime::random_id()?));
    symlink(relative, &tmp)?;
    fs::rename(tmp, root.join("current"))?;
    File::open(root)?.sync_all()
}

enum Feed {
    Local(PathBuf),
    Online(Box<ureq::Agent>),
}
impl Feed {
    fn open(dist: Option<PathBuf>) -> Self {
        match dist {
            Some(path) => Self::Local(path),
            None => Self::Online(Box::new(
                ureq::Agent::config_builder()
                    .https_only(true)
                    .max_redirects(4)
                    .timeout_global(Some(Duration::from_secs(120)))
                    .build()
                    .into(),
            )),
        }
    }
    fn get(&self, name: &str, limit: usize) -> io::Result<Vec<u8>> {
        match self {
            Self::Local(path) => read(&path.join(name), limit),
            Self::Online(agent) => {
                let mut response =
                    agent
                        .get(format!("{CHANNEL_BASE}/{name}"))
                        .call()
                        .map_err(|_| {
                            io::Error::new(
                                io::ErrorKind::NotConnected,
                                "preview channel unavailable",
                            )
                        })?;
                archive::read_bounded(response.body_mut().as_reader(), limit)
            }
        }
    }
}

fn assets(sums: &BTreeMap<String, String>) -> io::Result<(&str, String, &str)> {
    if sums.len() != ADMIN.len() + GROUPS.len()
        || ADMIN.iter().any(|name| !sums.contains_key(*name))
    {
        return Err(invalid("incomplete signed Rust artifact set"));
    }
    let group = if cfg!(target_os = "macos") {
        "darwin_all"
    } else if cfg!(target_arch = "aarch64") {
        "linux_arm64"
    } else {
        "linux_amd64"
    };
    for name in sums.keys() {
        if let Some(ver) = name
            .strip_prefix("swarm-rust_")
            .and_then(|s| s.strip_suffix(&format!("_{group}.tar.gz")))
        {
            version(ver)?;
            if GROUPS
                .iter()
                .any(|group| !sums.contains_key(&format!("swarm-rust_{ver}_{group}.tar.gz")))
            {
                return Err(invalid("inconsistent signed release versions"));
            }
            return Ok((ver, name.clone(), group));
        }
    }
    Err(invalid("signed assets omit this architecture"))
}

fn archive_card(files: &archive::Files, ver: &str, group: &str) -> io::Result<Value> {
    let get = |name: &str| {
        files
            .get(name)
            .map(Vec::as_slice)
            .ok_or_else(|| invalid("required release member is missing"))
    };
    let card: Value = serde_json::from_slice(get("compat.json")?)?;
    card_id(&card)?;
    let pins = get("media-pins.json")?;
    if card["version"] != ver || card["media_pins_sha256"] != archive::sha(pins) {
        return Err(invalid("release card does not match signed asset"));
    }
    let pins: BTreeMap<String, BTreeMap<String, String>> = serde_json::from_slice(pins)?;
    let media = match group {
        "darwin_all" => &["darwin-aarch64", "darwin-x86_64"][..],
        "linux_arm64" => &["linux-aarch64"][..],
        _ => &["linux-x86_64"][..],
    };
    if pins.len() != 4
        || [
            "darwin-aarch64",
            "darwin-x86_64",
            "linux-aarch64",
            "linux-x86_64",
        ]
        .iter()
        .any(|key| !pins.contains_key(*key))
    {
        return Err(invalid("release media platform set differs"));
    }
    let mut expected = BTreeSet::from([
        "swarm".to_owned(),
        "compat.json".into(),
        "media-pins.json".into(),
        "LICENSE".into(),
    ]);
    for platform in media {
        let pins = &pins[*platform];
        let native = match *platform {
            "darwin-aarch64" => "darwin-arm64",
            "darwin-x86_64" => "darwin-x64",
            "linux-aarch64" => "linux-arm64-gnu",
            _ => "linux-x64-gnu",
        };
        let addon = format!("node_modules/@snazzah/davey-{native}/package.json");
        let required = [
            "node",
            "NODE-LICENSE",
            "swarm-discord-voice",
            "media.mjs",
            "package.json",
            "package-lock.json",
            &addon,
        ];
        if required.iter().any(|name| !pins.contains_key(*name)) {
            return Err(invalid(
                "signed release is missing a required media component",
            ));
        }
        for (relative, sum) in pins {
            if !archive::safe_name(relative)
                || !required.contains(&relative.as_str()) && !relative.starts_with("node_modules/")
            {
                return Err(invalid("unexpected pinned media member"));
            }
            let path = format!("media/{platform}/{relative}");
            if &archive::sha(get(&path)?) != sum {
                return Err(invalid("signed media member differs from its pin"));
            }
            expected.insert(path);
        }
    }
    let inventory: BTreeMap<String, Value> = serde_json::from_slice(get("notices/cargo.json")?)?;
    expected.extend(
        [
            "notices/cargo.json",
            "notices/toolchain/COPYRIGHT-library.html",
            "notices/davey/LICENSE",
            "notices/davey/Cargo.lock",
            "notices/davey/source.json",
        ]
        .map(str::to_owned),
    );
    for item in inventory.values() {
        for name in item["notice_files"]
            .as_array()
            .ok_or_else(|| invalid("invalid Cargo notice inventory"))?
        {
            let name = name
                .as_str()
                .filter(|name| name.starts_with("notices/crates/"))
                .ok_or_else(|| invalid("invalid Cargo notice location"))?;
            expected.insert(name.to_owned());
        }
    }
    expected.extend(
        files
            .keys()
            .filter(|name| {
                name.strip_prefix("notices/toolchain/")
                    .is_some_and(|name| !name.contains('/') && name.ends_with(".txt"))
            })
            .cloned(),
    );
    if expected.len() != files.len() || expected.iter().any(|name| !files.contains_key(name)) {
        return Err(invalid(
            "signed archive contains unlisted or retired members",
        ));
    }
    Ok(card)
}

fn release_executable(slot: &Path, card: &Value, media: bool) -> io::Result<()> {
    let exe = slot.join("swarm");
    let out = Command::new(&exe)
        .arg("release-info")
        .env_clear()
        .stdin(Stdio::null())
        .stderr(Stdio::null())
        .output()?;
    if !out.status.success() || serde_json::from_slice::<Value>(&out.stdout)? != *card {
        return Err(invalid(
            "staged application does not match its release card",
        ));
    }
    if media
        && !Command::new(exe)
            .arg("release-check-media")
            .env_clear()
            .stdin(Stdio::null())
            .status()?
            .success()
    {
        return Err(invalid("staged media check failed"));
    }
    Ok(())
}

fn slot_members(
    path: &Path,
    root: &Path,
    depth: usize,
    result: &mut BTreeMap<String, String>,
) -> io::Result<()> {
    use sha2::{Digest, Sha256};
    if depth > 32 {
        return Err(invalid("release slot is nested too deeply"));
    }
    for entry in fs::read_dir(path)? {
        let entry = entry?;
        let path = entry.path();
        let kind = entry.file_type()?;
        if kind.is_dir() {
            slot_members(&path, root, depth + 1, result)?;
            continue;
        }
        let name = path
            .strip_prefix(root)
            .unwrap()
            .to_str()
            .ok_or_else(|| invalid("invalid release slot path"))?;
        if !kind.is_file() || !archive::safe_name(name) || result.len() >= 16_384 {
            return Err(invalid("unsafe release slot member"));
        }
        if name != ".members.json" {
            let mut file = OpenOptions::new()
                .read(true)
                .custom_flags(libc::O_NOFOLLOW | libc::O_NONBLOCK)
                .open(&path)?;
            let meta = file.metadata()?;
            if !meta.is_file()
                || meta.len() > archive::MAX_ARCHIVE as u64
                || meta.nlink() != 1
                || meta.uid() != runtime::uid()
                || meta.mode() & 0o777 != archive::file_mode(name)
            {
                return Err(invalid("unsafe release slot file"));
            }
            let mut hash = Sha256::new();
            let bytes = io::copy(&mut file, &mut hash)?;
            if bytes != meta.len() {
                return Err(invalid("release slot file size changed"));
            }
            result.insert(name.into(), format!("{:x}", hash.finalize()));
        }
    }
    Ok(())
}

fn verify_slot(slot: &Path) -> io::Result<Value> {
    let meta = fs::symlink_metadata(slot)?;
    if !meta.is_dir() || meta.uid() != runtime::uid() || meta.mode() & 0o077 != 0 {
        return Err(invalid("release slot is absent or unsafe"));
    }
    let members: BTreeMap<String, String> = serde_json::from_slice(&archive::read_bounded(
        owner_file(&slot.join(".members.json"), false)?,
        METADATA_LIMIT,
    )?)?;
    let mut actual = BTreeMap::new();
    slot_members(slot, slot, 0, &mut actual)?;
    if members != actual {
        return Err(invalid(
            "release slot contents differ from the staged manifest",
        ));
    }
    let card = value(&slot.join("compat.json"))?;
    if slot.file_name().and_then(|name| name.to_str()) != Some(card_id(&card)?.as_str()) {
        return Err(invalid("release slot identity differs"));
    }
    release_executable(slot, &card, false)?;
    Ok(card)
}

fn write_slot(root: &Path, files: archive::Files, card: &Value, media: bool) -> io::Result<String> {
    let name = card_id(card)?;
    let target = release_path(root, &name)?;
    let manifest: BTreeMap<_, _> = files
        .iter()
        .map(|(name, data)| (name, archive::sha(data)))
        .collect();
    match fs::symlink_metadata(&target) {
        Ok(_) => {
            let existing = verify_slot(&target)?;
            let previous: BTreeMap<String, String> =
                serde_json::from_value(value(&target.join(".members.json"))?)?;
            if &existing != card
                || previous
                    .iter()
                    .any(|(name, sum)| manifest.get(name) != Some(sum))
                || previous.len() != manifest.len()
            {
                return Err(invalid("immutable release slot differs from signed bytes"));
            }
            if media {
                release_executable(&target, card, true)?;
            }
            return Ok(name);
        }
        Err(error) if error.kind() == io::ErrorKind::NotFound => (),
        Err(error) => return Err(error),
    }
    let stage = target
        .parent()
        .unwrap()
        .join(format!(".swarm-stage-{}", runtime::random_id()?));
    dir(&stage)?;
    let result = (|| {
        let mut directories = BTreeSet::new();
        for (relative, contents) in &files {
            let path = stage.join(relative);
            let mut parent = stage.clone();
            for name in Path::new(relative).parent().unwrap().components() {
                parent.push(name);
                if directories.insert(parent.clone()) {
                    dir(&parent)?;
                }
            }
            durable(&path, contents, archive::file_mode(relative))?;
        }
        durable(
            &stage.join(".members.json"),
            &serde_json::to_vec(&manifest)?,
            0o600,
        )?;
        release_executable(&stage, card, media)?;
        for path in directories.iter().rev() {
            File::open(path)?.sync_all()?;
        }
        File::open(&stage)?.sync_all()?;
        fs::rename(&stage, &target)?;
        File::open(target.parent().unwrap())?.sync_all()?;
        Ok(name)
    })();
    if result.is_err() {
        let _ = fs::remove_dir_all(&stage);
    }
    result
}

fn compatible(old: &Value, new: &Value) -> io::Result<()> {
    for field in [
        "channel",
        "managed_format",
        "upgrade_runtime",
        "state_abi",
        "protocol",
        "shimwire",
        "media_protocol",
    ] {
        if old[field].is_null() || old[field] != new[field] {
            return Err(invalid(
                "release compatibility differs; use a qualified offline migration",
            ));
        }
    }
    Ok(())
}

fn exists(path: &Path) -> io::Result<bool> {
    match fs::symlink_metadata(path) {
        Ok(_) => Ok(true),
        Err(error) if error.kind() == io::ErrorKind::NotFound => Ok(false),
        Err(error) => Err(error),
    }
}

fn reverse_allowed(root: &Path, name: &str, current: &Value, target: &Value) -> io::Result<()> {
    if !exists(&root.join("effects").join(name))? {
        return Ok(());
    }
    let revoked = || {
        invalid(
            "active release may have written; keep it available for read/export and reconcile state offline",
        )
    };
    let sources = current["rollback_sources"].as_array().ok_or_else(revoked)?;
    let target = required(target, "source")?;
    if sources
        .iter()
        .all(|value| value.as_str().is_some_and(|sha| archive::hex(sha, 40)))
        && sources.iter().any(|value| value == target)
    {
        Ok(())
    } else {
        Err(revoked())
    }
}

fn rendered(as_json: bool, data: Value, plain: String) {
    if as_json {
        println!("{data}");
    } else {
        println!("{plain}");
    }
}

fn report(
    root: &Path,
    opts: &Options<'_>,
    installed: &str,
    outcome: &str,
    detail: &str,
    latest: &str,
) -> io::Result<u8> {
    use time::format_description::well_known::Rfc3339;
    let at = time::OffsetDateTime::now_utc()
        .format(&Rfc3339)
        .map_err(io::Error::other)?;
    let mut data = json!({"checked_at":at,"installed":installed,"owner":"self","outcome":outcome,"detail":detail});
    if !latest.is_empty() {
        data["latest"] = latest.into();
        if outcome == "staged" {
            data["staged_version"] = latest.into();
        }
    }
    save(&root.join("upgrade.json"), &data)?;
    rendered(opts.json, data, format!("{outcome}: {detail}"));
    Ok(0)
}

fn check(
    opts: &Options<'_>,
    installed: &str,
    action: &str,
    reason: &str,
    latest: &str,
    refusal: &str,
) -> u8 {
    let display = if latest.is_empty() {
        "(unresolved)"
    } else {
        latest
    };
    rendered(
        opts.json,
        json!({"Action": action,"Reason": reason,"Latest":latest,
        "Owner":"self","RefuseOutcome":refusal}),
        format!("installed {installed}, latest {display}: would {action} ({reason})"),
    );
    0
}

fn writer_locks(root: &Path) -> io::Result<Vec<File>> {
    let mut custody = vec![secure_lock(&root.join("writers.lock"))?];
    let scope = crate::ipc::state_path()?;
    let daemon = scope.join("daemon.lock");
    let paths = BTreeSet::from([
        daemon.clone(),
        scope.join(".rust-state-writer.lock"),
        crate::ipc::env_path("SWARM_DAEMON_LOCK").unwrap_or(daemon),
    ]);
    let mut known = BTreeSet::new();
    for path in paths {
        if exists(&path)? {
            let meta = fs::symlink_metadata(&path)?;
            if !meta.is_file() {
                return Err(invalid("unsafe configured state writer lock"));
            }
            if known.insert((meta.dev(), meta.ino())) {
                custody.push(secure_lock(&path)?);
            }
        }
    }
    if fs::read_dir(root.join("inflight"))?
        .next()
        .transpose()?
        .is_some()
    {
        return Err(invalid(
            "unfinished managed writers require offline ownership and effect reconciliation",
        ));
    }
    Ok(custody)
}

pub(super) fn run(root: &Path, exe: &Path, opts: Options<'_>) -> io::Result<u8> {
    match transaction(root, exe, &opts) {
        Err(error) if error.kind() == io::ErrorKind::WouldBlock => {
            let defer = matches!(opts.step, "activate" | "rollback" | "unattended");
            let outcome = if defer { "deferred-wirebump" } else { "busy" };
            let detail = "a managed writer or upgrader holds the installation lease";
            rendered(
                opts.json,
                json!({"outcome":outcome,"detail":detail}),
                format!("{outcome}: {detail}"),
            );
            Ok(if defer { 2 } else { 0 })
        }
        result => result,
    }
}

fn transaction(root: &Path, exe: &Path, opts: &Options<'_>) -> io::Result<u8> {
    let _admin = secure_lock(&root.join("admin.lock"))?;
    let current = route(root)?;
    let active = release_path(root, &current.active)?;
    if exe != active.join("swarm") {
        return Err(invalid("use the current managed launcher"));
    }
    let card = value(&active.join("compat.json"))?;
    let installed = required(&card, "version")?;
    let pending = root.join("staged.json");
    let mut staged: Option<Stage> = if exists(&pending)? {
        Some(serde_json::from_value(value(&pending)?)?)
    } else {
        None
    };
    staged = staged.filter(|stage| stage.from == current.active);
    if opts.step == "status" {
        let unfinished = fs::read_dir(root.join("inflight"))?
            .map(|entry| Ok(entry?.file_name().to_string_lossy().into_owned()))
            .collect::<io::Result<BTreeSet<_>>>()?;
        println!(
            "{}",
            json!({"owner":"self","active":current.active,"previous":current.previous,"held":current.held,
            "staged":staged,"unfinished_writers":unfinished,"active_may_have_written":exists(&root.join("effects").join(&current.active))?})
        );
        return Ok(0);
    }
    if matches!(opts.step, "check" | "stage" | "unattended") {
        let feed = Feed::open(opts.dist.clone());
        let metadata = feed.get("checksums.txt", 32 * 1024);
        let bytes = match metadata {
            Ok(bytes) => bytes,
            Err(error) if error.kind() == io::ErrorKind::NotConnected => {
                let detail = "signed Rust preview channel unavailable";
                return if opts.step == "check" {
                    Ok(check(opts, installed, "refuse", detail, "", "offline"))
                } else {
                    report(root, opts, installed, "offline", detail, "")
                };
            }
            Err(error) => return Err(error),
        };
        let key = archive::read_bounded(owner_file(&root.join("public.pem"), false)?, 4096)?;
        let sig = feed.get("checksums.txt.sig", 4096)?;
        let utf8 = |bytes| String::from_utf8(bytes).map_err(io::Error::other);
        let sums = archive::checksums(&utf8(key)?, &bytes, &utf8(sig)?)?;
        let (latest, selected, group) = assets(&sums)?;
        let cmp = version(latest)?.cmp(&version(installed)?);
        let allow = opts.allow_downgrade && opts.step != "unattended";
        let (action, outcome, reason) = if current.held.as_deref() == Some(latest) {
            ("refuse", "held", "this version was rolled back".to_owned())
        } else if cmp == Ordering::Equal {
            ("current", "current", format!("{installed} is current"))
        } else if cmp == Ordering::Less && !allow {
            (
                "refuse",
                "refused-downgrade",
                "use --allow-downgrade for a qualified older release".into(),
            )
        } else {
            ("stage", "", format!("{installed} -> {latest}"))
        };
        if opts.step == "check" {
            return Ok(check(
                opts,
                installed,
                action,
                &reason,
                latest,
                if action == "refuse" { outcome } else { "" },
            ));
        }
        if action != "stage" {
            return report(root, opts, installed, outcome, &reason, latest);
        }
        let mut files = archive::unpack(
            &feed.get(&selected, archive::MAX_ARCHIVE)?,
            &sums[&selected],
        )?;
        let target = archive_card(&files, latest, group)?;
        compatible(&verify_slot(&active)?, &target)?;
        for name in ADMIN {
            let data = feed.get(name, METADATA_LIMIT)?;
            if archive::sha(&data) != sums[name] {
                return Err(invalid("signed installer bytes differ"));
            }
            files.insert(name.into(), data);
        }
        let next = Stage {
            from: current.active.clone(),
            to: write_slot(root, files, &target, opts.check_media)?,
            downgrade: cmp == Ordering::Less,
        };
        save(&pending, &next)?;
        if opts.step == "stage" {
            return report(root, opts, installed, "staged", &next.to, latest);
        }
        staged = Some(next);
    }

    let _custody = writer_locks(root)?;
    let old = verify_slot(&active)?;
    if opts.step == "rollback" {
        let target = current
            .previous
            .as_deref()
            .ok_or_else(|| invalid("no previous release slot"))?;
        let previous = verify_slot(&release_path(root, target)?)?;
        compatible(&old, &previous)?;
        reverse_allowed(root, &current.active, &old, &previous)?;
        point(
            root,
            Route {
                active: target.into(),
                previous: None,
                held: Some(installed.into()),
            },
        )?;
        return report(
            root,
            opts,
            installed,
            "rolled-back",
            target,
            required(&previous, "version")?,
        );
    }
    let staged = staged.ok_or_else(|| invalid("no stage for the active release"))?;
    let target = verify_slot(&release_path(root, &staged.to)?)?;
    compatible(&old, &target)?;
    let latest = required(&target, "version")?;
    match version(latest)?.cmp(&version(installed)?) {
        Ordering::Less => {
            if !staged.downgrade {
                return Err(invalid("staged downgrade was not selected explicitly"));
            }
            reverse_allowed(root, &current.active, &old, &target)?;
        }
        Ordering::Equal => return Err(invalid("staged release is already current")),
        Ordering::Greater => (),
    }
    point(
        root,
        Route {
            active: staged.to.clone(),
            previous: Some(current.active),
            held: current.held,
        },
    )?;
    report(root, opts, installed, "activated", &staged.to, latest)
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn artifact_identity_does_not_guess_channel_or_arch() {
        let mut sums = ADMIN
            .iter()
            .map(|name| (name.to_string(), "a".repeat(64)))
            .collect::<BTreeMap<_, _>>();
        for group in GROUPS {
            sums.insert(format!("swarm-rust_0.2.0_{group}.tar.gz"), "b".repeat(64));
        }
        assert_eq!(assets(&sums).unwrap().0, "0.2.0");
        sums.remove("swarm-rust_0.2.0_linux_arm64.tar.gz");
        sums.insert("swarm_0.2.0_linux_arm64.tar.gz".into(), "c".repeat(64));
        assert!(assets(&sums).is_err());
        assert!(version("0.2.0-rc.2").unwrap() < version("0.2.0-rc.10").unwrap());
        assert!(version("0.2.0-rc.10").unwrap() < version("0.2.0").unwrap());
    }

    #[test]
    fn reverse_after_effects_requires_the_exact_prior_source() {
        let root = std::env::temp_dir().join(format!(
            "swarm-native-reverse-{}",
            runtime::random_id().unwrap()
        ));
        dir(&root).unwrap();
        dir(&root.join("effects")).unwrap();
        let previous = json!({"source":"a".repeat(40)});
        let read_compatible = json!({"rollback_sources":[previous["source"]]});
        let unproven = json!({"rollback_sources":["b".repeat(40)]});
        assert!(reverse_allowed(&root, "active", &unproven, &previous).is_ok());
        durable(&root.join("effects/active"), b"", 0o600).unwrap();
        durable(&root.join("recent-work"), b"new work", 0o600).unwrap();
        assert!(reverse_allowed(&root, "active", &unproven, &previous).is_err());
        assert!(reverse_allowed(&root, "active", &read_compatible, &previous).is_ok());
        assert_eq!(fs::read(root.join("recent-work")).unwrap(), b"new work");
        fs::remove_dir_all(root).unwrap();
    }

    #[test]
    fn route_switch_records_previous_generation_and_excludes_another_holder() {
        let root = std::env::temp_dir().join(format!(
            "swarm-native-upgrade-{}",
            runtime::random_id().unwrap()
        ));
        dir(&root).unwrap();
        for name in ["releases", "routes", "inflight"] {
            dir(&root.join(name)).unwrap();
        }
        let old = format!("0.1.0-{}", "a".repeat(40));
        let new = format!("0.2.0-{}", "b".repeat(40));
        for name in [&old, &new] {
            let slot = root.join("releases").join(name);
            dir(&slot).unwrap();
            durable(&slot.join("swarm"), b"test", 0o755).unwrap();
        }
        durable(&root.join("writers.lock"), b"", 0o600).unwrap();
        symlink("current/swarm", root.join("swarm")).unwrap();
        point(
            &root,
            Route {
                active: old.clone(),
                previous: None,
                held: None,
            },
        )
        .unwrap();
        assert_eq!(route(&root).unwrap().active, old);
        let held = secure_lock(&root.join("writers.lock")).unwrap();
        assert!(secure_lock(&root.join("writers.lock")).is_err());
        drop(held);
        point(
            &root,
            Route {
                active: new.clone(),
                previous: Some(old.clone()),
                held: None,
            },
        )
        .unwrap();
        let record = route(&root).unwrap();
        assert_eq!(record.active, new);
        assert_eq!(record.previous, Some(old));
        fs::remove_dir_all(root).unwrap();
    }
}
