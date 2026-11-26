//! Go-compatible worker mailbox. Note, sequence and pending index commit together.
use super::mail_files::{Captured, Descriptor, MAX_BYTES};
use bbolt_rs::{
    Bolt, BucketApi, BucketRwApi, CursorApi, DbApi, DbRwAPI, TxApi, TxRwApi, TxRwRefApi,
};
use serde_json::{Value, json};
use std::os::unix::fs::{MetadataExt, OpenOptionsExt};
use std::{
    fs::{File, OpenOptions},
    io,
    path::{Path, PathBuf},
};
use time::{OffsetDateTime, format_description::well_known::Rfc3339};

const ZERO: &str = "0001-01-01T00:00:00Z";

fn io_error(err: impl std::fmt::Display) -> io::Error {
    io::Error::other(err.to_string())
}
fn absent() -> io::Error {
    io_error("worker mailbox: unknown note")
}

pub(crate) fn key(binding: &Value, id: &str) -> io::Result<Vec<u8>> {
    let mut parts = Vec::with_capacity(6);
    for name in ["Discussion", "Session", "Instance", "Attempt"] {
        let value = binding[name]
            .as_str()
            .filter(|value| !value.is_empty() && !value.contains('\0') && value.len() <= 512)
            .ok_or_else(|| io_error("invalid worker mailbox binding"))?;
        parts.push(value);
    }
    if id.is_empty() || id.len() > 128 || id.contains('\0') {
        return Err(io_error("invalid worker message ID"));
    }
    parts.push(id);
    Ok(parts.join("\0").into_bytes())
}

fn order(note: &Value) -> io::Result<Vec<u8>> {
    let discussion = note["Discussion"]
        .as_str()
        .filter(|d| !d.is_empty() && !d.contains('\0'))
        .ok_or_else(|| io_error("damaged worker discussion ordering"))?;
    let sequence = note["Sequence"]
        .as_u64()
        .filter(|n| *n > 0)
        .ok_or_else(|| io_error("damaged worker sequence"))?;
    Ok([discussion.as_bytes(), b"\0", &sequence.to_be_bytes()].concat())
}

fn open_path(root: &Path, create: bool) -> io::Result<PathBuf> {
    if !root.is_absolute() {
        return Err(io_error(
            "worker mailbox needs an absolute supervisor state directory",
        ));
    }
    let path = root.join("dispatcher-mail.db");
    let open = || {
        OpenOptions::new()
            .read(true)
            .custom_flags(libc::O_NOFOLLOW | libc::O_NONBLOCK)
            .open(&path)
    };
    let file = match open() {
        Ok(file) => file,
        Err(err) if create && err.kind() == io::ErrorKind::NotFound => {
            let file = OpenOptions::new()
                .write(true)
                .create_new(true)
                .mode(0o600)
                .open(&path);
            match file {
                Ok(file) => {
                    file.sync_all()?;
                    File::open(root)?.sync_all()?;
                }
                Err(err) if err.kind() == io::ErrorKind::AlreadyExists => {}
                Err(err) => return Err(err),
            }
            open()?
        }
        Err(err) => return Err(err),
    };
    let meta = file.metadata()?;
    // SAFETY: geteuid has no arguments.
    if !meta.is_file()
        || meta.nlink() != 1
        || meta.uid() != unsafe { libc::geteuid() }
        || meta.mode() & 0o777 != 0o600
    {
        return Err(io_error("worker mailbox is not a private owner file"));
    }
    Ok(path)
}

fn now() -> io::Result<String> {
    OffsetDateTime::now_utc().format(&Rfc3339).map_err(io_error)
}

fn descriptors<'tx>(tx: &impl TxApi<'tx>, key: &[u8]) -> io::Result<Vec<Descriptor>> {
    let Some(root) = tx.bucket(b"note-files") else {
        return Ok(Vec::new());
    };
    let Some(files) = root.bucket(key) else {
        return Ok(Vec::new());
    };
    let manifest: Value = serde_json::from_slice(files.get(b"manifest").ok_or_else(absent)?)?;
    let descriptors: Vec<Descriptor> = serde_json::from_value(manifest["Files"].clone())?;
    if descriptors.is_empty() || descriptors.len() > super::mail_files::MAX_FILES {
        return Err(io_error("damaged worker file manifest"));
    }
    Ok(descriptors)
}

fn with_files<'tx>(tx: &impl TxApi<'tx>, key: &[u8], mut note: Value) -> io::Result<Value> {
    let files = descriptors(tx, key)?;
    if !files.is_empty() {
        note["Files"] = serde_json::to_value(files)?;
    }
    Ok(note)
}

