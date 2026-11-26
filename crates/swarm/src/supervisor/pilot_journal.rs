//! Bounded local owner journal. A cursor is published only after its state is durable.
use super::{Session, runtime};
use crate::ipc::field;
use serde_json::{Map, Value, json};
use std::collections::BTreeMap;
use std::io;
use std::os::fd::AsRawFd;
use std::os::unix::net::UnixStream;
use std::path::{Path, PathBuf};
use std::sync::{Condvar, Mutex};
use std::time::Duration;
use time::{OffsetDateTime, format_description::well_known::Rfc3339};

const LIMIT: usize = 512 * 1024;
const RECORDS: usize = 512;

pub(super) struct Journal {
    file: PathBuf,
    owned: Mutex<Inner>,
    change: Condvar,
}

struct Inner {
    head: u64,
    records: Vec<Value>,
    seen: Map<String, Value>,
    roster: BTreeMap<String, Value>,
    unavailable: bool,
}

impl Journal {
    pub(super) fn load(root: &Path, rows: impl IntoIterator<Item = Value>) -> Self {
        let file = root.join("pilot-journal.json");
        let mut owned = Inner {
            head: 0,
            records: Vec::new(),
            seen: Map::new(),
            roster: BTreeMap::new(),
            unavailable: false,
        };
        let mut legacy = false;
        match runtime::read_json(&file) {
            Ok(raw) => {
                let parsed = (|| {
                    if raw["version"] != 1 && raw["version"] != 2 {
                        return None;
                    }
                    let head = raw["head"].as_u64()?;
                    let records = raw["records"].as_array()?.clone();
                    let seen = raw["seen"].as_object()?.clone();
                    let start = head.checked_sub(records.len() as u64)?;
                    if records.len() > RECORDS
                        || records.iter().enumerate().any(|(i, record)| {
                            record["cursor"] != start + i as u64 + 1
                                || field(record, "session_id").is_empty()
                        })
                        || seen.values().any(|value| value.as_u64().is_none())
                    {
                        return None;
                    }
                    let previous = BTreeMap::from_iter(
                        raw["roster"]
                            .as_object()
                            .into_iter()
                            .flatten()
                            .map(|(id, row)| (id.clone(), row.clone())),
                    );
                    if raw["version"] == 2
                        && (!raw["roster"].is_object()
                            || previous.iter().any(|(id, row)| {
                                row["session_id"] != *id
                                    || row["type"] != "roster"
                                    || !row["group"].is_string()
                                    || !row["process"].is_string()
                            }))
                    {
                        return None;
                    }
                    Some((head, records, seen, previous))
                })();
                if let Some((head, records, seen, previous)) = parsed {
                    legacy = raw["version"] == 1;
                    owned.head = head;
                    owned.records = records;
                    owned.seen = seen;
                    owned.roster = previous;
                } else {
                    owned.unavailable = true;
                }
            }
            Err(err) if err.kind() == io::ErrorKind::NotFound => {}
            Err(_) => owned.unavailable = true,
        }
        let current: BTreeMap<_, _> = rows
            .into_iter()
            .filter_map(|row| projection(&row))
            .collect();
        let mut events = Vec::new();
        let mut gap = legacy;
        for id in owned.roster.keys().filter(|id| !current.contains_key(*id)) {
            if super::removal::completed(root, id).unwrap_or(false) {
                events.push(json!({"session_id":id,"type":"deleted"}));
                owned.seen.shift_remove(id);
            } else {
                // A skipped or unreadable session is not evidence of deletion.
                gap = true;
            }
        }
        for (id, row) in &current {
            if let Some(event) = transition(id, owned.roster.get(id), row) {
                events.push(event)
            }
        }
        if gap {
            // Keep available interactions for view while forcing old cursors to
            // reconcile the new projection before advancing.
            events.push(json!({"session_id":"*","type":"structured_gap","recovery_gap":true}));
        }
        owned.roster = current;
        let journal = Self {
            file,
            owned: Mutex::new(owned),
            change: Condvar::new(),
        };
        {
            let mut inner = journal.owned.lock().unwrap();
            journal.commit(&mut inner, events);
        }
        journal
    }

    pub(super) fn publish(&self, row: &Value, captures: Vec<Value>) {
        let Some((id, projected)) = projection(row) else {
            return;
        };
        let mut inner = self.owned.lock().unwrap();
        let mut events = transition(&id, inner.roster.get(&id), &projected)
            .into_iter()
            .collect::<Vec<_>>();
        let mut seen = inner.seen.get(&id).and_then(Value::as_u64).unwrap_or(0);
        let mut captures = captures
            .into_iter()
            .filter_map(|capture| {
                Some((
                    capture["sequence"].as_u64()?,
                    capture["item"].as_object()?.clone(),
                ))
            })
            .collect::<Vec<_>>();
        captures.sort_by_key(|(sequence, _)| *sequence);
        for (sequence, item) in captures {
            if sequence <= seen {
                continue;
            }
            if sequence > seen + 1 {
                events.push(json!({"session_id":id,"type":"structured_gap"}));
            }
            events.push(json!({"session_id":id,"type":"interaction","item":item}));
            seen = sequence;
        }
        if seen > 0 {
            inner.seen.insert(id.clone(), json!(seen));
        }
        if !events.is_empty() {
            inner.roster.insert(id, projected);
            self.commit(&mut inner, events);
        }
        self.change.notify_all();
    }

