//! Private MCP transport for the manager's deliberately authenticated publish call.
use crate::socket::DeadlineReader;
use serde_json::{Value, json};
use std::fmt::Write as _;
use std::fs::{self, OpenOptions};
use std::io::{self, BufRead, Read, Write};
use std::os::unix::fs::{FileTypeExt, MetadataExt, OpenOptionsExt};
use std::os::unix::net::UnixStream;
use std::path::Path;
use std::time::{Duration, Instant};

pub(crate) fn run(args: &[String]) -> u8 {
    match args {
        [socket, auth, conversation, session] => {
            match serve(Path::new(socket), Path::new(auth), conversation, session) {
                Ok(()) => 0,
                Err(err) => {
                    eprintln!("manager-mcp: {err}");
                    1
                }
            }
        }
        _ => 2,
    }
}

fn serve(socket: &Path, auth: &Path, conversation: &str, session: &str) -> io::Result<()> {
    if !socket.is_absolute()
        || !auth.is_absolute()
        || socket.parent() != auth.parent()
        || !conversation.starts_with("discord-teammate-dt_")
        || conversation.len() > 128
        || !crate::supervisor::runtime::valid_id(session)
    {
        return Err(refusal("invalid private manager bridge coordinate"));
    }
    let credential = read_credential(auth)?;
    let mut input = io::stdin().lock();
    let mut output = io::stdout().lock();
    loop {
        let mut line = Vec::new();
        let read = (&mut input).take(16_385).read_until(b'\n', &mut line)?;
        if read == 0 {
            return Ok(());
        }
        if read > 16_384 || !line.ends_with(b"\n") {
            return Err(refusal("manager MCP frame exceeds its bound"));
        }
        let request: Value = serde_json::from_slice(&line)?;
        let Some(id) = request.get("id") else {
            continue;
        };
        if !(id.is_string() || id.is_u64() || id.is_i64()) {
            return Err(refusal("invalid manager MCP request identifier"));
        }
        let response = match request["method"].as_str() {
            Some("initialize") => {
                let version = request["params"]["protocolVersion"]
                    .as_str()
                    .filter(|version| {
                        matches!(*version, "2024-11-05" | "2025-03-26" | "2025-06-18")
                    })
                    .unwrap_or("2025-03-26");
                json!({"result":{"protocolVersion":version,"capabilities":{"tools":{}},
                    "serverInfo":{"name":"swarm_voice","version":"1"}}})
            }
            Some("tools/list") => json!({"result":{"tools":[{
                "name":"publish", "description":"Publish a final plain-text reply to the bound owner discussion.",
                "inputSchema":{"type":"object","additionalProperties":false,
                    "properties":{"output_id":{"type":"string"},"sequence":{"type":"integer","const":1},
                        "text":{"type":"string"},"final":{"type":"boolean","const":true}},
                    "required":["output_id","sequence","text","final"]}
            }]}}),
            Some("tools/call") => {
                let params = &request["params"];
                let reply = if params["name"] == "publish"
                    && params["arguments"].is_object()
                    && params["_meta"].is_object()
                {
                    let call = json!({"version":1,"conversation_id":conversation,"session_id":session,
                        "call":{"name":"publish","arguments":params["arguments"],"meta":params["_meta"]}});
                    bridge_call(socket, &credential, &call)
                } else {
                    Err(refusal("unsupported or unproved manager tool call"))
                };
                match reply {
                    Ok(reply) => {
                        json!({"result":{"content":[{"type":"text","text":reply.to_string()}]}})
                    }
                    Err(_) => {
                        json!({"result":{"isError":true,"content":[{"type":"text","text":"Manager request was not confirmed."}]}})
                    }
                }
            }
            Some("ping") => json!({"result":{}}),
            _ => json!({"error":{"code":-32601,"message":"Method not found"}}),
        };
        let mut response = response;
        response["jsonrpc"] = json!("2.0");
        response["id"] = id.clone();
        serde_json::to_writer(&mut output, &response)?;
        output.write_all(b"\n")?;
        output.flush()?;
    }
}

pub(crate) fn read_credential(auth: &Path) -> io::Result<String> {
    let file = OpenOptions::new()
        .read(true)
        .custom_flags(libc::O_NOFOLLOW | libc::O_NONBLOCK)
        .open(auth)?;
    let meta = file.metadata()?;
    // SAFETY: geteuid takes no arguments.
    if !meta.is_file()
        || meta.nlink() != 1
        || meta.mode() & 0o777 != 0o600
        || meta.uid() != unsafe { libc::geteuid() }
    {
        return Err(refusal("manager bridge credential lacks owner custody"));
    }
    let mut contents = String::new();
    file.take(65).read_to_string(&mut contents)?;
    if contents.len() != 64
        || !contents
            .bytes()
            .all(|byte| byte.is_ascii_digit() || (b'a'..=b'f').contains(&byte))
    {
        return Err(refusal("manager bridge credential is invalid"));
    }
    Ok(contents)
}