pub(super) fn submit(
    root: &Path,
    binding: &Value,
    id: &str,
    kind: &str,
    text: &str,
    fresh: bool,
    files: &[Captured],
) -> io::Result<Value> {
    let text = if !files.is_empty() && text.is_empty() {
        "Worker attached files to this completion."
    } else {
        text
    };
    if !files.is_empty() && kind != "complete" {
        return Err(io_error("worker files require a completion note"));
    }
    if !crate::teammate::valid_kind(kind) || !crate::teammate::valid_text(text) {
        return Err(io_error("invalid worker note"));
    }
    let key = key(binding, id)?;
    let path = open_path(root, true)?;
    let mut db = Bolt::open(path).map_err(io_error)?;
    let mut tx = db.begin_rw().map_err(io_error)?;
    if let Some(data) = tx
        .bucket(b"notes")
        .and_then(|notes| notes.get(&key).map(<[u8]>::to_vec))
    {
        let prior: Value = serde_json::from_slice(&data)?;
        if prior["ID"] != id
            || prior["Kind"] != kind
            || prior["Text"] != text
            || binding
                .as_object()
                .is_none_or(|fields| fields.iter().any(|(field, value)| prior[field] != *value))
            || !descriptors(&tx, &key)?
                .iter()
                .eq(files.iter().map(|file| &file.file))
        {
            return Err(io_error("worker mailbox: conflicting note"));
        }
        return with_files(&tx, &key, prior);
    }
    if !fresh {
        return Err(io_error("worker mailbox: expired new note"));
    }
    let sequence = {
        let mut queue = tx
            .create_bucket_if_not_exists(b"pending")
            .map_err(io_error)?;
        if queue.stats().key_n() >= 4096 {
            return Err(io_error("worker mailbox backlog full"));
        }
        queue.next_sequence().map_err(io_error)?
    };
    let mut note = binding.clone();
    let obj = note
        .as_object_mut()
        .ok_or_else(|| io_error("invalid worker mailbox binding"))?;
    for (name, value) in [
        ("ID", json!(id)),
        ("Kind", json!(kind)),
        ("Text", json!(text)),
        ("Answer", json!("")),
        ("State", json!("queued")),
        ("Sequence", json!(sequence)),
        ("Created", json!(now()?)),
        ("Settled", json!(ZERO)),
    ] {
        obj.insert(name.into(), value);
    }
    {
        let mut notes = tx.create_bucket_if_not_exists(b"notes").map_err(io_error)?;
        if notes.stats().key_n() >= 8192 {
            return Err(io_error("worker mailbox backlog full"));
        }
        notes
            .put(&key, serde_json::to_vec(&note)?)
            .map_err(io_error)?;
    }
    if !files.is_empty() {
        let bytes: usize = files.iter().map(|file| file.data.len()).sum();
        if bytes > MAX_BYTES || tx.size().saturating_add(bytes as u64) > 256 << 20 {
            return Err(io_error("worker file storage full"));
        }
        let mut root = tx
            .create_bucket_if_not_exists(b"note-files")
            .map_err(io_error)?;
        let mut bucket = root.create_bucket(&key).map_err(io_error)?;
        for file in files {
            bucket.put(&file.file.id, &file.data).map_err(io_error)?;
        }
        let descriptors: Vec<_> = files.iter().map(|file| &file.file).collect();
        bucket
            .put(
                b"manifest",
                serde_json::to_vec(&json!({"Files": descriptors}))?,
            )
            .map_err(io_error)?;
    }
    tx.bucket_mut(b"pending")
        .ok_or_else(absent)?
        .put(order(&note)?, &key)
        .map_err(io_error)?;
    let note = with_files(&tx, &key, note)?;
    tx.commit().map_err(io_error)?;
    Ok(note)
}

pub(crate) fn lookup(root: &Path, binding: &Value, id: &str) -> io::Result<Value> {
    let key = key(binding, id)?;
    let path = open_path(root, false)?;
    let db = Bolt::open_ro(path).map_err(io_error)?;
    let tx = db.begin().map_err(io_error)?;
    let data = tx
        .bucket(b"notes")
        .and_then(|notes| notes.get(&key).map(<[u8]>::to_vec))
        .ok_or_else(absent)?;
    let note: Value = serde_json::from_slice(&data)?;
    if note["ID"] != id
        || binding
            .as_object()
            .is_none_or(|fields| fields.iter().any(|(field, value)| note[field] != *value))
    {
        return Err(io_error("worker mailbox binding changed"));
    }
    with_files(&tx, &key, note)
}

