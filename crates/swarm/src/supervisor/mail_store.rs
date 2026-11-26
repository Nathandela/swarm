//! Go-compatible text worker mailbox. Note, sequence and pending index commit together.
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

pub(super) fn key(binding: &Value, id: &str) -> io::Result<Vec<u8>> {
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

pub(super) fn submit(
    root: &Path,
    binding: &Value,
    id: &str,
    kind: &str,
    text: &str,
    fresh: bool,
) -> io::Result<Value> {
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
            || tx
                .bucket(b"note-files")
                .is_some_and(|files| files.bucket(&key).is_some())
        {
            return Err(io_error("worker mailbox: conflicting note"));
        }
        return Ok(prior);
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
    tx.bucket_mut(b"pending")
        .ok_or_else(absent)?
        .put(order(&note)?, &key)
        .map_err(io_error)?;
    tx.commit().map_err(io_error)?;
    Ok(note)
}

pub(super) fn lookup(root: &Path, binding: &Value, id: &str) -> io::Result<Value> {
    let key = key(binding, id)?;
    let path = open_path(root, false)?;
    let db = Bolt::open_ro(path).map_err(io_error)?;
    let tx = db.begin().map_err(io_error)?;
    let data = tx
        .bucket(b"notes")
        .and_then(|notes| notes.get(&key).map(<[u8]>::to_vec))
        .ok_or_else(absent)?;
    if tx
        .bucket(b"note-files")
        .is_some_and(|files| files.bucket(&key).is_some())
    {
        return Err(io_error("worker note requires a file-aware mailbox"));
    }
    let note: Value = serde_json::from_slice(&data)?;
    if note["ID"] != id
        || binding
            .as_object()
            .is_none_or(|fields| fields.iter().any(|(field, value)| note[field] != *value))
    {
        return Err(io_error("worker mailbox binding changed"));
    }
    Ok(note)
}

pub(super) fn pending(root: &Path, discussion: &str) -> io::Result<Vec<Value>> {
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
    let mut row = cursor.seek(&prefix);
    let mut result = Vec::new();
    while let Some((order_key, key)) = row {
        let key = key.ok_or_else(absent)?;
        if !order_key.starts_with(prefix.as_bytes()) || result.len() >= 64 {
            break;
        }
        // File-bearing Go notes stay queued for the compatible file-aware reader.
        if tx
            .bucket(b"note-files")
            .is_none_or(|files| files.bucket(key).is_none())
        {
            let raw = notes.get(key).ok_or_else(absent)?;
            let note: Value = serde_json::from_slice(raw)?;
            if note["Discussion"] != discussion
                || note["State"] != "queued"
                || order(&note)? != order_key
            {
                return Err(io_error("damaged worker pending index"));
            }
            result.push(note);
        }
        row = cursor.next();
    }
    Ok(result)
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
        let rows:Vec<_>=pending(root,discussion)?.into_iter().filter(same).take(12)
            .map(|note|json!({"id":note["ID"],"session":note["Session"],"session_instance":note["Instance"],
                "kind":note["Kind"],"state":note["State"]})).collect();
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
                if filter(&note)
                    && note["State"] != "superseded"
                    && tx
                        .bucket(b"note-files")
                        .is_none_or(|files| files.bucket(key).is_none())
                {
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
    Ok(
        json!({"id":updated["ID"],"kind":updated["Kind"],"state":updated["State"],
        "session":updated["Session"],"session_instance":updated["Instance"],"text":updated["Text"]}),
    )
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
    if tx
        .bucket(b"note-files")
        .is_some_and(|files| files.bucket(&key).is_some())
    {
        return Err(io_error("worker note requires a file-aware mailbox"));
    }
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
            return Ok(note);
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
            terminal = note["Kind"] != "needs-input";
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
    tx.commit().map_err(io_error)?;
    Ok(note)
}

#[cfg(test)]
mod tests {
    use super::*;
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
                false
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
        )
        .unwrap();
        assert_eq!(
            submit(
                &root,
                &worker,
                "question",
                "needs-input",
                "Which path?",
                false
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
                true
            )
            .is_err()
        );
        assert!(submit(&root, &worker, "expired", "complete", "Finished", false).is_err());
        assert_eq!(pending(&root, "discussion").unwrap(), [created]);
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
        assert!(pending(&root, "discussion").unwrap().is_empty());
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
            submit(&root, &worker, "done", "complete", "Finished", true).unwrap()["Sequence"],
            2
        );
        std::fs::remove_dir_all(root).unwrap();
    }
}
