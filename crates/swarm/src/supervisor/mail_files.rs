use rustix::fs::{Mode, OFlags, open, openat};
use serde::{Deserialize, Serialize};
use sha2::{Digest, Sha256};
use std::fs::File;
use std::io::{self, Read};
use std::os::unix::fs::MetadataExt;
use std::path::{Component, Path};

pub(crate) const MAX_FILES: usize = 10;
pub(crate) const MAX_BYTES: usize = 8 << 20;

#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
pub(crate) struct Descriptor {
    pub id: String,
    pub name: String,
    pub content_type: String,
    pub sha256: String,
    pub size: usize,
}

pub(crate) struct Captured {
    pub file: Descriptor,
    pub data: Vec<u8>,
}

fn directory_flags() -> OFlags {
    OFlags::RDONLY | OFlags::DIRECTORY | OFlags::NOFOLLOW | OFlags::CLOEXEC
}

fn descend(start: File, path: &Path) -> io::Result<File> {
    path.components().try_fold(start, |directory, piece| {
        let Component::Normal(piece) = piece else {
            return Err(io::Error::other(
                "worker files: checkout path is not physical",
            ));
        };
        Ok(openat(&directory, piece, directory_flags(), Mode::empty())?.into())
    })
}

pub(super) fn capture(
    checkout: &Path,
    identity: (u64, u64),
    paths: &[&str],
) -> io::Result<Vec<Captured>> {
    let relative = checkout
        .strip_prefix("/")
        .ok()
        .filter(|path| !path.as_os_str().is_empty())
        .ok_or_else(|| io::Error::other("worker files: expected a physical checkout path"))?;
    if paths.is_empty() || paths.len() > MAX_FILES {
        return Err(io::Error::other("worker files: select 1 to 10 files"));
    }
    let root = descend(
        open("/", directory_flags(), Mode::empty())?.into(),
        relative,
    )?;
    let metadata = root.metadata()?;
    if (metadata.dev(), metadata.ino()) != identity {
        return Err(io::Error::other(
            "worker checkout identity changed during capture",
        ));
    }
    let mut remaining = MAX_BYTES;
    let mut seen = std::collections::HashSet::new();
    let mut files = Vec::with_capacity(paths.len());
    for path in paths {
        let relative = Path::new(path)
            .strip_prefix(checkout)
            .ok()
            .filter(|relative| {
                path.len() <= 4096
                    && relative
                        .components()
                        .all(|c| matches!(c, Component::Normal(_)))
            })
            .ok_or_else(|| {
                io::Error::other("worker files: all files must be inside your checkout")
            })?;
        let original = relative
            .file_name()
            .and_then(|name| name.to_str())
            .filter(|name| {
                name.len() <= 128
                    && name.trim() == *name
                    && !name.contains('\\')
                    && !name.chars().any(char::is_control)
            })
            .ok_or_else(|| {
                io::Error::other("worker files: use safe file names of at most 128 bytes")
            })?;
        let name = discord_name(original);
        if secret_path(relative) || secret_path(Path::new(&name)) {
            return Err(io::Error::other(format!(
                "worker files: {original} looks like a secret or hidden file and stays on the VM"
            )));
        }
        if !seen.insert(name.to_ascii_lowercase()) {
            return Err(io::Error::other(format!(
                "worker files: two files would both be named {name} in Discord; rename one"
            )));
        }
        let parent = descend(
            root.try_clone()?,
            relative.parent().unwrap_or(Path::new("")),
        )?;
        let open_flags = OFlags::RDONLY | OFlags::NOFOLLOW | OFlags::NONBLOCK | OFlags::CLOEXEC;
        let file: File = openat(&parent, original, open_flags, Mode::empty())?.into();
        let before = file.metadata()?;
        // SAFETY: geteuid takes no arguments.
        if !before.is_file()
            || before.nlink() != 1
            || before.uid() != unsafe { libc::geteuid() }
            || before.len() == 0
            || before.len() > remaining as u64
        {
            return Err(io::Error::other(
                "worker files: expected a nonempty, singly linked owned regular file within the 8 MiB total",
            ));
        }
        let mut data = Vec::with_capacity(before.len() as usize);
        (&file).take(remaining as u64 + 1).read_to_end(&mut data)?;
        let after = file.metadata()?;
        if data.len() as u64 != before.len()
            || after.len() != before.len()
            || after.modified()? != before.modified()?
            || after.ctime() != before.ctime()
            || after.ctime_nsec() != before.ctime_nsec()
            || after.nlink() != 1
        {
            return Err(io::Error::other("worker files: file changed while reading"));
        }
        let content_type = content_type(&name, &data).ok_or_else(|| {
            io::Error::other(format!("worker files: {original} was refused. Use an approved format whose contents match its extension"))
        })?;
        let sha256 = format!("{:x}", Sha256::digest(&data));
        let id = format!("{:x}", Sha256::digest(format!("{name}\0{sha256}")));
        remaining -= data.len();
        files.push(Captured {
            file: Descriptor {
                id,
                name,
                content_type: content_type.into(),
                sha256,
                size: data.len(),
            },
            data,
        });
    }
    Ok(files)
}