pub(crate) fn pending(
    root: &Path,
    discussion: &str,
    after: u64,
    matches: impl Fn(&Value) -> bool,
) -> io::Result<Vec<Value>> {
    let path = match open_path(root, false) {
        Ok(path) => path,
        Err(err) if err.kind() == io::ErrorKind::NotFound => return Ok(Vec::new()),
        Err(err) => return Err(err),
    };
    let db = Bolt::open_ro(path).map_err(io_error)?;
    let tx = db.begin().map_err(io_error)?;
    let Some(notes) = tx.bucket(b"notes") else {
        return Ok(Vec::new());
    };
    let Some(pending) = tx.bucket(b"pending") else {
        return Ok(Vec::new());
    };
    let prefix = format!("{discussion}\0");
    let mut cursor = pending.cursor();
    let start = [prefix.as_bytes(), &after.saturating_add(1).to_be_bytes()].concat();
    let mut row = cursor.seek(&start);
    let mut result = Vec::new();
    while let Some((order_key, key)) = row {
        let key = key.ok_or_else(absent)?;
        if !order_key.starts_with(prefix.as_bytes()) || result.len() >= 12 {
            break;
        }
        let raw = notes.get(key).ok_or_else(absent)?;
        let note: Value = serde_json::from_slice(raw)?;
        if note["Discussion"] != discussion
            || note["State"] != "queued"
            || order(&note)? != order_key
        {
            return Err(io_error("damaged worker pending index"));
        }
        if matches(&note) {
            result.push(with_files(&tx, key, note)?);
        }
        row = cursor.next();
    }
    Ok(result)
}

pub(crate) fn original_queued_file(root: &Path, discussion: &str) -> io::Result<Option<Value>> {
    let path = match open_path(root, false) {
        Ok(path) => path,
        Err(error) if error.kind() == io::ErrorKind::NotFound => return Ok(None),
        Err(error) => return Err(error),
    };
    let (_pin, db) = crate::checkout::state::receipts::open_bolt(&path, 256 << 20)?;
    let tx = db.begin().map_err(io_error)?;
    let pending = tx.bucket(b"pending").ok_or_else(absent)?;
    let mut cursor = pending.cursor();
    let prefix = format!("{discussion}\0");
    let Some((order_key, raw_key)) = cursor.seek(prefix.as_bytes()) else {
        return Ok(None);
    };
    if !order_key.starts_with(prefix.as_bytes()) {
        return Ok(None);
    }
    let raw_key = raw_key.ok_or_else(absent)?.to_vec();
    let order_key = order_key.to_vec();
    if cursor
        .next()
        .is_some_and(|(key, _)| key.starts_with(prefix.as_bytes()))
    {
        return Err(io_error(
            "more than one old worker note is queued for this discussion",
        ));
    }
    let notes = tx.bucket(b"notes").ok_or_else(absent)?;
    let raw = notes
        .get(&raw_key)
        .filter(|value| value.len() <= 128 << 10)
        .ok_or_else(absent)?;
    let mut note: Value = serde_json::from_slice(raw)?;
    let id = note["ID"].as_str().ok_or_else(absent)?;
    if note["Discussion"] != discussion
        || note["Kind"] != "complete"
        || note["State"] != "queued"
        || order(&note)? != order_key
        || key(&note, id)? != raw_key
    {
        return Err(io_error("unqualified original queued worker completion"));
    }
    let files = serde_json::to_value(descriptors(&tx, &raw_key)?)?;
    let fields = [
        "Discussion",
        "Parent",
        "Session",
        "Instance",
        "Attempt",
        "Agent",
        "Project",
        "ParentInputHash",
        "Kind",
        "Text",
        "Sequence",
    ];
    let binding = Value::Object(
        fields
            .into_iter()
            .map(|key| (key.to_owned(), note[key].clone()))
            .collect(),
    );
    publication_files(&tx, &raw_key, &binding, id, &files, "queued")?;
    note["Files"] = files;
    Ok(Some(note))
}

pub(crate) fn read_publication_files(root: &Path, source: &Value) -> io::Result<Vec<Captured>> {
    let binding = &source["binding"];
    let id = source["id"].as_str().ok_or_else(absent)?;
    let key = key(binding, id)?;
    let path = open_path(root, false)?;
    let (_pin, db) = crate::checkout::state::receipts::open_bolt(&path, 256 << 20)?;
    let tx = db.begin().map_err(io_error)?;
    publication_files(&tx, &key, binding, id, &source["files"], "ingested")
}

