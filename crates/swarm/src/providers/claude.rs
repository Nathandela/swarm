use serde_json::{Map, Value, json};
use sha2::{Digest, Sha256};
use std::io::{self, Read};

pub(crate) const EVENTS: &[&str] = &[
    "SessionStart",
    "Notification",
    "PermissionRequest",
    "PostToolUse",
    "PreToolUse",
    "Stop",
    "SubagentStart",
    "SubagentStop",
    "UserPromptSubmit",
];

pub(crate) fn version(output: &str) -> io::Result<&str> {
    let version = output
        .split_whitespace()
        .find_map(|word| {
            let word = word.strip_prefix('v').unwrap_or(word);
            if word.contains('.')
                && word
                    .split('.')
                    .all(|part| !part.is_empty() && part.bytes().all(|c| c.is_ascii_digit()))
            {
                Some(word)
            } else {
                None
            }
        })
        .ok_or_else(|| io::Error::other("unrecognized Claude Code version"))?;
    let release: Vec<u32> = version
        .split('.')
        .map(str::parse)
        .collect::<Result<_, _>>()
        .map_err(|_| io::Error::other("invalid Claude Code release version"))?;
    let bound = (
        release[0],
        release.get(1).copied().unwrap_or(0),
        release.get(2).copied().unwrap_or(0),
    );
    if !((2, 0, 0)..=(9999, 0, 0)).contains(&bound) {
        return Err(io::Error::other(format!(
            "Claude Code {version} is outside supported versions 2.0.0–9999.0.0"
        )));
    }
    Ok(version)
}

pub(crate) fn argv(label: &str, request: &Value) -> io::Result<Vec<String>> {
    let current = std::env::current_exe()?;
    let current = current
        .to_str()
        .ok_or_else(|| io::Error::other("Swarm path is not representable as UTF-8"))?;
    let hook = format!("'{}' hook", current.replace('\'', "'\\''"));
    let hooks: Map<_, _> = EVENTS
        .iter()
        .map(|event| {
            (
                (*event).to_owned(),
                json!([{"hooks":[{"type":"command","command":format!("{hook} {event}")}]}]),
            )
        })
        .collect();
    let settings = serde_json::to_string(&json!({"hooks": hooks}))?;
    let mut argv = vec!["--settings".to_owned(), settings];
    if !label.is_empty() {
        argv.extend(["--name".into(), label.to_owned()]);
    }
    let options = &request["options"];
    if let Some(model) = options["model"]
        .as_str()
        .filter(|m| !m.is_empty() && !m.starts_with('-'))
    {
        argv.extend(["--model".into(), model.to_owned()]);
    }
    if options["dangerously-skip-permissions"] == "true" {
        argv.push("--dangerously-skip-permissions".into());
    }
    if let Some(id) = request["claude_retained_conversation"].as_str() {
        if !canonical_id(id) || !crate::ipc::field(request, "initial_prompt").is_empty() {
            return Err(io::Error::other("invalid Claude resume binding"));
        }
        argv.extend(["--resume".into(), id.to_owned()]);
        return Ok(argv);
    }
    if let Some(prompt) = request["initial_prompt"].as_str().filter(|p| !p.is_empty()) {
        if prompt.starts_with('-') {
            argv.push("--".into());
        }
        argv.push(prompt.to_owned());
    }
    Ok(argv)
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn floor_version_and_option_arguments() {
        assert!(version("1.9.9 (Claude Code)").is_err());
        assert!(version("3.0.0-preview (Claude Code)").is_err());
        assert!(version("garbage").is_err());
        assert_eq!(version("v2.0.0 (Claude Code)").unwrap(), "2.0.0");
        let args = argv(
            "test name",
            &json!({
                "options": {"model":"--remote-control"},
                "initial_prompt":"--not-an-option\n東京",
            }),
        )
        .unwrap();
        assert_eq!(
            &args[2..],
            ["--name", "test name", "--", "--not-an-option\n東京"]
        );
        let settings: Value = serde_json::from_str(&args[1]).unwrap();
        assert_eq!(settings["hooks"].as_object().unwrap().len(), EVENTS.len());
        assert!(
            settings["hooks"]["PermissionRequest"][0]["hooks"][0]["command"]
                .as_str()
                .unwrap()
                .ends_with(" hook PermissionRequest")
        );
    }
}

