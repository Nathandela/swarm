//! Claude keeps a relocated conversation in its original project transcript.
use serde_json::Value;
use std::{
    fs, io,
    path::{Path, PathBuf},
};

pub(crate) fn transcript(home: &Path, original: &Path, id: &str) -> io::Result<(PathBuf, Value)> {
    let (path, identity) = super::transcript(home, original, id)?;
    let filename = path
        .file_name()
        .ok_or_else(|| io::Error::other("missing Claude transcript name"))?;
    let projects = home.join(".claude/projects");
    for (index, item) in fs::read_dir(&projects)?.enumerate() {
        if index >= 4096 {
            return Err(io::Error::other(
                "Claude project inventory exceeds the relocation bound",
            ));
        }
        let entry = item?;
        let kind = entry.file_type()?;
        if kind.is_symlink() {
            return Err(io::Error::other(
                "Claude project inventory contains an alias",
            ));
        }
        if !kind.is_dir() {
            continue;
        }
        let alternative = entry.path().join(filename);
        if alternative == path {
            continue;
        }
        match alternative.symlink_metadata() {
            Ok(_) => {
                return Err(io::Error::other(
                    "Claude relocation has multiple transcripts for this conversation",
                ));
            }
            Err(error) if error.kind() == io::ErrorKind::NotFound => {}
            Err(error) => return Err(error),
        }
    }
    Ok((path, identity))
}
