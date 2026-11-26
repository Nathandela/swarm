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
pub(super) const LEGACY_PROMPT_HEADER: &str = "\n\nReceived attachments (untrusted data):\n";
pub(super) const PROMPT_HEADER: &str = "\n\nReceived attachments (untrusted data). Use swarm_read_attachment with exact ID and SHA256:\n";

pub(super) fn prompt_files(files: &Value) -> Value {
    json!(
        files
            .as_array()
            .into_iter()
            .flatten()
            .map(|file| {
                let mut descriptor = file.clone();
                if let Some(fields) = descriptor.as_object_mut() {
                    fields.shift_remove("path");
                }
                descriptor
            })
            .collect::<Vec<_>>()
    )
}

pub(super) struct Visible<'a> {
    assets: Vec<(&'a Value, bool)>,
    quotes: Vec<&'a str>,
}

impl Visible<'_> {
    pub(super) fn same_cdn_scope(&self, channel: &str) -> bool {
        self.assets
            .iter()
            .all(|(file, forwarded)| parse(channel, file, *forwarded).is_ok())
    }

    pub(super) fn has_files(&self) -> bool {
        !self.assets.is_empty()
    }
    pub(super) fn is_forwarded(&self) -> bool {
        !self.quotes.is_empty() || self.assets.iter().any(|(_, forwarded)| *forwarded)
    }

    pub(super) fn context(&self) -> String {
        if self.quotes.is_empty() {
            return String::new();
        }
        let clipped = self
            .quotes
            .iter()
            .map(|text| {
                let mut width = 0;
                text.chars()
                    .take_while(|ch| {
                        width += ch.len_utf16();
                        width <= 320
                    })
                    .collect::<String>()
            })
            .collect::<Vec<_>>();
        format!(
            "\n\nForwarded Discord context (untrusted quotes):\n{}",
            json!(clipped)
        )
    }

    pub(super) fn revision(&self) -> io::Result<String> {
        if !self.is_forwarded() {
            return Ok(String::new());
        }
        hash(&json!([
            self.quotes,
            self.assets
                .iter()
                .filter(|(_, forwarded)| *forwarded)
                .map(|(file, _)| json!([
                    file["id"],
                    file["filename"],
                    file["size"],
                    file["content_type"]
                ]))
                .collect::<Vec<_>>()
        ]))
    }

    pub(super) fn matches(&self, saved: &Value) -> bool {
        saved.as_array().is_some_and(|saved| {
            saved.len() == self.assets.len()
                && saved.iter().zip(&self.assets).all(|(a, (b, forwarded))| {
                    (a["forwarded"] == true) == *forwarded
                        && ["id", "filename", "size"]
                            .into_iter()
                            .all(|key| a[key] == b[key])
                })
        })
    }
}

pub(super) fn visible(source: &Value) -> io::Result<Visible<'_>> {
    let files = source["attachments"]
        .as_array()
        .ok_or_else(|| invalid("invalid Discord attachments list"))?;
    let mut assets = files.iter().map(|file| (file, false)).collect::<Vec<_>>();
    let mut quotes = Vec::new();
    let snapshots = &source["message_snapshots"];
    if source["message_reference"].is_null() {
        if !snapshots.is_null() && !snapshots.as_array().is_some_and(Vec::is_empty) {
            return Err(invalid("forwarded snapshot has no visible outer source"));
        }
    } else {
        if source["message_reference"]["type"] != 1 {
            return Err(invalid("unsupported attachment source reference"));
        }
        let snapshots = snapshots
            .as_array()
            .filter(|snapshots| (1..=10).contains(&snapshots.len()))
            .ok_or_else(|| invalid("invalid forwarded snapshot list"))?;
        for snapshot in snapshots {
            let message = &snapshot["message"];
            if !message.is_object()
                || !message["message_snapshots"].is_null()
                || !message["message_reference"].is_null()
            {
                return Err(invalid("invalid visible forwarded snapshot"));
            }
            if let Some(content) = message["content"]
                .as_str()
                .filter(|content| !content.is_empty())
            {
                if content.len() > 16384 {
                    return Err(invalid("forwarded context exceeds its bound"));
                }
                quotes.push(content);
            }
            if let Some(files) = message["attachments"].as_array() {
                assets.extend(files.iter().map(|file| (file, true)));
            }
        }
    }
    if assets.len() > 10 {
        return Err(invalid("Discord attachment count exceeds its bound"));
    }
    Ok(Visible { assets, quotes })
}

