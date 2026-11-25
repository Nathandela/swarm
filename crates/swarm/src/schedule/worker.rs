use super::{Completion, Launch, Result, valid_id};
use crate::{ipc, supervisor::runtime};
use serde::{Deserialize, Serialize};
use serde_json::{Value, json};
use std::{
    io::{self, BufRead, Read, Write},
    os::unix::net::UnixStream,
    path::Path,
};

pub(crate) const RUN: &str = "SWARM_SCHEDULE_RUN";
pub(crate) const ATTEMPT: &str = "SWARM_SCHEDULE_ATTEMPT";
pub(crate) const PROOF: &str = "SWARM_SCHEDULE_PROOF";
pub(crate) const SOCKET: &str = "SWARM_SCHEDULE_SOCKET";
pub(crate) const BIN: &str = "SWARM_SCHEDULE_BIN";
pub(crate) const SESSION: &str = "SWARM_SESSION_ID";
pub(crate) fn prompt_fits(text: &str, scheduled: bool) -> bool {
    if scheduled {
        // Leave room for the control and native RPC envelopes after JSON escaping.
        serde_json::to_vec(text).is_ok_and(|encoded| encoded.len() <= ipc::wire::MAX_FRAME - 4096)
    } else {
        text.len() <= 4096
    }
}
const MCP: &str = "schedule-receipt-bridge";

#[derive(Clone, Deserialize, Serialize)]
#[serde(deny_unknown_fields)]
pub(crate) struct Binding {
    pub run_id: String,
    pub attempt_id: String,
    pub operation_id: String,
    pub authority: String,
}

impl Binding {
    pub(crate) fn valid(&self) -> bool {
        (valid_id(&self.run_id)
            || (self.run_id.len() == 64
                && self
                    .run_id
                    .bytes()
                    .all(|b| b.is_ascii_digit() || (b'a'..=b'f').contains(&b))))
            && valid_id(&self.attempt_id)
            && self
                .operation_id
                .strip_prefix("schedule:")
                .is_some_and(valid_id)
            && matches!(
                self.authority.as_str(),
                "observe" | "workspace-write" | "agent-default"
            )
    }

    pub(crate) fn new(launch: &Launch<'_>) -> Result<Self> {
        let binding = Self {
            run_id: launch.run_id.into(),
            attempt_id: launch.attempt_id.into(),
            operation_id: launch.operation_id.into(),
            authority: launch.authority.into(),
        };
        if !binding.valid() {
            return Err("invalid scheduled worker binding".into());
        }
        Ok(binding)
    }

    pub(crate) fn environment(
        &self,
        token: &str,
        socket: &Path,
        exe: &Path,
        session: &str,
    ) -> Result<Vec<(String, String)>> {
        if !runtime::valid_id(session)
            || token.len() != 64
            || !token.bytes().all(|b| b.is_ascii_hexdigit())
        {
            return Err("invalid scheduled worker receipt custody".into());
        }
        let socket = path_string(socket)?;
        let exe = path_string(exe)?;
        Ok([
            (RUN, self.run_id.as_str()),
            (ATTEMPT, self.attempt_id.as_str()),
            (PROOF, token),
            (SOCKET, socket),
            (BIN, exe),
            (SESSION, session),
        ]
        .into_iter()
        .map(|(key, value)| (key.to_owned(), value.to_owned()))
        .collect())
    }
}

pub(crate) fn read_binding(config: &Value) -> io::Result<Option<Binding>> {
    if config["schedule"].is_null() {
        return Ok(None);
    }
    let binding: Binding = serde_json::from_value(config["schedule"].clone())?;
    let agent = config["provider"]["name"].as_str();
    let mode = config["provider"]["mode"].as_str();
    let qualified = matches!((agent, mode), (Some("codex"), Some("managed")))
        || (matches!((agent, mode), (Some("agy"), Some("acp")))
            && binding.authority == "agent-default");
    if !binding.valid() || !qualified {
        return Err(io::Error::other("invalid scheduled worker reservation"));
    }
    Ok(Some(binding))
}