fn publication_files<'a>(
    tx: &impl TxApi<'a>,
    key: &[u8],
    binding: &Value,
    id: &str,
    requested: &Value,
    state: &str,
) -> io::Result<Vec<Captured>> {
    let note: Value = serde_json::from_slice(
        tx.bucket(b"notes")
            .and_then(|notes| notes.get(key).map(<[u8]>::to_vec))
            .as_deref()
            .ok_or_else(absent)?,
    )?;
    if note["ID"] != id
        || note["Kind"] != "complete"
        || note["State"] != state
        || binding
            .as_object()
            .is_none_or(|fields| fields.iter().any(|(field, value)| note[field] != *value))
    {
        return Err(absent());
    }
    let selected: Vec<Descriptor> = serde_json::from_value(requested.clone())?;
    if selected.is_empty() || selected.len() > super::mail_files::MAX_FILES {
        return Err(io_error("invalid publication file selection"));
    }
    let all_files = descriptors(tx, key)?;
    let files_root = tx.bucket(b"note-files").ok_or_else(absent)?;
    let bucket = files_root.bucket(key).ok_or_else(absent)?;
    let mut total = 0usize;
    let mut files = Vec::with_capacity(selected.len());
    for file in selected {
        if !all_files.contains(&file)
            || files
                .iter()
                .any(|prior: &Captured| prior.file.id == file.id)
        {
            return Err(absent());
        }
        let data = bucket.get(&file.id).ok_or_else(absent)?;
        if !super::mail_files::verify(&file, data) {
            return Err(io_error(
                "worker publication bytes do not match their pinned receipt",
            ));
        }
        total += data.len();
        if total > MAX_BYTES {
            return Err(io_error("staged worker file total exceeds Discord limit"));
        }
        files.push(Captured {
            file,
            data: data.to_vec(),
        });
    }
    Ok(files)
}

/// Pin the exact proved selection while the Discord outcome remains outstanding.
pub(crate) fn pin_publication(
    root: &Path,
    source: &Value,
    nonce: &str,
) -> io::Result<Vec<Captured>> {
    let mut db = Bolt::open(open_path(root, false)?).map_err(io_error)?;
    pin_in(&mut db, source, nonce)
}

pub(crate) fn pin_original_publication(
    root: &Path,
    source: &Value,
    nonce: &str,
    identity: (u64, u64),
) -> io::Result<Vec<Captured>> {
    let (_pin, mut db) = open_original_writer(root, identity)?;
    pin_in(&mut db, source, nonce)
}

fn pin_in(db: &mut Bolt, source: &Value, nonce: &str) -> io::Result<Vec<Captured>> {
    let binding = &source["binding"];
    let id = source["id"].as_str().ok_or_else(absent)?;
    let key = key(binding, id)?;
    let mut tx = db.begin_rw().map_err(io_error)?;
    let files = publication_files(&tx, &key, binding, id, &source["files"], "ingested")?;
    let mut root_bucket = tx.bucket_mut(b"note-files").ok_or_else(absent)?;
    let mut bucket = root_bucket.bucket_mut(&key).ok_or_else(absent)?;
    let mut manifest: Value = serde_json::from_slice(bucket.get(b"manifest").ok_or_else(absent)?)?;
    let selection: Vec<_> = files.iter().map(|file| file.file.id.clone()).collect();
    if !manifest["Pins"][nonce].is_null() && manifest["Pins"][nonce] != json!(selection) {
        return Err(io_error("conflicting publication pin"));
    }
    if manifest["Pins"]
        .as_object()
        .is_some_and(|pins| pins.len() >= 128 && !pins.contains_key(nonce))
    {
        return Err(io_error("worker file publication pin limit"));
    }
    manifest["Pins"][nonce] = json!(selection);
    bucket
        .put(b"manifest", serde_json::to_vec(&manifest)?)
        .map_err(io_error)?;
    tx.commit().map_err(io_error)?;
    Ok(files)
}

fn open_original_writer(root: &Path, identity: (u64, u64)) -> io::Result<(File, Bolt)> {
    let path = open_path(root, false)?;
    let pin = OpenOptions::new()
        .read(true)
        .write(true)
        .custom_flags(libc::O_NOFOLLOW | libc::O_NONBLOCK)
        .open(&path)?;
    let metadata = pin.metadata()?;
    if (metadata.dev(), metadata.ino()) != identity
        || !metadata.is_file()
        || metadata.nlink() != 1
        || metadata.uid() != unsafe { libc::geteuid() }
        || metadata.mode() & 0o777 != 0o600
        || !(4096..=256 << 20).contains(&metadata.len())
    {
        return Err(io_error("original worker mailbox identity changed"));
    }
    let (pin, db) = super::runtime::bounded_bolt_writer(pin)?;
    let current = std::fs::symlink_metadata(path)?;
    if (current.dev(), current.ino()) != identity {
        return Err(io_error("original worker mailbox identity changed"));
    }
    Ok((pin, db))
}