fn discord_name(original: &str) -> String {
    let mut name = String::with_capacity(original.len());
    for ch in original.chars() {
        if ch.is_ascii_alphanumeric() || matches!(ch, '.' | '-' | '_') {
            name.push(ch);
        } else if !name.ends_with('_') {
            name.push('_');
        }
    }
    name
}

fn secret_path(path: &Path) -> bool {
    if path
        .components()
        .any(|part| part.as_os_str().as_encoded_bytes().starts_with(b"."))
    {
        return true;
    }
    let Some(name) = path.file_name().and_then(|name| name.to_str()) else {
        return true;
    };
    // Match Go's simple Unicode lowercase, including I with dot above.
    let name: String = name
        .chars()
        .filter_map(|ch| ch.to_lowercase().next())
        .collect();
    [
        "id_rsa",
        "id_ed25519",
        "id_ecdsa",
        "credential",
        "secret",
        "token",
        "private_key",
        ".env",
        ".pem",
        ".key",
    ]
    .iter()
    .any(|secret| name.contains(secret))
}

fn legacy_type(content_type: &str) -> bool {
    matches!(
        content_type,
        "image/png" | "image/jpeg" | "text/csv" | "application/pdf"
    )
}

pub(crate) fn verify_publication(file: &Descriptor, data: &[u8]) -> bool {
    let name = &file.name;
    !file.id.is_empty()
        && file.id.len() <= 128
        && file.id.bytes().all(|b| (33..=126).contains(&b))
        && !name.is_empty()
        && name.len() <= 160
        && name.trim() == name
        && !matches!(name.as_str(), "." | "..")
        && !name.contains(['/', '\\'])
        && !name.chars().any(char::is_control)
        && !data.is_empty()
        && data.len() == file.size
        && data.len() <= MAX_BYTES
        && file.sha256 == format!("{:x}", Sha256::digest(data))
        // Keep previously authorized v0.29.25 names and IDs in their original receipts.
        && (legacy_type(&file.content_type) || name.len() <= 128
            && discord_name(name) == *name
            && !secret_path(Path::new(name))
            && content_type(name, data) == Some(&file.content_type))
}

pub(super) fn verify(file: &Descriptor, data: &[u8]) -> bool {
    verify_publication(file, data)
        && file.name.len() <= 128
        && content_type(&file.name, data) == Some(&file.content_type)
        && file.id
            == format!(
                "{:x}",
                Sha256::digest(format!("{}\0{}", file.name, file.sha256))
            )
}

fn content_type(name: &str, data: &[u8]) -> Option<&'static str> {
    let ext = Path::new(name).extension()?.to_str()?.to_ascii_lowercase();
    let zip = data.starts_with(b"PK\x03\x04");
    const PROGRAMS: &[&[u8]] = &[
        b"MZ",
        b"\x7fELF",
        b"\xfe\xed\xfa",
        b"\xcf\xfa\xed\xfe",
        b"\xce\xfa\xed\xfe",
        b"\xca\xfe\xba\xbe",
    ];
    match ext.as_str() {
        "png" if data.starts_with(b"\x89PNG\r\n\x1a\n") => Some("image/png"),
        "jpeg" | "jpg" if data.starts_with(b"\xff\xd8\xff") => Some("image/jpeg"),
        "gif" if data.starts_with(b"GIF87a") || data.starts_with(b"GIF89a") => Some("image/gif"),
        "webp" if data.starts_with(b"RIFF") && data.get(8..14) == Some(b"WEBPVP") => {
            Some("image/webp")
        }
        "pdf" if data.starts_with(b"%PDF-") => Some("application/pdf"),
        "csv" if csv(data) => Some("text/csv"),
        "zip" if zip => Some("application/zip"),
        "docx" if zip => {
            Some("application/vnd.openxmlformats-officedocument.wordprocessingml.document")
        }
        "xlsx" if zip => Some("application/vnd.openxmlformats-officedocument.spreadsheetml.sheet"),
        "pptx" if zip => {
            Some("application/vnd.openxmlformats-officedocument.presentationml.presentation")
        }
        "odt" if zip => Some("application/vnd.oasis.opendocument.text"),
        "ods" if zip => Some("application/vnd.oasis.opendocument.spreadsheet"),
        "odp" if zip => Some("application/vnd.oasis.opendocument.presentation"),
        "txt" | "md" | "json" | "log" | "yaml" | "yml" | "tsv"
            if !data.contains(&0) && std::str::from_utf8(data).is_ok() =>
        {
            Some("text/plain; charset=utf-8")
        }
        "bin" | "dat" if !PROGRAMS.iter().any(|magic| data.starts_with(magic)) => {
            Some("application/octet-stream")
        }
        _ => None,
    }
}

