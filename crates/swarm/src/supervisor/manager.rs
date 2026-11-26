//! Owner-only admission of the active private personal dispatcher binding.
use super::{State, runtime};
use crate::ipc::field;
use serde_json::{Value, json};
use sha2::{Digest, Sha256};
use std::fs;
use std::io;
use std::os::unix::fs::{FileTypeExt, MetadataExt};
use std::path::Path;

pub(crate) const NATIVE_CLI_CAP: &str = "manager-native-cli";
pub(crate) const QUOTA_RETIRE_CAP: &str = "manager-quota-retire-v1";
pub(crate) const QUOTA_REPLACEMENT: &str = "provider_exhausted";

pub(crate) fn retirement_id(request: &Value) -> io::Result<String> {
    let mut binding = serde_json::Map::new();
    let supplied = &request["binding"];
    for key in [
        "session",
        "session_instance",
        "conversation_id",
        "provider",
        "native_session_id",
        "native_generation",
        "observation_generation",
        "login_epoch",
    ] {
        binding.insert(key.into(), supplied[key].clone());
    }
    let turn = &request["expected_turn"];
    let mut normalized = serde_json::Map::new();
    for key in [
        "native_input_id",
        "native_turn_id",
        "state",
        "reason",
        "external_input",
    ] {
        if key == "state" || key == "external_input" || !field(turn, key).is_empty() {
            normalized.insert(key.into(), turn[key].clone());
        }
    }
    let mut canonical = json!({});
    if !request["replacement"].is_null() {
        canonical["replacement"] = request["replacement"].clone();
    }
    canonical["binding"] = json!(binding);
    canonical["generation"] = request["generation"].clone();
    canonical["checkpoint"] = request["checkpoint"].clone();
    canonical["expected_turn"] = json!(normalized);
    Ok(format!(
        "manager-retire:{:x}",
        Sha256::digest(serde_json::to_vec(&canonical)?)
    ))
}

pub(super) fn launch_scope(state: &State, launch: &Value) -> io::Result<Value> {
    let mut scope = crate::discord::native_manager_enrollment(launch)?;
    if scope["launch"] != *launch
        || scope["supervisor_socket"] != state.socket.to_string_lossy().as_ref()
    {
        return Err(refusal(
            "manager launch differs from active personal enrollment",
        ));
    }
    let manager = &launch["manager"];
    let socket = Path::new(field(manager, "bridge_socket"));
    let auth = Path::new(field(manager, "auth_file"));
    let bridge = socket
        .parent()
        .ok_or_else(|| refusal("missing manager bridge directory"))?;
    let meta = fs::symlink_metadata(bridge)?;
    // SAFETY: geteuid takes no arguments.
    let uid = unsafe { libc::geteuid() };
    if !meta.is_dir()
        || meta.uid() != uid
        || meta.mode() & 0o777 != 0o700
        || fs::canonicalize(bridge)? != bridge
        || auth.parent() != Some(bridge)
    {
        return Err(refusal(
            "manager bridge directory lacks exact private ownership",
        ));
    }
    let endpoint = fs::symlink_metadata(socket)?;
    if !endpoint.file_type().is_socket()
        || endpoint.uid() != uid
        || endpoint.nlink() != 1
        || endpoint.mode() & 0o777 != 0o600
    {
        return Err(refusal(
            "manager bridge endpoint lacks exact private ownership",
        ));
    }
    let peer = std::os::unix::net::UnixStream::connect(socket)?;
    let bridge_pid = crate::supervisor::runtime::peer_pid(&peer)?;
    let bridge_birth = crate::supervisor::runtime::birth(bridge_pid)?;
    let current = fs::symlink_metadata(socket)?;
    if (current.dev(), current.ino()) != (endpoint.dev(), endpoint.ino()) {
        return Err(refusal("manager bridge endpoint changed during admission"));
    }
    scope["bridge_issuer"] = json!({
        "socket":[endpoint.dev(),endpoint.ino()],"pid":bridge_pid,"birth":bridge_birth
    });
    let credential = crate::manager_mcp::read_credential(auth)?;
    scope["credential_hash"] = json!(format!("{:x}", Sha256::digest(credential.as_bytes())));
    scope["endpoint_id"] = json!(state.endpoint);
    Ok(scope)
}

pub(crate) fn ambient_codex_account(scope: &Value) -> io::Result<()> {
    let home = Path::new(field(scope, "account_home"));
    let ambient_home = std::env::var_os("HOME");
    if ambient_home.as_deref().map(Path::new) != Some(home)
        || ["OPENAI_API_KEY", "CODEX_HOME"]
            .iter()
            .any(|key| std::env::var_os(key).is_some_and(|value| !value.is_empty()))
        || scope["account"] != account(home)?
    {
        return Err(refusal(
            "quota recovery requires its original ambient Codex account",
        ));
    }
    crate::providers::codex::refuse_unqualified_origin(&Value::Null)
}