#[derive(Default)]
pub(crate) struct Observation {
    parent_active: bool,
    idle_known: bool,
    interaction: &'static str,
    children: std::collections::BTreeSet<String>,
}

impl Observation {
    pub(crate) fn apply(&mut self, event: &str, payload: &Value) {
        let child = crate::ipc::field(payload, "agent_id");
        match event {
            "UserPromptSubmit" | "PreToolUse" | "PostToolUse" => {
                self.parent_active = true;
                self.interaction = "none";
            }
            "SubagentStart" if !child.is_empty() && self.children.len() < 256 => {
                self.children.insert(child.to_owned());
            }
            "SubagentStop" if !child.is_empty() => {
                self.children.remove(child);
            }
            "Stop" => {
                self.parent_active = false;
                self.idle_known = true;
                self.interaction = "none";
            }
            "PermissionRequest" => {
                self.parent_active = false;
                self.idle_known = true;
                self.interaction = "permission";
            }
            "Notification" => {
                let subtype = crate::ipc::field(payload, "notification_type");
                let interaction = match subtype {
                    "permission" | "permission_prompt" => "permission",
                    "idle" | "idle_prompt" => "none",
                    "prompt" => "prompt",
                    _ => return,
                };
                self.parent_active = false;
                self.idle_known = true;
                self.interaction = interaction;
            }
            _ => {}
        }
    }

    pub(crate) fn status(&self) -> Value {
        let turn = if self.parent_active || !self.children.is_empty() {
            "active"
        } else if self.idle_known {
            "idle"
        } else {
            "unknown"
        };
        let interaction = if self.interaction.is_empty() {
            "none"
        } else {
            self.interaction
        };
        let mut status = json!({"Process": "running", "Turn": turn, "Interaction": interaction});
        if matches!(interaction, "permission" | "prompt") && turn == "active" {
            status["Attention"] = json!(true);
        }
        status
    }
}

pub(crate) fn conversation_id(payload: &Value) -> Option<&str> {
    payload["session_id"].as_str().filter(|id| canonical_id(id))
}

pub(crate) fn canonical_id(id: &str) -> bool {
    id.len() == 36
        && id.bytes().enumerate().all(|(i, c)| {
            if matches!(i, 8 | 13 | 18 | 23) {
                c == b'-'
            } else {
                c.is_ascii_digit() || (b'a'..=b'f').contains(&c)
            }
        })
}

#[cfg(test)]
mod observation_tests {
    use super::*;

    #[test]
    fn late_child_exit_does_not_finish_a_new_parent_turn() {
        let mut observation = Observation::default();
        let child = json!({"agent_id":"child","turn":"idle"});
        observation.apply("SubagentStart", &child);
        observation.apply("PermissionRequest", &json!({}));
        assert_eq!(observation.status()["Attention"], true);
        observation.apply("Notification", &json!({"notification_type":"auth_success"}));
        assert_eq!(observation.status()["Interaction"], "permission");
        observation.apply("UserPromptSubmit", &json!({}));
        observation.apply("SubagentStop", &child);
        assert_eq!(observation.status()["Turn"], "active");
        observation.apply("Stop", &json!({}));
        assert_eq!(observation.status()["Turn"], "idle");
        assert!(conversation_id(&json!({"session_id":"not-a-real-id"})).is_none());
    }
}

pub(crate) fn scheduled(version: &str) -> bool {
    version == "2.1.329"
}

pub(crate) fn new_conversation() -> io::Result<String> {
    let mut bytes = [0; 16];
    std::fs::File::open("/dev/urandom")?.read_exact(&mut bytes)?;
    bytes[6] = (bytes[6] & 0x0f) | 0x40;
    bytes[8] = (bytes[8] & 0x3f) | 0x80;
    let hex: String = bytes.iter().map(|byte| format!("{byte:02x}")).collect();
    Ok(format!(
        "{}-{}-{}-{}-{}",
        &hex[..8],
        &hex[8..12],
        &hex[12..16],
        &hex[16..20],
        &hex[20..]
    ))
}

