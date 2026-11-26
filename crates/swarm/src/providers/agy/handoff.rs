use crate::{
    ipc::field,
    providers::codex_receipt,
    supervisor::{runtime, session},
};
use rustix::fs::{Mode, OFlags, openat};
use serde::de::{IgnoredAny, MapAccess, Visitor};
use serde_json::{Value, json};
use sha2::{Digest, Sha256};
use std::{
    collections::BTreeSet,
    fmt, fs,
    io::{self, BufRead, BufReader, Read},
    os::unix::ffi::OsStrExt,
    os::unix::fs::MetadataExt,
    path::{Component, Path, PathBuf},
};

const MAX_RECORD: usize = 64 << 10;
const MAX_RECORDS: usize = 16;
const DIRECTORY: OFlags = OFlags::RDONLY
    .union(OFlags::DIRECTORY)
    .union(OFlags::NOFOLLOW)
    .union(OFlags::CLOEXEC);

fn invalid(reason: &str) -> io::Error {
    io::Error::other(format!("Antigravity hands-off: {reason}"))
}

fn inode(meta: &fs::Metadata) -> [u64; 2] {
    [meta.dev(), meta.ino()]
}

fn owner(meta: &fs::Metadata) -> bool {
    meta.uid() == unsafe { libc::geteuid() }
}

fn safe(meta: &fs::Metadata) -> bool {
    owner(meta) && meta.mode() & 0o022 == 0
}

fn open_transcript(home: &Path, id: &str) -> io::Result<(fs::File, PathBuf, Vec<[u64; 2]>)> {
    if !home.is_absolute() || !codex_receipt::canonical_thread(id) {
        return Err(invalid("unproved home or conversation"));
    }
    let base = fs::File::from(rustix::fs::open(home, DIRECTORY, Mode::empty())?);
    let base_info = base.metadata()?;
    if !safe(&base_info) || inode(&base_info) != inode(&home.symlink_metadata()?) {
        return Err(invalid("unsafe home"));
    }

    let alias = Path::new(".gemini");
    let link = rustix::fs::statat(&base, alias, rustix::fs::AtFlags::SYMLINK_NOFOLLOW)?;
    let target =
        if rustix::fs::FileType::from_raw_mode(link.st_mode) == rustix::fs::FileType::Symlink {
            let raw = rustix::fs::readlinkat(&base, alias, Vec::new())?;
            let path = Path::new(std::ffi::OsStr::from_bytes(raw.to_bytes()));
            let relative = path
                .strip_prefix(home)
                .map_err(|_| invalid("provider alias escapes home"))?;
            let parts: Vec<_> = relative.components().collect();
            if parts.is_empty()
                || parts.len() > 64
                || !parts
                    .iter()
                    .all(|part| matches!(part, Component::Normal(_)))
                || home.join(parts.iter().collect::<PathBuf>()).as_os_str() != path.as_os_str()
            {
                return Err(invalid(
                    "provider alias is not a clean strict home descendant",
                ));
            }
            relative.to_owned()
        } else {
            alias.to_owned()
        };
    let relative = target
        .join("antigravity-cli")
        .join("brain")
        .join(id)
        .join(".system_generated")
        .join("logs");
    let mut chain = vec![inode(&base_info), [link.st_dev as u64, link.st_ino as u64]];
    let mut dir = base.try_clone()?;
    // A private ancestor prevents other users from reaching a group-writable
    // provider directory even when its creator inherits a permissive umask.
    let mut private = base_info.mode() & 0o077 == 0;
    for part in relative.iter() {
        dir = fs::File::from(openat(&dir, part, DIRECTORY, Mode::empty())?);
        let meta = dir.metadata()?;
        if !owner(&meta) || !private && meta.mode() & 0o022 != 0 {
            return Err(invalid("unsafe provider directory"));
        }
        private |= meta.mode() & 0o077 == 0;
        chain.push(inode(&meta));
    }
    let file = fs::File::from(openat(
        &dir,
        "transcript.jsonl",
        OFlags::RDONLY | OFlags::CLOEXEC | OFlags::NOFOLLOW | OFlags::NONBLOCK,
        Mode::empty(),
    )?);
    let meta = file.metadata()?;
    if !meta.is_file() || !safe(&meta) || meta.nlink() != 1 {
        return Err(invalid("unsafe transcript"));
    }
    chain.push(inode(&meta));

    let current = rustix::fs::statat(&base, alias, rustix::fs::AtFlags::SYMLINK_NOFOLLOW)?;
    if (link.st_dev, link.st_ino) != (current.st_dev, current.st_ino)
        || target != alias
            && rustix::fs::readlinkat(&base, alias, Vec::new())?.to_bytes()
                != home.join(&target).as_os_str().as_encoded_bytes()
    {
        return Err(invalid("provider root changed"));
    }
    Ok((file, home.join(relative).join("transcript.jsonl"), chain))
}