fn csv(data: &[u8]) -> bool {
    if std::str::from_utf8(data).is_err()
        || data
            .iter()
            .any(|byte| matches!(*byte, 0..=8 | 11 | 14..=26 | 28..=31))
    {
        return false;
    }
    let head = &data[..data.len().min(512)];
    let head = head.trim_ascii_start();
    let markup = [
        "<!DOCTYPE HTML",
        "<HTML",
        "<HEAD",
        "<SCRIPT",
        "<IFRAME",
        "<H1",
        "<DIV",
        "<FONT",
        "<TABLE",
        "<A",
        "<STYLE",
        "<TITLE",
        "<B",
        "<BODY",
        "<BR",
        "<P",
        "<!--",
    ];
    if head.starts_with(b"<?xml")
        || markup.iter().any(|sig| {
            head.get(..sig.len())
                .is_some_and(|prefix| prefix.eq_ignore_ascii_case(sig.as_bytes()))
                && matches!(head.get(sig.len()), Some(b' ' | b'>'))
        })
        || [b"%PDF-".as_slice(), b"%!PS-Adobe-", b"GIF87a", b"GIF89a"]
            .iter()
            .any(|signature| data.starts_with(signature))
    {
        return false;
    }
    enum Field {
        Start,
        Bare,
        Quoted,
        AfterQuote,
    }
    use Field::*;
    let data = data.strip_prefix(b"\xef\xbb\xbf").unwrap_or(data);
    let mut state = Start;
    let mut record = false;
    let mut bytes = data.iter().copied().peekable();
    while let Some(byte) = bytes.next() {
        if byte == b'\r' && bytes.peek() == Some(&b'\n') {
            continue;
        }
        record |= byte != b'\n' && byte != b'\r';
        state = match (state, byte) {
            (Start | AfterQuote, b',' | b'\n') | (Bare, b',' | b'\n') => Start,
            (Start | AfterQuote, b'"') => Quoted,
            (Quoted, b'"') => AfterQuote,
            (Quoted, _) => Quoted,
            (Start | Bare, byte) if byte != b'"' => Bare,
            _ => return false,
        };
    }
    record && !matches!(state, Quoted)
}

#[cfg(test)]
mod tests {
    use super::*;
    use std::{fs, os::unix::fs::symlink};