fn bridge_call(socket: &Path, credential: &str, call: &Value) -> io::Result<Value> {
    let meta = fs::symlink_metadata(socket)?;
    // SAFETY: geteuid takes no arguments.
    if !meta.file_type().is_socket()
        || meta.nlink() != 1
        || meta.mode() & 0o777 != 0o600
        || meta.uid() != unsafe { libc::geteuid() }
    {
        return Err(refusal("manager socket lacks owner custody"));
    }
    let mut stream = UnixStream::connect(socket)?;
    stream.set_write_timeout(Some(Duration::from_secs(2)))?;
    let body = serde_json::to_vec(call)?;
    if body.len() > 8192 {
        return Err(refusal("manager tool body too large"));
    }
    let mut headers = String::new();
    write!(&mut headers,"POST /manager/call HTTP/1.0\r\nHost: voice\r\nAuthorization: Bearer {credential}\r\nContent-Type: application/json\r\nContent-Length: {}\r\n\r\n",body.len()).unwrap();
    stream.write_all(headers.as_bytes())?;
    stream.write_all(&body)?;
    let mut output = Vec::new();
    DeadlineReader(&stream, Instant::now() + Duration::from_secs(5))
        .take(16_385)
        .read_to_end(&mut output)?;
    let separator = output
        .windows(4)
        .position(|bytes| bytes == b"\r\n\r\n")
        .ok_or_else(|| refusal("manager bridge response is incomplete"))?;
    if output.len() > 16_384
        || !(output.starts_with(b"HTTP/1.0 200 ") || output.starts_with(b"HTTP/1.1 200 "))
    {
        return Err(refusal("manager bridge rejected the tool call"));
    }
    let result: Value = serde_json::from_slice(&output[separator + 4..])?;
    if !result["data"].is_object() || !result["error"].is_null() {
        return Err(refusal("manager bridge did not acknowledge the tool call"));
    }
    Ok(result["data"].clone())
}

fn refusal(message: &str) -> io::Error {
    io::Error::new(io::ErrorKind::InvalidInput, message)
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::supervisor::runtime;
    use std::io::BufReader;
    use std::os::unix::fs::symlink;

    #[test]
    fn private_bridge_preserves_exact_call_coordinates_and_refuses_replaced_socket() {
        let dir = Path::new("/tmp").join(format!("mcp-{}", &runtime::random_id().unwrap()[..12]));
        runtime::private_dir(&dir).unwrap();
        let socket = dir.join("bridge.sock");
        let listener = runtime::bind(&socket).unwrap();
        let credential = "a".repeat(64);
        let call = json!({"version":1,"conversation_id":"discord-teammate-dt_fixture","session_id":"local",
            "call":{"name":"publish","arguments":{"text":"scoped reply"},
                "meta":{"threadId":"native","callId":"observed"}}});
        let expected_call = call.clone();
        let expected_auth = format!("Authorization: Bearer {credential}\r\n");
        let server = std::thread::spawn(move || {
            let (mut peer, _) = listener.accept().unwrap();
            let mut read = BufReader::new(&mut peer);
            let mut first = String::new();
            read.read_line(&mut first).unwrap();
            assert_eq!(first, "POST /manager/call HTTP/1.0\r\n");
            let mut length = 0;
            let mut authorised = false;
            loop {
                let mut header = String::new();
                assert!(read.read_line(&mut header).unwrap() > 0);
                if header == "\r\n" {
                    break;
                }
                if let Some(n) = header.strip_prefix("Content-Length: ") {
                    length = n.trim().parse().unwrap();
                }
                authorised |= header == expected_auth;
            }
            assert!(authorised);
            let mut bytes = vec![0; length];
            read.read_exact(&mut bytes).unwrap();
            assert_eq!(
                serde_json::from_slice::<Value>(&bytes).unwrap(),
                expected_call
            );
            peer.write_all(
                b"HTTP/1.0 200 OK\r\nContent-Length: 26\r\n\r\n{\"data\":{\"state\":\"queued\"}}",
            )
            .unwrap();
        });
        assert_eq!(
            bridge_call(&socket, &credential, &call).unwrap(),
            json!({"state":"queued"})
        );
        server.join().unwrap();
        fs::remove_file(&socket).unwrap();
        symlink(dir.join("missing.sock"), &socket).unwrap();
        assert!(bridge_call(&socket, &credential, &call).is_err());
        fs::remove_dir_all(dir).unwrap();
    }
}
