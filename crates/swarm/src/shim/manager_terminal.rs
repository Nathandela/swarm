use super::{State, field, pty, runtime};
use serde_json::{Value, json};
use std::fs::{self, File};
use std::io;

pub(crate) const UNRESOLVED: &str = "manager proof: unresolved direct terminal input";
pub(crate) const CODE: &str = "manager_direct_input_unresolved";
const FILE: &str = "direct-input-unresolved.json";

#[derive(Default)]
pub(super) struct Input {
    durable_draft: bool,
    submission_uncertain: bool,
    deletion_uncertain: bool,
}

pub(super) fn unresolved(state: &State) -> bool {
    if !state.manager {
        return false;
    }
    let input = state.manager_terminal.lock().unwrap();
    input.durable_draft
        || input.deletion_uncertain
        || !matches!(fs::symlink_metadata(state.dir.join(FILE)), Err(err) if err.kind() == io::ErrorKind::NotFound)
}

pub(super) fn write(state: &State, bytes: &[u8]) -> io::Result<()> {
    if !state.manager {
        state.terminal_input.store(true, super::Ordering::Release);
        return pty::input(&state.master, bytes);
    }
    if state.dir.join("manager-retire.json").try_exists()? {
        return Err(io::Error::other("manager retirement owns the terminal"));
    }
    let mut input = state.manager_terminal.lock().unwrap();
    let path = state.dir.join(FILE);
    // Persist before the PTY write, whose failure may still mean bytes arrived.
    if !input.durable_draft || input.submission_uncertain {
        runtime::write_json(&path, &json!({"version":1,"class":"draft"}))?;
        input.durable_draft = true;
        input.submission_uncertain = false;
    }
    pty::input(&state.master, bytes)?;
    if bytes == b"\r" {
        let observation =
            runtime::read_json(&state.dir.join("observation.json")).unwrap_or(Value::Null);
        if let Some(record) = submitted_record(&observation, &state.token) {
            if runtime::write_json(&path, &record).is_ok() {
                input.durable_draft = false;
            } else {
                // Rename may have reached disk before the directory sync failed.
                input.submission_uncertain = true;
            }
        }
    }
    Ok(())
}

pub(super) fn guard_dispatch(state: &State, request: &Value, held: bool) -> io::Result<()> {
    let composer = &request["composer_send"];
    let op = field(request, "operation_id");
    if composer["session"] != request["session"]
        || composer["session_instance"]
            != crate::providers::codex_receipt::provider_instance(&state.provider, &state.token)
        || composer["text"] != request["text"]
        || composer["manager_admission"].is_null()
        || composer["manager_admission"] != request["manager_admission"]
    {
        return Err(io::Error::other(
            "dispatcher gate requires the exact admitted composer",
        ));
    }
    if crate::providers::codex_receipt::manager_dispatch(&state.dir, op, composer, held)? {
        return Err(io::Error::other(UNRESOLVED));
    }
    Ok(())
}

fn known_turn(turn: &str) -> bool {
    matches!(turn, "active" | "idle")
}

fn known_interaction(interaction: &str) -> bool {
    matches!(interaction, "none" | "prompt" | "permission" | "error")
}

fn known_status(status: &Value) -> bool {
    status["Process"] == "running"
        && known_turn(field(status, "Turn"))
        && known_interaction(field(status, "Interaction"))
}

fn submitted_record(observation: &Value, token: &str) -> Option<Value> {
    let status = &observation["status"];
    (observation["token"] == token && known_status(status)).then(|| {
        json!({"version":1,"class":"submitted","baseline_turn":status["Turn"],
            "baseline_interaction":status["Interaction"]})
    })
}

fn resolved(record: &Value, status: &Value) -> bool {
    record["version"] == 1
        && record["class"] == "submitted"
        && known_turn(field(record, "baseline_turn"))
        && known_interaction(field(record, "baseline_interaction"))
        && known_status(status)
        && (record["baseline_turn"] != status["Turn"]
            || record["baseline_interaction"] != status["Interaction"])
}

pub(crate) fn decode_probe(response: io::Result<Value>) -> io::Result<bool> {
    response
        .ok()
        .filter(|response| response["type"] == "ok")
        .and_then(|response| response["unresolved"].as_bool())
        .ok_or_else(|| {
            io::Error::new(
                io::ErrorKind::WouldBlock,
                "manager terminal input state is unavailable",
            )
        })
}

pub(super) fn observe(state: &State, observation: &Value) -> io::Result<()> {
    let input_guard = state
        .manager
        .then(|| state.manager_terminal.lock().unwrap());
    runtime::write_json(&state.dir.join("observation.json"), observation)?;
    if let Some(mut input) = input_guard
        && !input.durable_draft
    {
        let path = state.dir.join(FILE);
        if observation["token"] == state.token
            && runtime::read_json(&path)
                .is_ok_and(|record| resolved(&record, &observation["status"]))
            && fs::remove_file(path).is_ok()
        {
            input.deletion_uncertain = true;
        }
        if input.deletion_uncertain {
            input.deletion_uncertain = File::open(&state.dir)?.sync_all().is_err();
        }
    }
    Ok(())
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn submitted_record_requires_a_current_authoritative_baseline() {
        let mut observation = json!({"token":"current","status":{"Process":"running","Turn":"idle","Interaction":"none"}});
        assert!(submitted_record(&observation, "current").is_some());
        assert!(submitted_record(&observation, "replaced").is_none());
        assert!(submitted_record(&Value::Null, "current").is_none());
        for field in ["Turn", "Interaction"] {
            let old = std::mem::replace(&mut observation["status"][field], json!("unknown"));
            assert!(submitted_record(&observation, "current").is_none());
            observation["status"][field] = old;
        }
    }

    #[test]
    fn unavailable_or_malformed_probe_never_proves_no_draft() {
        for result in [
            Err(io::Error::from(io::ErrorKind::UnexpectedEof)),
            Ok(json!({"type":"error","unresolved":false})),
            Ok(json!({"type":"ok","unresolved":"false"})),
            Ok(json!({"type":"ok"})),
        ] {
            assert_eq!(
                decode_probe(result).unwrap_err().kind(),
                io::ErrorKind::WouldBlock
            );
        }
        for unresolved in [true, false] {
            assert_eq!(
                decode_probe(Ok(json!({"type":"ok","unresolved":unresolved}))).unwrap(),
                unresolved
            );
        }
    }

    #[test]
    fn only_submitted_input_with_proven_status_change_clears() {
        let mut record = json!({"version":1,"class":"draft","baseline_turn":"idle","baseline_interaction":"none"});
        let mut status = json!({"Process":"running","Turn":"active","Interaction":"none"});
        assert!(!resolved(&record, &status));
        record["class"] = json!("submitted");
        assert!(resolved(&record, &status));
        for turn in ["idle", "unknown"] {
            status["Turn"] = json!(turn);
            assert!(!resolved(&record, &status));
        }
        status["Turn"] = json!("active");
        for field in ["baseline_turn", "baseline_interaction"] {
            let old = std::mem::replace(&mut record[field], json!("unknown"));
            assert!(!resolved(&record, &status));
            record[field] = old;
        }
        status["Process"] = json!("lost");
        assert!(!resolved(&record, &status));
    }
}