    #[test]
    fn file_capture_is_scoped_and_bytes_remain_immutable() -> io::Result<()> {
        let scratch = std::env::temp_dir().join(super::super::runtime::random_id()?);
        super::super::runtime::private_dir(&scratch)?;
        let root = scratch.canonicalize()?.join("checkout");
        fs::create_dir(&root)?;
        let csv = root.join("results.csv");
        let pdf = root.join("report.pdf");
        let outside = scratch.join("private.pdf");
        fs::write(&csv, b"row,\"two, fields\"\r\n")?;
        fs::write(&pdf, b"%PDF-1.7\n")?;
        fs::write(&outside, b"%PDF-private\n")?;
        let paths = [csv.to_str().unwrap(), pdf.to_str().unwrap()];
        let metadata = root.metadata()?;
        let identity = (metadata.dev(), metadata.ino());
        assert!(capture(&root, (0, 0), &paths).is_err());
        let files = capture(&root, identity, &paths)?;
        assert_eq!(files[0].file.content_type, "text/csv");
        assert_eq!(files[1].file.content_type, "application/pdf");
        fs::write(&csv, b"new text")?;
        assert_eq!(files[0].data, b"row,\"two, fields\"\r\n");
        assert_ne!(files[0].file, capture(&root, identity, &paths)?[0].file);
        let link = root.join("link.pdf");
        symlink(&outside, &link)?;
        for denied in [&outside, &link, &root.join("../private.pdf")] {
            assert!(capture(&root, identity, &[denied.to_str().unwrap()]).is_err());
        }
        fs::remove_file(&link)?;
        fs::hard_link(&outside, &link)?;
        assert!(capture(&root, identity, &[link.to_str().unwrap()]).is_err());
        fs::write(&csv, b"\"unfinished")?;
        assert!(capture(&root, identity, &paths).is_err());
        fs::remove_dir_all(&scratch)
    }
    #[test]
    #[ignore = "requires the exact Go general-files mailbox oracle"]
    fn go_general_files_match_captured_metadata_and_fail_before_mailbox_writes() -> io::Result<()> {
        use base64::Engine;
        use serde_json::{Value, json};
        use std::{
            io::Write,
            process::{Command, Stdio},
        };

        let go = std::env::var_os("SWARM_GO_WORKER_MAIL_FIXTURE").unwrap();
        let scratch = std::env::temp_dir().join(super::super::runtime::random_id()?);
        super::super::runtime::private_dir(&scratch)?;
        let scratch = scratch.canonicalize()?;
        let checkout = scratch.join("checkout");
        let go_root = scratch.join("go");
        let rust_root = scratch.join("rust");
        for path in [&checkout, &go_root, &rust_root] {
            super::super::runtime::private_dir(path)?;
        }
        let metadata = checkout.metadata()?;
        let identity = (metadata.dev(), metadata.ino());
        let binding = json!({
            "Discussion":"discussion","Parent":"manager","Session":"endpoint/worker",
            "Instance":"instance","Attempt":"attempt","Agent":"codex","Project":checkout,
            "ParentInputHash":"owned-input"
        });
        let run = |action: &str, request: Value| -> io::Result<std::process::Output> {
            let mut command = Command::new(&go)
                .arg(action)
                .arg(&go_root)
                .stdin(Stdio::piped())
                .stdout(Stdio::piped())
                .stderr(Stdio::piped())
                .spawn()?;
            write!(command.stdin.take().unwrap(), "{request}")?;
            command.wait_with_output()
        };
        let prepare = |name: &str, bytes: &[u8]| -> io::Result<String> {
            let path = checkout.join(name);
            fs::create_dir_all(path.parent().unwrap())?;
            fs::write(&path, bytes)?;
            Ok(path.to_str().unwrap().to_owned())
        };
        let cases: &[(&str, &str, &[u8])] = &[
            ("Résumé (v2).docx", "R_sum_v2_.docx", b"PK\x03\x04fixture"),
            (
                "Quarterly deck.PPTX",
                "Quarterly_deck.PPTX",
                b"PK\x03\x04slides",
            ),
            ("model.xlsx", "model.xlsx", b"PK\x03\x04model"),
            ("bundle.zip", "bundle.zip", b"PK\x03\x04zip"),
            ("source.odt", "source.odt", b"PK\x03\x04odt"),
            ("source.ods", "source.ods", b"PK\x03\x04ods"),
            ("source.odp", "source.odp", b"PK\x03\x04odp"),
            ("日本語.csv", "_.csv", b"field,value\n"),
            ("nested/result--!_.gif", "result--__.gif", b"GIF89aframe"),
            ("still.webp", "still.webp", b"RIFF\x10\x00\x00\x00WEBPVP8 "),
            ("notes.md", "notes.md", b"# fixture\n"),
            ("notes.json", "notes.json", br#"{"fixture":true}"#),
            ("notes.log", "notes.log", b"fixture\n"),
            ("notes.yaml", "notes.yaml", b"fixture: yes\n"),
            ("notes.yml", "notes.yml", b"fixture: yes\n"),
            ("notes.tsv", "notes.tsv", b"field\tvalue\n"),
            ("plain-name_1.2.txt", "plain-name_1.2.txt", b"fixture\n"),
            ("opaque.bin", "opaque.bin", b"\x00\x01\xff"),
            ("opaque.dat", "opaque.dat", b"\x00\x12\xfe"),
        ];
        for (batch, cases) in cases.chunks(MAX_FILES).enumerate() {
            let names = cases
                .iter()
                .map(|(name, _, data)| prepare(name, data))
                .collect::<io::Result<Vec<_>>>()?;
            let paths: Vec<_> = names.iter().map(String::as_str).collect();
            let id = format!("fixture-{batch}");
            let expected = run(
                "submit",
                json!({"binding":binding,"id":id,"kind":"complete","paths":paths}),
            )?;
            assert!(
                expected.status.success(),
                "{}",
                String::from_utf8_lossy(&expected.stderr)
            );
            let expected: Value = serde_json::from_slice(&expected.stdout)?;
            let files = capture(&checkout, identity, &paths)?;
            let actual = super::super::mail_store::submit(
                &rust_root, &binding, &id, "complete", "", true, &files,
            )?;
            assert_eq!(actual["Files"], expected["Files"]);
            assert_eq!(actual["Text"], expected["Text"]);
            for (file, (_, want, bytes)) in files.iter().zip(cases) {
                assert_eq!(file.file.name, *want);
                assert_eq!(&file.data, bytes);
                let response = run(
                    "read-file",
                    json!({"binding":binding,"id":id,"FileID":file.file.id}),
                )?;
                assert!(
                    response.status.success(),
                    "{}",
                    String::from_utf8_lossy(&response.stderr)
                );
                let encoded: String = serde_json::from_slice(&response.stdout)?;
                assert_eq!(
                    base64::engine::general_purpose::STANDARD
                        .decode(encoded)
                        .unwrap(),
                    *bytes
                );
            }
        }

        let rust_mail = rust_root.join("dispatcher-mail.db");
        let go_mail = go_root.join("dispatcher-mail.db");
        let saved_rust = fs::read(&rust_mail)?;
        let saved_go = fs::read(&go_mail)?;
        let reject = |names: &[&str]| -> io::Result<()> {
            let go = run(
                "submit",
                json!({"binding":binding,"id":"refused","kind":"complete","paths":names}),
            )?;
            assert!(!go.status.success(), "{names:?}");
            let rust = capture(&checkout, identity, names).and_then(|files| {
                super::super::mail_store::submit(
                    &rust_root, &binding, "refused", "complete", "", true, &files,
                )
            });
            assert!(rust.is_err(), "{names:?}");
            assert_eq!(fs::read(&go_mail)?, saved_go);
            assert_eq!(fs::read(&rust_mail)?, saved_rust);
            Ok(())
        };
        for (denied, data) in [
            ("deck.docx", b"not a ZIP".as_slice()),
            ("archive.zip", b"plain text"),
            ("image.gif", b"%PDF-1.7"),
            ("image.webp", b"RIFF0000WEBPXX"),
            ("program.bin", b"\x7fELFfixture"),
            ("program.dat", b"MZfixture"),
            ("macro.xlsm", b"PK\x03\x04fixture"),
            ("run.sh", b"echo fixture"),
            ("page.html", b"<html>fixture</html>"),
            ("text.txt", b"text\x00binary"),
            ("text.md", b"\xff"),
            ("token.json", b"{}"),
            ("private_key.txt", b"fixture"),
            (".hidden/readme.txt", b"fixture"),
            ("zero\u{200b}.env", b"fixture"),
            ("İD_RSA.txt", b"fixture"),
            ("empty.txt", b""),
            ("no-extension", b"fixture"),
        ] {
            reject(&[&prepare(denied, data)?])?;
        }
        let collision = [
            prepare("a b.txt", b"first")?,
            prepare("A_b.TXT", b"second")?,
        ];
        reject(&[&collision[0], &collision[1]])?;
        let names = (0..=MAX_FILES)
            .map(|i| prepare(&format!("{i}.txt"), b"fixture"))
            .collect::<io::Result<Vec<_>>>()?;
        reject(&names.iter().map(String::as_str).collect::<Vec<_>>())?;
        let big = prepare("large.bin", b"x")?;
        fs::OpenOptions::new()
            .write(true)
            .open(&big)?
            .set_len(MAX_BYTES as u64)?;
        reject(&[&big, &prepare("extra.bin", b"x")?])?;
        let exact = capture(&checkout, identity, &[&big])?;
        assert_eq!(exact[0].file.size, MAX_BYTES);
        let go_exact = run(
            "submit",
            json!({"binding":binding,"id":"exact-limit","kind":"complete","paths":[big]}),
        )?;
        assert!(
            go_exact.status.success(),
            "{}",
            String::from_utf8_lossy(&go_exact.stderr)
        );
        let go_exact: Value = serde_json::from_slice(&go_exact.stdout)?;
        assert_eq!(go_exact["Files"][0], serde_json::to_value(&exact[0].file)?);
        fs::remove_dir_all(scratch)
    }
}
