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
    deletion_uncertain: bool,
}

pub(super) fn unresolved(state: &State) -> bool {
    if !state.manager {
        return false;
    }
    let input = state.manager_terminal.lock().unwrap();
    input.deletion_uncertain
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
    if !input.durable_draft {
        runtime::write_json(&path, &json!({"version":1,"class":"draft"}))?;
        input.durable_draft = true;
    }
    pty::input(&state.master, bytes)?;
    if bytes == b"\r" {
        let observation =
            runtime::read_json(&state.dir.join("observation.json")).unwrap_or(Value::Null);
        let baseline = if observation["token"] == state.token {
            &observation["status"]
        } else {
            &Value::Null
        };
        let _ = runtime::write_json(
            &path,
            &json!({"version":1,"class":"submitted",
                "baseline_turn":baseline["Turn"].as_str().unwrap_or("unknown"),
                "baseline_interaction":baseline["Interaction"].as_str().unwrap_or("unknown")}),
        );
        input.durable_draft = false;
    }
    Ok(())
}

fn resolved(record: &Value, status: &Value) -> bool {
    record["version"] == 1
        && record["class"] == "submitted"
        && matches!(
            field(record, "baseline_turn"),
            "active" | "idle" | "unknown"
        )
        && matches!(
            field(record, "baseline_interaction"),
            "none" | "prompt" | "permission" | "error" | "unknown"
        )
        && status["Process"] == "running"
        && matches!(field(status, "Turn"), "active" | "idle")
        && matches!(
            field(status, "Interaction"),
            "none" | "prompt" | "permission" | "error"
        )
        && (record["baseline_turn"] != status["Turn"]
            || record["baseline_interaction"] != status["Interaction"])
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
        if runtime::read_json(&path).is_ok_and(|record| resolved(&record, &observation["status"]))
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
        status["Process"] = json!("lost");
        assert!(!resolved(&record, &status));
    }
}