pub(crate) fn scheduled_text(text: &str) -> bool {
    !text.is_empty() && !text.chars().any(|c| c.is_control() && c != '\n')
}

pub(crate) fn text_hash(text: &str) -> String {
    format!("{:x}", Sha256::digest(text.as_bytes()))
}

pub(crate) fn scheduled_paste(prompt: &str) -> Option<&str> {
    // Interactive 2.1.329 wraps a bracketed multiline paste in an identified native block.
    let (id, body) = prompt
        .strip_prefix("\n\n<pasted_content id=\"")?
        .split_once("\">\n")?;
    if id.len() != 4
        || !id
            .bytes()
            .all(|c| c.is_ascii_digit() || (b'a'..=b'f').contains(&c))
    {
        return None;
    }
    body.strip_suffix(&format!("\n</pasted_content id=\"{id}\">\n"))
}

pub(crate) fn empty_composer(screen: &vt100::Screen) -> bool {
    composer(screen, "", "")
}

pub(crate) fn rendered_paste(screen: &vt100::Screen, text: &str) -> bool {
    let newlines = text.bytes().filter(|&byte| byte == b'\n').count();
    composer(
        screen,
        &format!("[Pasted text #1 +{newlines} lines]"),
        "paste again to expand",
    )
}

fn composer(screen: &vt100::Screen, draft: &str, footer: &str) -> bool {
    if !screen.bracketed_paste() || !screen.alternate_screen() {
        return false;
    }
    let (rows, cols) = screen.size();
    let (rows, cols) = (rows.get(), cols.get());
    let (row, col) = screen.cursor_position();
    if usize::from(col) != draft.len() + 2 || row < 1 || row.checked_add(3) != Some(rows) {
        return false;
    }
    let mut lines = screen.rows(0, cols).skip(usize::from(row - 1));
    let separator =
        |s: String| s.chars().count() == usize::from(cols) && s.chars().all(|c| c == '─');
    // The upper divider may include Claude's session name on the right.
    lines.next().is_some_and(|line| {
        line.ends_with('─') && line.chars().take_while(|c| *c == '─').count() >= 8
    }) && lines.next().is_some_and(|line| {
        line.strip_prefix('❯')
            .is_some_and(|body| body.trim() == draft)
    }) && lines.next().is_some_and(separator)
        && (footer.is_empty()
            || lines
                .next()
                .is_some_and(|line| line.trim_start().starts_with(footer)))
}

pub(crate) fn startup_attention(screen: &vt100::Screen) -> bool {
    let grid = screen.contents();
    [
        ("Yes, I trust this folder", "No, exit"),
        (
            "Do you want to use this API key?",
            "Detected a custom API key",
        ),
        ("Make auto mode your default permission mode?", "No, keep"),
    ]
    .iter()
    .any(|(heading, choice)| grid.contains(heading) && grid.contains(choice))
}

#[cfg(test)]
mod schedule_tests {
    use super::*;

    #[test]
    fn scheduled_composer_refuses_modal_or_owner_draft() {
        let mut parser = vt100::Parser::new(8.try_into().unwrap(), 20.try_into().unwrap(), 0);
        parser.process("\x1b[?1049h\x1b[?2004h\x1b[5;1H───────────── Test ─\x1b[6;1H❯  \x1b[7;1H────────────────────\x1b[6;3H".as_bytes());
        assert!(empty_composer(parser.screen()));
        parser.process(b"typed\x1b[6;3H");
        assert!(!empty_composer(parser.screen()));
        parser.process("\x1b[2J\x1b[6;1H❯ No, exit\x1b[6;3H".as_bytes());
        assert!(!empty_composer(parser.screen()));
        assert!(!scheduled_text("bad\x1b[201~"));
        assert!(scheduled_text("exact\nline"));
        assert_eq!(
            scheduled_paste(
                "\n\n<pasted_content id=\"abcd\">\nexact\nline\n</pasted_content id=\"abcd\">\n"
            ),
            Some("exact\nline")
        );
        assert_eq!(
            scheduled_paste(
                "\n\n<pasted_content id=\"abcd\">\nexact\nline\n</pasted_content id=\"acbd\">\n"
            ),
            None
        );
        assert!(canonical_id(&new_conversation().unwrap()));
    }
}

