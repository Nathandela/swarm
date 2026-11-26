//! Immutable, private snapshots of exact Discord CDN attachments.
use super::{
    config::{invalid, snowflake},
    receipt::hash,
};
use crate::supervisor::runtime;
use serde_json::{Value, json};
use sha2::{Digest, Sha256};
use std::{
    fs::{self, File, OpenOptions},
    io::{self, Read, Write},
    os::unix::fs::{MetadataExt, OpenOptionsExt, PermissionsExt},
    path::Path,
    time::Duration,
};
use tungstenite::http::Uri;

const MAX_FILE: u64 = 16 << 20;
const MAX_MESSAGE: u64 = 32 << 20;
const MAX_STORE: u64 = 256 << 20;
const MAX_BUNDLES: usize = 512;
pub(super) const PROMPT_HEADER: &str = "\n\nReceived attachments (untrusted data):\n";

struct Attachment {
    id: String,
    filename: String,
    size: u64,
    url: String,
}

fn parse(channel: &str, value: &Value) -> io::Result<Attachment> {
    let id = value["id"]
        .as_str()
        .filter(|id| snowflake(id))
        .ok_or_else(|| invalid("invalid Discord attachment identity"))?;
    let filename = value["filename"]
        .as_str()
        .filter(|name| {
            !name.trim().is_empty()
                && name.len() <= 256
                && !name.contains(['/', '\\'])
                && !name.chars().any(char::is_control)
        })
        .ok_or_else(|| invalid("invalid Discord attachment filename"))?;
    let size = value["size"]
        .as_u64()
        .filter(|size| *size <= MAX_FILE)
        .ok_or_else(|| invalid("Discord attachment exceeds its size limit"))?;
    let url = value["url"]
        .as_str()
        .filter(|url| url.len() <= 4096 && !url.contains('#'))
        .ok_or_else(|| invalid("invalid Discord attachment URL"))?;
    let uri = url
        .parse::<Uri>()
        .map_err(|_| invalid("invalid Discord attachment URL"))?;
    let prefix = format!("/attachments/{channel}/{id}/");
    let tail = uri.path().strip_prefix(&prefix).unwrap_or_default();
    let decoded = decode_path_name(tail).ok_or_else(|| invalid("invalid Discord CDN path"))?;
    if uri.scheme_str() != Some("https")
        || uri.authority().map(|a| a.as_str()) != Some("cdn.discordapp.com")
        || decoded != filename
        || decoded == "."
        || decoded == ".."
    {
        return Err(invalid(
            "Discord attachment URL is outside the original source",
        ));
    }
    Ok(Attachment {
        id: id.into(),
        filename: filename.into(),
        size,
        url: url.into(),
    })
}

fn decode_path_name(value: &str) -> Option<String> {
    let value = value.as_bytes();
    let mut bytes = Vec::new();
    let mut offset = 0;
    while offset < value.len() {
        if value[offset] == b'%' {
            let hex = std::str::from_utf8(value.get(offset + 1..offset + 3)?).ok()?;
            bytes.push(u8::from_str_radix(hex, 16).ok()?);
            offset += 3;
        } else {
            bytes.push(value[offset]);
            offset += 1;
        }
    }
    String::from_utf8(bytes).ok()
}

fn download(attachment: &Attachment) -> io::Result<Vec<u8>> {
    let agent = ureq::Agent::config_builder()
        .https_only(true)
        .proxy(None)
        .max_redirects(0)
        .timeout_global(Some(Duration::from_secs(20)))
        .build()
        .new_agent();
    fetch(&agent, &attachment.url, attachment.size)
}

