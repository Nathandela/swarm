//! Exact initial worker result from the owned Claude native transcript.
use super::{canonical_id, text_hash, transcript};
use crate::ipc::field;
use serde_json::{Value, json};
use std::{
    fs::OpenOptions,
    io::{self, BufRead, BufReader, Read},
    os::unix::fs::{MetadataExt, OpenOptionsExt},
    path::Path,
};

const MAX_SCAN: u64 = 1 << 20;

pub(crate) fn read(
    home: &Path,
    cwd: &Path,
    id: &str,
    prompt_hash: &Value,
    budget: usize,
) -> io::Result<Value> {
    let (path, identity) = match transcript(home, cwd, id) {
        Ok(source) => source,
        Err(error)
            if matches!(
                error.kind(),
                io::ErrorKind::NotFound | io::ErrorKind::WouldBlock
            ) =>
        {
            return Ok(json!({"state":"pending"}));
        }
        Err(error) => return Err(error),
    };
    let file = OpenOptions::new()
        .read(true)
        .custom_flags(libc::O_NOFOLLOW | libc::O_NONBLOCK)
        .open(&path)?;
    let before = file.metadata()?;
    if identity != json!([before.dev(), before.ino()]) || before.len() > MAX_SCAN {
        return Err(io::Error::other(
            "Claude result transcript changed or exceeds the proof bound",
        ));
    }
    let result = scan(
        BufReader::new((&file).take(MAX_SCAN)),
        cwd,
        id,
        prompt_hash,
        budget,
    )?;
    let after = std::fs::symlink_metadata(path)?;
    if !after.is_file() || identity != json!([after.dev(), after.ino()]) {
        return Err(io::Error::other(
            "Claude result transcript identity changed during read",
        ));
    }
    Ok(result)
}

fn scan(
    mut reader: impl BufRead,
    cwd: &Path,
    id: &str,
    prompt_hash: &Value,
    budget: usize,
) -> io::Result<Value> {
    let refused = || io::Error::other("Claude transcript has no exact tool-free initial result");
    let mut input = String::new();
    let mut tail = String::new();
    let mut response = None;
    let mut line = Vec::new();
    while reader.read_until(b'\n', &mut line)? != 0 {
        if line.last() != Some(&b'\n') {
            break;
        }
        let row: Value = serde_json::from_slice(&line)?;
        line.clear();
        let uuid = field(&row, "uuid");
        let kind = field(&row, "type");
        if row["isSidechain"] == true
            || !matches!(kind, "user" | "assistant" | "attachment" | "system")
            || !canonical_id(uuid)
        {
            continue;
        }
        if row["sessionId"] != id || row["cwd"].as_str().map(Path::new) != Some(cwd) {
            return Err(refused());
        }
        let message = &row["message"];
        if input.is_empty() {
            if kind != "user" {
                continue;
            }
            if !row["parentUuid"].is_null()
                || message["role"] != "user"
                || prompt_hash.as_str() != Some(text_hash(field(message, "content")).as_str())
            {
                return Err(refused());
            }
            input = uuid.to_owned();
        } else {
            if row["parentUuid"] != tail || row["agentId"].is_string() {
                return Err(refused());
            }
            match kind {
                "assistant"
                    if response.is_none()
                        && message["role"] == "assistant"
                        && message["stop_reason"] == "end_turn" =>
                {
                    let blocks = message["content"]
                        .as_array()
                        .filter(|blocks| !blocks.is_empty())
                        .ok_or_else(refused)?;
                    let texts = blocks
                        .iter()
                        .map(|block| {
                            (block["type"] == "text")
                                .then(|| block["text"].as_str())
                                .flatten()
                        })
                        .collect::<Option<Vec<_>>>()
                        .ok_or_else(refused)?;
                    let body = texts.join("\n");
                    if body.trim().is_empty() {
                        return Err(refused());
                    }
                    let end = body.floor_char_boundary(body.len().min(budget));
                    response = Some(json!({"state":"completed","native_input_message_id":input,
                        "native_result_message_id":uuid,"text":&body[..end],"truncated":end<body.len()}));
                }
                "assistant" | "user" => return Err(refused()),
                "system" if row["subtype"] == "turn_duration" => {
                    return response.ok_or_else(refused);
                }
                "attachment" | "system" => {}
                _ => return Err(refused()),
            }
        }
        tail = uuid.to_owned();
    }
    Ok(json!({"state":"pending"}))
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn native_header_can_precede_the_first_conversation_record() {
        use std::fs;
        use std::io::Write;
        let home = crate::test_temp::root()
            .canonicalize()
            .unwrap()
            .join(crate::supervisor::runtime::random_id().unwrap());
        let cwd = Path::new("/fixture");
        let id = "a1b2c3d4-0000-4000-8000-000000000001";
        let project = home.join(".claude/projects/-fixture");
        fs::create_dir_all(&project).unwrap();
        let path = project.join(format!("{id}.jsonl"));
        let mut output = fs::File::create(&path).unwrap();
        writeln!(output, "{{\"type\":\"permission-mode\"}}").unwrap();
        let result = || {
            read(
                &home,
                cwd,
                id,
                &json!(text_hash("Exact delegated task")),
                64,
            )
        };
        assert_eq!(result().unwrap()["state"], "pending");
        writeln!(
            output,
            "{}",
            json!({"type":"user","sessionId":id,"cwd":"/wrong"})
        )
        .unwrap();
        assert!(result().is_err());
        fs::write(&path, " ".repeat(MAX_SCAN as usize)).unwrap();
        assert!(result().is_err());
        fs::remove_dir_all(home).unwrap();
    }

    #[test]
    fn incomplete_or_unrelated_transcript_never_releases_result() {
        let native = "a1b2c3d4-0000-4000-8000-000000000001";
        let input = "a1b2c3d4-0000-4000-8000-000000000002";
        let assistant = "a1b2c3d4-0000-4000-8000-000000000003";
        let closed = "a1b2c3d4-0000-4000-8000-000000000004";
        let cwd = Path::new("/fixture");
        let open = json!({"type":"user","uuid":input,"parentUuid":null,"sessionId":native,"cwd":cwd,
            "message":{"role":"user","content":"Exact delegated task"}});
        let answer = json!({"type":"assistant","uuid":assistant,"parentUuid":input,"sessionId":native,"cwd":cwd,
            "message":{"role":"assistant","stop_reason":"end_turn","content":[{"type":"text","text":"Evidence"}]}});
        let end = json!({"type":"system","subtype":"turn_duration","uuid":closed,"parentUuid":assistant,
            "sessionId":native,"cwd":cwd});
        let scan_rows = |rows: &[Value], hash| {
            let text = rows
                .iter()
                .map(Value::to_string)
                .collect::<Vec<_>>()
                .join("\n")
                + "\n";
            scan(text.as_bytes(), cwd, native, &json!(text_hash(hash)), 64)
        };
        let mut events = vec![open, answer, end];
        assert_eq!(
            scan_rows(&events[..2], "Exact delegated task").unwrap()["state"],
            "pending"
        );
        assert_eq!(
            scan_rows(&events, "Exact delegated task").unwrap()["text"],
            "Evidence"
        );
        assert!(scan_rows(&events, "Unrelated task").is_err());
        events[1]["parentUuid"] = json!(closed);
        assert!(scan_rows(&events, "Exact delegated task").is_err());
    }
}