fn complete_step(line: &[u8]) -> bool {
    struct Step;
    impl<'de> Visitor<'de> for Step {
        type Value = bool;
        fn expecting(&self, formatter: &mut fmt::Formatter<'_>) -> fmt::Result {
            formatter.write_str("an Antigravity transcript step")
        }
        fn visit_map<A: MapAccess<'de>>(self, mut map: A) -> Result<bool, A::Error> {
            let mut keys = BTreeSet::new();
            let (mut index, mut kind, mut created) = (false, false, false);
            while let Some(key) = map.next_key::<String>()? {
                match key.as_str() {
                    "step_index" => {
                        index = map.next_value::<Value>()?.as_i64().is_some_and(|v| v >= 0)
                    }
                    "type" => {
                        kind = map
                            .next_value::<Value>()?
                            .as_str()
                            .is_some_and(|v| !v.is_empty())
                    }
                    "created_at" => {
                        created = map
                            .next_value::<Value>()?
                            .as_str()
                            .is_some_and(|v| !v.is_empty())
                    }
                    _ => {
                        map.next_value::<IgnoredAny>()?;
                    }
                }
                if !keys.insert(key) {
                    return Err(serde::de::Error::custom("duplicate transcript key"));
                }
            }
            Ok(index && kind && created)
        }
    }
    use serde::Deserializer;
    let mut decoder = serde_json::Deserializer::from_slice(line);
    decoder
        .deserialize_map(Step)
        .is_ok_and(|valid| valid && decoder.end().is_ok())
}

fn prefix(file: fs::File) -> io::Result<String> {
    let mut reader = BufReader::new(file);
    let mut line = Vec::new();
    let mut digest = Sha256::new();
    for _ in 0..MAX_RECORDS {
        line.clear();
        (&mut reader)
            .take((MAX_RECORD + 1) as u64)
            .read_until(b'\n', &mut line)?;
        if line.len() > MAX_RECORD {
            return Err(invalid("transcript record exceeds bound"));
        }
        if line.last() != Some(&b'\n') {
            break;
        }
        digest.update(&line);
        if complete_step(&line) {
            return Ok(format!("{:x}", digest.finalize()));
        }
    }
    Err(invalid("no complete native transcript step"))
}

fn transcript(home: &Path, id: &str) -> io::Result<(PathBuf, Value)> {
    let (file, path, chain) = open_transcript(home, id)?;
    let evidence = prefix(file)?;
    let (current, current_path, current_chain) = open_transcript(home, id)?;
    if path != current_path || chain != current_chain || evidence != prefix(current)? {
        return Err(invalid("transcript changed during evidence read"));
    }
    Ok((path, json!({"chain":chain,"prefix_sha256":evidence})))
}

pub(crate) fn reserve_home(env: &[(String, String)]) -> Option<Value> {
    let worker_home = env.iter().rev().find(|(key, _)| key == "HOME")?;
    let daemon_home = std::env::var_os("HOME").map(PathBuf::from)?;
    if !daemon_home.is_absolute() || daemon_home != Path::new(&worker_home.1) {
        return None;
    }
    let metadata = daemon_home.symlink_metadata().ok()?;
    (metadata.is_dir() && safe(&metadata))
        .then(|| json!({"path":daemon_home, "identity":inode(&metadata)}))
}