pub(crate) fn release_prepared_publication(
    root: &Path,
    source: &Value,
    publication_id: &str,
    identity: (u64, u64),
) -> io::Result<()> {
    let (_pin, mut db) = open_original_writer(root, identity)?;
    let id = source["id"].as_str().ok_or_else(absent)?;
    let key = key(&source["binding"], id)?;
    let mut tx = db.begin_rw().map_err(io_error)?;
    let files = publication_files(
        &tx,
        &key,
        &source["binding"],
        id,
        &source["files"],
        "ingested",
    )?;
    let mut root = tx.bucket_mut(b"note-files").ok_or_else(absent)?;
    let mut note = root.bucket_mut(&key).ok_or_else(absent)?;
    let mut manifest: Value = serde_json::from_slice(note.get(b"manifest").ok_or_else(absent)?)?;
    if manifest["Pins"].is_null() {
        return Ok(());
    }
    let pins = manifest["Pins"].as_object_mut().ok_or_else(absent)?;
    let Some(saved) = pins.get(publication_id) else {
        return Ok(());
    };
    let expected = json!(files.iter().map(|file| &file.file.id).collect::<Vec<_>>());
    if saved != &expected {
        return Err(io_error(
            "original worker publication pin selection changed",
        ));
    }
    pins.shift_remove(publication_id);
    note.put(b"manifest", serde_json::to_vec(&manifest)?)
        .map_err(io_error)?;
    tx.commit().map_err(io_error)
}

pub(crate) fn release_publication(root: &Path, source: &Value, nonce: &str) -> io::Result<()> {
    let key = key(
        &source["binding"],
        source["id"].as_str().ok_or_else(absent)?,
    )?;
    let mut db = Bolt::open(open_path(root, false)?).map_err(io_error)?;
    let mut tx = db.begin_rw().map_err(io_error)?;
    let Some(mut root) = tx.bucket_mut(b"note-files") else {
        return Ok(());
    };
    let Some(mut bucket) = root.bucket_mut(&key) else {
        return Ok(());
    };
    let mut manifest: Value = serde_json::from_slice(bucket.get(b"manifest").ok_or_else(absent)?)?;
    let Some(pins) = manifest["Pins"].as_object_mut() else {
        return Ok(());
    };
    if pins.shift_remove(nonce).is_none() {
        return Ok(());
    }
    bucket
        .put(b"manifest", serde_json::to_vec(&manifest)?)
        .map_err(io_error)?;
    tx.commit().map_err(io_error)
}

pub(crate) fn manager_note(
    root: &Path,
    discussion: &str,
    parent: &str,
    input: &str,
    args: &Value,
    reply: bool,
) -> io::Result<Value> {
    let same = |note: &Value| note["Parent"] == parent && note["ParentInputHash"] == input;
    let filter = |note: &Value| {
        same(note)
            && [
                ("session", "Session"),
                ("session_instance", "Instance"),
                ("id", "ID"),
            ]
            .into_iter()
            .all(|(field, key)| args[field].is_null() || args[field] == note[key])
            && (!reply || note["Kind"] == "needs-input")
    };
    if !reply && args.as_object().is_some_and(|obj| obj.is_empty()) {
        let rows: Vec<_> = pending(root, discussion, 0, same)?.into_iter()
            .map(|note| {
                let mut row = json!({"id":note["ID"],"session":note["Session"],"session_instance":note["Instance"],
                    "kind":note["Kind"],"state":note["State"]});
                if !note["Files"].is_null() {
                    row["files"] = note["Files"].clone();
                }
                row
            }).collect();
        return Ok(json!({"notes":rows}));
    }
    let picked = {
        let path = open_path(root, false)?;
        let db = Bolt::open_ro(path).map_err(io_error)?;
        let tx = db.begin().map_err(io_error)?;
        let notes = tx.bucket(b"notes").ok_or_else(absent)?;
        let mut cursor = notes.cursor();
        let prefix = format!("{discussion}\0");
        let mut row = cursor.seek(&prefix);
        let mut chosen = None;
        while let Some((key, data)) = row {
            if !key.starts_with(prefix.as_bytes()) {
                break;
            }
            if let Some(data) = data {
                let note: Value = serde_json::from_slice(data)?;
                if filter(&note) && note["State"] != "superseded" {
                    if chosen.is_some() {
                        return Err(io_error("select one exact worker note"));
                    }
                    chosen = Some(note);
                }
            }
            row = cursor.next();
        }
        chosen.ok_or_else(absent)?
    };
    let binding = json!({"Discussion":picked["Discussion"],"Parent":picked["Parent"],"Session":picked["Session"],
        "Instance":picked["Instance"],"Attempt":picked["Attempt"],"Agent":picked["Agent"],
        "Project":picked["Project"],"ParentInputHash":picked["ParentInputHash"]});
    let answer = if reply {
        Some(
            args["text"]
                .as_str()
                .ok_or_else(|| io_error("worker reply requires exact text"))?,
        )
    } else {
        None
    };
    let updated = settle(
        root,
        &binding,
        picked["ID"].as_str().unwrap_or_default(),
        answer,
        false,
    )?;
    let mut result = json!({"id":updated["ID"],"kind":updated["Kind"],"state":updated["State"],
        "session":updated["Session"],"session_instance":updated["Instance"],"text":updated["Text"]});
    if !updated["Files"].is_null() {
        result["files"] = updated["Files"].clone();
        result["file_binding"] = binding;
    }
    Ok(result)
}

