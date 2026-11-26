use super::{directory, entries, existing, qualified, receipt};
use crate::{ipc::field, supervisor::runtime};
use bbolt_rs::{Bolt, BucketApi, DbApi, TxApi};
use serde_json::{Value, json};
use std::os::{
    fd::AsRawFd,
    unix::fs::{MetadataExt, OpenOptionsExt},
};
use std::{
    collections::BTreeMap,
    fs::{File, OpenOptions},
    io::{self, BufRead, BufReader, Read},
    path::{Path, PathBuf},
    time::Instant,
};

fn private_file(path: &Path, max: u64) -> io::Result<File> {
    let file = OpenOptions::new()
        .read(true)
        .custom_flags(libc::O_NOFOLLOW | libc::O_NONBLOCK)
        .open(path)?;
    let info = file.metadata()?;
    if !info.is_file()
        || info.nlink() != 1
        || info.uid() != unsafe { libc::geteuid() }
        || info.mode() & 0o077 != 0
        || info.len() > max
    {
        return Err(io::Error::other("expected a bounded private regular file"));
    }
    Ok(file)
}

pub(super) type BucketRows = Vec<(Vec<u8>, Vec<u8>)>;
type Lookup<'a> = dyn Fn(&[&[u8]]) -> io::Result<BucketRows> + 'a;

fn read_bucket<'tx>(bucket: &impl BucketApi<'tx>, end: Instant) -> io::Result<BucketRows> {
    let mut rows = Vec::new();
    let mut total = 0;
    for (key, value) in bucket.iter_entries() {
        total += key.len() + value.len();
        if total > 64 << 20 || rows.len() >= 4096 || Instant::now() >= end {
            return Err(io::Error::other("receipt inventory limit reached"));
        }
        rows.push((key.to_vec(), value.to_vec()));
    }
    Ok(rows)
}

pub(crate) fn open_bolt(path: &Path, max: u64) -> io::Result<(File, impl DbApi + use<>)> {
    let file = private_file(path, max)?;
    if file.metadata()?.len() < 4096 {
        return Err(io::Error::other("incomplete bbolt file"));
    }
    // Go and Rust share this flock. Pin the inode for the second read-only
    // descriptor and refuse immediately if a live writer owns the database.
    if unsafe { libc::flock(file.as_raw_fd(), libc::LOCK_SH | libc::LOCK_NB) } != 0 {
        return Err(io::Error::other("database is locked by its writer"));
    }
    let alias = if cfg!(target_os = "macos") {
        "/dev/fd"
    } else {
        "/proc/self/fd"
    };
    let db = Bolt::open_ro(format!("{alias}/{}", file.as_raw_fd())).map_err(io::Error::other)?;
    Ok((file, db))
}

