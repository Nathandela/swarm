use crate::discord::linked_board::{self, Binding};
use crate::ipc::{Client, field};
use serde_json::{Value, json};
use std::{
    collections::{HashMap, HashSet},
    io,
    path::Path,
    sync::mpsc::{self, Receiver},
    time::{Duration, Instant},
};

pub(super) const SECTION: &str = "\0discord-dispatcher";

pub(super) fn manager(row: &Value) -> bool {
    row["role"] == "manager"
        && field(row, "manager_conversation_id").starts_with("discord-teammate-")
}

pub(super) fn visible(row: &Value) -> bool {
    !field(row, "id").is_empty()
        && row["roster_hidden"] != true
        && field(row, "superseded_by").is_empty()
}

pub(super) fn project(rows: Vec<Value>, endpoint: &str, linked: bool) -> Vec<Value> {
    let qualified = |row: &Value| {
        let id = field(row, "id");
        field(row, "endpoint_id") == endpoint
            && id.split_once('/').is_some_and(|(owner, local)| {
                owner == endpoint && !local.is_empty() && !local.contains('/')
            })
    };
    let mut counts = HashMap::new();
    if linked {
        for row in rows.iter().filter(|row| qualified(row)) {
            *counts.entry(field(row, "id").to_owned()).or_insert(0) += 1;
        }
    }
    let mut parents = HashSet::new();
    for row in rows.iter().filter(|row| {
        qualified(row) && manager(row) && (!linked || counts.get(field(row, "id")) == Some(&1))
    }) {
        parents.insert(field(row, "id").to_owned());
        if let Some(prior) = row["supersedes"].as_array() {
            parents.extend(
                prior
                    .iter()
                    .filter_map(Value::as_str)
                    .filter(|id| {
                        id.split_once('/').is_some_and(|(owner, local)| {
                            owner == endpoint && !local.is_empty() && !local.contains('/')
                        })
                    })
                    .map(str::to_owned),
            );
        }
    }
    let mut seen = HashSet::new();
    rows.into_iter()
        .filter_map(|mut row| {
            if !visible(&row)
                || (linked && (!qualified(&row) || counts.get(field(&row, "id")) != Some(&1)))
                || !seen.insert(field(&row, "id").to_owned())
            {
                return None;
            }
            let parent = field(&row, "spawned_from");
            let worker = !parent.is_empty() && parents.contains(&format!("{endpoint}/{parent}"));
            if linked && !(manager(&row) || worker) {
                return None;
            }
            if worker && field(&row, "tag").is_empty() {
                row["tag"] = json!("DSP");
            }
            Some(row)
        })
        .collect()
}

pub(super) struct Snapshot {
    pub(super) binding: Binding,
    pub(super) rows: Vec<Value>,
}

type Scan = Option<Snapshot>;

#[derive(Default)]
pub(super) struct Poll {
    pending: Option<Receiver<Scan>>,
    next: Option<Instant>,
}

impl Poll {
    pub(super) fn update(&mut self, local: &Path, endpoint: &str) -> Option<Scan> {
        if let Some(result) = &self.pending {
            let result = match result.try_recv() {
                Ok(rows) => rows,
                Err(mpsc::TryRecvError::Empty) => return None,
                Err(mpsc::TryRecvError::Disconnected) => None,
            };
            self.pending = None;
            self.next = Some(Instant::now() + Duration::from_secs(3));
            return Some(result);
        }
        if self.next.is_none_or(|next| Instant::now() >= next) {
            let (send, receive) = mpsc::sync_channel(1);
            let (local, endpoint) = (local.to_owned(), endpoint.to_owned());
            self.pending = Some(receive);
            std::thread::spawn(move || {
                let scan = || -> io::Result<Scan> {
                    let deadline = Instant::now() + Duration::from_secs(2);
                    let Some((binding, mut client)) =
                        linked_board::resolve(&local, &endpoint, super::attach::CAPABILITIES)?
                    else {
                        return Ok(None);
                    };
                    let rows = roster(&mut client, deadline)?;
                    let rows = project(rows, &binding.endpoint, true);
                    Ok(Some(Snapshot { binding, rows }))
                };
                let _ = send.send(scan().ok().flatten());
            });
        }
        None
    }
}

fn roster(client: &mut Client, deadline: Instant) -> io::Result<Vec<Value>> {
    let time = deadline.saturating_duration_since(Instant::now());
    if time.is_zero() {
        return Err(io::ErrorKind::TimedOut.into());
    }
    let mut reply = client.exchange(json!({"op":"list"}), "list", time)?;
    Ok(reply["sessions"]
        .as_array_mut()
        .map(std::mem::take)
        .unwrap_or_default())
}