/// Retain proof of the native account and launch-path origin, never its credential bytes.
pub(crate) fn profile(
    env: &[(String, String)],
    supplied: &Value,
    options: &Value,
) -> io::Result<Value> {
    use std::os::unix::fs::MetadataExt;
    use std::path::Path;
    let env_value = |name: &str| {
        env.iter()
            .rev()
            .find(|(key, _)| key == name)
            .map(|(_, value)| value.as_str())
            .unwrap_or_default()
    };
    let unsupported = |key: &str| {
        matches!(
            key,
            "CLAUDE_CONFIG_DIR"
                | "ANTHROPIC_CONFIG_DIR"
                | "ANTHROPIC_AUTH_TOKEN"
                | "ANTHROPIC_BASE_URL"
                | "CLAUDE_SECURESTORAGE_CONFIG_DIR"
        ) || key.starts_with("CLAUDE_CODE_USE_")
            || key.starts_with("CLAUDE_CODE_GATEWAY_")
    };
    let unproven = if supplied.is_null() {
        std::env::vars_os()
            .any(|(key, value)| key.to_str().is_some_and(unsupported) && !value.is_empty())
    } else {
        supplied
            .as_array()
            .into_iter()
            .flatten()
            .filter_map(Value::as_str)
            .filter_map(|entry| entry.split_once('='))
            .any(|(key, value)| unsupported(key) && !value.is_empty())
    };
    if unproven {
        return Err(io::Error::other(
            "unqualified Claude profile or authority override",
        ));
    }
    let original = Path::new(env_value("HOME"));
    if !original.is_absolute() {
        return Err(io::Error::other("Claude HOME is missing"));
    }
    let home = original.canonicalize()?;
    let info = home.metadata()?;
    let settings = read_private_optional(&home.join(".claude/settings.json"))?;
    if dynamic_settings(&settings)
        || [
            "apiKeyHelper",
            "primaryApiKey",
            "awsAuthRefresh",
            "awsCredentialExport",
        ]
        .into_iter()
        .any(|key| !settings[key].is_null() && settings[key] != "")
    {
        return Err(io::Error::other(
            "unqualified dynamic Claude credential helper",
        ));
    }
    let account_file = read_private_optional(&home.join(".claude.json"))?;
    let account = &account_file["oauthAccount"];
    let account_id = account["accountUuid"].as_str().filter(|id| !id.is_empty());
    let organization = account["organizationUuid"].as_str().unwrap_or_default();
    let key = env_value("ANTHROPIC_API_KEY");
    let config_key = settings["env"]["ANTHROPIC_API_KEY"]
        .as_str()
        .or_else(|| settings["env"]["ANTHROPIC_AUTH_TOKEN"].as_str())
        .filter(|key| !key.is_empty());
    let origin = if !key.is_empty() {
        json!({"kind":"api-key","fingerprint":text_hash(key)})
    } else if let Some(key) = config_key {
        json!({"kind":"settings-key","fingerprint":text_hash(key)})
    } else if let Some(account_id) = account_id {
        json!({"kind":"oauth","fingerprint":text_hash(&json!([account_id,organization]).to_string())})
    } else {
        return Err(io::Error::other("Claude native account is not proved"));
    };
    Ok(json!({
        "home":home,"home_id":[info.dev(),info.ino()],"path":env_value("PATH"),
        "origin":origin,"settings":text_hash(&settings.to_string()),
        "options":options.as_object().cloned().unwrap_or_default(),
    }))
}

fn read_private_optional(path: &std::path::Path) -> io::Result<Value> {
    read_owned_config(path, false)
}