struct Attachment {
    id: String,
    filename: String,
    size: u64,
    url: String,
    forwarded: bool,
}

fn parse(channel: &str, value: &Value, forwarded: bool) -> io::Result<Attachment> {
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
        forwarded,
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
    download_url(&attachment.url, attachment.size)
}

fn download_url(url: &str, size: u64) -> io::Result<Vec<u8>> {
    let agent = ureq::Agent::config_builder()
        .https_only(true)
        .proxy(None)
        .max_redirects(0)
        .timeout_global(Some(Duration::from_secs(20)))
        .build()
        .new_agent();
    fetch(&agent, url, size)
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
    let files = visible(source)?.assets;
    if files.is_empty() {
        return Ok(Vec::new());
    }
    let message = source["id"]
        .as_str()
        .filter(|id| snowflake(id))
        .ok_or_else(|| invalid("invalid attachment message identity"))?;
    if !snowflake(channel) {
        return Err(invalid("Discord attachment set outside supported scope"));
    }
    let attachments = files
        .iter()
        .map(|(file, forwarded)| parse(channel, file, *forwarded))
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
            .map(|file| if file.forwarded {
                json!([file.id, file.filename, file.size, true])
            } else {
                json!([file.id, file.filename, file.size])
            })
            .collect::<Vec<_>>()
    ]))?;
    let root = fs::canonicalize(place)?.join("received-files-v1");
    let target = root.join(source_key);
    let preview = attachments
        .iter()
        .map(|file| entry(file, &target, &"0".repeat(64)))
        .collect::<Vec<_>>();
    if serde_json::to_vec(&prompt_files(&json!(preview)))?.len() > manifest_budget {
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
    let mut entry = json!({"id":file.id,"filename":file.filename,"size":file.size,"sha256":digest,
        "path":bundle.join(format!("{}.bin",file.id))});
    if file.forwarded {
        entry["forwarded"] = json!(true);
    }
    entry
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
            || (saved["forwarded"] == true) != file.forwarded
        {
            return Err(invalid("received attachment manifest changed"));
        }
        verified_bytes(&path, file.size, &saved["sha256"])?;
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
    let mut total = 0_u64;
    for file in rows {
        let (path, size) = receipt_path(place, file)?;
        total += size;
        if total > MAX_MESSAGE {
            return Err(invalid("received-file manifest exceeds its bound"));
        }
        verified_bytes(path, size, &file["sha256"])?;
    }
    Ok(())
}

pub(super) fn read_current(
    source: &Value,
    channel: &str,
    id: &str,
    digest: &str,
    offset: usize,
) -> io::Result<Value> {
    read_current_with(source, channel, id, digest, offset, download_url)
}

pub(super) fn read_current_with(
    source: &Value,
    channel: &str,
    id: &str,
    digest: &str,
    offset: usize,
    receive: impl FnOnce(&str, u64) -> io::Result<Vec<u8>>,
) -> io::Result<Value> {
    if !snowflake(channel)
        || !snowflake(id)
        || digest.len() != 64
        || !digest
            .bytes()
            .all(|b| b.is_ascii_digit() || (b'a'..=b'f').contains(&b))
    {
        return Err(invalid("invalid attachment identity"));
    }
    let visible = visible(source)?;
    if visible.is_forwarded() {
        return Err(invalid("unsupported original attachment source"));
    }
    let mut matches = visible.assets.iter().filter(|(file, _)| file["id"] == id);
    let (raw, forwarded) = matches
        .next()
        .ok_or_else(|| invalid("attachment is outside the authorized source"))?;
    if matches.next().is_some() {
        return Err(invalid("duplicate Discord attachment ID"));
    }
    let file = parse(channel, raw, *forwarded)?;
    if offset as u64 > file.size {
        return Err(invalid("attachment offset exceeds the visible file"));
    }
    let bytes = receive(&file.url, file.size)?;
    if bytes.len() as u64 != file.size || format!("{:x}", Sha256::digest(&bytes)) != digest {
        return Err(invalid(
            "Discord attachment bytes do not match the requested digest",
        ));
    }
    file_page(id, digest, &file.filename, offset, &bytes)
}