fn path_string(path: &Path) -> Result<&str> {
    path.to_str()
        .filter(|s| path.is_absolute() && !s.chars().any(char::is_control))
        .ok_or_else(|| {
            "scheduled worker path must be absolute, valid UTF-8 and free of control characters"
                .into()
        })
}

/// Apply to both native App Server and terminal launch plans created for this exact scheduled binding.
pub(crate) fn codex_policy(
    authority: &str,
    exe: &Path,
    receipt_enabled: bool,
) -> Result<Vec<String>> {
    let mut configs = Vec::new();
    let sandbox = match authority {
        "observe" => Some("read-only"),
        "workspace-write" => Some("workspace-write"),
        "agent-default" => None,
        _ => return Err("invalid scheduled Codex authority".into()),
    };
    if let Some(sandbox) = sandbox {
        configs.extend([
            format!("sandbox_mode=\"{sandbox}\""),
            "approval_policy=\"never\"".into(),
        ]);
    }
    if sandbox == Some("workspace-write") {
        configs.extend([
            "sandbox_workspace_write.writable_roots=[]".into(),
            "sandbox_workspace_write.network_access=false".into(),
            "sandbox_workspace_write.exclude_slash_tmp=true".into(),
            "sandbox_workspace_write.exclude_tmpdir_env_var=true".into(),
        ]);
    }
    if receipt_enabled {
        let exe = serde_json::to_string(path_string(exe)?)?;
        let env = serde_json::to_string(&[RUN, ATTEMPT, PROOF, SOCKET, SESSION])?;
        configs.extend([
            "check_for_update_on_startup=false".into(),
            format!(r#"mcp_servers.swarm_schedule={{command={exe},args=["{MCP}"],env_vars={env},required=true,enabled_tools=["complete"],tools={{complete={{approval_mode="approve"}}}}}}"#),
        ]);
    }
    Ok(configs
        .into_iter()
        .flat_map(|cfg| ["-c".into(), cfg])
        .collect())
}

pub(crate) fn codex_turn(authority: &str, cwd: &Path, params: &mut Value) {
    let sandbox = match authority {
        "observe" => json!({"type":"readOnly","networkAccess":false}),
        "workspace-write" => {
            json!({"type":"workspaceWrite","networkAccess":false,"writableRoots":[],"excludeSlashTmp":true,"excludeTmpdirEnvVar":true})
        }
        _ => return,
    };
    params["cwd"] = json!(cwd);
    params["approvalPolicy"] = json!("never");
    params["sandboxPolicy"] = sandbox;
}

#[derive(Deserialize)]
#[serde(deny_unknown_fields)]
struct Receipt {
    run_id: String,
    attempt_id: String,
    token: String,
    summary: String,
    #[serde(default)]
    links: Vec<String>,
}

pub(crate) fn handle_receipt(
    mut stream: UnixStream,
    endpoint: &str,
    submit: impl FnOnce(&str, &str, &str, &str, Completion) -> Result<()>,
) -> Result<()> {
    stream.set_read_timeout(Some(runtime::IO_TIMEOUT))?;
    stream.set_write_timeout(Some(runtime::IO_TIMEOUT))?;
    let hello = runtime::read_control(&mut stream)?;
    if hello["op"] != "hello"
        || hello["protocol_version"] != ipc::VERSION
        || !hello["capabilities"]
            .as_array()
            .is_some_and(|caps| caps.iter().any(|cap| cap == "schedule"))
    {
        return Err("incompatible scheduled receipt handshake".into());
    }
    runtime::write_control(
        &mut stream,
        &json!({
            "op":"hello", "protocol_version":ipc::VERSION, "endpoint_id":endpoint, "capabilities":["schedule"],
        }),
    )?;
    let request = runtime::read_control(&mut stream)?;
    let accept = || -> Result<()> {
        if request["op"] != "schedule_complete" || request["endpoint_id"] != endpoint {
            return Err("receipt listener only accepts scheduled completion".into());
        }
        let session = ipc::field(&request, "session_id");
        let (who, local) = session
            .split_once('/')
            .ok_or("invalid scheduled worker session identity")?;
        if who != endpoint || !runtime::valid_id(local) {
            return Err("invalid scheduled worker session identity".into());
        }
        let receipt: Receipt = serde_json::from_value(request["schedule_completion"].clone())
            .map_err(|_| "invalid scheduled completion request")?;
        submit(
            &receipt.run_id,
            &receipt.attempt_id,
            session,
            &receipt.token,
            Completion {
                summary: receipt.summary,
                links: receipt.links,
            },
        )
    };
    let reply = match accept() {
        Ok(()) => json!({"op":"schedule_complete", "endpoint_id":endpoint}),
        Err(e) => {
            json!({"op":"error", "endpoint_id":endpoint, "error_code":"invalid_field", "error":e.to_string()})
        }
    };
    runtime::write_control(&mut stream, &reply)?;
    Ok(())
}

pub(crate) fn bridge() -> u8 {
    match serve_bridge(io::stdin().lock(), io::stdout().lock(), super::rpc::submit) {
        Ok(()) => 0,
        Err(e) => {
            eprintln!("{e}");
            1
        }
    }
}

fn serve_bridge(
    mut input: impl BufRead,
    mut output: impl Write,
    submit: impl Fn(&str, &[String]) -> Result<()>,
) -> Result<()> {
    const LIMIT: u64 = 48 << 10;
    let mut line = Vec::new();
    loop {
        line.clear();
        if (&mut input).take(LIMIT + 1).read_until(b'\n', &mut line)? == 0 {
            return Ok(());
        }
        if line.len() as u64 > LIMIT {
            return Err("automation receipt bridge request exceeds 48 KiB".into());
        }
        let request: Value = serde_json::from_slice(&line)
            .map_err(|_| "invalid automation receipt bridge request")?;
        let Some(id) = request.get("id") else {
            continue;
        };
        if !(id.is_number() || id.is_string() || id.is_null()) {
            return Err("invalid automation receipt bridge request ID".into());
        }
        let mut reply = json!({"jsonrpc":"2.0", "id":id});
        let params = &request["params"];
        reply["result"] = match ipc::field(&request, "method") {
            "initialize" => {
                if !params.is_object() {
                    return Err("invalid automation receipt bridge initialization".into());
                }
                let version = match ipc::field(params, "protocolVersion") {
                    v @ ("2024-11-05" | "2025-03-26" | "2025-06-18" | "2025-11-25") => v,
                    _ => "2025-11-25",
                };
                json!({"protocolVersion":version,"capabilities":{"tools":{}},"serverInfo":{"name":"swarm_schedule","version":"1"}})
            }
            "ping" => json!({}),
            "tools/list" => json!({"tools":[{
                "name":"complete",
                "description":"Record that your scheduled automation finished. Post the closing result in chat before calling this tool.",
                "inputSchema":{"type":"object","additionalProperties":false,"required":["summary"],"properties":{
                    "summary":{"type":"string","maxLength":4096},
                    "links":{"type":"array","maxItems":16,"items":{"type":"string","maxLength":1024}}
                }}
            }]}),
            "tools/call" => {
                let body = serde_json::from_value::<Completion>(params["arguments"].clone());
                let (text, is_error) = match body {
                    Ok(c) if params["name"] == "complete" => match submit(&c.summary, &c.links) {
                        Ok(()) => ("Receipt recorded. Include its summary verbatim in your closing chat message.".to_owned(), false),
                        Err(err) => (err.to_string(), true),
                    },
                    _ => ("Unknown completion tool or invalid input.".into(), true),
                };
                json!({"content":[{"type":"text","text":text}],"isError":is_error})
            }
            _ => {
                reply["error"] = json!({"code":-32601,"message":"Method not found"});
                serde_json::to_writer(&mut output, &reply)?;
                output.write_all(b"\n")?;
                output.flush()?;
                continue;
            }
        };
        serde_json::to_writer(&mut output, &reply)?;
        output.write_all(b"\n")?;
        output.flush()?;
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use std::{cell::Cell, thread};

    #[test]
    fn restricted_codex_sets_native_authority_and_scoped_tool_only() {
        let path = Path::new("/tmp/test paths/a\"b");
        let flags = codex_policy("workspace-write", path, true).unwrap();
        for (index, value) in flags.iter().enumerate() {
            if index % 2 == 0 {
                assert_eq!(value, "-c");
            }
        }
        assert_eq!(flags[1], "sandbox_mode=\"workspace-write\"");
        assert!(
            flags
                .iter()
                .any(|arg| arg == "sandbox_workspace_write.network_access=false")
        );
        assert!(
            flags
                .last()
                .unwrap()
                .contains(r#"command="/tmp/test paths/a\"b""#)
        );
        assert_eq!(
            codex_policy("observe", path, false).unwrap(),
            [
                "-c",
                "sandbox_mode=\"read-only\"",
                "-c",
                "approval_policy=\"never\""
            ]
        );
        assert!(
            codex_policy("agent-default", path, false)
                .unwrap()
                .is_empty()
        );
        let default = codex_policy("agent-default", path, true).unwrap();
        assert!(
            !default
                .iter()
                .any(|s| s.contains("sandbox_") || s.contains("approval_policy"))
        );
    }

    #[test]
    fn stdio_receipt_exposes_one_tool_with_no_identity() {
        let lines = concat!(
            "{\"id\":1,\"method\":\"initialize\",\"params\":{\"protocolVersion\":\"2025-06-18\"}}\n",
            "{\"method\":\"notifications/initialized\"}\n",
            "{\"id\":2,\"method\":\"tools/list\"}\n",
            "{\"id\":3,\"method\":\"tools/call\",\"params\":{\"name\":\"complete\",\"arguments\":{\"summary\":\"fixture\",\"links\":[\"file:result\"]}}}\n",
            "{\"id\":4,\"method\":\"tools/call\",\"params\":{\"name\":\"reopen\",\"arguments\":{\"summary\":\"fixture\"}}}\n"
        );
        let mut out = Vec::new();
        let calls = Cell::new(0);
        serve_bridge(lines.as_bytes(), &mut out, |summary, links| {
            assert_eq!(summary, "fixture");
            assert_eq!(links, ["file:result"]);
            calls.set(calls.get() + 1);
            Ok(())
        })
        .unwrap();
        let replies: Vec<Value> = std::str::from_utf8(&out)
            .unwrap()
            .lines()
            .map(|s| serde_json::from_str(s).unwrap())
            .collect();
        assert_eq!(calls.get(), 1);
        assert_eq!(replies.len(), 4);
        assert_eq!(replies[0]["result"]["protocolVersion"], "2025-06-18");
        assert_eq!(replies[1]["result"]["tools"].as_array().unwrap().len(), 1);
        assert_eq!(replies[2]["result"]["isError"], false);
        assert_eq!(replies[3]["result"]["isError"], true);
        assert!(
            !String::from_utf8(out)
                .unwrap()
                .contains("SWARM_SCHEDULE_PROOF")
        );
    }

    #[test]
    fn receipt_listener_cannot_dispatch_owner_commands() {
        let (client, server) = UnixStream::pair().unwrap();
        let worker = thread::spawn(move || {
            handle_receipt(server, "ep-fixture", |_, _, _, _, _| {
                panic!("non-receipt reached the engine")
            })
            .unwrap();
        });
        let mut client = client;
        runtime::write_control(&mut client, &json!({"op":"hello","protocol_version":ipc::VERSION,"capabilities":["schedule", "owner-launch"]})).unwrap();
        let reply = runtime::read_control(&mut client).unwrap();
        assert_eq!(reply["capabilities"], json!(["schedule"]));
        runtime::write_control(
            &mut client,
            &json!({"op":"schedule_run","endpoint_id":"ep-fixture","schedule_id":"job"}),
        )
        .unwrap();
        assert_eq!(runtime::read_control(&mut client).unwrap()["op"], "error");
        worker.join().unwrap();
    }
}