fn read_owned_config(path: &std::path::Path, allow_system: bool) -> io::Result<Value> {
    use std::fs::OpenOptions;
    use std::os::unix::fs::{MetadataExt, OpenOptionsExt};
    let file = match OpenOptions::new()
        .read(true)
        .custom_flags(libc::O_NOFOLLOW | libc::O_NONBLOCK)
        .open(path)
    {
        Ok(file) => file,
        Err(err) if err.kind() == io::ErrorKind::NotFound => return Ok(Value::Null),
        Err(err) => return Err(err),
    };
    let info = file.metadata()?;
    let final_info = path.symlink_metadata()?;
    if path.canonicalize()? != path
        || (info.dev(), info.ino()) != (final_info.dev(), final_info.ino())
    {
        return Err(io::Error::other(
            "Claude config path changed or uses an alias",
        ));
    }
    // SAFETY: geteuid does not modify process state.
    if !info.is_file()
        || (info.uid() != unsafe { libc::geteuid() } && !(allow_system && info.uid() == 0))
        || info.nlink() != 1
        || info.mode() & 0o022 != 0
    {
        return Err(io::Error::other("unsafe Claude native config ownership"));
    }
    let mut data = Vec::new();
    file.take(1024 * 1024 + 1).read_to_end(&mut data)?;
    if data.len() > 1024 * 1024 {
        return Err(io::Error::other(
            "Claude native config exceeds inspection limit",
        ));
    }
    let value: Value = serde_json::from_slice(&data)?;
    if !value.is_object() {
        return Err(io::Error::other("invalid Claude native config"));
    }
    Ok(value)
}

pub(crate) fn release(value: &str) -> Option<(u32, u32, u32)> {
    let parts: Vec<u32> = value
        .split('.')
        .map(str::parse)
        .collect::<Result<_, _>>()
        .ok()?;
    Some((
        *parts.first()?,
        *parts.get(1)?,
        parts.get(2).copied().unwrap_or_default(),
    ))
}

pub(crate) fn agent_view_settled(row: &Value) -> bool {
    matches!(
        crate::ipc::field(row, "state")
            .trim()
            .to_ascii_lowercase()
            .as_str(),
        "done" | "stopped" | "complete" | "completed" | "failed" | "cancelled" | "canceled"
    )
}

pub(crate) fn external_proof(
    executable: &std::path::Path,
    cwd: &std::path::Path,
    environment: &[(String, String)],
    home: &std::path::Path,
    id: &str,
    require_stopped: bool,
) -> io::Result<Value> {
    use std::os::unix::fs::MetadataExt;
    let rows = super::claude_agent_view(executable, cwd, environment, true)?;
    let mut matching = rows
        .into_iter()
        .filter(|row| row["kind"] == "background" && row["sessionId"] == id);
    let row = matching
        .next()
        .ok_or_else(|| io::Error::other("Claude background conversation is not registered"))?;
    if matching.next().is_some()
        || require_stopped && !agent_view_settled(&row)
        || std::path::Path::new(crate::ipc::field(&row, "cwd")).canonicalize()? != cwd
    {
        return Err(io::Error::other(
            "Claude background conversation is not an exact stopped source",
        ));
    }
    let (path, identity) = transcript(home, cwd, id)?;
    let stat = path.symlink_metadata()?;
    if identity != json!([stat.dev(), stat.ino()]) {
        return Err(io::Error::other("Claude external history changed"));
    }
    Ok(json!({"transcript":identity,"size":stat.len(),"mtime":[stat.mtime(),stat.mtime_nsec()]}))
}