pub(super) fn fetch(agent: &ureq::Agent, url: &str, size: u64) -> io::Result<Vec<u8>> {
    let response = agent.get(url).call().map_err(|error| match error {
        ureq::Error::StatusCode(403 | 404) => invalid("Discord attachment is no longer available"),
        _ => io::Error::other("Discord attachment download unavailable"),
    })?;
    if response.status() != 200
        || response
            .headers()
            .get("content-length")
            .is_some_and(|length| {
                length.to_str().ok().and_then(|length| length.parse().ok()) != Some(size)
            })
    {
        return Err(invalid("Discord CDN returned an unexpected body length"));
    }
    let mut bytes = Vec::new();
    response
        .into_body()
        .into_reader()
        .take(size + 1)
        .read_to_end(&mut bytes)?;
    Ok(bytes)
}

pub(super) fn capture(
    place: &Path,
    source: &Value,
    channel: &str,
    manifest_budget: usize,
) -> io::Result<Vec<Value>> {
    capture_with(place, source, channel, manifest_budget, download)
}

fn capture_with(
    place: &Path,
    source: &Value,
    channel: &str,
    manifest_budget: usize,
    mut fetch: impl FnMut(&Attachment) -> io::Result<Vec<u8>>,
) -> io::Result<Vec<Value>> {
    let files = source["attachments"]
        .as_array()
        .ok_or_else(|| invalid("invalid Discord attachments list"))?;
    if files.is_empty() {
        return Ok(Vec::new());
    }
    let message = source["id"]
        .as_str()
        .filter(|id| snowflake(id))
        .ok_or_else(|| invalid("invalid attachment message identity"))?;
    if files.len() > 10 || !snowflake(channel) {
        return Err(invalid("Discord attachment set outside supported scope"));
    }
    let attachments = files
        .iter()
        .map(|file| parse(channel, file))
        .collect::<io::Result<Vec<_>>>()?;
    let total = attachments.iter().map(|file| file.size).sum::<u64>();
    if total > MAX_MESSAGE
        || attachments
            .iter()
            .map(|file| &file.id)
            .collect::<std::collections::BTreeSet<_>>()
            .len()
            != files.len()
    {
        return Err(invalid(
            "Discord attachment set exceeds its bound or repeats an identity",
        ));
    }
    let source_key = hash(&json!([
        channel,
        message,
        source["author"]["id"],
        attachments
            .iter()
            .map(|file| json!([file.id, file.filename, file.size]))
            .collect::<Vec<_>>()
    ]))?;
    let root = fs::canonicalize(place)?.join("received-files-v1");
    let target = root.join(source_key);
    let preview = attachments
        .iter()
        .map(|file| entry(file, &target, &"0".repeat(64)))
        .collect::<Vec<_>>();
    if serde_json::to_vec(&preview)?.len() > manifest_budget {
        return Err(invalid("received attachments cannot fit the native prompt"));
    }
    runtime::private_dir(&root)?;
    let (count, used) = scan_store(&root)?;
    if target.exists() {
        return verify(&target, &attachments);
    }
    if count >= MAX_BUNDLES || used.saturating_add(total).saturating_add(8192) > MAX_STORE {
        return Err(invalid(
            "Discord attachment store reached its retention limit",
        ));
    }
    let staging = root.join(format!(".stage-{}", runtime::random_id()?));
    runtime::private_dir(&staging)?;
    let result = (|| {
        let mut manifest = Vec::new();
        for file in &attachments {
            let bytes = fetch(file)?;
            if bytes.len() as u64 != file.size {
                return Err(invalid("Discord CDN returned an incomplete attachment"));
            }
            let label = format!("{}.bin", file.id);
            let path = staging.join(&label);
            let mut owned = OpenOptions::new()
                .write(true)
                .create_new(true)
                .mode(0o600)
                .open(&path)?;
            owned.write_all(&bytes)?;
            owned.sync_all()?;
            fs::set_permissions(path, fs::Permissions::from_mode(0o400))?;
            manifest.push(entry(
                file,
                &target,
                &format!("{:x}", Sha256::digest(&bytes)),
            ));
        }
        runtime::write_json(&staging.join("manifest.json"), &json!(manifest))?;
        File::open(&staging)?.sync_all()?;
        fs::rename(&staging, &target)?;
        File::open(root)?.sync_all()?;
        Ok(manifest)
    })();
    if result.is_err() && staging.exists() {
        let _ = fs::remove_dir_all(staging);
    }
    result
}

