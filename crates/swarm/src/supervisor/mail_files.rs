use rustix::fs::{Mode, OFlags, open, openat};
use serde::{Deserialize, Serialize};
use sha2::{Digest, Sha256};
use std::fs::File;
use std::io::{self, Read};
use std::os::unix::fs::MetadataExt;
use std::path::{Component, Path};

pub(super) const MAX_FILES: usize = 10;
pub(super) const MAX_BYTES: usize = 8 << 20;

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
        let name = relative
            .file_name()
            .and_then(|name| name.to_str())
            .filter(|name| {
                name.len() <= 128
                    && name.trim() == *name
                    && !name.contains('\\')
                    && !name.chars().any(char::is_control)
                    && seen.insert(name.to_lowercase())
            })
            .ok_or_else(|| io::Error::other("worker files: use distinct safe file names"))?;
        let parent = descend(
            root.try_clone()?,
            relative.parent().unwrap_or(Path::new("")),
        )?;
        let open_flags = OFlags::RDONLY | OFlags::NOFOLLOW | OFlags::NONBLOCK | OFlags::CLOEXEC;
        let file: File = openat(&parent, name, open_flags, Mode::empty())?.into();
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
        let content_type = content_type(name, &data)
            .ok_or_else(|| io::Error::other("worker files: expected PNG, JPEG, PDF or CSV"))?;
        let sha256 = format!("{:x}", Sha256::digest(&data));
        let id = format!("{:x}", Sha256::digest(format!("{name}\0{sha256}")));
        remaining -= data.len();
        files.push(Captured {
            file: Descriptor {
                id,
                name: name.into(),
                content_type: content_type.into(),
                sha256,
                size: data.len(),
            },
            data,
        });
    }
    Ok(files)
}

pub(super) fn verify(file: &Descriptor, data: &[u8]) -> bool {
    let name = &file.name;
    !name.is_empty()
        && name.len() <= 128
        && name.trim() == name
        && !name.contains(['/', '\\'])
        && !name.chars().any(char::is_control)
        && !data.is_empty()
        && data.len() == file.size
        && data.len() <= MAX_BYTES
        && content_type(name, data) == Some(&file.content_type)
        && file.sha256 == format!("{:x}", Sha256::digest(data))
        && file.id == format!("{:x}", Sha256::digest(format!("{name}\0{}", file.sha256)))
}

fn content_type(name: &str, data: &[u8]) -> Option<&'static str> {
    match Path::new(name)
        .extension()?
        .to_str()?
        .to_ascii_lowercase()
        .as_str()
    {
        "png" if data.starts_with(b"\x89PNG\r\n\x1a\n") => Some("image/png"),
        "jpeg" | "jpg" if data.starts_with(b"\xff\xd8\xff") => Some("image/jpeg"),
        "pdf" if data.starts_with(b"%PDF-") => Some("application/pdf"),
        "csv" if csv(data) => Some("text/csv"),
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
}
