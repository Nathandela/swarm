//! Physical native-profile and rollout custody for an ordinary stopped Codex viewer.
use crate::ipc::field;
use crate::providers::codex_receipt;
use serde_json::{Value, json};
use sha2::{Digest, Sha256};
use std::fs::{self, File, OpenOptions};
use std::io::{self, BufRead, BufReader, Read};
use std::os::unix::fs::{MetadataExt, OpenOptionsExt};
use std::path::Path;
use time::OffsetDateTime;

fn owned(path: &Path) -> io::Result<File> {
    let file = OpenOptions::new()
        .read(true)
        .custom_flags(libc::O_NOFOLLOW | libc::O_NONBLOCK)
        .open(path)?;
    let stat = file.metadata()?;
    let current = path.symlink_metadata()?;
    // SAFETY: UID lookup has no process side effects.
    if !stat.is_file()
        || stat.uid() != unsafe { libc::geteuid() }
        || stat.nlink() != 1
        || stat.mode() & 0o022 != 0
        || path.canonicalize()? != path
        || (stat.dev(), stat.ino()) != (current.dev(), current.ino())
    {
        return Err(io::Error::other(
            "Codex native file is outside proven owner custody",
        ));
    }
    Ok(file)
}

fn config(path: &Path) -> io::Result<Value> {
    match owned(path) {
        Ok(file) => {
            let meta = file.metadata()?;
            let mut data = Vec::new();
            file.take(1024 * 1024 + 1).read_to_end(&mut data)?;
            if data.len() > 1024 * 1024 {
                return Err(io::Error::other("Codex native config exceeds proof limit"));
            }
            Ok(json!([
                meta.dev(),
                meta.ino(),
                format!("{:x}", Sha256::digest(data))
            ]))
        }
        Err(err) if err.kind() == io::ErrorKind::NotFound => Ok(Value::Null),
        Err(err) => Err(err),
    }
}

fn no_system_files(directory: &Path) -> io::Result<()> {
    match directory.symlink_metadata() {
        Ok(stat) if stat.is_dir() => {}
        Err(err) if err.kind() == io::ErrorKind::NotFound => return Ok(()),
        Err(err) => return Err(err),
        Ok(_) => {
            return Err(io::Error::other(
                "Codex system configuration directory is not physical",
            ));
        }
    }
    for name in ["config.toml", "requirements.toml", "managed_config.toml"] {
        match directory.join(name).symlink_metadata() {
            Ok(_) => {
                return Err(io::Error::other(
                    "Codex continuity cannot verify managed system configuration",
                ));
            }
            Err(err) if err.kind() == io::ErrorKind::NotFound => {}
            Err(err) => return Err(err),
        }
    }
    Ok(())
}

#[cfg(target_os = "macos")]
fn no_forced_preferences() -> io::Result<()> {
    use std::ffi::{c_char, c_void};
    type Cf = *const c_void;
    #[link(name = "CoreFoundation", kind = "framework")]
    unsafe extern "C" {
        fn CFStringCreateWithCString(allocator: Cf, value: *const c_char, encoding: u32) -> Cf;
        fn CFPreferencesAppSynchronize(app: Cf) -> u8;
        fn CFPreferencesAppValueIsForced(key: Cf, app: Cf) -> u8;
        fn CFRelease(value: Cf);
    }
    // SAFETY: The C literals are NUL-terminated; valid CF objects are checked and released.
    unsafe {
        let make = |value: &std::ffi::CStr| {
            CFStringCreateWithCString(std::ptr::null(), value.as_ptr(), 0x08000100)
        };
        let app = make(c"com.openai.codex");
        if app.is_null() {
            return Err(io::Error::other(
                "Codex managed preferences probe unavailable",
            ));
        }
        let result = (|| {
            if CFPreferencesAppSynchronize(app) == 0 {
                return Err(io::Error::other(
                    "Codex managed preferences probe unavailable",
                ));
            }
            for name in [c"config_toml_base64", c"requirements_toml_base64"] {
                let key = make(name);
                if key.is_null() {
                    return Err(io::Error::other(
                        "Codex managed preferences probe unavailable",
                    ));
                }
                let forced = CFPreferencesAppValueIsForced(key, app);
                CFRelease(key);
                if forced != 0 {
                    return Err(io::Error::other(
                        "Codex continuity cannot verify forced macOS configuration",
                    ));
                }
            }
            Ok(())
        })();
        CFRelease(app);
        result
    }
}