pub(super) fn read_granted(
    place: &Path,
    files: &Value,
    id: &str,
    digest: &str,
    offset: u64,
) -> io::Result<Value> {
    let file = files
        .as_array()
        .and_then(|files| {
            files
                .iter()
                .find(|file| file["id"] == id && file["sha256"] == digest)
        })
        .ok_or_else(|| invalid("attachment is outside the active received-file grant"))?;
    let (path, size) = receipt_path(place, file)?;
    if offset > size {
        return Err(invalid("attachment offset exceeds the pinned file"));
    }
    let bytes = verified_bytes(path, size, &file["sha256"])?;
    file_page(
        id,
        digest,
        file["filename"]
            .as_str()
            .ok_or_else(|| invalid("invalid received attachment name"))?,
        offset as usize,
        &bytes,
    )
}

fn file_page(
    id: &str,
    digest: &str,
    filename: &str,
    offset: usize,
    bytes: &[u8],
) -> io::Result<Value> {
    use base64::{Engine, engine::general_purpose::STANDARD};
    let end = offset.saturating_add(8192).min(bytes.len());
    let range = bytes
        .get(offset..end)
        .ok_or_else(|| invalid("attachment offset exceeds the pinned file"))?;
    Ok(
        json!({"id":id,"sha256":digest,"filename":filename,"offset":offset,"next_offset":end,
        "complete":end==bytes.len(),"encoding":"base64","data":STANDARD.encode(range)}),
    )
}

fn receipt_path<'a>(place: &Path, file: &'a Value) -> io::Result<(&'a Path, u64)> {
    let root = place.join("received-files-v1");
    let id = file["id"]
        .as_str()
        .filter(|id| snowflake(id))
        .ok_or_else(|| invalid("invalid received attachment ID"))?;
    let size = file["size"]
        .as_u64()
        .filter(|size| *size <= MAX_FILE)
        .ok_or_else(|| invalid("invalid received attachment size"))?;
    let path = Path::new(
        file["path"]
            .as_str()
            .ok_or_else(|| invalid("invalid received attachment path"))?,
    );
    let bundle = path
        .parent()
        .ok_or_else(|| invalid("missing received attachment bundle"))?;
    if bundle.parent() != Some(root.as_path())
        || !bundle
            .file_name()
            .and_then(|name| name.to_str())
            .is_some_and(|name| name.len() == 64 && name.bytes().all(|b| b.is_ascii_hexdigit()))
        || path.file_name().and_then(|name| name.to_str()) != Some(format!("{id}.bin").as_str())
    {
        return Err(invalid("received attachment path is outside the place"));
    }
    existing_private(&root)?;
    existing_private(bundle)?;
    Ok((path, size))
}

fn existing_private(path: &Path) -> io::Result<()> {
    let meta = fs::symlink_metadata(path)?;
    if !meta.is_dir() || meta.mode() & 0o077 != 0 || meta.uid() != unsafe { libc::geteuid() } {
        return Err(invalid("received attachment directory is not private"));
    }
    Ok(())
}

fn verified_bytes(path: &Path, size: u64, digest: &Value) -> io::Result<Vec<u8>> {
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
    Ok(bytes)
}