pub(crate) fn transcript(
    home: &std::path::Path,
    cwd: &std::path::Path,
    id: &str,
) -> io::Result<(std::path::PathBuf, Value)> {
    use std::fs::{self, OpenOptions};
    use std::io::{BufRead, BufReader};
    use std::os::unix::fs::{MetadataExt, OpenOptionsExt};
    if !canonical_id(id) || !home.is_absolute() || !cwd.is_absolute() {
        return Err(io::Error::other(
            "unqualified Claude transcript coordinates",
        ));
    }
    let cwd_text = cwd
        .to_str()
        .ok_or_else(|| io::Error::other("Claude checkout is not UTF-8"))?;
    let project: String = cwd_text
        .chars()
        .map(|ch| if ch.is_ascii_alphanumeric() { ch } else { '-' })
        .collect();
    let mut parent = home.to_owned();
    for part in [".claude", "projects", &project] {
        parent.push(part);
        if !fs::symlink_metadata(&parent)?.is_dir() {
            return Err(io::Error::other(
                "Claude transcript directory is not an anchored directory",
            ));
        }
    }
    let path = parent.join(format!("{id}.jsonl"));
    let file = OpenOptions::new()
        .read(true)
        .custom_flags(libc::O_NOFOLLOW | libc::O_NONBLOCK)
        .open(&path)?;
    let stat = file.metadata()?;
    // SAFETY: geteuid has no process side effects.
    if !stat.is_file()
        || stat.uid() != unsafe { libc::geteuid() }
        || stat.nlink() != 1
        || stat.mode() & 0o022 != 0
        || path.canonicalize()? != path
    {
        return Err(io::Error::other(
            "Claude transcript is not a verified owner file",
        ));
    }
    let mut input = BufReader::new(file.take(1024 * 1024));
    let mut line = Vec::new();
    while input.read_until(b'\n', &mut line)? != 0 {
        if line.last() != Some(&b'\n') {
            break;
        }
        let value: Value = serde_json::from_slice(&line)?;
        line.clear();
        let thread = crate::ipc::field(&value, "sessionId");
        let location = crate::ipc::field(&value, "cwd");
        if !thread.is_empty() && !location.is_empty() && value["isSidechain"] != true {
            if thread != id || std::path::Path::new(location).canonicalize()? != cwd {
                return Err(io::Error::other(
                    "Claude transcript identity or checkout changed",
                ));
            }
            return Ok((path, json!([stat.dev(), stat.ino()])));
        }
    }
    Err(io::Error::other(
        "Claude transcript has no complete matching root record",
    ))
}

/// Bind only authority-neutral project and managed settings and verify their
/// physical ancestors. User settings are separately bound by profile().
fn dynamic_settings(settings: &Value) -> bool {
    ["hooks", "enabledPlugins", "extraKnownMarketplaces"]
        .into_iter()
        .any(|key| {
            !settings[key].is_null()
                && settings[key]
                    .as_object()
                    .is_none_or(|object| !object.is_empty())
        })
}