    pub(super) fn deleted(&self, id: &str) {
        let mut inner = self.owned.lock().unwrap();
        if inner.roster.remove(id).is_some() {
            inner.seen.shift_remove(id);
            self.commit(&mut inner, vec![json!({"session_id":id,"type":"deleted"})]);
            self.change.notify_all();
        }
    }

    fn commit(&self, inner: &mut Inner, events: Vec<Value>) {
        if inner.unavailable {
            return;
        }
        for mut event in events {
            let Some(next) = inner.head.checked_add(1) else {
                inner.unavailable = true;
                return;
            };
            inner.head = next;
            event["cursor"] = json!(next);
            event["ts"] = json!(
                OffsetDateTime::now_utc()
                    .format(&Rfc3339)
                    .unwrap_or_default()
            );
            inner.records.push(event);
        }
        let mut size: usize = inner
            .records
            .iter()
            .map(|record| record.to_string().len() + 1)
            .sum();
        let mut remove = 0;
        while remove < inner.records.len()
            && (inner.records.len() - remove > RECORDS || size > LIMIT)
        {
            size = size.saturating_sub(inner.records[remove].to_string().len() + 1);
            remove += 1;
        }
        inner.records.drain(..remove);
        let current = json!({"version":2,"head":inner.head,"records":inner.records,"seen":inner.seen,"roster":inner.roster});
        let persisted = serde_json::to_vec(&current)
            .map_err(io::Error::other)
            .and_then(|bytes| {
                if bytes.len() >= 1 << 20 {
                    return Err(io::Error::other(
                        "pilot journal state exceeds its read limit",
                    ));
                }
                use std::io::Write;
                runtime::write_atomic(&self.file, |file| file.write_all(&bytes))
            });
        if persisted.is_err() {
            // A cursor after an uncertain persistence result cannot be advertised.
            inner.unavailable = true;
        }
    }

    pub(super) fn history(
        &self,
        id: &str,
        before: &str,
        limit: u64,
    ) -> io::Result<(Vec<Value>, bool)> {
        let inner = self.owned.lock().unwrap();
        ensure(&inner)?;
        let mut page: Vec<_> = inner
            .records
            .iter()
            .filter(|record| {
                record["session_id"] == id
                    && matches!(field(record, "type"), "interaction" | "structured_gap")
            })
            .collect();
        if !before.is_empty() {
            let Some(boundary) = page
                .iter()
                .position(|record| record["item"]["item_id"] == before)
            else {
                return Ok((Vec::new(), true));
            };
            page.truncate(boundary);
        }
        let start = page.len().saturating_sub(limit.min(20) as usize);
        Ok((
            page[start..].iter().map(|row| (*row).clone()).collect(),
            start == 0,
        ))
    }

    pub(super) fn stream(
        &self,
        mut socket: UnixStream,
        endpoint: &str,
        from: u64,
        max_bytes: u64,
    ) -> io::Result<()> {
        let (head, full, events, roster) = {
            let inner = self.owned.lock().unwrap();
            ensure(&inner)?;
            let full = from < inner.head.saturating_sub(inner.records.len() as u64)
                || inner.records.iter().any(|event| {
                    event["recovery_gap"] == true
                        && event["cursor"].as_u64().is_some_and(|cursor| cursor > from)
                });
            let events = if full {
                Vec::new()
            } else {
                inner
                    .records
                    .iter()
                    .filter(|r| r["cursor"].as_u64().is_some_and(|n| n > from))
                    .cloned()
                    .collect()
            };
            let roster = inner
                .roster
                .values()
                .map(|row| {
                    let mut row = row.clone();
                    row.as_object_mut()
                        .expect("roster projection")
                        .shift_remove("process");
                    row["cursor"] = json!(inner.head);
                    row
                })
                .collect::<Vec<_>>();
            (inner.head, full, events, roster)
        };
        snapshot(&mut socket, endpoint, head, full, roster, events, max_bytes)?;
        let mut cursor = head;
        loop {
            let mut inner = self.owned.lock().unwrap();
            while cursor == inner.head && !inner.unavailable {
                let wait = self
                    .change
                    .wait_timeout(inner, Duration::from_millis(200))
                    .unwrap();
                inner = wait.0;
                if wait.1.timed_out() && disconnected(&socket)? {
                    return Ok(());
                }
            }
            ensure(&inner)?;
            if cursor < inner.head.saturating_sub(inner.records.len() as u64) {
                return Err(io::Error::other("journal subscriber fell behind retention"));
            }
            let page = inner
                .records
                .iter()
                .filter(|r| r["cursor"].as_u64().is_some_and(|n| n > cursor))
                .cloned()
                .collect::<Vec<_>>();
            cursor = inner.head;
            drop(inner);
            runtime::write_control(
                &mut socket,
                &json!({"op":"journal_event","endpoint_id":endpoint,"journal":page}),
            )?;
        }
    }
}

