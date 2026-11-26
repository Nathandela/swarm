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

fn rollout_id(name: &str) -> Option<&str> {
    let name = name.strip_prefix("rollout-")?.strip_suffix(".jsonl")?;
    let id = name.get(20..56)?;
    if name.as_bytes().get(19) != Some(&b'-') || !codex_receipt::canonical_thread(id) {
        return None;
    }
    match name.len() {
        56 => Some(id),
        93 if name.as_bytes()[56] == b'_'
            && name.get(57..).is_some_and(codex_receipt::canonical_thread) =>
        {
            Some(id)
        }
        _ => None,
    }
}

pub(crate) fn discover_external(
    home: &Path,
    archived: bool,
    selected: &str,
) -> io::Result<Vec<Value>> {
    if !selected.is_empty() && !codex_receipt::canonical_thread(selected) {
        return Err(io::Error::other("invalid Codex conversation ID"));
    }
    if home
        .symlink_metadata()
        .is_err_and(|err| err.kind() == io::ErrorKind::NotFound)
        && home.is_absolute()
    {
        return Ok(Vec::new());
    }
    if !home.is_absolute() || home.canonicalize()? != home || !home.symlink_metadata()?.is_dir() {
        return Err(io::Error::other(
            "Codex discovery requires a rooted native home",
        ));
    }
    let mut items = std::collections::BTreeMap::new();
    let mut visited = 0;
    let mut bytes = 0;
    for (root, is_archived) in [("sessions", false), ("archived_sessions", true)] {
        if is_archived && !archived {
            continue;
        }
        let mut pending = vec![(home.join(root), 0)];
        while let Some((dir, depth)) = pending.pop() {
            match dir.symlink_metadata() {
                Err(err) if err.kind() == io::ErrorKind::NotFound && depth == 0 => continue,
                Ok(stat) if stat.is_dir() && dir.canonicalize()? == dir => {}
                _ => return Err(io::Error::other("unsafe Codex rollout directory")),
            }
            for child in fs::read_dir(&dir)? {
                let child = child?;
                visited += 1;
                if visited > 16_384 || depth > 8 {
                    return Err(io::Error::other(
                        "Codex rollout discovery exceeds inventory bounds",
                    ));
                }
                if child.file_type()?.is_dir() {
                    pending.push((child.path(), depth + 1));
                    continue;
                }
                let name = child.file_name();
                let Some(id) = name.to_str().and_then(rollout_id) else {
                    continue;
                };
                if !selected.is_empty() && id != selected {
                    continue;
                }
                let path = child.path();
                let file = owned(&path)?;
                let meta = file.metadata()?;
                let mut header = Vec::new();
                BufReader::new(file.take(64 * 1024 + 1)).read_until(b'\n', &mut header)?;
                bytes += header.len();
                if header.len() > 64 * 1024 || header.last() != Some(&b'\n') || bytes > 16 << 20 {
                    return Err(io::Error::other(
                        "Codex external rollout metadata exceeds safe bounds",
                    ));
                }
                let node: Value = serde_json::from_slice(&header)?;
                let info = &node["payload"];
                if node["type"] != "session_meta" || info["id"] != id || info["source"].is_null() {
                    return Err(io::Error::other("invalid Codex external rollout metadata"));
                }
                if info["source"] != "cli" {
                    continue;
                }
                let cwd = Path::new(field(info, "cwd"));
                if !cwd.is_absolute() || !cwd.is_dir() || cwd.canonicalize()? != cwd {
                    return Err(io::Error::other(
                        "Codex external rollout checkout is not physically rooted",
                    ));
                }
                let proof = json!({"id":id,"cwd":cwd,"archived":is_archived,
                    "version":info["cli_version"],"path":path,"identity":[meta.dev(),meta.ino()],
                    "mtime":[meta.mtime(),meta.mtime_nsec()],"size":meta.len(),
                    "header":format!("{:x}",Sha256::digest(&header))});
                if items.insert(id.to_owned(), proof).is_some() {
                    return Err(io::Error::other("ambiguous Codex external rollout"));
                }
            }
        }
    }
    Ok(items.into_values().collect())
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
    let mut result = None;
    for (index, entry) in fs::read_dir(&rooted)?.enumerate() {
        if index >= 2048 {
            return Err(io::Error::other(
                "Codex rollout day exceeds the bounded inventory",
            ));
        }
        let entry = entry?;
        if entry.file_name().to_str().and_then(rollout_id) == Some(thread) {
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

/// Reads one worker turn by its opening prompt or by an admitted native scope.
pub(crate) fn worker_turn(
    home: &Path,
    cwd: &Path,
    thread: &str,
    prompt: &str,
    target: Option<(&str, &str)>,
    budget: usize,
) -> io::Result<Option<Value>> {
    let anchor = rollout(home, cwd, thread)?;
    let path = Path::new(field(&anchor, "path"));
    let file = owned(path)?;
    let metadata = file.metadata()?;
    if anchor["identity"] != json!([metadata.dev(), metadata.ino()]) {
        return Err(io::Error::other("Codex worker rollout was replaced"));
    }
    // Keep the scan tied to the initially opened file and the observed byte window.
    let cap = 64 << 20;
    let mut reader = BufReader::new(file.take(metadata.len().min(cap)));
    let evidence = scan_worker_turn(&mut reader, thread, cwd, prompt, target, budget)?;
    if anchor != rollout(home, cwd, thread)? {
        return Err(io::Error::other("Codex worker rollout changed"));
    }
    if evidence.is_none() && metadata.len() >= cap {
        return Err(io::Error::other(
            "Codex worker evidence exceeds the proof limit",
        ));
    }
    Ok(evidence)
}

fn scan_worker_turn(
    reader: &mut impl BufRead,
    thread: &str,
    cwd: &Path,
    prompt: &str,
    target: Option<(&str, &str)>,
    budget: usize,
) -> io::Result<Option<Value>> {
    let invalid = || io::Error::other("invalid Codex worker rollout evidence");
    let mut bytes = String::new();
    let mut read = || -> io::Result<Option<Value>> {
        bytes.clear();
        (&mut *reader).take((2 << 20) + 1).read_line(&mut bytes)?;
        if bytes.len() > 2 << 20 {
            return Err(invalid());
        }
        if !bytes.ends_with('\n') {
            return Ok(None);
        }
        if !super::codex_context_guard::unique_keys(&bytes) {
            return Err(invalid());
        }
        Ok(Some(serde_json::from_str(&bytes)?))
    };
    let head = read()?.ok_or_else(invalid)?;
    if head["type"] != "session_meta"
        || head["payload"]["id"] != thread
        || head["payload"]["cwd"] != json!(cwd)
    {
        return Err(invalid());
    }
    let mut active = String::new();
    let mut matched = false;
    while let Some(row) = read()? {
        if row["type"] != "event_msg" {
            continue;
        }
        let p = &row["payload"];
        let id = field(p, "turn_id");
        let kind = field(p, "type");
        let selected = !active.is_empty() && target.is_none_or(|(turn, _)| active == turn);
        if p.get("thread_id").is_some_and(|v| v != thread) {
            return Err(invalid());
        }
        match kind {
            "task_started" => {
                if selected {
                    return Ok(None);
                }
                if !codex_receipt::canonical_thread(id) {
                    return Err(invalid());
                }
                active = id.to_owned();
            }
            "item_completed" | "user_message" if selected => {
                let item = &p["item"];
                let input_id = target.map(|(_, input)| input);
                let input = if kind == "user_message" {
                    let plain = ["images", "file_ids", "local_images", "audio"]
                        .iter()
                        .all(|key| {
                            p[*key].is_null() || p[*key].as_array().is_some_and(Vec::is_empty)
                        });
                    (plain && input_id.is_none_or(|input| p["client_id"] == input))
                        .then(|| field(p, "message"))
                } else if item["type"] == "UserMessage" {
                    item["content"]
                        .as_array()
                        .filter(|parts| parts.len() == 1)
                        .and_then(|parts| {
                            (id == active
                                && !field(item, "id").is_empty()
                                && input_id.is_none_or(|input| item["client_id"] == input)
                                && parts[0]["type"] == "text"
                                && (parts[0]["text_elements"].is_null()
                                    || parts[0]["text_elements"]
                                        .as_array()
                                        .is_some_and(Vec::is_empty)))
                            .then(|| field(&parts[0], "text"))
                        })
                } else {
                    continue;
                };
                if matched
                    || !id.is_empty() && id != active
                    || input
                        .is_none_or(|text| text.is_empty() || codex_receipt::hash(text) != prompt)
                {
                    return Ok(None);
                }
                matched = true;
            }
            "task_complete" | "turn_aborted" if selected => {
                if id != active || !matched {
                    return Ok(None);
                }
                let state = if kind == "turn_aborted" {
                    "interrupted"
                } else if p["error"].is_null() {
                    "completed"
                } else {
                    "failed"
                };
                let text = if state == "completed" {
                    field(p, "last_agent_message").trim()
                } else {
                    ""
                };
                let mut end = text.len().min(budget.min(4096));
                while !text.is_char_boundary(end) {
                    end -= 1;
                }
                return Ok(Some(json!({"turn_id":id,"state":state,"text":&text[..end],
                    "text_truncated":end != text.len()})));
            }
            _ => {}
        }
    }
    Ok(None)
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

pub(crate) fn relogin_proof(
    session: &Path,
    token: &str,
    cwd: &Path,
    environment: &[(String, String)],
    thread: &str,
) -> io::Result<Value> {
    use crate::supervisor::runtime;
    let reservation = runtime::read_json(&session.join("session.json"))?;
    let folder = cwd.metadata()?;
    let identity = json!([folder.dev(), folder.ino()]);
    if reservation["token"] != token
        || reservation["cwd"].as_str().map(Path::new) != Some(cwd)
        || reservation["cwd_identity"] != identity
        || cwd.canonicalize()? != cwd
        || !reservation["checkout"].is_null()
        || !reservation["manager"].is_null()
        || !reservation["mailbox"].is_null()
        || !reservation["schedule"].is_null()
    {
        return Err(io::Error::other(
            "original Codex checkout or worker scope changed",
        ));
    }
    match session.join("codex-thread-changed.json").symlink_metadata() {
        Err(error) if error.kind() == io::ErrorKind::NotFound => {}
        _ => {
            return Err(io::Error::other(
                "original Codex thread custody is ambiguous",
            ));
        }
    }
    let origin = runtime::read_json(&session.join("codex-profile.json"))?;
    let old = &origin["profile"];
    let current = profile(environment, cwd, &old["options"])?;
    let mut unchanged = old.clone();
    unchanged["account"] = current["account"].clone();
    if origin["token"] != token || unchanged != current || old["account"] == current["account"] {
        return Err(io::Error::other("Codex native origin or authority changed"));
    }
    let rollover = rollout(Path::new(field(old, "native_home")), cwd, thread)?;
    Ok(json!({
        "from":old["account"], "to":current["account"], "thread":thread, "rollout":rollover,
        "profile":current, "cwd_identity":identity,
    }))
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

#[cfg(test)]
mod worker_turn_tests {
    use super::*;

    const FIRST: &str = "019c0000-0000-7000-8000-000000000001";
    const SECOND: &str = "019c0000-0000-7000-8000-000000000002";
    const THREAD: &str = "019c0000-0000-7000-8000-000000000003";

    fn scan(events: &[Value], target: Option<(&str, &str)>) -> io::Result<Option<Value>> {
        let home = Path::new("/worker");
        let mut lines = format!(
            "{}\n",
            json!({"type":"session_meta","payload":{"id":THREAD,"cwd":home}})
        );
        for event in events {
            lines.push_str(&format!(
                "{}\n",
                json!({"type":"event_msg","payload":event})
            ));
        }
        scan_worker_turn(
            &mut lines.as_bytes(),
            THREAD,
            home,
            &codex_receipt::hash("Approved prompt"),
            target,
            4096,
        )
    }

    #[test]
    fn worker_evidence_only_reads_exact_opening_or_admitted_turn() -> io::Result<()> {
        let first = [
            json!({"type":"task_started","turn_id":FIRST}),
            json!({"type":"user_message","message":"Approved prompt"}),
            json!({"type":"task_complete","turn_id":FIRST,"last_agent_message":"Approved result"}),
        ];
        let second = [
            json!({"type":"task_started","turn_id":SECOND}),
            json!({"type":"item_completed","turn_id":SECOND,"item":{"id":"candidate","type":"UserMessage",
                    "client_id":"verified","content":[{"type":"text","text":"Approved prompt"}]}}),
            json!({"type":"task_complete","turn_id":SECOND,"last_agent_message":"Private result"}),
        ];
        let both = [first.clone(), second].concat();
        assert_eq!(scan(&both, None)?.unwrap()["text"], "Approved result");
        assert!(scan(&both, Some((SECOND, "wrong")))?.is_none());
        assert_eq!(
            scan(&both, Some((SECOND, "verified")))?.unwrap()["turn_id"],
            SECOND
        );
        assert!(scan(&first[..2], None)?.is_none());
        let mut steered = first.to_vec();
        steered.insert(
            2,
            json!({"type":"user_message","message":"Unrelated steering"}),
        );
        assert!(scan(&steered, None)?.is_none());
        let mut unrelated = both;
        unrelated[1]["message"] = json!("A different first prompt");
        assert!(scan(&unrelated, None)?.is_none());
        Ok(())
    }
}
