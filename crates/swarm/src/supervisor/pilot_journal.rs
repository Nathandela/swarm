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
        match runtime::read_json(&file) {
            Ok(raw) => {
                let parsed = (|| {
                    if raw["version"] != 1 {
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
                    Some((head, records, seen))
                })();
                if let Some((head, records, seen)) = parsed {
                    owned.head = head;
                    owned.records = records;
                    owned.seen = seen;
                } else {
                    owned.unavailable = true;
                }
            }
            Err(err) if err.kind() == io::ErrorKind::NotFound => {}
            Err(_) => owned.unavailable = true,
        }
        for row in rows {
            if let Some((id, projection)) = projection(&row) {
                owned.roster.insert(id, projection);
            }
        }
        Self {
            file,
            owned: Mutex::new(owned),
            change: Condvar::new(),
        }
    }

    pub(super) fn publish(&self, row: &Value, captures: Vec<Value>) {
        let Some((id, mut projected)) = projection(row) else {
            return;
        };
        let mut inner = self.owned.lock().unwrap();
        let kind = match field(&row["status"], "Process") {
            "exited" => "exited",
            "lost" => "lost",
            _ => "group_transition",
        };
        let mut events = Vec::new();
        if inner.roster.get(&id).is_none_or(|previous| {
            previous["group"] != projected["group"] || previous["process"] != projected["process"]
        }) {
            events.push(json!({"session_id":id,"type":kind,"group":projected["group"]}));
        }
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
        self.add(&mut inner, events);
        projected["cursor"] = json!(inner.head);
        inner.roster.insert(id, projected);
        self.change.notify_all();
    }

    pub(super) fn deleted(&self, id: &str) {
        let mut inner = self.owned.lock().unwrap();
        if inner.roster.remove(id).is_some() {
            inner.seen.shift_remove(id);
            self.add(&mut inner, vec![json!({"session_id":id,"type":"deleted"})]);
            self.change.notify_all();
        }
    }

    fn add(&self, inner: &mut Inner, mut events: Vec<Value>) {
        if inner.unavailable || events.is_empty() {
            return;
        }
        for mut event in events.drain(..) {
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
        if runtime::write_json(
            &self.file,
            &json!({"version":1,"head":inner.head,"records":inner.records,"seen":inner.seen}),
        )
        .is_err()
        {
            // A cursor after an uncertain persistence result cannot be advertised.
            inner.unavailable = true;
        }
    }

    pub(super) fn history(&self, id: &str, limit: u64) -> io::Result<Vec<Value>> {
        let inner = self.owned.lock().unwrap();
        ensure(&inner)?;
        let mut page: Vec<_> = inner
            .records
            .iter()
            .rev()
            .filter(|record| {
                record["session_id"] == id
                    && matches!(field(record, "type"), "interaction" | "structured_gap")
            })
            .take(limit.min(20) as usize)
            .cloned()
            .collect();
        page.reverse();
        Ok(page)
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
            let full = from < inner.head.saturating_sub(inner.records.len() as u64);
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

fn projection(row: &Value) -> Option<(String, Value)> {
    let (_, local) = field(row, "id").split_once('/')?;
    Some((
        local.to_owned(),
        json!({"session_id":local,"type":"roster","group":row["group"],"agent":row["agent"],"name":row["name"],"process":row["status"]["Process"]}),
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

    #[test]
    fn retained_reply_cursor_survives_reload_without_duplication() {
        let path =
            Path::new("/tmp").join(format!("pilot-journal-{}", runtime::random_id().unwrap()));
        runtime::private_dir(&path).unwrap();
        let row = json!({"id":"endpoint/session","group":"working","status":{"Process":"running"}});
        let capture = json!({"sequence":1,"item":{"kind":"agent_message","status":"completed","text":"literal evidence"}});
        let book = Journal::load(&path, [row.clone()]);
        book.publish(&row, vec![capture.clone()]);
        let entry = book.history("session", 20).unwrap();
        assert_eq!(entry.len(), 1);
        drop(book);
        let reloaded = Journal::load(&path, [row.clone()]);
        reloaded.publish(&row, vec![capture]);
        assert_eq!(reloaded.history("session", 20).unwrap(), entry);
        std::fs::remove_dir_all(path).unwrap();
    }
}