fn original_home(meta: &Value) -> io::Result<(&Path, [u64; 2])> {
    // Sessions reserved before the physical stamp used the saved private launch environment.
    let recorded = &meta["agy_source_home"];
    let home = if recorded.is_object() {
        recorded["path"].as_str()
    } else if let Some(home) = meta.get("agy_source_home") {
        home.as_str()
    } else {
        meta["maintenance_env"].as_array().and_then(|env| {
            env.iter()
                .rev()
                .filter_map(Value::as_str)
                .find_map(|item| item.strip_prefix("HOME="))
        })
    };
    let path = home
        .map(Path::new)
        .filter(|path| path.is_absolute())
        .ok_or_else(|| invalid("original source home is unproved"))?;
    let metadata = path.symlink_metadata()?;
    let identity = inode(&metadata);
    if !metadata.is_dir()
        || !safe(&metadata)
        || recorded.is_object() && recorded["identity"] != json!(identity)
    {
        return Err(invalid("original source home changed"));
    }
    Ok((path, identity))
}

fn coordinates(dir: &Path) -> io::Result<Value> {
    let directory = dir.symlink_metadata()?;
    if !directory.is_dir() || !safe(&directory) || directory.mode() & 0o077 != 0 {
        return Err(invalid("source reservation is not private"));
    }
    let meta = runtime::read_json(&dir.join("session.json"))?;
    let (home, home_identity) = original_home(&meta)?;
    let id = field(&meta, "id");
    let token = field(&meta, "token");
    if dir.file_name().and_then(|name| name.to_str()) != Some(id)
        || token.is_empty()
        || meta["provider"]["name"] != "agy"
        || meta["provider"]["mode"] != "classic"
        || !["manager", "schedule", "worker_discussion"]
            .iter()
            .all(|key| meta[*key].is_null())
        || !field(&meta, "role").is_empty()
    {
        return Err(invalid("source is not an ordinary Antigravity session"));
    }
    let observation = runtime::read_json(&dir.join("observation.json"))?;
    let conversation = field(&observation, "conversation_id");
    if observation["token"] != token || !codex_receipt::canonical_thread(conversation) {
        return Err(invalid("source conversation is unproved"));
    }
    let repo = Path::new(field(&meta, "cwd"));
    let repo_info = repo.metadata()?;
    if !repo.is_absolute()
        || repo.canonicalize()? != repo
        || !repo_info.is_dir()
        || meta["cwd_identity"] != json!(inode(&repo_info))
    {
        return Err(invalid("source project changed"));
    }
    let cwd = if meta["checkout"].is_null() {
        repo
    } else {
        crate::workspace::verify(repo, id, &meta["checkout"])?;
        Path::new(field(&meta["checkout"], "path"))
    };
    let info = cwd.metadata()?;
    if !info.is_dir() || cwd.canonicalize()? != cwd {
        return Err(invalid("source checkout changed"));
    }
    Ok(
        json!({"kind":"agy","source_dir":dir,"instance":codex_receipt::instance(token),
        "cwd":cwd,"cwd_identity":inode(&info),"project_identity":inode(&repo_info),
        "checkout":meta["checkout"],"home":home,"home_identity":home_identity,
        "source_identity":inode(&directory),"conversation":conversation}),
    )
}

pub(crate) fn source(dir: &Path) -> io::Result<Value> {
    let mut proof = coordinates(dir)?;
    let (path, identity) = transcript(
        Path::new(field(&proof, "home")),
        field(&proof, "conversation"),
    )?;
    if identity["chain"][0] != proof["home_identity"] || coordinates(dir)? != proof {
        return Err(invalid("source identity changed during transcript read"));
    }
    proof["transcript"] = json!(path);
    proof["identity"] = identity;
    Ok(proof)
}