pub(super) fn same_target(old: &Value, new: &Value) -> bool {
    let old_instance = old["capabilities"]["session_instance"]
        .as_str()
        .filter(|value| !value.is_empty());
    let new_instance = new["capabilities"]["session_instance"]
        .as_str()
        .filter(|value| !value.is_empty());
    // Legacy Go can omit live capabilities during observation. Its immutable
    // namespaced ID and creation time still bind an unchanged worker attempt.
    let instance_matches = old_instance.zip(new_instance).map_or_else(
        || !field(old, "created_at").is_empty(),
        |(old, new)| old == new,
    );
    instance_matches
        && [
            "id",
            "role",
            "agent",
            "cwd",
            "created_at",
            "manager_conversation_id",
            "spawned_from",
            "worker_discussion",
            "worker_attempt",
            "worker_instance",
        ]
        .into_iter()
        .all(|key| old[key] == new[key])
        && super::running(old) == super::running(new)
}

pub(super) fn revalidate(
    local: &Path,
    endpoint: &str,
    binding: &Binding,
    row: &Value,
) -> io::Result<Client> {
    let deadline = Instant::now() + Duration::from_secs(2);
    let Some((current, mut client)) =
        linked_board::resolve(local, endpoint, super::attach::CAPABILITIES)?
    else {
        return Err(io::Error::other(
            "Linked dispatcher is unavailable; reopen the action",
        ));
    };
    if &current != binding {
        return Err(io::Error::other(
            "Linked dispatcher binding changed; reopen the action",
        ));
    }
    let rows = project(roster(&mut client, deadline)?, &binding.endpoint, true);
    let Some(fresh) = rows.iter().find(|candidate| candidate["id"] == row["id"]) else {
        return Err(io::Error::other(
            "Linked row is absent from the qualified roster; reopen the action",
        ));
    };
    if same_target(row, fresh) {
        Ok(client)
    } else {
        Err(io::Error::other(
            "Linked row identity or process state changed; reopen the action",
        ))
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn optional_legacy_observation_preserves_an_exact_attempt_fence() {
        let fingerprint = json!({"id":"peer/worker","created_at":"2026-01-01T00:00:00Z",
                                 "status":{"Process":"running"},"worker_attempt":"one"});
        let mut stamped = fingerprint.clone();
        stamped["capabilities"] = json!({"session_instance":"original"});
        assert!(same_target(&stamped, &fingerprint));
        let mut changed = stamped.clone();
        changed["capabilities"]["session_instance"] = json!("replacement");
        assert!(!same_target(&stamped, &changed));
        changed = fingerprint.clone();
        changed["worker_attempt"] = json!("different");
        assert!(!same_target(&stamped, &changed));
    }

    #[test]
    fn only_visible_endpoint_bound_managers_and_their_workers_are_linked() {
        let rows = vec![
            json!({"id":"peer/m", "endpoint_id":"peer", "role":"manager",
                   "manager_conversation_id":"discord-teammate-one", "supersedes":["peer/old","foreign/f"]}),
            json!({"id":"peer/old","endpoint_id":"peer","role":"manager",
                   "manager_conversation_id":"discord-teammate-one","superseded_by":"peer/m"}),
            json!({"id":"peer/worker","endpoint_id":"peer","spawned_from":"old"}),
            json!({"id":"peer/tagged","endpoint_id":"peer","spawned_from":"m","tag":"saved"}),
            json!({"id":"peer/unrelated","endpoint_id":"peer"}),
            json!({"id":"peer/foreign-worker","endpoint_id":"peer","spawned_from":"f"}),
            json!({"id":"foreign/claimed","endpoint_id":"peer","spawned_from":"m"}),
            json!({"id":"peer/hidden","endpoint_id":"peer","spawned_from":"m","roster_hidden":true}),
        ];
        let linked = project(rows.clone(), "peer", true);
        assert_eq!(
            linked.iter().map(|r| field(r, "id")).collect::<Vec<_>>(),
            ["peer/m", "peer/worker", "peer/tagged"]
        );
        assert_eq!(linked[1]["tag"], "DSP");
        assert_eq!(linked[2]["tag"], "saved");
        assert!(
            project(rows, "peer", false)
                .iter()
                .any(|r| r["id"] == "peer/unrelated")
        );
    }
}
