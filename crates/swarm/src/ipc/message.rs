use super::{CliResult, Client, field, json_out, options, report};
use crate::providers::codex_receipt;
use serde_json::{Value, json};
use std::time::Duration;

pub(super) const CAPS: &[&str] = &["semantic-send", "semantic-target"];

pub(super) fn run(client: &mut Client, args: &[String]) -> CliResult {
    let Some((session, args)) = args
        .split_first()
        .filter(|(id, _)| !id.is_empty() && !id.starts_with('-'))
    else {
        return Err((
            2,
            "swarm message <session> --text <exact message> [--operation-id <id>]".into(),
        ));
    };
    let mut status = false;
    let mut text = "";
    let mut id = "";
    let extra = options(
        args,
        &["status", "text", "operation-id"],
        &["status"],
        |name, value| {
            match name {
                "status" => {
                    status = match value {
                        "1" | "t" | "T" | "true" | "True" | "TRUE" => true,
                        "0" | "f" | "F" | "false" | "False" | "FALSE" => false,
                        _ => return Err("invalid boolean status flag".into()),
                    }
                }
                "text" => text = value,
                "operation-id" => id = value,
                _ => unreachable!(),
            }
            Ok(())
        },
    )
    .map_err(|err| (2, err))?;
    if !extra.is_empty() || text.is_empty() != status || status && id.is_empty() {
        return Err((
            2,
            "requires --text <exact message> or --status --operation-id <id>".into(),
        ));
    }
    if !client.supports("semantic-send") {
        return Err((
            1,
            "semantic messages require an updated daemon; run swarm daemon restart".into(),
        ));
    }
    if status {
        return json_out(&receipt(client, session, id)?).map_err(report);
    }
    let operation = if id.is_empty() {
        codex_receipt::operation().map_err(report)?
    } else {
        id.into()
    };
    if !codex_receipt::timely(&operation) {
        return Err((
            1,
            format!("operation_id={operation}: invalid or expired semantic operation ID"),
        ));
    }
    let result = (|| {
        let instance = if client.supports("semantic-target") {
            let target = client
                .request("semantic_target", Some(session), "semantic_target")
                .map_err(report)?;
            if target["session_id"] != session.as_str() {
                return Err((1, "uncorrelated semantic target".into()));
            }
            field(&target, "semantic_target_instance").to_owned()
        } else {
            let rows = client.sessions().map_err(report)?;
            rows.iter()
                .find(|row| row["id"] == session.as_str() && row["status"]["Process"] == "running")
                .map(|row| field(&row["capabilities"], "session_instance").to_owned())
                .unwrap_or_default()
        };
        if instance.is_empty() {
            return Err((
                1,
                "discussion has no current native session incarnation".into(),
            ));
        }
        let reply = client.exchange(json!({
            "op":"semantic_send","session_id":session,"operation_id":operation,"body_version":1,
            "composer_send":{"session":session,"session_instance":instance,"text":text}
        }), "ok", Duration::from_secs(35)).map_err(report)?;
        if reply["operation_id"] != operation {
            return Err((1, "uncorrelated semantic receipt; outcome unknown".into()));
        }
        let observed = receipt(client, session, &operation)?;
        json_out(&observed).map_err(report)
    })();
    result.map_err(|(code, err)| (code, format!("operation_id={operation}: {err}")))
}

fn receipt(client: &mut Client, session: &str, operation: &str) -> Result<Value, (i32, String)> {
    let reply = client
        .exchange(
            json!({
                "op":"semantic_status","session_id":session,"operation_id":operation,
            }),
            "semantic_status",
            Duration::from_secs(35),
        )
        .map_err(report)?;
    let result = &reply["semantic_receipt"];
    if reply["operation_id"] != operation
        || result["operation_id"] != operation
        || result["session"] != session
    {
        return Err((1, "uncorrelated semantic receipt".into()));
    }
    Ok(result.clone())
}

pub(super) fn requests(client: &mut Client, args: &[String]) -> CliResult {
    let Some((session, args)) = args
        .split_first()
        .filter(|(id, _)| !id.is_empty() && !id.starts_with('-'))
    else {
        return Err((2, "requests requires a session".into()));
    };
    let mut request = "";
    let mut native = "";
    let extra = options(
        args,
        &["request-id", "native-session-id"],
        &[],
        |name, value| {
            if name == "request-id" {
                request = value;
            } else {
                native = value;
            }
            Ok(())
        },
    )
    .map_err(|err| (2, err))?;
    if !extra.is_empty() || request.is_empty() != native.is_empty() {
        return Err((
            2,
            "requests requires both --request-id and --native-session-id, or neither".into(),
        ));
    }
    if !client.supports("native-requests-v1") {
        return Err((1, "native request reads require an updated daemon".into()));
    }
    let mut body = json!({"session":session});
    if !request.is_empty() {
        body["request_id"] = json!(request);
        body["native_session_id"] = json!(native);
    }
    let reply = client
        .request_body(
            json!({
                "op":"native_requests","session_id":session,"native_requests":body
            }),
            "native_requests",
        )
        .map_err(report)?;
    let result = &reply["native_requests_result"];
    if result["session"] != session.as_str() {
        return Err((1, "uncorrelated native request response".into()));
    }
    json_out(result).map_err(report)
}