pub(crate) fn project_authority(
    home: &std::path::Path,
    cwd: &std::path::Path,
) -> io::Result<Value> {
    use std::os::unix::fs::MetadataExt;
    use std::path::Path;
    fn safe_directory(path: &Path) -> io::Result<Option<Value>> {
        match path.symlink_metadata() {
            Ok(info) if info.is_dir() && path.canonicalize()? == path => {
                Ok(Some(json!([info.dev(), info.ino()])))
            }
            Ok(_) => Err(io::Error::other(
                "Claude settings directory has an unqualified alias",
            )),
            Err(e) if e.kind() == io::ErrorKind::NotFound => Ok(None),
            Err(e) => Err(e),
        }
    }
    fn settings_directory(path: &Path) -> io::Result<Option<Value>> {
        let identity = safe_directory(path)?;
        if identity.is_some() {
            let info = path.symlink_metadata()?;
            // SAFETY: geteuid does not modify process state.
            if (info.uid() != 0 && info.uid() != unsafe { libc::geteuid() })
                || info.mode() & 0o022 != 0
                || identity != Some(json!([info.dev(), info.ino()]))
            {
                return Err(io::Error::other(
                    "Claude settings directory authority is unqualified",
                ));
            }
        }
        Ok(identity)
    }

    fn settings(path: &Path) -> io::Result<Value> {
        let source = read_owned_config(path, true)?;
        if source.is_null() {
            return Ok(Value::Null);
        }
        if dynamic_settings(&source)
            || [
                "apiKeyHelper",
                "primaryApiKey",
                "awsAuthRefresh",
                "awsCredentialExport",
                "forceLoginMethod",
                "forceLoginOrgUUID",
                "modelProviders",
            ]
            .into_iter()
            .any(|key| !source[key].is_null() && source[key] != "")
            || source.get("env").is_some_and(|env| {
                env.as_object().is_none_or(|vars| {
                    vars.keys().any(|key| {
                        key.starts_with("ANTHROPIC_")
                            || key.starts_with("AWS_")
                            || key.starts_with("GOOGLE_")
                            || key.starts_with("VERTEX_")
                            || key.starts_with("CLAUDE_CODE_HOST_")
                            || key.starts_with("CLAUDE_CODE_GATEWAY_")
                            || key.starts_with("CLAUDE_CODE_OAUTH_")
                            || key.starts_with("CLAUDE_CODE_USE_")
                            || matches!(
                                key.as_str(),
                                "CLAUDE_CONFIG_DIR"
                                    | "CLAUDE_SECURESTORAGE_CONFIG_DIR"
                                    | "CLAUDE_CODE_ACCOUNT_UUID"
                                    | "CLAUDE_CODE_API_KEY"
                                    | "CLAUDE_CODE_API_KEY_HELPER_TTL_MS"
                                    | "CLAUDE_CODE_FEDERATION_PROFILE"
                                    | "CLAUDE_CODE_PROFILE"
                                    | "CLAUDE_CODE_DEFAULT_PROFILE"
                                    | "CLAUDE_CODE_MANAGED_SETTINGS_PATH"
                                    | "CLAUDE_CODE_REMOTE_SETTINGS_PATH"
                                    | "HTTP_PROXY"
                                    | "HTTPS_PROXY"
                                    | "ALL_PROXY"
                                    | "NO_PROXY"
                                    | "http_proxy"
                                    | "https_proxy"
                                    | "all_proxy"
                                    | "no_proxy"
                            )
                    })
                })
            })
        {
            return Err(io::Error::other(
                "Claude project or managed settings change native authority",
            ));
        }
        Ok(json!(text_hash(&source.to_string())))
    }
    if !cwd.is_absolute()
        || cwd.canonicalize()? != cwd
        || !home.is_absolute()
        || home.canonicalize()? != home
    {
        return Err(io::Error::other(
            "Claude project origin is not a physical directory",
        ));
    }
    let mut records = Vec::new();
    for ancestor in cwd.ancestors() {
        let directory = ancestor.join(".claude");
        let identity = settings_directory(&directory)?;
        let user = home.join(".claude");
        // A user settings file at the exact HOME alias is already bound by
        // profile(); settings.local.json is never a user authority source.
        let standard = if directory == user {
            Value::Null
        } else {
            settings(&directory.join("settings.json"))?
        };
        let local = settings(&directory.join("settings.local.json"))?;
        records.push(json!([
            ancestor,
            safe_directory(ancestor)?,
            identity,
            standard,
            local
        ]));
    }
    let managed = if cfg!(target_os = "macos") {
        vec![
            "/private/etc/claude-code",
            "/Library/Application Support/ClaudeCode",
        ]
    } else {
        vec!["/etc/claude-code"]
    };
    for path in managed {
        let root = Path::new(path);
        let root_id = settings_directory(root)?;
        let directory = root.join("managed-settings.d");
        let children = settings_directory(&directory)?;
        let mut files = Vec::new();
        if children.is_some() {
            for (index, item) in std::fs::read_dir(&directory)?.enumerate() {
                let item = item?;
                if index == 256 {
                    return Err(io::Error::other("Claude managed settings exceed scope"));
                }
                if item.path().extension().is_some_and(|ext| ext == "json") {
                    let name = item.file_name().into_string().map_err(|_| {
                        io::Error::other("Claude managed setting has an unqualified file name")
                    })?;
                    files.push((name, settings(&item.path())?));
                }
            }
        }
        files.sort_by(|a, b| a.0.cmp(&b.0));
        records.push(json!([
            root,
            root_id,
            children,
            settings(&root.join("managed-settings.json"))?,
            files
        ]));
    }
    Ok(json!(text_hash(&json!(records).to_string())))
}

pub(crate) fn verify_project_authority(
    home: &std::path::Path,
    cwd: &std::path::Path,
    expected: &Value,
) -> io::Result<()> {
    if !expected.is_string() || project_authority(home, cwd)? != *expected {
        return Err(io::Error::other(
            "Claude project settings or ancestor identity changed",
        ));
    }
    Ok(())
}