pub(crate) fn profile(env: &[(String, String)], cwd: &Path, options: &Value) -> io::Result<Value> {
    no_system_files(Path::new("/etc/codex"))?;
    #[cfg(target_os = "macos")]
    no_forced_preferences()?;
    let value = |key: &str| {
        env.iter()
            .rev()
            .find(|(name, _)| name == key)
            .map(|(_, value)| value.as_str())
            .unwrap_or_default()
    };
    let root = Path::new(value("HOME"));
    if !root.is_absolute()
        || root.canonicalize()? != root
        || !cwd.is_absolute()
        || cwd.canonicalize()? != cwd
    {
        return Err(io::Error::other(
            "Codex native profile or checkout is not physically rooted",
        ));
    }
    let explicit = value("CODEX_HOME");
    let home = if explicit.is_empty() {
        root.join(".codex")
    } else {
        super::codex::native_home(root, Path::new(explicit))?
    };
    let info = home.symlink_metadata()?;
    // SAFETY: UID lookup has no process side effects.
    if !info.is_dir() || info.uid() != unsafe { libc::geteuid() } || info.mode() & 0o022 != 0 {
        return Err(io::Error::other(
            "Codex native HOME is not a verified owner directory",
        ));
    }
    let key = value("OPENAI_API_KEY");
    let account = if !key.is_empty() {
        json!({"kind":"api-key","key_hash":format!("{:x}",Sha256::digest(key))})
    } else {
        let mut data = Vec::new();
        owned(&home.join("auth.json"))?
            .take(1024 * 1024 + 1)
            .read_to_end(&mut data)?;
        if data.len() > 1024 * 1024 {
            return Err(io::Error::other("Codex native auth exceeds proof limit"));
        }
        let auth: Value = serde_json::from_slice(&data)?;
        if auth["auth_mode"] == "apikey" && !field(&auth, "OPENAI_API_KEY").is_empty() {
            json!({"kind":"native-api-key","key_hash":format!("{:x}",Sha256::digest(field(&auth,"OPENAI_API_KEY")))})
        } else {
            let id = field(&auth["tokens"], "account_id");
            if auth["auth_mode"] != "chatgpt"
                || id.is_empty()
                || id.len() > 256
                || id.chars().any(char::is_control)
            {
                return Err(io::Error::other("unverified native Codex account"));
            }
            json!({"kind":"native","account":format!("{:x}",Sha256::digest(id))})
        }
    };
    let mut project = Vec::new();
    for ancestor in cwd.ancestors() {
        let metadata = ancestor.metadata()?;
        project.push(json!([
            ancestor,
            metadata.dev(),
            metadata.ino(),
            config(&ancestor.join(".codex/config.toml"))?
        ]));
    }
    Ok(
        json!({"home":root,"native_home":home,"native_home_id":[info.dev(),info.ino()],"path":value("PATH"),
        "config":config(&home.join("config.toml"))?,"project":project,"account":account,
        "system_policy":"absent-v1", "cwd_config":config(&cwd.join("config.toml"))?,
        "options":options.as_object().cloned().unwrap_or_default()}),
    )
}

pub(crate) fn rollout(home: &Path, cwd: &Path, thread: &str) -> io::Result<Value> {
    if !codex_receipt::canonical_thread(thread) || thread.as_bytes().get(14) != Some(&b'7') {
        return Err(io::Error::other(
            "Codex rollout requires a canonical UUIDv7 thread",
        ));
    }
    let compact = thread[..13].replace('-', "");
    let ms = u64::from_str_radix(&compact, 16).map_err(io::Error::other)?;
    let day = OffsetDateTime::from_unix_timestamp_nanos(i128::from(ms) * 1_000_000)
        .map_err(io::Error::other)?;
    if !home.is_absolute() || home.canonicalize()? != home {
        return Err(io::Error::other(
            "Codex rollout home is not physically rooted",
        ));
    }
    let mut rooted = home.to_owned();
    let year = format!("{:04}", day.year());
    let month = format!("{:02}", u8::from(day.month()));
    let date = format!("{:02}", day.day());
    for component in ["sessions", &year, &month, &date] {
        rooted.push(component);
        if !rooted.symlink_metadata()?.is_dir() || rooted.canonicalize()? != rooted {
            return Err(io::Error::other("Codex rollout directory is an alias"));
        }
    }
    let suffix = format!("-{thread}.jsonl");
    let mut result = None;
    for (index, entry) in fs::read_dir(&rooted)?.enumerate() {
        if index >= 2048 {
            return Err(io::Error::other(
                "Codex rollout day exceeds the bounded inventory",
            ));
        }
        let entry = entry?;
        if entry
            .file_name()
            .to_str()
            .is_some_and(|name| name.starts_with("rollout-") && name.ends_with(&suffix))
        {
            if result.is_some() {
                return Err(io::Error::other("ambiguous Codex native rollout"));
            }
            result = Some(entry.path());
        }
    }
    let path = result.ok_or_else(|| io::Error::other("exact Codex native rollout not found"))?;
    let file = owned(&path)?;
    let stat = file.metadata()?;
    let mut line = Vec::new();
    BufReader::new(file.take(1 << 20)).read_until(b'\n', &mut line)?;
    if line.last() != Some(&b'\n') {
        return Err(io::Error::other("incomplete Codex rollout header"));
    }
    let row: Value = serde_json::from_slice(&line)?;
    if row["type"] != "session_meta"
        || row["payload"]["id"] != thread
        || Path::new(field(&row["payload"], "cwd")).canonicalize()? != cwd
    {
        return Err(io::Error::other("Codex rollout root or checkout differs"));
    }
    Ok(json!({"path":path,"identity":[stat.dev(),stat.ino()]}))
}