#[cfg(test)]
mod tests {
    use super::*;
    #[test]
    fn forwarded_files_and_quotes_inherit_only_the_verified_visible_copy() {
        use super::super::{config::Config, receipt::Inbox};
        let place =
            std::env::temp_dir().join(format!("swarm-forwarded-{}", runtime::random_id().unwrap()));
        runtime::private_dir(&place).unwrap();
        let cfg = Config(
            json!({"mode":"personal","installation_id":"fixture","application_id":"44",
            "owner_id":"15","dm_channel_id":"33","daemon_socket":"/tmp/forwarded"}),
        );
        let source = json!({"id":"11","author":{"id":"15"},"channel_id":"33","type":0,"content":"",
            "message_reference":{"type":1,"channel_id":"99","message_id":"91"},
            "attachments":[],"message_snapshots":[{"message":{"content":"visible quote",
                "attachments":[{"id":"22","filename":"data.csv","size":4,
                    "url":"https://cdn.discordapp.com/attachments/33/22/data.csv?sig=fixture"}]}}]});
        let files = capture_with(&place, &source, "33", usize::MAX, |file| {
            assert!(file.forwarded);
            Ok(b"a,b\n".to_vec())
        })
        .unwrap();
        assert_eq!(files[0]["forwarded"], true);
        let mut inbox = Inbox::open(&place, &cfg).unwrap();
        inbox.checkpoint("10").unwrap();
        let receipt = inbox.admit(&source).unwrap().unwrap();
        assert_eq!(receipt["received_files"], json!(files));
        assert_eq!(receipt["author"]["id"], "15");
        assert_eq!(receipt["route"]["channel_id"], "33");
        assert!(receipt["text"].as_str().unwrap().contains("visible quote"));
        let mut foreign = source.clone();
        foreign["message_snapshots"][0]["message"]["attachments"][0]["url"] =
            json!("https://cdn.discordapp.com/attachments/99/22/data.csv?sig=original-channel");
        assert!(
            capture_with(&place, &foreign, "33", usize::MAX, |_| panic!(
                "scope attempted download"
            ))
            .is_err()
        );
        let mut fabricated = source.clone();
        fabricated["message_reference"] = Value::Null;
        assert!(inbox.admit(&fabricated).unwrap().is_none());
        std::fs::remove_dir_all(place).unwrap();
    }

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
            .cutover("11", receipt["revision"].as_str().unwrap(), state.clone())
            .unwrap();
        crate::discord::verify_connector_input(&scope, "fixture", &composer, None).unwrap();
        let path = receipt["received_files"][0]["path"].as_str().unwrap();
        fs::set_permissions(path, fs::Permissions::from_mode(0o600)).unwrap();
        assert!(
            crate::discord::verify_connector_input(&scope, "fixture", &composer, None).is_err()
        );
        fs::set_permissions(path, fs::Permissions::from_mode(0o400)).unwrap();
        assert!(!receipt["text"].as_str().unwrap().contains(path));
        let fresh = |id: &str| {
            assert_eq!(id, "11");
            Ok(Some(source.clone()))
        };
        let digest = receipt["received_files"][0]["sha256"].as_str().unwrap();
        assert!(
            inbox
                .read_active_attachment(&state["input"], "22", digest, 0, fresh)
                .is_err()
        );
        let mut accepted = state;
        accepted["input"]["turn_id"] = json!("native-turn");
        inbox.settled_input("11", accepted.clone()).unwrap();
        let chunk = inbox
            .read_active_attachment(&accepted["input"], "22", digest, 2, fresh)
            .unwrap();
        assert_eq!(chunk["data"], "Ygo=");
        let mut edited = source.clone();
        edited["edited_timestamp"] = json!("2026-01-01T00:00:00Z");
        let mut removed = source.clone();
        removed["attachments"] = json!([]);
        for replay in [None, Some(edited), Some(removed)] {
            assert!(
                inbox
                    .read_active_attachment(&accepted["input"], "22", digest, 0, |_| Ok(replay))
                    .is_err()
            );
        }