pub(super) fn settle(
    root: &Path,
    binding: &Value,
    id: &str,
    answer: Option<&str>,
    read: bool,
) -> io::Result<Value> {
    let key = key(binding, id)?;
    let mut db = Bolt::open(open_path(root, false)?).map_err(io_error)?;
    let mut tx = db.begin_rw().map_err(io_error)?;
    let has_files = !descriptors(&tx, &key)?.is_empty();
    let mut note: Value = serde_json::from_slice(
        &tx.bucket(b"notes")
            .and_then(|rows| rows.get(&key).map(<[u8]>::to_vec))
            .ok_or_else(absent)?,
    )?;
    if note["ID"] != id
        || binding
            .as_object()
            .is_none_or(|fields| fields.iter().any(|(field, value)| note[field] != *value))
    {
        return Err(io_error("worker mailbox binding changed"));
    }
    let state = note["State"].as_str().ok_or_else(absent)?;
    let mut terminal = false;
    if read {
        terminal = state == "answered"
            && note["Answer"]
                .as_str()
                .is_some_and(crate::teammate::valid_text);
        if !terminal || note["Settled"] != ZERO {
            return with_files(&tx, &key, note);
        }
    } else {
        if state == "superseded" {
            return Err(io_error(
                "worker note no longer belongs to the active attempt",
            ));
        }
        if let Some(answer) = answer {
            if note["Kind"] != "needs-input"
                || !crate::teammate::valid_text(answer)
                || note["Answer"] != "" && note["Answer"] != answer
            {
                return Err(io_error("worker mailbox: conflicting answer"));
            }
            note["Answer"] = json!(answer);
            note["State"] = json!("answered");
        } else if state == "queued" {
            // Keep immutable files available for an explicit later publication.
            terminal = note["Kind"] != "needs-input" && !has_files;
            note["State"] = json!("ingested");
        }
        tx.bucket_mut(b"pending")
            .ok_or_else(absent)?
            .delete(order(&note)?)
            .map_err(io_error)?;
    }
    if terminal && note["Settled"] == ZERO {
        note["Settled"] = json!(now()?);
        let stamp = crate::ipc::spawn::now_ms().to_be_bytes();
        tx.create_bucket_if_not_exists(b"settled")
            .map_err(io_error)?
            .put([stamp.as_slice(), &key].concat(), &key)
            .map_err(io_error)?;
    }
    tx.bucket_mut(b"notes")
        .ok_or_else(absent)?
        .put(&key, serde_json::to_vec(&note)?)
        .map_err(io_error)?;
    let note = with_files(&tx, &key, note)?;
    tx.commit().map_err(io_error)?;
    Ok(note)
}

#[cfg(test)]
mod tests {
    use super::*;
    #[test]
    fn pending_pages_past_twelve_failed_source_checks() -> io::Result<()> {
        let root =
            std::env::temp_dir().join(format!("mail-page-{}", super::super::runtime::random_id()?));
        super::super::runtime::private_dir(&root)?;
        let binding = json!({"Discussion":"discussion","Parent":"manager","Session":"endpoint/worker",
            "Instance":"worker","Attempt":"old","Agent":"codex","Project":"/project","ParentInputHash":"old"});
        for index in 0..13 {
            submit(
                &root,
                &binding,
                &format!("note-{index}"),
                "complete",
                "Queued",
                true,
                &[],
            )?;
        }
        let first = pending(&root, "discussion", 0, |_| true)?;
        assert_eq!(first.len(), 12);
        let next = pending(
            &root,
            "discussion",
            first[11]["Sequence"].as_u64().unwrap(),
            |_| true,
        )?;
        assert_eq!(next.len(), 1);
        assert_eq!(next[0]["ID"], "note-12");
        std::fs::remove_dir_all(&root)
    }

    #[test]
    fn older_scope_cannot_starve_current_manager_mail() -> io::Result<()> {
        let root = std::env::temp_dir().join(format!(
            "mail-scope-{}",
            super::super::runtime::random_id()?
        ));
        super::super::runtime::private_dir(&root)?;
        let mut binding = json!({"Discussion":"discussion","Parent":"manager","Session":"endpoint/worker",
            "Instance":"worker","Attempt":"prior","Agent":"codex","Project":"/project","ParentInputHash":"old"});
        for index in 0..65 {
            submit(
                &root,
                &binding,
                &format!("prior-{index}"),
                "complete",
                "Old",
                true,
                &[],
            )?;
        }
        binding["Attempt"] = json!("current");
        binding["ParentInputHash"] = json!("current");
        submit(&root, &binding, "current", "complete", "Current", true, &[])?;
        let mailbox = manager_note(&root, "discussion", "manager", "current", &json!({}), false)?;
        assert_eq!(mailbox["notes"].as_array().unwrap().len(), 1);
        assert_eq!(mailbox["notes"][0]["id"], "current");
        std::fs::remove_dir_all(&root)
    }