pub(crate) fn verify(dir: &Path, proof: &Value) -> io::Result<()> {
    let meta = runtime::read_json(&dir.join("session.json"))?;
    session::agy_handoff::verify_owner(dir, field(&meta, "token"))?;
    if source(dir)? != *proof {
        return Err(invalid("source changed after handoff admission"));
    }
    Ok(())
}

#[cfg(test)]
mod tests {
    use super::*;
    use std::os::unix::fs::PermissionsExt;

    struct Scratch(PathBuf);

    impl Scratch {
        fn new() -> io::Result<Self> {
            let path = std::env::temp_dir()
                .canonicalize()?
                .join(runtime::random_id()?);
            runtime::private_dir(&path)?;
            Ok(Self(path))
        }
    }

    impl Drop for Scratch {
        fn drop(&mut self) {
            let _ = fs::remove_dir_all(&self.0);
        }
    }

    const ID: &str = "fb5e3e02-e5ef-4d25-b398-aead20366441";
    const STEP: &str =
        "{\"step_index\":0,\"type\":\"USER_INPUT\",\"created_at\":\"2026-11-16T18:39:25Z\"}\n";

    fn log_at(home: &Path) -> PathBuf {
        home.join(".gemini/antigravity-cli/brain")
            .join(ID)
            .join(".system_generated/logs/transcript.jsonl")
    }

    #[test]
    fn bounds_native_step_evidence_and_transcript_path() -> io::Result<()> {
        let root = Scratch::new()?;
        let home = root.0.join("home");
        fs::create_dir(&home)?;
        let path = log_at(&home);
        fs::create_dir_all(path.parent().unwrap())?;
        let duplicate = STEP.replacen("\"step_index\":0", "\"step_index\":-1,\"step_index\":0", 1);
        let duplicate_unknown = STEP.replacen(
            "\"step_index\":0",
            "\"trace\":null,\"trace\":null,\"step_index\":0",
            1,
        );
        for raw in [
            String::new(),
            duplicate,
            duplicate_unknown,
            STEP.trim_end().into(),
            STEP.replace(":0", ":0.0"),
            STEP.replace(":0", ":-1"),
            STEP.replace("USER_INPUT", ""),
            "{}\n".repeat(MAX_RECORDS) + STEP,
            " ".repeat(MAX_RECORD) + "\n" + STEP,
        ] {
            fs::write(&path, raw)?;
            assert!(transcript(&home, ID).is_err());
        }
        fs::write(&path, "{}\n".repeat(MAX_RECORDS - 1) + STEP + "{partial")?;
        let (found, _) = transcript(&home, ID)?;
        assert_eq!(found, path);
        fs::set_permissions(&home, fs::Permissions::from_mode(0o755))?;
        fs::set_permissions(home.join(".gemini"), fs::Permissions::from_mode(0o775))?;
        assert!(transcript(&home, ID).is_err());
        fs::set_permissions(&home, fs::Permissions::from_mode(0o700))?;
        assert_eq!(transcript(&home, ID)?.0, path);
        fs::set_permissions(home.join(".gemini"), fs::Permissions::from_mode(0o755))?;

        let real = home.join("private/native");
        fs::create_dir_all(real.parent().unwrap())?;
        fs::rename(home.join(".gemini"), &real)?;
        std::os::unix::fs::symlink(&real, home.join(".gemini"))?;
        let target = real.join(path.strip_prefix(home.join(".gemini")).unwrap());
        assert_eq!(transcript(&home, ID)?.0, target);
        let other = root.0.join("other");
        fs::rename(&real, &other)?;
        fs::remove_file(home.join(".gemini"))?;
        std::os::unix::fs::symlink(&other, home.join(".gemini"))?;
        assert!(transcript(&home, ID).is_err());
        fs::remove_file(home.join(".gemini"))?;
        fs::rename(&other, home.join(".gemini"))?;
        let moved = path.with_file_name("moved");
        fs::rename(&path, &moved)?;
        std::os::unix::fs::symlink(&moved, &path)?;
        assert!(transcript(&home, ID).is_err());
        fs::remove_file(&path)?;
        fs::hard_link(&moved, &path)?;
        assert!(transcript(&home, ID).is_err());
        fs::remove_file(&path)?;
        fs::rename(&moved, &path)?;
        let logs = path.parent().unwrap();
        let prior_logs = logs.with_file_name("prior-logs");
        fs::rename(logs, &prior_logs)?;
        std::os::unix::fs::symlink(prior_logs, logs)?;
        assert!(transcript(&home, ID).is_err());
        Ok(())
    }

