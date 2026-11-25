use super::{CliResult, Client, flag_error, options, report};
use serde_json::json;

pub(super) fn kill(client: &mut Client, args: &[String]) -> CliResult {
    let [id] = args else {
        return Err((
            1,
            "need exactly one session id: swarm kill <session>".into(),
        ));
    };
    client.request("kill", Some(id), "ok").map_err(report)?;
    println!("killed {id}");
    Ok(())
}

pub(super) fn parse_bool(name: &str, value: &str) -> Result<bool, String> {
    match value {
        "1" | "t" | "T" | "TRUE" | "true" | "True" => Ok(true),
        "0" | "f" | "F" | "FALSE" | "false" | "False" => Ok(false),
        _ => Err(format!(
            "invalid boolean value {value:?} for -{name}: parse error"
        )),
    }
}

pub(super) fn send(client: &mut Client, args: &[String]) -> CliResult {
    let Some((id, rest)) = args.split_first().filter(|(s, _)| !s.starts_with('-')) else {
        return Err((
            2,
            "need a session id first: swarm send <session> (--text s [--no-submit] | --key name)"
                .into(),
        ));
    };
    let (mut text, mut key, mut no_submit) = ("", "", false);
    let rest = options(
        rest,
        &["text", "key", "no-submit"],
        &["no-submit"],
        |name, value| {
            match name {
                "text" => text = value,
                "key" => key = value,
                _ => no_submit = parse_bool(name, value)?,
            }
            Ok(())
        },
    )
    .map_err(|e| flag_error("send", 2, e))?;
    if let Some(arg) = rest.first() {
        return Err((
            2,
            format!("unexpected argument {arg:?}; the message goes in --text"),
        ));
    }
    if text.is_empty() == key.is_empty() {
        return Err((2, "need exactly one of --text or --key".into()));
    }
    let input = if key.is_empty() {
        json!({"text":text, "submit":!no_submit})
    } else {
        if no_submit {
            return Err((2, "--no-submit only applies to --text".into()));
        }
        if !matches!(key, "enter" | "esc" | "ctrl-c" | "tab" | "up" | "down") {
            return Err((
                2,
                format!("unknown --key {key:?}: want enter, esc, ctrl-c, tab, up or down"),
            ));
        }
        json!({"key":key})
    };
    client
        .request_body(
            json!({"op":"send_input","session_id":id,"send_input":input}),
            "ok",
        )
        .map_err(report)?;
    Ok(())
}
