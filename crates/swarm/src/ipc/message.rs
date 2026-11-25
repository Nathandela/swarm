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

pub(super) fn decide(client: &mut Client, args: &[String]) -> CliResult {
    let Some((session, args)) = args
        .split_first()
        .filter(|(id, _)| !id.is_empty() && !id.starts_with('-'))
    else {
        return Err((2, "decide requires a session".into()));
    };
    let mut flags = std::collections::BTreeMap::new();
    let extra = options(
        args,
        &[
            "status",
            "operation-id",
            "request-id",
            "native-session-id",
            "request-hash",
            "reply",
            "answers",
        ],
        &["status"],
        |key, value| {
            if flags.insert(key.to_owned(), value).is_some() {
                return Err(format!("duplicate --{key}"));
            }
            Ok(())
        },
    )
    .map_err(|error| (2, error))?;
    if !extra.is_empty() {
        return Err((2, "unexpected native decision arguments".into()));
    }
    let get = |key| flags.get(key).copied().unwrap_or_default();
    let status = flags
        .get("status")
        .map(|value| crate::flags::parse_bool("status", value))
        .transpose()
        .map_err(|error| (2, error))?
        .unwrap_or(false);
    let operation = get("operation-id");
    let (op, operation, body) = if status {
        if operation.is_empty()
            || flags
                .keys()
                .any(|key| !matches!(key.as_str(), "status" | "operation-id"))
        {
            return Err((2, "decide --status requires only --operation-id".into()));
        }
        ("native_decision_status", operation.to_owned(), Value::Null)
    } else {
        let request = get("request-id");
        let target = get("native-session-id");
        let hash = get("request-hash");
        let answer = get("answers");
        let reply = get("reply");
        if request.is_empty()
            || target.is_empty()
            || hash.is_empty()
            || !matches!(reply, "once" | "always" | "reject" | "answer")
            || (reply == "answer") == answer.is_empty()
        {
            return Err((
                2,
                "decide requires the exact request ID, native session ID, hash and reply".into(),
            ));
        }
        let card = client
            .exchange(
                json!({
                    "op":"native_requests","session_id":session,"native_requests":{
                        "session":session,"request_id":request,"native_session_id":target,
                    },
                }),
                "native_requests",
                Duration::from_secs(18),
            )
            .map_err(report)?;
        let card = &card["native_requests_result"]["request"];
        if card["request_hash"] != hash
            || card["request_id"] != request
            || card["native_session_id"] != target
            || card["session"] != session.as_str()
        {
            return Err((
                1,
                "OpenCode request content changed or the card expired".into(),
            ));
        }
        let kind = match (field(card, "kind"), reply) {
            ("permission", "once" | "always" | "reject") => "permission_reply",
            ("question", "answer") => "question_reply",
            ("question", "reject") => "question_reject",
            _ => return Err((2, "reply is not offered for this native request".into())),
        };
        let mut body = json!({"session":session,"kind":kind});
        for name in [
            "session_instance",
            "root_id",
            "generation",
            "native_session_id",
            "request_id",
            "request_hash",
            "expires_at",
        ] {
            body[name] = card[name].clone();
        }
        if kind == "permission_reply" {
            body["reply"] = json!(reply);
        } else if kind == "question_reply" {
            let answers = serde_json::from_str::<Vec<Vec<String>>>(answer).map_err(|_| {
                (
                    2,
                    "--answers requires grouped JSON arrays of strings".into(),
                )
            })?;
            body["answers"] = json!(answers);
        }
        (
            "native_decide",
            if operation.is_empty() {
                codex_receipt::operation().map_err(report)?
            } else {
                operation.to_owned()
            },
            body,
        )
    };
    let result = client.exchange(json!({
        "op":op,"session_id":session,"operation_id":operation,"body_version":1,"native_decision":body,
    }),op,Duration::from_secs(35)).map_err(|error|(1,format!("operation_id={operation}: {error}")))?;
    let receipt = &result["native_decision_receipt"];
    if receipt["operation_id"] != operation || receipt["session"] != session.as_str() {
        return Err((
            1,
            format!("operation_id={operation}: uncorrelated OpenCode decision outcome"),
        ));
    }
    json_out(receipt).map_err(report)
}

pub(super) fn history(client: &mut Client, args: &[String]) -> CliResult {
    let Some((session, args)) = args
        .split_first()
        .filter(|(id, _)| !id.is_empty() && !id.starts_with('-'))
    else {
        return Err((2, "history requires a session".into()));
    };
    let mut limit = 50;
    let mut before = "";
    let extra = options(args, &["limit", "before"], &[], |name, value| {
        if name == "before" {
            before = value;
        } else {
            limit = value
                .parse::<u64>()
                .ok()
                .filter(|n| (1..=200).contains(n))
                .ok_or("history limit must be between 1 and 200")?;
        }
        Ok(())
    })
    .map_err(|error| (2, error))?;
    if !extra.is_empty() {
        return Err((2, "unexpected history arguments".into()));
    }
    let reply = client
        .exchange(
            json!({"op":"opencode_history","session_id":session,"limit":limit,"before":before}),
            "opencode_history",
            Duration::from_secs(18),
        )
        .map_err(report)?;
    if reply["session_id"] != session.as_str() {
        return Err((1, "uncorrelated OpenCode history response".into()));
    }
    json_out(&reply["history_result"]).map_err(report)
}

pub(super) fn resume(client: &mut Client, args: &[String]) -> CliResult {
    let [session] = args else {
        return Err((
            2,
            "resume requires exactly one stopped OpenCode session".into(),
        ));
    };
    if session.is_empty() || session.starts_with('-') {
        return Err((
            2,
            "resume requires exactly one stopped OpenCode session".into(),
        ));
    }
    let environment: Vec<_> = std::env::vars_os()
        .filter_map(|(key, value)| Some(format!("{}={}", key.to_str()?, value.to_str()?)))
        .collect();
    let environment = crate::environment::reliable("opencode", environment);
    let reply = client
        .exchange(
            json!({"op":"opencode_resume","session_id":session,"env":environment}),
            "opencode_resume",
            Duration::from_secs(60),
        )
        .map_err(report)?;
    if reply["session_id"] != session.as_str() || field(&reply["session"], "id").is_empty() {
        return Err((1, "uncorrelated OpenCode resume response".into()));
    }
    json_out(&reply["session"]).map_err(report)
}