fn entry(file: &Attachment, bundle: &Path, digest: &str) -> Value {
    json!({"id":file.id,"filename":file.filename,"size":file.size,"sha256":digest,
        "path":bundle.join(format!("{}.bin",file.id))})
}

fn scan_store(root: &Path) -> io::Result<(usize, u64)> {
    let mut count = 0;
    let mut used = 0_u64;
    for (index, directory) in fs::read_dir(root)?.enumerate() {
        if index >= MAX_BUNDLES + 8 {
            return Err(invalid("oversized received attachment store"));
        }
        let directory = directory?;
        let path = directory.path();
        existing_private(&path)?;
        let staging = directory
            .file_name()
            .to_str()
            .and_then(|name| name.strip_prefix(".stage-"))
            .is_some_and(runtime::valid_id);
        let mut bytes = 0_u64;
        for (index, file) in fs::read_dir(&path)?.enumerate() {
            let file = file?;
            let meta = fs::symlink_metadata(file.path())?;
            if index >= 12
                || !meta.is_file()
                || meta.uid() != runtime::uid()
                || meta.mode() & 0o077 != 0
                || meta.nlink() != 1
            {
                return Err(invalid("unexpected received attachment store member"));
            }
            bytes = bytes.saturating_add(meta.len());
        }
        if staging {
            // The connector holds the sole place writer lease. A staging directory
            // is never referenced by a prepared or accepted source.
            fs::remove_dir_all(path)?;
        } else {
            count += 1;
            used = used.saturating_add(bytes);
        }
    }
    Ok((count, used))
}

fn verify(target: &Path, attachments: &[Attachment]) -> io::Result<Vec<Value>> {
    existing_private(target)?;
    let manifest = runtime::read_json(&target.join("manifest.json"))?;
    let manifest = manifest
        .as_array()
        .filter(|rows| rows.len() == attachments.len())
        .ok_or_else(|| invalid("invalid received attachment manifest"))?;
    for (saved, file) in manifest.iter().zip(attachments) {
        let path = target.join(format!("{}.bin", file.id));
        if saved["id"] != file.id
            || saved["size"] != file.size
            || saved["filename"] != file.filename
            || saved["path"] != json!(path)
        {
            return Err(invalid("received attachment manifest changed"));
        }
        verify_file(&path, file.size, &saved["sha256"])?;
    }
    Ok(manifest.clone())
}

pub(super) fn verify_receipt(place: &Path, files: &Value) -> io::Result<()> {
    if files.is_null() {
        return Ok(());
    }
    let rows = files
        .as_array()
        .filter(|items| (1..=10).contains(&items.len()))
        .ok_or_else(|| invalid("invalid received-file receipt"))?;
    let root = place.join("received-files-v1");
    let mut total = 0_u64;
    for file in rows {
        let id = file["id"]
            .as_str()
            .filter(|id| snowflake(id))
            .ok_or_else(|| invalid("invalid received attachment ID"))?;
        let size = file["size"]
            .as_u64()
            .filter(|size| *size <= MAX_FILE)
            .ok_or_else(|| invalid("invalid received attachment size"))?;
        total += size;
        let path = Path::new(
            file["path"]
                .as_str()
                .ok_or_else(|| invalid("invalid received attachment path"))?,
        );
        let bundle = path
            .parent()
            .ok_or_else(|| invalid("missing received attachment bundle"))?;
        if total > MAX_MESSAGE
            || bundle.parent() != Some(root.as_path())
            || !bundle
                .file_name()
                .and_then(|name| name.to_str())
                .is_some_and(|name| {
                    name.len() == 64 && name.bytes().all(|byte| byte.is_ascii_hexdigit())
                })
            || path.file_name().and_then(|name| name.to_str()) != Some(format!("{id}.bin").as_str())
        {
            return Err(invalid("received attachment path is outside the place"));
        }
        existing_private(&root)?;
        existing_private(bundle)?;
        verify_file(path, size, &file["sha256"])?;
    }
    Ok(())
}