fn snapshot(
    socket: &mut UnixStream,
    endpoint: &str,
    head: u64,
    full: bool,
    roster: Vec<Value>,
    events: Vec<Value>,
    max_bytes: u64,
) -> io::Result<()> {
    let budget = (if max_bytes == 0 {
        1_048_575
    } else {
        max_bytes.min(1_048_575)
    })
    .checked_sub(384)
    .ok_or_else(|| io::Error::other("journal page budget is too small"))? as usize;
    if roster
        .iter()
        .chain(events.iter())
        .any(|record| record.to_string().len() + 1 > budget)
    {
        return Err(io::Error::other("journal record exceeds page budget"));
    }
    let mut rows = Vec::new();
    let mut records = Vec::new();
    let mut size = 0;
    for (is_row, record) in roster
        .into_iter()
        .map(|r| (true, r))
        .chain(events.into_iter().map(|r| (false, r)))
    {
        let bytes = record.to_string().len() + 1;
        if size + bytes > budget {
            runtime::write_control(
                socket,
                &json!({"op":"journal_subscribe_from","endpoint_id":endpoint,"cursor":head,
                "full_resync":full,"roster":rows,"journal":records,"journal_more":true}),
            )?;
            rows.clear();
            records.clear();
            size = 0;
        }
        if is_row {
            rows.push(record)
        } else {
            records.push(record)
        }
        size += bytes;
    }
    runtime::write_control(
        socket,
        &json!({"op":"journal_subscribe_from","endpoint_id":endpoint,"cursor":head,
        "full_resync":full,"roster":rows,"journal":records}),
    )
}

fn ensure(inner: &Inner) -> io::Result<()> {
    if inner.unavailable {
        Err(io::Error::other("pilot journal is unavailable"))
    } else {
        Ok(())
    }
}

fn transition(id: &str, previous: Option<&Value>, current: &Value) -> Option<Value> {
    if previous == Some(current) {
        return None;
    }
    let kind = match field(current, "process") {
        "exited" if previous.is_none_or(|row| row["process"] != "exited") => "exited",
        "lost" if previous.is_none_or(|row| row["process"] != "lost") => "lost",
        _ if previous.is_none() => "launched",
        _ if previous.is_some_and(|row| {
            row["group"] != current["group"] || row["process"] != current["process"]
        }) =>
        {
            "group_transition"
        }
        _ => "session_state",
    };
    let mut event = current.clone();
    event
        .as_object_mut()
        .expect("roster projection")
        .shift_remove("process");
    event["session_id"] = json!(id);
    event["type"] = json!(kind);
    Some(event)
}

fn projection(row: &Value) -> Option<(String, Value)> {
    let (_, local) = field(row, "id").split_once('/')?;
    Some((
        local.to_owned(),
        json!({"session_id":local,"type":"roster","group":row["group"],"agent":row["agent"],"name":row["name"],"process":row["status"]["Process"],"tag":row["tag"]}),
    ))
}

fn disconnected(socket: &UnixStream) -> io::Result<bool> {
    let mut fd = libc::pollfd {
        fd: socket.as_raw_fd(),
        events: libc::POLLIN,
        revents: 0,
    };
    // SAFETY: socket remains live and poll accesses this initialized entry only.
    let ready = unsafe { libc::poll(&mut fd, 1, 0) };
    if ready < 0 {
        return Err(io::Error::last_os_error());
    }
    // This is a dedicated stream. Any extra client frames end it.
    Ok(ready > 0)
}

pub(super) fn refresh(journal: &Journal, session: &Session, endpoint: &str) {
    journal.publish(&session.view(endpoint), session.pilot_replies());
}

#[cfg(test)]
mod tests {
    use super::*;

    fn row(local: &str, name: &str, group: &str, process: &str) -> Value {
        json!({"id":format!("endpoint/{local}"),"name":name,"agent":"claude","group":group,"status":{"Process":process}})
    }

    fn initial_snapshot(journal: &Journal, after: u64) -> Value {
        let (mut client, server) = UnixStream::pair().unwrap();
        client
            .set_read_timeout(Some(Duration::from_secs(2)))
            .unwrap();
        std::thread::scope(|scope| {
            scope.spawn(move || journal.stream(server, "endpoint", after, 0).unwrap());
            let first = runtime::read_control(&mut client).unwrap();
            drop(client);
            first
        })
    }