    #[test]
    #[ignore = "requires the locally built pinned Go mailbox oracle"]
    fn go_reads_the_exact_rust_pinned_worker_files() {
        use std::io::Write;
        use std::process::{Command, Stdio};
        let binary = std::env::var_os("SWARM_GO_WORKER_MAIL_FIXTURE").unwrap();
        let root = std::env::temp_dir().join(format!(
            "mail-file-interop-{}",
            crate::supervisor::runtime::random_id().unwrap()
        ));
        crate::supervisor::runtime::private_dir(&root).unwrap();
        let root = root.canonicalize().unwrap();
        let csv = root.join("dataset.csv");
        let pdf = root.join("report.pdf");
        let csv_data = b"source,result\nfixture,ok\n";
        let pdf_data = b"%PDF-1.7\n";
        std::fs::write(&csv, csv_data).unwrap();
        std::fs::write(&pdf, pdf_data).unwrap();
        let metadata = root.metadata().unwrap();
        let identity = (metadata.dev(), metadata.ino());
        let files = super::super::mail_files::capture(
            &root,
            identity,
            &[csv.to_str().unwrap(), pdf.to_str().unwrap()],
        )
        .unwrap();
        let binding = json!({"Discussion":"discussion","Parent":"manager","Session":"endpoint/worker","Instance":"instance",
           "Attempt":"attempt","Agent":"codex","Project":root,"ParentInputHash":"owned-input"});
        let id = "file-note";
        let queued = submit(&root, &binding, id, "complete", "", true, &files).unwrap();
        assert_eq!(queued["State"], "queued");
        assert_eq!(queued["Files"][0]["id"], files[0].file.id);
        std::fs::write(&csv, "edited checkout").unwrap();
        let replacement = super::super::mail_files::capture(
            &root,
            identity,
            &[csv.to_str().unwrap(), pdf.to_str().unwrap()],
        )
        .unwrap();
        assert!(submit(&root, &binding, id, "complete", "", true, &replacement).is_err());
        assert_eq!(lookup(&root, &binding, id).unwrap(), queued);
        let ingested = manager_note(
            &root,
            "discussion",
            "manager",
            "owned-input",
            &json!({"id":id}),
            false,
        )
        .unwrap();
        assert_eq!(ingested["state"], "ingested");
        assert_eq!(ingested["files"], queued["Files"]);
        let source = json!({"binding":binding,"id":id,"files":ingested["files"]});
        let pinned = pin_publication(&root, &source, "nonce").unwrap();
        assert_eq!(
            pinned
                .iter()
                .map(|file| file.data.as_slice())
                .collect::<Vec<_>>(),
            [csv_data.as_slice(), pdf_data.as_slice()]
        );
        let mut corrupt = source.clone();
        corrupt["files"][0]["size"] = json!(1);
        assert!(pin_publication(&root, &corrupt, "nonce").is_err());
        let mut foreign = source.clone();
        foreign["binding"]["Instance"] = json!("other-worker");
        assert!(pin_publication(&root, &foreign, "nonce").is_err());
        let mut changed_order = source.clone();
        changed_order["files"].as_array_mut().unwrap().reverse();
        assert!(pin_publication(&root, &changed_order, "nonce").is_err());
        release_publication(&root, &source, "nonce").unwrap();
        for file in &files {
            let mut worker = Command::new(&binary)
                .arg("read-file")
                .arg(&root)
                .stdin(Stdio::piped())
                .stdout(Stdio::piped())
                .spawn()
                .unwrap();
            write!(
                worker.stdin.take().unwrap(),
                "{}",
                json!({"Binding":binding,"ID":id,"FileID":file.file.id})
            )
            .unwrap();
            let result = worker.wait_with_output().unwrap();
            assert!(result.status.success());
            use base64::Engine;
            let encoded: String = serde_json::from_slice(&result.stdout).unwrap();
            assert_eq!(
                base64::engine::general_purpose::STANDARD
                    .decode(encoded)
                    .unwrap(),
                file.data
            );
        }
        std::fs::remove_dir_all(&root).unwrap();
    }