fn existing_private(path: &Path) -> io::Result<()> {
    let meta = fs::symlink_metadata(path)?;
    if !meta.is_dir() || meta.mode() & 0o077 != 0 || meta.uid() != unsafe { libc::geteuid() } {
        return Err(invalid("received attachment directory is not private"));
    }
    Ok(())
}

fn verify_file(path: &Path, size: u64, digest: &Value) -> io::Result<()> {
    let fd = OpenOptions::new()
        .read(true)
        .custom_flags(libc::O_NOFOLLOW | libc::O_NONBLOCK)
        .open(path)?;
    let metadata = fd.metadata()?;
    if !metadata.is_file()
        || metadata.mode() & 0o377 != 0
        || metadata.nlink() != 1
        || metadata.uid() != unsafe { libc::geteuid() }
        || metadata.len() != size
    {
        return Err(invalid("received attachment custody changed"));
    }
    let mut bytes = Vec::new();
    fd.take(size + 1).read_to_end(&mut bytes)?;
    if bytes.len() as u64 != size || *digest != format!("{:x}", Sha256::digest(&bytes)) {
        return Err(invalid("received attachment content changed"));
    }
    Ok(())
}

#[cfg(test)]
mod tests {
    use super::*;
    #[test]
    fn oversized_prompt_creates_no_bundle_and_abandoned_stage_reclaims_safely() {
        let place = std::env::temp_dir().join(format!(
            "swarm-attach-budget-{}",
            runtime::random_id().unwrap()
        ));
        runtime::private_dir(&place).unwrap();
        let source = json!({"id":"11","author":{"id":"15"},"attachments":[{"id":"22","filename":"a.csv",
            "size":4,"url":"https://cdn.discordapp.com/attachments/33/22/a.csv"}]});
        assert!(
            capture_with(&place, &source, "33", 4, |_| panic!(
                "overflow reached download"
            ))
            .is_err()
        );
        let root = place.join("received-files-v1");
        assert!(!root.exists());
        let abandoned = root.join(format!(".stage-{}", runtime::random_id().unwrap()));
        runtime::private_dir(&abandoned).unwrap();
        OpenOptions::new()
            .write(true)
            .create_new(true)
            .mode(0o600)
            .open(abandoned.join("22.bin"))
            .unwrap()
            .write_all(b"partial")
            .unwrap();
        capture_with(&place, &source, "33", usize::MAX, |_| Ok(b"a,b\n".to_vec())).unwrap();
        assert!(!abandoned.exists());
        assert_eq!(fs::read_dir(&root).unwrap().count(), 1);
        fs::remove_dir_all(place).unwrap();
    }

