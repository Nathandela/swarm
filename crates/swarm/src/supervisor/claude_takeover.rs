//! Admission and native stop share the launch gate, so other owner or schedule
//! launches cannot consume capacity or claim a conversation between them.
use super::{SESSION_LIMIT, Session, State, scheduling};
use crate::{
    ipc::field,
    providers::{self, claude},
};
use serde_json::{Value, json};
use std::{collections::BTreeSet, io};

pub(super) fn acquire(state: &State, request: &Value) -> io::Result<Vec<Value>> {
    let launches = request["launches"]
        .as_array()
        .filter(|rows| (1..=SESSION_LIMIT).contains(&rows.len()))
        .ok_or_else(|| io::Error::other("invalid Claude takeover batch size"))?;
    let mut ids = BTreeSet::new();
    for row in launches {
        if row["agent"] != "claude"
            || row["env"] != launches[0]["env"]
            || !ids.insert(field(&row["options"], "resume_conversation_id"))
        {
            return Err(io::Error::other(
                "Claude takeover requires distinct sources in one origin",
            ));
        }
    }
    let gate = state.launch_gate.lock().unwrap();
    if state
        .sessions
        .read()
        .unwrap()
        .values()
        .filter(|row| row.may_occupy_slot())
        .count()
        + launches.len()
        > SESSION_LIMIT
    {
        return Err(io::Error::other(
            "Claude takeover batch exceeds available session capacity",
        ));
    }
    let mut preview = Vec::with_capacity(launches.len());
    for launch in launches {
        let prepared = Session::prepare_claude_external(launch, false)?;
        let id = field(&prepared.claude_resume, "conversation_id");
        if state
            .sessions
            .read()
            .unwrap()
            .values()
            .any(|row| row.owns_claude_conversation(id))
            || scheduling::conflict(state, &prepared.cwd, false).is_some()
        {
            return Err(io::Error::other(
                "Claude takeover found a live writer or workspace conflict",
            ));
        }
        preview.push(prepared);
    }
    let first = &preview[0];
    if preview.iter().any(|plan| {
        plan.provider.executable != first.provider.executable
            || plan.provider.identity != first.provider.identity
            || plan.claude_profile != first.claude_profile
    }) {
        return Err(io::Error::other(
            "Claude takeover provider or profile differs within the batch",
        ));
    }
    let rows = providers::claude_agent_view(
        &first.provider.executable,
        &first.cwd,
        &first.provider.env,
        true,
    )?;
    let inventory = claude::background_inventory(&rows)?;
    if json!(inventory) != request["inventory"]
        || !inventory.values().any(|(_, settled)| !settled)
        || inventory
            .iter()
            .any(|(id, (_, settled))| !settled && !ids.contains(id.as_str()))
    {
        return Err(io::Error::other(
            "Claude background inventory changed or contains an unselected live source",
        ));
    }
    for plan in &preview {
        let home = std::path::Path::new(field(&plan.claude_profile, "home"));
        if providers::identity(&plan.provider.executable)? != plan.provider.identity
            || claude::profile(&plan.provider.env, &launches[0]["env"], &Value::Null)?
                != plan.claude_profile
            || claude::project_authority(home, &plan.cwd)?
                != plan.claude_resume["project_authority"]
            || claude::transcript(
                home,
                &plan.cwd,
                field(&plan.claude_resume, "conversation_id"),
            )?
            .1 != plan.claude_resume["transcript"]
        {
            return Err(io::Error::other(
                "Claude source authority changed before native stop",
            ));
        }
    }
    providers::stop_claude_agent_view(&first.provider.executable, &first.cwd, &first.provider.env)?;
    let mut reserved = Vec::with_capacity(launches.len());
    for (launch, original) in launches.iter().zip(preview) {
        let id = field(&original.claude_resume, "conversation_id").to_owned();
        let result = (|| {
            let ready = Session::prepare_claude_external(launch, true)?;
            if ready.provider.identity != original.provider.identity
                || ready.claude_profile != original.claude_profile
                || ready.cwd != original.cwd
                || ready.claude_resume["transcript"] != original.claude_resume["transcript"]
                || ready.claude_resume["project_authority"]
                    != original.claude_resume["project_authority"]
            {
                return Err(io::Error::other(
                    "Claude source custody changed after native stop",
                ));
            }
            state.reserve_locked(ready, false, None, None, None, &gate)
        })();
        reserved.push((id, result));
    }
    drop(gate);
    Ok(reserved.into_iter().map(|(id, reservation)| {
        match reservation {
            Ok(session) => match state.start(&session) {
                Ok(view)=>json!({"conversation_id":id,"session":view}),
                Err(err)=>json!({"conversation_id":id,"session_id":format!("{}/{}",state.endpoint,session.id),"error":err.to_string()}),
            },
            Err(err)=>json!({"conversation_id":id,"error":err.to_string()}),
        }
    }).collect())
}