    #[test]
    fn reopens_original_home_project_instance_and_transcript_at_native_exec() -> io::Result<()> {
        let root = Scratch::new()?;
        let home = root.0.join("source-home");
        let forged = root.0.join("target-home");
        let project = root.0.join("project");
        let session = root.0.join("source");
        for directory in [&home, &forged, &project, &session] {
            fs::create_dir(directory)?;
            fs::set_permissions(directory, fs::Permissions::from_mode(0o700))?;
        }
        for home in [&home, &forged] {
            let path = log_at(home);
            fs::create_dir_all(path.parent().unwrap())?;
            fs::write(path, STEP)?;
        }
        let token = "session-instance";
        let mut metadata = json!({
            "id":"source","token":token,"cwd":project,"cwd_identity":inode(&project.metadata()?),
            "agy_source_home":{"path":home,"identity":inode(&home.metadata()?)},"provider":{"name":"agy","mode":"classic"}
        });
        let meta_path = session.join("session.json");
        runtime::write_json(&meta_path, &metadata)?;
        let obs_path = session.join("observation.json");
        runtime::write_json(&obs_path, &json!({"token":token,"conversation_id":ID}))?;
        runtime::write_json(
            &session.join("owner.json"),
            &json!({"token":token,"pid":std::process::id(),"birth":1}),
        )?;
        runtime::write_json(&session.join("exit.json"), &json!({"token":token,"code":0}))?;

        let proof = source(&session)?;
        verify(&session, &proof)?;
        let prior_home = home.with_file_name("prior-home");
        fs::rename(&home, &prior_home)?;
        fs::rename(&forged, &home)?;
        assert!(
            source(&session).is_err(),
            "first offer trusted a replacement HOME"
        );
        fs::rename(&home, &forged)?;
        fs::rename(prior_home, &home)?;

        let mut supplied = proof.clone();
        supplied["home"] = json!(forged);
        supplied["transcript"] = json!(log_at(&forged));
        assert!(verify(&session, &supplied).is_err());
        let transcript = log_at(&home);
        fs::write(&transcript, format!("{STEP}{{partial"))?;
        verify(&session, &proof)?;
        fs::write(&transcript, STEP.replace(":0", ":1"))?;
        assert!(verify(&session, &proof).is_err());
        fs::write(&transcript, STEP)?;
        fs::rename(&transcript, transcript.with_file_name("previous"))?;
        fs::write(&transcript, STEP)?;
        assert!(verify(&session, &proof).is_err());

        let proof = source(&session)?;
        let prior_session = session.with_file_name("prior-reservation");
        fs::rename(&session, &prior_session)?;
        runtime::private_dir(&session)?;
        for name in [
            "owner.json",
            "exit.json",
            "session.json",
            "observation.json",
        ] {
            fs::copy(prior_session.join(name), session.join(name))?;
        }
        assert!(verify(&session, &proof).is_err());
        fs::remove_dir_all(&session)?;
        fs::rename(prior_session, &session)?;

        let old_project = project.with_file_name("prior-project");
        fs::rename(&project, &old_project)?;
        fs::create_dir(&project)?;
        assert!(verify(&session, &proof).is_err());
        fs::remove_dir(&project)?;
        fs::rename(old_project, &project)?;
        metadata["token"] = json!("replacement");
        runtime::write_json(&meta_path, &metadata)?;
        assert!(verify(&session, &proof).is_err());
        Ok(())
    }
}
