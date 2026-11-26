//! Read the signed GNU tar format emitted by the release builder.
use super::invalid;
use base64::{Engine, engine::general_purpose::STANDARD};
use flate2::bufread::GzDecoder;
use sha2::{Digest, Sha256};
use std::{
    collections::BTreeMap,
    io::{self, Read},
};

pub(super) type Files = BTreeMap<String, Vec<u8>>;
pub(super) const MAX_ARCHIVE: usize = 512 * 1024 * 1024;
const MAX_CONTENT: usize = 700 * 1024 * 1024;
const MAX_MEMBERS: usize = 16_384;

pub(super) fn sha(bytes: &[u8]) -> String {
    format!("{:x}", Sha256::digest(bytes))
}

pub(super) fn read_bounded(reader: impl Read, cap: usize) -> io::Result<Vec<u8>> {
    let mut bytes = Vec::new();
    reader.take((cap + 1) as u64).read_to_end(&mut bytes)?;
    if bytes.len() > cap {
        return Err(invalid("release input exceeds its size limit"));
    }
    Ok(bytes)
}

pub(super) fn checksums(
    pem: &str,
    bytes: &[u8],
    signature: &str,
) -> io::Result<BTreeMap<String, String>> {
    let body = pem
        .trim()
        .strip_prefix("-----BEGIN PUBLIC KEY-----")
        .and_then(|s| s.strip_suffix("-----END PUBLIC KEY-----"))
        .ok_or_else(|| invalid("expected a pinned Ed25519 public key"))?;
    let der = STANDARD
        .decode(body.split_whitespace().collect::<String>())
        .map_err(io::Error::other)?;
    let key = der
        .strip_prefix(b"\x30\x2a\x30\x05\x06\x03\x2b\x65\x70\x03\x21\x00")
        .filter(|key| key.len() == 32)
        .ok_or_else(|| invalid("expected a pinned Ed25519 public key"))?;
    let signature = STANDARD
        .decode(signature.trim())
        .map_err(io::Error::other)?;
    ring::signature::UnparsedPublicKey::new(&ring::signature::ED25519, key)
        .verify(bytes, &signature)
        .map_err(|_| invalid("signed release checksums did not verify"))?;
    let mut result = BTreeMap::new();
    for line in std::str::from_utf8(bytes)
        .map_err(io::Error::other)?
        .lines()
    {
        let (hash, name) = line
            .split_once("  ")
            .ok_or_else(|| invalid("invalid checksum line"))?;
        if !hex(hash, 64)
            || name.is_empty()
            || !name
                .bytes()
                .all(|c| c.is_ascii_alphanumeric() || b"._-".contains(&c))
            || result.insert(name.into(), hash.into()).is_some()
        {
            return Err(invalid("invalid or duplicate checksum entry"));
        }
    }
    Ok(result)
}

pub(super) fn hex(value: &str, length: usize) -> bool {
    value.len() == length
        && value
            .bytes()
            .all(|c| c.is_ascii_digit() || (b'a'..=b'f').contains(&c))
}

pub(super) fn safe_name(name: &str) -> bool {
    !name.is_empty()
        && !name.contains(['\\', '\0'])
        && name.split('/').all(|part| !matches!(part, "" | "." | ".."))
}

pub(super) fn file_mode(name: &str) -> u32 {
    if name == "swarm"
        || name.starts_with("media/")
            && name.split('/').count() == 3
            && (name.ends_with("/node") || name.ends_with("/swarm-discord-voice"))
    {
        0o755
    } else {
        0o644
    }
}

fn octal(field: &[u8]) -> io::Result<usize> {
    let text = std::str::from_utf8(field).map_err(io::Error::other)?;
    usize::from_str_radix(text.trim_matches(['\0', ' ']), 8).map_err(io::Error::other)
}