pub(crate) fn account(home: &Path) -> io::Result<String> {
    let credentials = home.join(".codex").join("auth.json");
    let auth = runtime::read_json(&credentials)?;
    let identity = auth["tokens"]["account_id"]
        .as_str()
        .filter(|id| !id.is_empty() && id.len() <= 256 && !id.chars().any(char::is_control))
        .ok_or_else(|| refusal("manager requires a qualified native Codex account"))?;
    if auth["auth_mode"] != "chatgpt" {
        return Err(refusal("manager requires native Codex account custody"));
    }
    Ok(format!("{:x}", Sha256::digest(identity.as_bytes())))
}

pub(crate) fn mcp_args(scope: &Value, session: &str) -> io::Result<Vec<String>> {
    let manager = &scope["launch"]["manager"];
    let args = json!([
        "manager-mcp",
        manager["bridge_socket"],
        manager["auth_file"],
        manager["conversation_id"],
        session
    ]);
    let binary = std::env::current_exe()?;
    let native_cli = manager["native_cli"] == true;
    let mut transport = vec![
        "publish",
        "swarm_read_note",
        "swarm_reply",
        "swarm_read_attachment",
    ];
    if !native_cli {
        transport.extend([
            "swarm_create",
            "swarm_status",
            "swarm_list",
            "swarm_read",
            "swarm_send",
        ]);
    }
    if scope["launch"]["agent"] == "claude" {
        let mut server = json!({"mcpServers":{"swarm_voice":{"command":binary,"args":args}}});
        if native_cli {
            server["mcpServers"]["swarm_voice"]["env"] = json!({"SWARM_MANAGER_VM_CLI":"1"});
        }
        let allowed = transport
            .iter()
            .map(|name| format!("mcp__swarm_voice__{name}"))
            .collect::<Vec<_>>()
            .join(",");
        let mut flags = vec![
            "--strict-mcp-config".into(),
            "--mcp-config".into(),
            serde_json::to_string(&server)?,
            "--permission-mode".into(),
            "default".into(),
            format!("--allowedTools={allowed}"),
        ];
        if !native_cli {
            flags.extend(["--tools".into(), String::new()]);
        }
        return Ok(flags);
    }
    let command = serde_json::to_string(&binary)?;
    let authority = if native_cli {
        let approvals = transport
            .iter()
            .map(|tool| format!(r#"{tool}={{approval_mode="approve"}}"#))
            .collect::<Vec<_>>()
            .join(",");
        format!(r#",env={{SWARM_MANAGER_VM_CLI="1"}},tools={{{approvals}}}"#)
    } else {
        String::new()
    };
    let mut flags = Vec::new();
    if field(manager, "conversation_id").starts_with("discord-teammate-") {
        flags.extend([
            "-c".into(),
            "notice.hide_rate_limit_model_nudge=true".into(),
        ]);
    }
    flags.extend([
        "-c".into(),
        format!(
            "mcp_servers.swarm_voice={{command={command},args={args},required=true{authority}}}"
        ),
    ]);
    Ok(flags)
}

fn refusal(reason: &str) -> io::Error {
    io::Error::new(io::ErrorKind::InvalidInput, reason)
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn hides_model_nudge_for_both_codex_discord_manager_scopes() {
        let mut scope = json!({"launch":{"agent":"codex","manager":{
            "conversation_id":"discord-teammate-fixture","native_cli":false
        }}});
        let option = "notice.hide_rate_limit_model_nudge=true";
        for native in [false, true] {
            scope["launch"]["manager"]["native_cli"] = json!(native);
            let flags = mcp_args(&scope, "fixture").unwrap();
            assert_eq!(flags[..2], ["-c", option]);
            assert!(flags[3].starts_with("mcp_servers.swarm_voice="));
            assert_eq!(flags[3].contains("SWARM_MANAGER_VM_CLI"), native);
        }
        scope["launch"]["agent"] = json!("claude");
        assert!(
            !mcp_args(&scope, "fixture")
                .unwrap()
                .iter()
                .any(|flag| flag == option)
        );
        scope["launch"]["agent"] = json!("codex");
        scope["launch"]["manager"]["conversation_id"] = json!("ordinary");
        assert!(
            !mcp_args(&scope, "fixture")
                .unwrap()
                .iter()
                .any(|flag| flag == option)
        );
    }
}