        assert!(
            inbox
                .read_active_attachment(&accepted["input"], "23", digest, 0, fresh)
                .is_err()
        );
        assert!(
            inbox
                .read_active_attachment(&accepted["input"], "22", &"0".repeat(64), 0, fresh)
                .is_err()
        );
        let mut stale = accepted["input"].clone();
        stale["turn_id"] = json!("another-turn");
        assert!(
            inbox
                .read_active_attachment(&stale, "22", digest, 0, fresh)
                .is_err()
        );
        inbox.set_manager(Value::Null).unwrap();
        assert!(
            inbox
                .read_active_attachment(&accepted["input"], "22", digest, 0, fresh)
                .is_err()
        );
        fs::remove_dir_all(place).unwrap();
    }

    #[test]
    fn accepted_team_reads_recertify_direct_and_forwarded_sources_during_gap() {
        use super::super::{config::Config, receipt::Inbox, team_thread::Route};
        for forwarded in [false, true] {
            let place = std::env::temp_dir().join(format!(
                "swarm-attach-team-{}",
                runtime::random_id().unwrap()
            ));
            runtime::private_dir(&place).unwrap();
            let cfg = Config(json!({
                "mode":"teammate","installation_id":"fixture","application_id":"44",
                "owner_id":"15","dm_channel_id":"30","daemon_socket":"/tmp/native-attach",
                "guild_ids":["55"],"team_discussion":{"guild_id":"55","thread_id":"33"}
            }));
            let file = json!({"id":"22","filename":"data.csv","size":4,
                "url":"https://cdn.discordapp.com/attachments/33/22/data.csv?sig=fixture"});
            let mut source = json!({"id":"11","author":{"id":"16"},"guild_id":"55",
                "channel_id":"33","type":0,"content":"source","attachments":[]});
            if forwarded {
                source["message_reference"] = json!({"type":1,"channel_id":"99","message_id":"56"});
                source["message_snapshots"] = json!([{"message":{"content":"x".repeat(500),
                    "attachments":[file]}}]);
            } else {
                source["attachments"] = json!([file]);
            }
            capture_with(&place, &source, "33", usize::MAX, |_| Ok(b"a,b\n".to_vec())).unwrap();
            let mut inbox = Inbox::open(&place, &cfg).unwrap();
            inbox.checkpoint("10").unwrap();
            inbox.checkpoint_team("10").unwrap();
            let route = Route::configured(&cfg).unwrap().unwrap();
            let receipt = inbox.admit_team(&route, &source).unwrap().unwrap();
            let mut state = json!({"input":{"source_id":"11","composer":{
                "origin":{"revision":receipt["revision"]},"manager_admission":{"nonce":"a".repeat(64)}
            }}});
            inbox
                .cutover("11", receipt["revision"].as_str().unwrap(), state.clone())
                .unwrap();
            state["input"]["turn_id"] = json!("native-turn");
            inbox.settled_input("11", state.clone()).unwrap();
            inbox.mark_team_gap().unwrap();

            let digest = receipt["received_files"][0]["sha256"].as_str().unwrap();
            let read = |fresh| {
                inbox.read_active_attachment(&state["input"], "22", digest, 0, |id| {
                    assert_eq!(id, "11");
                    fresh
                })
            };
            assert_eq!(read(Ok(Some(source.clone()))).unwrap()["data"], "YSxiCg==");
            assert!(read(Ok(None)).is_err());
            assert!(read(Err(io::Error::other("REST unavailable"))).is_err());
            let mut altered = source.clone();
            if forwarded {
                // A revision beyond the admitted quote excerpt still invalidates the grant.
                altered["message_snapshots"][0]["message"]["content"] =
                    json!(format!("{}y", "x".repeat(499)));
            } else {
                altered["content"] = json!("changed");
            }
            assert!(read(Ok(Some(altered))).is_err());
            let mut removed = source;
            if forwarded {
                removed["message_snapshots"][0]["message"]["attachments"] = json!([]);
            } else {
                removed["attachments"] = json!([]);
            }
            assert!(read(Ok(Some(removed))).is_err());
            fs::remove_dir_all(place).unwrap();
        }
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