pub(super) fn unpack(bytes: &[u8], expected: &str) -> io::Result<Files> {
    if bytes.len() > MAX_ARCHIVE || sha(bytes) != expected {
        return Err(invalid("release archive checksum differs"));
    }
    let mut reader = GzDecoder::new(bytes);
    let mut files = BTreeMap::new();
    let mut total = 0usize;
    let mut long_name = None;
    let mut header = [0u8; 512];
    let mut padding = [0u8; 512];
    loop {
        reader.read_exact(&mut header)?;
        if header.iter().all(|&b| b == 0) {
            if long_name.is_some() {
                return Err(invalid("dangling archive path record"));
            }
            reader.read_exact(&mut header)?;
            if header.iter().any(|&b| b != 0)
                || read_bounded(&mut reader, 10 * 1024)?
                    .iter()
                    .any(|&b| b != 0)
            {
                return Err(invalid("invalid end of release archive"));
            }
            break;
        }
        let checksum = header
            .iter()
            .enumerate()
            .map(|(i, &b)| usize::from(if (148..156).contains(&i) { b' ' } else { b }))
            .sum::<usize>();
        if checksum != octal(&header[148..156])? || &header[257..265] != b"ustar  \0" {
            return Err(invalid("invalid GNU tar header"));
        }
        let size = octal(&header[124..136])?;
        let kind = header[156];
        total = total
            .checked_add(size)
            .filter(|&sum| sum <= MAX_CONTENT)
            .ok_or_else(|| invalid("release members exceed their size limit"))?;
        if files.len() >= MAX_MEMBERS
            || !matches!(kind, b'0' | b'L')
            || kind == b'L' && (long_name.is_some() || size > 4096)
        {
            return Err(invalid("unsupported archive member"));
        }
        let mut data = vec![0; size];
        reader.read_exact(&mut data)?;
        reader.read_exact(&mut padding[..(512 - size % 512) % 512])?;
        if kind == b'L' {
            let raw = data
                .strip_suffix(&[0])
                .ok_or_else(|| invalid("unterminated GNU tar path"))?;
            long_name = Some(
                std::str::from_utf8(raw)
                    .map_err(io::Error::other)?
                    .to_owned(),
            );
            continue;
        }
        let name = match long_name.take() {
            Some(name) => name,
            None => std::str::from_utf8(header[..100].split(|&b| b == 0).next().unwrap())
                .map_err(io::Error::other)?
                .to_owned(),
        };
        if !safe_name(&name)
            || octal(&header[100..108])? != file_mode(&name) as usize
            || files.insert(name, data).is_some()
        {
            return Err(invalid("invalid or duplicate archive member"));
        }
    }
    if !reader.into_inner().is_empty() {
        return Err(invalid("trailing data after the release gzip member"));
    }
    let manifest: BTreeMap<String, String> = serde_json::from_slice(
        &files
            .remove("files.json")
            .ok_or_else(|| invalid("archive manifest is missing"))?,
    )?;
    if manifest.len() != files.len()
        || files
            .iter()
            .any(|(name, bytes)| manifest.get(name) != Some(&sha(bytes)))
    {
        return Err(invalid("archive member digest differs"));
    }
    Ok(files)
}

#[cfg(test)]
mod tests {
    use super::*;
    use ring::{
        rand::SystemRandom,
        signature::{Ed25519KeyPair, KeyPair},
    };

    #[test]
    fn signature_rejects_wrong_key_and_changed_manifest() {
        fn pair() -> Ed25519KeyPair {
            let pkcs8 = Ed25519KeyPair::generate_pkcs8(&SystemRandom::new()).unwrap();
            Ed25519KeyPair::from_pkcs8(pkcs8.as_ref()).unwrap()
        }
        fn pem(pair: &Ed25519KeyPair) -> String {
            let mut der = b"\x30\x2a\x30\x05\x06\x03\x2b\x65\x70\x03\x21\x00".to_vec();
            der.extend(pair.public_key().as_ref());
            format!(
                "-----BEGIN PUBLIC KEY-----\n{}\n-----END PUBLIC KEY-----",
                STANDARD.encode(der)
            )
        }
        let pair_a = pair();
        let text = format!("{}  swarm.tar.gz\n", "a".repeat(64));
        let sig = STANDARD.encode(pair_a.sign(text.as_bytes()).as_ref());
        assert!(checksums(&pem(&pair_a), text.as_bytes(), &sig).is_ok());
        assert!(checksums(&pem(&pair_a), text.trim().as_bytes(), &sig).is_err());
        assert!(checksums(&pem(&pair()), text.as_bytes(), &sig).is_err());
    }