    #[test]
    fn interrupted_commit_recovers_exit_updates_deletion_and_creation_from_previous_head() {
        let path =
            Path::new("/tmp").join(format!("pilot-recovery-{}", runtime::random_id().unwrap()));
        runtime::private_dir(&path.join("deleted")).unwrap();
        let removed = "0123456789abcdef01234567";
        let before = [
            row("exit", "exit", "working", "running"),
            row("rename", "old", "working", "running"),
            row(removed, "removed", "completed", "exited"),
        ];
        let current = [
            row("exit", "exit", "completed", "exited"),
            row("rename", "new", "working", "running"),
            row("added", "added", "working", "running"),
        ];
        let mut book = Journal::load(&path, before);
        let saved = runtime::read_json(&path.join("pilot-journal.json")).unwrap();
        let checkpoint = saved["head"].as_u64().unwrap();
        book.file = path.join("blocked").join("pilot-journal.json");
        book.publish(&current[0], Vec::new());
        assert!(book.history("exit", "", 20).is_err());
        assert_eq!(
            runtime::read_json(&path.join("pilot-journal.json")).unwrap(),
            saved
        );
        drop(book);
        runtime::write_json(
            &path.join("deleted").join(removed),
            &json!({"schema":1,"id":removed,"device":1,"inode":2}),
        )
        .unwrap();
        let recovered = Journal::load(&path, current.clone());
        let snapshot = initial_snapshot(&recovered, checkpoint);
        assert_eq!(snapshot["full_resync"], false);
        let after: Vec<_> = snapshot["journal"]
            .as_array()
            .unwrap()
            .iter()
            .map(|event| (field(event, "session_id"), field(event, "type")))
            .collect();
        assert_eq!(
            after,
            [
                (removed, "deleted"),
                ("added", "launched"),
                ("exit", "exited"),
                ("rename", "session_state")
            ]
        );
        assert!(
            snapshot["roster"]
                .as_array()
                .unwrap()
                .iter()
                .any(|entry| entry["session_id"] == "rename" && entry["name"] == "new")
        );
        let new_head = snapshot["cursor"].as_u64().unwrap();
        let restarted = Journal::load(&path, current);
        assert_eq!(restarted.owned.lock().unwrap().head, new_head);
        std::fs::remove_dir_all(path).unwrap();
    }

    #[test]
    fn legacy_or_unproven_disappearance_forces_a_recovery_gap() {
        let path = Path::new("/tmp").join(format!("pilot-gap-{}", runtime::random_id().unwrap()));
        runtime::private_dir(&path).unwrap();
        runtime::write_json(
            &path.join("pilot-journal.json"),
            &json!({"version":1,"head":4,"records":[],"seen":{}}),
        )
        .unwrap();
        let book = Journal::load(&path, [row("skipped", "worker", "working", "running")]);
        assert!(
            book.owned
                .lock()
                .unwrap()
                .records
                .iter()
                .any(|event| event["recovery_gap"] == true)
        );
        let previous = book.owned.lock().unwrap().head;
        let restarted = Journal::load(&path, []);
        let owned = restarted.owned.lock().unwrap();
        assert_eq!(owned.head, previous + 1);
        assert_eq!(owned.records.last().unwrap()["recovery_gap"], true);
        assert!(!owned.records.iter().any(|event| event["type"] == "deleted"));
        drop(owned);
        let snapshot = initial_snapshot(&restarted, previous);
        assert_eq!(snapshot["full_resync"], true);
        assert_eq!(snapshot["cursor"], previous + 1);
        assert_eq!(snapshot["roster"], json!([]));
        std::fs::remove_dir_all(path).unwrap();
    }

    #[test]
    fn retained_reply_cursor_survives_reload_without_duplication() {
        let path =
            Path::new("/tmp").join(format!("pilot-journal-{}", runtime::random_id().unwrap()));
        runtime::private_dir(&path).unwrap();
        let row = json!({"id":"endpoint/session","group":"working","status":{"Process":"running"}});
        let capture = json!({"sequence":1,"item":{"kind":"agent_message","status":"completed","text":"literal evidence"}});
        let book = Journal::load(&path, [row.clone()]);
        book.publish(&row, vec![capture.clone()]);
        let entry = book.history("session", "", 20).unwrap().0;
        assert_eq!(entry.len(), 1);
        drop(book);
        let reloaded = Journal::load(&path, [row.clone()]);
        reloaded.publish(&row, vec![capture]);
        assert_eq!(reloaded.history("session", "", 20).unwrap().0, entry);
        std::fs::remove_dir_all(path).unwrap();
    }
}
