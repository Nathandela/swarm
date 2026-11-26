//! One coalescing, durable attention notice per passive handoff child.
use super::{State, runtime, session::Session};
use crate::{ipc::field, providers::codex_receipt};
use serde::{Deserialize, Serialize};
use serde_json::{Value, json};
use std::{
    collections::HashMap,
    io,
    sync::{Arc, mpsc::Receiver},
    time::Duration,
};

#[derive(Clone, Serialize, Deserialize)]
struct Event {
    seq: u64,
    group: String,
    interaction: String,
    operation: String,
}
#[derive(Clone, Serialize, Deserialize)]
struct Record {
    child: String,
    source: String,
    last_group: String,
    seen_working: bool,
    seq: u64,
    pending: Option<Event>,
}

impl Record {
    fn load(child: &Session) -> io::Result<Self> {
        let source = field(&child.lineage, "spawned_from");
        match runtime::read_json(&child.dir.join("supervision.json")) {
            Ok(v) => {
                let r: Self = serde_json::from_value(v)?;
                let valid = r.child == child.id
                    && r.source == source
                    && r.pending.as_ref().is_none_or(|e| {
                        e.seq == r.seq
                            && e.seq != 0
                            && matches!(
                                e.group.as_str(),
                                "completed" | "needs_input" | "ready_for_review"
                            )
                    });
                if !valid {
                    return Err(io::Error::other("handoff supervision identity mismatch"));
                }
                Ok(r)
            }
            Err(e) if e.kind() == io::ErrorKind::NotFound => Ok(Self {
                child: child.id.clone(),
                source: source.into(),
                last_group: "working".into(),
                seen_working: false,
                seq: 0,
                pending: None,
            }),
            Err(e) => Err(e),
        }
    }
    fn save(&self, child: &Session) -> io::Result<()> {
        runtime::write_json(
            &child.dir.join("supervision.json"),
            &serde_json::to_value(self)?,
        )
    }
    fn observe(&mut self, child: &Session, view: &Value) -> io::Result<()> {
        let group = field(view, "group");
        let active = view["status"]["Turn"] == "active";
        let changed_attention = group == "needs_input"
            && self
                .pending
                .as_ref()
                .is_some_and(|e| e.interaction != field(&view["status"], "Interaction"));
        if self.last_group == group && !changed_attention && (!active || self.seen_working) {
            return Ok(());
        }
        let mut next = self.clone();
        next.seen_working |= active;
        if self.last_group != group || changed_attention {
            next.last_group = group.into();
            next.pending = if group == "needs_input"
                || group == "completed"
                || group == "ready_for_review" && next.seen_working
            {
                next.seq = next
                    .seq
                    .checked_add(1)
                    .ok_or_else(|| io::Error::other("handoff sequence exhausted"))?;
                Some(Event {
                    seq: next.seq,
                    group: group.into(),
                    interaction: field(&view["status"], "Interaction").into(),
                    operation: String::new(),
                })
            } else {
                None
            };
        }
        next.save(child)?;
        *self = next;
        Ok(())
    }
}

fn text(id: &str, e: &Event) -> String {
    let local = id.split_once('/').map_or(id, |(_, v)| v);
    let state = match e.group.as_str() {
        "needs_input" if e.interaction == "permission" => {
            "needs_input (permission request - ask the human, never approve it yourself)"
        }
        "needs_input" => "needs_input (prompt)",
        "completed" => "completed - do a final review of its work and report",
        other => other,
    };
    format!(
        "[swarm supervision {local}#{}] child {id} is {state}. Inspect: swarm peek {id} · steer: swarm send {id} --text \"...\" · never approve permissions yourself.",
        e.seq
    )
}

fn delivery(
    state: &State,
    child: &Session,
    source: &Session,
    record: &mut Record,
) -> io::Result<bool> {
    let Some(e) = &record.pending else {
        return Ok(false);
    };
    if source.is_discord_manager()
        || !source.owner_interactive
        || !child.owner_interactive
        || child.lineage["source_instance"] != source.incarnation()
    {
        return Ok(false);
    }
    let view = source.view(&state.endpoint);
    if view["status"]["Process"] != "running"
        || view["status"]["Turn"] != "idle"
        || view["status"]["Interaction"] != "none"
        || view["status"]["Attention"] == true
    {
        return Ok(false);
    }
    let still_current = |e: &Event| {
        let latest = child.view(&state.endpoint);
        latest["group"] == e.group
            && (e.group != "needs_input" || latest["status"]["Interaction"] == e.interaction)
    };
    let msg = text(&format!("{}/{}", state.endpoint, child.id), e);
    if let Ok(instance) = source.semantic_target() {
        if e.operation.is_empty() {
            let mut saved = record.clone();
            saved.pending.as_mut().unwrap().operation = codex_receipt::operation()?;
            saved.save(child)?;
            *record = saved;
        }
        let operation = &record.pending.as_ref().unwrap().operation;
        let session = field(&view, "id");
        if !still_current(record.pending.as_ref().unwrap()) {
            return Ok(false);
        }
        let receipt = source.semantic_send(
            &json!({"body_version":1,"operation_id":operation,
            "composer_send":{"session":session,"session_instance":instance,"text":msg}}),
            session,
            state,
        )?;
        return Ok(receipt["state"] == "native_accepted");
    }
    if !still_current(e) {
        return Ok(false);
    }
    let result = source.status_exchange(
        &json!({"type":"supervision_input","child":child.id,"seq":e.seq,"data":msg}),
    )?;
    Ok(result["type"] == "ok")
}

pub(super) fn run(state: Arc<State>, wake: Receiver<()>) {
    let mut records = HashMap::<String, Record>::new();
    let mut pending = false;
    loop {
        if pending {
            if matches!(
                wake.recv_timeout(Duration::from_secs(2)),
                Err(std::sync::mpsc::RecvTimeoutError::Disconnected)
            ) {
                return;
            }
        } else if wake.recv().is_err() {
            return;
        }
        pending = false;
        let sessions = state.sessions.read().unwrap().clone();
        records.retain(|id, _| sessions.contains_key(id));
        for child in sessions
            .values()
            .filter(|s| s.lineage["supervision"] == "passive")
        {
            let _notification = child.notification_gate.lock().unwrap();
            if !state
                .sessions
                .read()
                .unwrap()
                .get(&child.id)
                .is_some_and(|s| Arc::ptr_eq(s, child))
            {
                continue;
            }
            let record = match records.entry(child.id.clone()) {
                std::collections::hash_map::Entry::Occupied(e) => e.into_mut(),
                std::collections::hash_map::Entry::Vacant(e) => match Record::load(child) {
                    Ok(r) => e.insert(r),
                    Err(_) => {
                        pending = true;
                        continue;
                    }
                },
            };
            if record.observe(child, &child.view(&state.endpoint)).is_err() {
                pending = true;
                continue;
            }
            if record.pending.is_some()
                && let Some(source) = sessions.get(&record.source)
                && delivery(&state, child, source, record).unwrap_or(false)
            {
                let mut saved = record.clone();
                saved.pending = None;
                if saved.save(child).is_ok() {
                    *record = saved;
                }
            }
            let has_pending = record.pending.is_some();
            pending |= has_pending;
            if child
                .supervision_pending
                .swap(has_pending, std::sync::atomic::Ordering::AcqRel)
                != has_pending
            {
                state.publish(child);
            }
        }
    }
}