    fn test_tar(entries: &[(&str, &[u8])]) -> Vec<u8> {
        use std::io::Write;
        let mut zip = flate2::write::GzEncoder::new(Vec::new(), flate2::Compression::fast());
        for (name, payload) in entries {
            let mut hdr = [0; 512];
            hdr[..name.len()].copy_from_slice(name.as_bytes());
            hdr[100..108].copy_from_slice(format!("{:07o}\0", file_mode(name)).as_bytes());
            hdr[124..136].copy_from_slice(format!("{:011o}\0", payload.len()).as_bytes());
            hdr[148..156].fill(b' ');
            hdr[156] = b'0';
            hdr[257..265].copy_from_slice(b"ustar  \0");
            let sum: usize = hdr.iter().map(|&b| usize::from(b)).sum();
            hdr[148..156].copy_from_slice(format!("{sum:06o}\0 ").as_bytes());
            zip.write_all(&hdr).unwrap();
            zip.write_all(payload).unwrap();
            zip.write_all(&vec![0; (512 - payload.len() % 512) % 512])
                .unwrap();
        }
        zip.write_all(&[0; 1024]).unwrap();
        zip.finish().unwrap()
    }

    #[test]
    fn archive_rejects_tampering_and_unsafe_members() {
        let manifest = serde_json::to_vec(&serde_json::json!({"swarm": sha(b"app")})).unwrap();
        let good = test_tar(&[("swarm", b"app"), ("files.json", &manifest)]);
        assert_eq!(unpack(&good, &sha(&good)).unwrap()["swarm"], b"app");
        let digest = sha(&good);
        let mut extra = good;
        extra.push(0);
        assert!(unpack(&extra, &digest).is_err());
        assert!(unpack(&extra, &sha(&extra)).is_err());
        for file in [
            "../swarm",
            "/swarm",
            "media//swarm",
            "media/../swarm",
            "media\\swarm",
        ] {
            let bad = test_tar(&[(file, b"app"), ("files.json", &manifest)]);
            assert!(unpack(&bad, &sha(&bad)).is_err(), "{file}");
        }
        let bad = test_tar(&[("swarm", b"changed"), ("files.json", &manifest)]);
        assert!(unpack(&bad, &sha(&bad)).is_err());
    }

    #[test]
    #[ignore = "requires disposable signed release paths"]
    fn verify_real_release() {
        let dist = std::path::PathBuf::from(std::env::var_os("SWARM_VERIFY_DIST").unwrap());
        let key = std::fs::read_to_string(std::env::var_os("SWARM_VERIFY_KEY").unwrap()).unwrap();
        let sums = checksums(
            &key,
            &std::fs::read(dist.join("checksums.txt")).unwrap(),
            &std::fs::read_to_string(dist.join("checksums.txt.sig")).unwrap(),
        )
        .unwrap();
        for (name, sum) in sums.iter().filter(|(name, _)| name.ends_with(".tar.gz")) {
            let bytes = std::fs::read(dist.join(name)).unwrap();
            let files = unpack(&bytes, sum).unwrap();
            assert!(files.contains_key("swarm") && files.contains_key("compat.json"));
            assert!(unpack(&bytes, &"a".repeat(64)).is_err());
            eprintln!("verified {name} and {} members", files.len());
        }
    }
}