pub(super) fn bolt<T>(
    path: &Path,
    max: u64,
    end: Instant,
    inspect: impl FnOnce(&Lookup<'_>) -> io::Result<T>,
) -> io::Result<T> {
    let (_pin, db) = open_bolt(path, max)?;
    let tx = db.begin().map_err(io::Error::other)?;
    let lookup = |path: &[&[u8]]| {
        let (first, rest) = path
            .split_first()
            .ok_or_else(|| io::Error::other("empty receipt bucket path"))?;
        let bucket = tx
            .bucket(first)
            .ok_or_else(|| io::Error::other("missing receipt bucket"))?;
        match rest {
            [] => read_bucket(&bucket, end),
            [name] => {
                let nested = bucket
                    .bucket(name)
                    .ok_or_else(|| io::Error::other("missing nested receipt bucket"))?;
                read_bucket(&nested, end)
            }
            _ => Err(io::Error::other("unqualified receipt bucket depth")),
        }
    };
    inspect(&lookup)
}

fn mailbox(path: &Path, end: Instant) -> io::Result<Vec<Value>> {
    let (_pin, db) = match open_bolt(path, 256 << 20) {
        Err(e) if e.kind() == io::ErrorKind::NotFound => return Ok(Vec::new()),
        result => result?,
    };
    let tx = db.begin().map_err(io::Error::other)?;
    let notes = tx
        .bucket(b"notes")
        .ok_or_else(|| io::Error::other("missing worker notes"))?;
    let pending = tx
        .bucket(b"pending")
        .ok_or_else(|| io::Error::other("missing pending worker note index"))?;
    let mut rows = Vec::new();
    let mut bytes = 0;
    for (_, key) in pending.iter_entries() {
        if rows.len() >= 4096 || Instant::now() >= end {
            return Err(io::Error::other("worker mailbox inventory limit reached"));
        }
        let raw = notes
            .get(key)
            .ok_or_else(|| io::Error::other("pending worker note is missing"))?;
        bytes += raw.len();
        if bytes > 64 << 20 {
            return Err(io::Error::other("worker mailbox inventory limit reached"));
        }
        let note: Value = serde_json::from_slice(raw)?;
        let id = field(&note, "ID");
        if crate::supervisor::mail_store::key(&note, id)? != key
            || !note["State"].is_string()
            || !note["Kind"].is_string()
        {
            return Err(io::Error::other("pending worker binding is unverified"));
        }
        rows.push(note);
    }
    Ok(rows)
}

fn settled(evidence: &Value) -> bool {
    evidence["version"] == 1
        && evidence["receipt"]["status"] == "admitted"
        && evidence["outcome_at"].as_i64().is_some_and(|n| n > 0)
        && matches!(
            field(evidence, "outcome_source"),
            "live" | "imported" | "reconciled"
        )
        && matches!(
            field(evidence, "outcome"),
            "completed" | "failed" | "cancelled" | "declined"
        )
}
fn operations(path: &Path, end: Instant) -> io::Result<BTreeMap<String, Value>> {
    let parent = path.parent().expect("known receipt path");
    directory(parent)?;
    let file = private_file(path, 256 << 20)?;
    let before = file.metadata()?;
    let mut rd = BufReader::new(file.take(before.len()));
    let mut line = Vec::new();
    let mut pending = BTreeMap::new();
    loop {
        if Instant::now() >= end {
            return Err(io::Error::other("receipt inventory deadline reached"));
        }
        line.clear();
        if (&mut rd)
            .take((16 << 20) + 1)
            .read_until(b'\n', &mut line)?
            == 0
        {
            break;
        }
        if !line.ends_with(b"\n") || line.len() > 16 << 20 {
            return Err(io::Error::other("unsealed or oversized receipt record"));
        }
        let item: Value = serde_json::from_slice(&line)?;
        let id = field(&item, "operation_id");
        let action = field(&item, "action");
        let phase = field(&item, "phase");
        if id.is_empty() || action.is_empty() || phase.is_empty() {
            return Err(io::Error::other("damaged operation receipt"));
        }
        let unresolved = phase == "completed"
            && !item["request"].is_null()
            && matches!(action, "semantic_send" | "composer_send")
            && !settled(&item["evidence"]);
        if matches!(phase, "failed" | "completed") && !unresolved {
            pending.remove(id);
        } else {
            let phase = if unresolved {
                "native_outcome_unresolved"
            } else {
                phase
            };
            pending.insert(
                id.into(),
                json!({"operation_id":id,"action":action,"phase":phase,
                "session_id":item["session_id"],"session_instance":item["session_instance"]}),
            );
            if pending.len() > 100_000 {
                return Err(io::Error::other("operation inventory limit reached"));
            }
        }
    }
    let after = fs_meta(path)?;
    if before.dev() != after.dev()
        || before.ino() != after.ino()
        || before.len() != after.len()
        || before.modified()? != after.modified()?
    {
        return Err(io::Error::other("receipt log changed during inventory"));
    }
    Ok(pending)
}

fn fs_meta(path: &Path) -> io::Result<std::fs::Metadata> {
    std::fs::symlink_metadata(path)
}

fn semantic(
    rust: &Path,
    endpoint: &str,
    path: &Path,
    binding: &Value,
) -> io::Result<Option<(String, Value)>> {
    let operation = field(binding, "operation");
    let session = field(binding, "session");
    let instance = field(binding, "instance");
    let digest = crate::providers::codex_receipt::hash(operation);
    if operation.is_empty()
        || instance.is_empty()
        || field(binding, "intent").len() != 64
        || path.file_name().and_then(|s| s.to_str()) != Some(&format!("semantic-{digest}.json"))
    {
        return Err(io::Error::other("unverified semantic operation binding"));
    }
    let local = session
        .strip_prefix(endpoint)
        .and_then(|s| s.strip_prefix('/'))
        .filter(|s| runtime::valid_id(s))
        .ok_or_else(|| io::Error::other("invalid semantic receipt target"))?;
    let dir = rust.join(local);
    let mut state = "outcome_unknown".to_owned();
    for store in ["semantic", "opencode-receipts", "hermes-semantic"] {
        let parent = dir.join(store);
        if !existing(&parent)? {
            continue;
        }
        directory(&dir)?;
        directory(&parent)?;
        let file = parent.join(format!("{digest}.json"));
        if !existing(&file)? {
            continue;
        }
        let item = runtime::read_json(&file)?;
        let record = &item["receipt"];
        if record["operation_id"] != operation
            || record["session"] != session
            || record["session_instance"] != instance
        {
            return Err(io::Error::other("native semantic receipt binding changed"));
        }
        let phase = field(record, "state");
        if matches!(
            phase,
            "completed" | "failed" | "cancelled" | "declined" | "rejected"
        ) {
            return Ok(None);
        }
        if !phase.is_empty() {
            state = phase.to_owned();
        }
        break;
    }
    Ok(Some((
        session.into(),
        receipt(operation, "semantic_send", &state, endpoint, instance),
    )))
}

pub(super) fn inspect(
    root: &Path,
    endpoint: &str,
    rows: &mut BTreeMap<String, crate::checkout::Session>,
    report: &mut crate::checkout::Report,
    additional: &mut Vec<PathBuf>,
    end: Instant,
) {
    let rust = root.join("rust-v1");
    let log = root.join("idempotency/idempotency.log");
    if !existing(&rust).unwrap_or(false) || existing(&log).unwrap_or(true) {
        match operations(&log, end) {
            Ok(records) => {
                for (_, row) in records {
                    let id = qualified(endpoint, field(&row, "session_id"));
                    let item = receipt(
                        field(&row, "operation_id"),
                        field(&row, "action"),
                        field(&row, "phase"),
                        endpoint,
                        field(&row, "session_instance"),
                    );
                    associate(rows, report, &id, item);
                }
            }
            Err(e) => report.unknowns.push(format!(
                "supervisor {root:?}: receipt evidence unavailable: {e}"
            )),
        }
    }
    match entries(&rust.join("operations"), 4096, end) {
        Ok(list) => {
            for entry in list {
                if entry.path().extension().and_then(|s| s.to_str()) != Some("json") {
                    continue;
                }
                let path = entry.path();
                if entry
                    .file_name()
                    .to_str()
                    .is_some_and(|name| name.starts_with("semantic-"))
                {
                    match runtime::read_json(&path)
                        .and_then(|binding| semantic(&rust, endpoint, &path, &binding))
                    {
                        Ok(Some((id, item))) => associate(rows, report, &id, item),
                        Ok(None) => {}
                        Err(e) => report.unknowns.push(format!(
                            "supervisor {root:?}: Rust semantic receipt unavailable: {e}"
                        )),
                    }
                    continue;
                }
                let parsed = runtime::read_json(&entry.path()).and_then(|row| {
                    if row["schema"] != 1
                        || field(&row, "operation_id").is_empty()
                        || field(&row, "state").is_empty()
                    {
                        return Err(io::Error::other("unqualified Rust operation record"));
                    }
                    Ok(row)
                });
                match parsed {
                    Ok(row)
                        if !matches!(
                            field(&row, "state"),
                            "applied"
                                | "rejected"
                                | "failed"
                                | "completed"
                                | "cancelled"
                                | "declined"
                        ) =>
                    {
                        let id = qualified(endpoint, field(&row, "session_id"));
                        let kind = row["action"].as_str().unwrap_or("owner_launch");
                        associate(
                            rows,
                            report,
                            &id,
                            receipt(
                                field(&row, "operation_id"),
                                kind,
                                field(&row, "state"),
                                endpoint,
                                field(&row, "session_instance"),
                            ),
                        );
                    }
                    Ok(_) => {}
                    Err(e) => report.unknowns.push(format!(
                        "supervisor {root:?}: Rust operation receipt unavailable: {e}"
                    )),
                }
            }
        }
        Err(e) if e.kind() == io::ErrorKind::NotFound => {}
        Err(e) => report.unknowns.push(format!(
            "supervisor {root:?}: Rust operations unavailable: {e}"
        )),
    }
    match mailbox(&root.join("dispatcher-mail.db"), end) {
        Ok(notes) => {
            for note in notes {
                let session = field(&note, "Session");
                let instance = field(&note, "Instance");
                let item = receipt(
                    field(&note, "ID"),
                    &format!("worker_mailbox_{}", field(&note, "Kind")),
                    field(&note, "State"),
                    endpoint,
                    instance,
                );
                if let Some(row) = rows.get_mut(session).filter(|row| {
                    row.worker_instance == instance
                        && row.worker_attempt == field(&note, "Attempt")
                        && row.discussion == field(&note, "Discussion")
                }) {
                    row.unresolved_receipts.push(item);
                } else {
                    report.unlinked_receipts.push(item);
                }
                let project = Path::new(field(&note, "Project"));
                if project.is_absolute() {
                    additional.push(project.to_owned());
                }
            }
        }
        Err(e) => report.unknowns.push(format!(
            "supervisor {root:?}: pending worker mailbox unavailable: {e}"
        )),
    }
}
fn associate(
    rows: &mut BTreeMap<String, crate::checkout::Session>,
    report: &mut crate::checkout::Report,
    id: &str,
    item: Value,
) {
    if let Some(row) = rows.get_mut(id) {
        row.unresolved_receipts.push(item);
    } else {
        report.unlinked_receipts.push(item);
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use bbolt_rs::{BucketRwApi, DbRwAPI, TxRwApi, TxRwRefApi};
    use serde_json::json;
    use std::time::Duration;

    #[test]
    fn mailbox_snapshot_never_creates_or_opens_a_writer_and_matches_incarnation() {
        let root = std::env::temp_dir().join(runtime::random_id().unwrap());
        runtime::private_dir(&root).unwrap();
        let path = root.join("dispatcher-mail.db");
        let end = Instant::now() + Duration::from_secs(5);
        assert!(mailbox(&path, end).unwrap().is_empty());
        assert!(!path.exists());
        let note = json!({"ID":"one","Kind":"complete","State":"pending","Session":"ep/local",
            "Instance":"instance","Discussion":"discussion","Attempt":"attempt","Project":root,"Text":"private"});
        let key = crate::supervisor::mail_store::key(&note, "one").unwrap();
        {
            OpenOptions::new()
                .create_new(true)
                .write(true)
                .mode(0o600)
                .open(&path)
                .unwrap();
            let mut db = Bolt::open(&path).unwrap();
            let mut tx = db.begin_rw().unwrap();
            tx.create_bucket(b"notes")
                .unwrap()
                .put(&key, serde_json::to_vec(&note).unwrap())
                .unwrap();
            tx.create_bucket(b"pending")
                .unwrap()
                .put(b"entry", &key)
                .unwrap();
            tx.commit().unwrap();
            assert!(
                mailbox(&path, end)
                    .unwrap_err()
                    .to_string()
                    .contains("locked")
            );
        }
        let before = std::fs::read(&path).unwrap();
        assert_eq!(mailbox(&path, end).unwrap(), vec![note]);
        assert_eq!(std::fs::read(&path).unwrap(), before);
        std::fs::remove_dir_all(root).unwrap();
    }
}