    #[test]
    #[ignore = "requires the locally built pinned Go mailbox oracle"]
    fn go_and_rust_share_worker_notes_answers_and_pending_order() {
        use std::io::Write;
        use std::process::{Command, Stdio};
        let binary = std::env::var_os("SWARM_GO_WORKER_MAIL_FIXTURE").unwrap();
        let root = std::env::temp_dir().join(format!(
            "mail-interop-{}",
            crate::supervisor::runtime::random_id().unwrap()
        ));
        crate::supervisor::runtime::private_dir(&root).unwrap();
        let binding = json!({"Discussion":"discussion","Parent":"manager","Session":"endpoint/worker","Instance":"instance",
           "Attempt":"attempt","Agent":"codex","Project":"/fixture","ParentInputHash":"owned-input"});
        let call = |action: &str, id: &str, text: &str| {
            let mut process = Command::new(&binary)
                .arg(action)
                .arg(&root)
                .stdin(Stdio::piped())
                .stdout(Stdio::piped())
                .stderr(Stdio::piped())
                .spawn()
                .unwrap();
            let mut input = process.stdin.take().unwrap();
            write!(
                input,
                "{}",
                json!({"Binding":binding,"ID":id,"Kind":"needs-input","Text":text})
            )
            .unwrap();
            drop(input);
            let out = process.wait_with_output().unwrap();
            assert!(
                out.status.success(),
                "Go oracle: {}",
                String::from_utf8_lossy(&out.stderr)
            );
            serde_json::from_slice::<Value>(&out.stdout).unwrap()
        };
        let historical = call("submit", "go-note", "Go question");
        assert_eq!(lookup(&root, &binding, "go-note").unwrap(), historical);
        assert_eq!(
            submit(
                &root,
                &binding,
                "go-note",
                "needs-input",
                "Go question",
                false,
                &[]
            )
            .unwrap(),
            historical
        );
        let new = submit(
            &root,
            &binding,
            "rust-note",
            "complete",
            "Rust result",
            true,
            &[],
        )
        .unwrap();
        assert_eq!(call("pending", "", ""), json!([historical, new]));
        assert_eq!(call("lookup", "rust-note", "")["Text"], "Rust result");
        settle(&root, &binding, "go-note", Some("Rust reply"), false).unwrap();
        assert_eq!(call("lookup", "go-note", "")["Answer"], "Rust reply");
        assert_eq!(call("pending", "", ""), json!([new]));
        std::fs::remove_dir_all(root).unwrap();
    }

    #[test]
    fn worker_requests_and_answers_replay_from_their_original_binding() {
        let root = std::env::temp_dir().join(format!(
            "mail-{}",
            crate::supervisor::runtime::random_id().unwrap()
        ));
        crate::supervisor::runtime::private_dir(&root).unwrap();
        let worker = json!({"Discussion":"discussion","Parent":"manager","Session":"endpoint/worker",
            "Instance":"worker-incarnation","Attempt":"attempt","Agent":"codex","Project":"/fixture","ParentInputHash":"input"});
        let created = submit(
            &root,
            &worker,
            "question",
            "needs-input",
            "Which path?",
            true,
            &[],
        )
        .unwrap();
        assert_eq!(
            submit(
                &root,
                &worker,
                "question",
                "needs-input",
                "Which path?",
                false,
                &[]
            )
            .unwrap(),
            created
        );
        assert!(
            submit(
                &root,
                &worker,
                "question",
                "needs-input",
                "Different body",
                true,
                &[]
            )
            .is_err()
        );
        assert!(
            submit(
                &root,
                &worker,
                "expired",
                "complete",
                "Finished",
                false,
                &[]
            )
            .is_err()
        );
        assert_eq!(
            pending(&root, "discussion", 0, |_| true).unwrap(),
            [created]
        );
        let filter = json!({"session":"endpoint/worker","session_instance":"worker-incarnation","id":"question"});
        assert!(
            manager_note(
                &root,
                "discussion",
                "manager",
                "foreign-input",
                &filter,
                false
            )
            .is_err()
        );
        let seen = manager_note(&root, "discussion", "manager", "input", &filter, false).unwrap();
        assert_eq!(seen["text"], "Which path?");
        assert_eq!(seen["state"], "ingested");
        assert!(
            pending(&root, "discussion", 0, |_| true)
                .unwrap()
                .is_empty()
        );
        assert_eq!(
            manager_note(&root, "discussion", "manager", "input", &filter, false).unwrap(),
            seen
        );
        let mut answer = filter;
        answer["text"] = json!("Use src");
        manager_note(&root, "discussion", "manager", "input", &answer, true).unwrap();
        manager_note(&root, "discussion", "manager", "input", &answer, true).unwrap();
        answer["text"] = json!("Use a different path");
        assert!(manager_note(&root, "discussion", "manager", "input", &answer, true).is_err());
        let receipt = settle(&root, &worker, "question", None, true).unwrap();
        assert_eq!(receipt["State"], "answered");
        assert_eq!(receipt["Answer"], "Use src");
        assert_ne!(receipt["Settled"], ZERO);
        assert_eq!(
            settle(&root, &worker, "question", None, true).unwrap(),
            receipt
        );
        assert_eq!(
            submit(&root, &worker, "done", "complete", "Finished", true, &[]).unwrap()["Sequence"],
            2
        );
        std::fs::remove_dir_all(root).unwrap();
    }
}