pub(crate) fn initial_history(home: &Path, cwd: &Path, thread: &str) -> io::Result<Vec<Value>> {
    let anchor = rollout(home, cwd, thread)?;
    let file = owned(Path::new(field(&anchor, "path")))?;
    let stat = file.metadata()?;
    if anchor["identity"] != json!([stat.dev(), stat.ino()]) || stat.len() > 4 << 20 {
        return Err(io::Error::other(
            "Codex opening history is outside the bounded proof",
        ));
    }
    let mut output = Vec::new();
    let mut user = None;
    let mut assistant = None;
    let mut turn = String::new();
    let mut reader = BufReader::new(file.take(4 << 20));
    let mut line = Vec::new();
    while reader.read_until(b'\n', &mut line)? > 0 && line.last() == Some(&b'\n') {
        let row: Value = serde_json::from_slice(&line)?;
        line.clear();
        if row["type"] != "event_msg" {
            continue;
        }
        let event = &row["payload"];
        match field(event, "type") {
            "task_started" => {
                let next = field(event, "turn_id");
                let root = field(event, "root_turn_id");
                if !next.is_empty() && (root.is_empty() || root == next) {
                    turn = next.to_owned();
                    user = None;
                    assistant = None;
                }
            }
            "item_completed"
                if event["thread_id"] == thread && !turn.is_empty() && event["turn_id"] == turn =>
            {
                let item = &event["item"];
                let Some(parts) = item["content"].as_array() else {
                    continue;
                };
                if parts.len() != 1 {
                    continue;
                }
                let text = field(&parts[0], "text");
                if text.is_empty() || text.len() > 4096 {
                    continue;
                }
                match (field(item, "type"), field(&parts[0], "type")) {
                    ("UserMessage", "text") => {
                        user = Some(
                            json!({"kind":"user_message","status":"completed","text":text,"turn_id":turn}),
                        )
                    }
                    ("AgentMessage", "Text") => {
                        assistant = Some(
                            json!({"kind":"agent_message","status":"completed","text":text,"turn_id":turn}),
                        )
                    }
                    _ => {}
                }
            }
            "task_complete" if !turn.is_empty() && event["turn_id"] == turn => {
                if assistant
                    .as_ref()
                    .is_some_and(|reply| reply["text"] == event["last_agent_message"])
                {
                    output.extend(user.take().into_iter().chain(assistant.take()));
                }
                turn.clear();
                if output.len() >= 16 {
                    break;
                }
            }
            _ => {}
        }
    }
    Ok(output)
}

pub(crate) fn newer_or_same(current: &str, saved: &str) -> bool {
    crate::providers::claude::release(current)
        .zip(crate::providers::claude::release(saved))
        .is_some_and(|(cur, old)| cur >= old)
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::supervisor::runtime;

    #[test]
    fn rollout_checks_exact_cwd_id_and_file_identity() {
        let root = Path::new("/tmp")
            .canonicalize()
            .unwrap()
            .join(format!("codex-proof-{}", runtime::random_id().unwrap()));
        let home = root.join("home/.codex");
        let cwd = root.join("work");
        let folder = home.join("sessions/2026/11/26");
        runtime::private_dir(&folder).unwrap();
        runtime::private_dir(&cwd).unwrap();
        let id = "01a21552-fdc6-7e60-94f0-3311a05424eb";
        let file = folder.join(format!("rollout-2026-11-26T00-48-10-{id}.jsonl"));
        runtime::write_json(
            &file,
            &json!({"type":"session_meta","payload":{"cwd":cwd,"id":id}}),
        )
        .unwrap();
        assert_eq!(
            rollout(&home, &cwd, id).unwrap()["path"],
            file.to_str().unwrap()
        );
        assert!(rollout(&home, &root, id).is_err());
        let duplicate = folder.join(format!("rollout-2026-11-26T01-48-10-{id}.jsonl"));
        fs::copy(&file, &duplicate).unwrap();
        assert!(rollout(&home, &cwd, id).is_err());
        fs::remove_file(duplicate).unwrap();
        let saved = file.with_extension("saved");
        fs::rename(&file, &saved).unwrap();
        std::os::unix::fs::symlink(&saved, &file).unwrap();
        assert!(rollout(&home, &cwd, id).is_err());
        fs::remove_dir_all(root).unwrap();
    }
}