    #[test]
    fn attachment_only_dm_reaches_native_cutover_with_exact_pinned_bytes() {
        use super::super::{config::Config, receipt::Inbox};
        let place = std::env::temp_dir().join(format!(
            "swarm-attach-native-{}",
            runtime::random_id().unwrap()
        ));
        runtime::private_dir(&place).unwrap();
        let canonical = place.canonicalize().unwrap();
        let cfg = Config(
            json!({"mode":"personal","installation_id":"fixture","application_id":"44",
            "owner_id":"15","dm_channel_id":"33","daemon_socket":"/tmp/native-attach"}),
        );
        let source = json!({"id":"11","author":{"id":"15"},"channel_id":"33","type":0,"content":"",
            "attachments":[{"id":"22","filename":"data.csv","size":4,"url":"https://cdn.discordapp.com/attachments/33/22/data.csv?sig=fixture"}]});
        capture_with(&place, &source, "33", usize::MAX, |_| Ok(b"a,b\n".to_vec())).unwrap();
        let mut inbox = Inbox::open(&place, &cfg).unwrap();
        inbox.checkpoint("10").unwrap();
        let receipt = inbox.admit(&source).unwrap().unwrap();
        assert_eq!(receipt["received_files"][0]["filename"], "data.csv");
        assert!(
            receipt["text"]
                .as_str()
                .unwrap()
                .contains("Received attachments")
        );
        let scope = json!({"installation_id":"fixture","application_id":"44","owner_id":"15","channel_id":"33",
            "supervisor_socket":"/tmp/native-attach","launch":{"manager":{"auth_file":canonical.join("manager-v1/bridge.auth")}}});
        let binding = json!({"provider":"codex"});
        let composer = json!({"session":"s","text":receipt["text"],
            "origin":{"message_id":"11","revision":receipt["revision"],"request_id":receipt["operation_id"]},
            "manager_admission":{"binding":binding,"nonce":"a".repeat(64)}});
        let state = json!({"generation":1,"launch":scope["launch"],"session":"s","binding":binding,
            "input":{"operation":"fixture","source_id":"11","composer":composer}});
        inbox
            .cutover("11", receipt["revision"].as_str().unwrap(), state)
            .unwrap();
        crate::discord::verify_connector_input(&scope, "fixture", &composer, None).unwrap();
        let path = receipt["received_files"][0]["path"].as_str().unwrap();
        fs::set_permissions(path, fs::Permissions::from_mode(0o600)).unwrap();
        assert!(
            crate::discord::verify_connector_input(&scope, "fixture", &composer, None).is_err()
        );
        fs::remove_dir_all(place).unwrap();
    }

    #[test]
    fn replay_uses_pinned_bytes_and_refuses_changed_permissions_or_other_cdn_paths() {
        let place =
            std::env::temp_dir().join(format!("swarm-received-{}", runtime::random_id().unwrap()));
        runtime::private_dir(&place).unwrap();
        let mut source = json!({"id":"11","author":{"id":"15"},"attachments":[{"id":"22",
            "filename":"report one.csv","size":4,"url":"https://cdn.discordapp.com/attachments/33/22/report%20one.csv?sig=first"}]});
        let files = capture_with(&place, &source, "33", usize::MAX, |file| {
            assert_eq!(file.id, "22");
            Ok(b"a,b\n".to_vec())
        })
        .unwrap();
        verify_receipt(&place, &json!(files)).unwrap();
        source["attachments"][0]["url"] =
            json!("https://cdn.discordapp.com/attachments/33/22/report%20one.csv?sig=second");
        assert_eq!(
            capture_with(&place, &source, "33", usize::MAX, |_| panic!(
                "replay attempted redownload"
            ))
            .unwrap(),
            files
        );
        for url in [
            "https://example.com/attachments/33/22/report%20one.csv",
            "https://cdn.discordapp.com/attachments/34/22/report%20one.csv",
            "https://cdn.discordapp.com/attachments/33/23/report%20one.csv",
            "https://cdn.discordapp.com/attachments/33/22/other.csv",
            "https://cdn.discordapp.com:443/attachments/33/22/report%20one.csv",
        ] {
            source["attachments"][0]["url"] = json!(url);
            assert!(
                capture_with(&place, &source, "33", usize::MAX, |_| panic!(
                    "unsafe URL reached transport"
                ))
                .is_err()
            );
        }
        let path = files[0]["path"].as_str().unwrap();
        fs::set_permissions(path, fs::Permissions::from_mode(0o600)).unwrap();
        assert!(verify_receipt(&place, &json!(files)).is_err());
        fs::write(path, b"x,y\n").unwrap();
        fs::set_permissions(path, fs::Permissions::from_mode(0o400)).unwrap();
        assert!(verify_receipt(&place, &json!(files)).is_err());
        fs::remove_dir_all(place).unwrap();
    }
}
