//! Owner-local instructions applied to each new manager admission.
use crate::supervisor::runtime;
use rustix::fs::{AtFlags, Mode, OFlags, openat, renameat, unlinkat};
use std::fs::{File, OpenOptions};
use std::io::{self, Read, Write};
use std::os::unix::fs::{MetadataExt, OpenOptionsExt};
use std::path::Path;

const NAME: &str = "starting-prompt";
const LIMIT: usize = 1024;
const HEADER: &str = "Current owner settings (supersede earlier settings)\n";
const INPUT: &str = "\n\nCurrent input\n";

pub(crate) fn input_budget(prompt: &str) -> usize {
    if prompt.is_empty() {
        4096
    } else {
        4096usize.saturating_sub(HEADER.len() + prompt.len() + INPUT.len())
    }
}

pub(crate) fn provider_input_budget(provider: &str, prompt: &str) -> usize {
    let budget = input_budget(prompt);
    if provider == "claude" {
        budget.saturating_sub(crate::providers::codex_proof::claude::ENVELOPE_OVERHEAD)
    } else {
        budget
    }
}

fn validate(text: &str) -> io::Result<()> {
    if text.len() > LIMIT || text.chars().any(char::is_control) {
        Err(io::Error::other(
            "manager prompt must be a single line of valid text, up to 1024 bytes",
        ))
    } else {
        Ok(())
    }
}

fn directory(root: &Path, create: bool) -> io::Result<File> {
    let path = root.join("manager-settings");
    if create {
        runtime::private_dir(&path)?;
    }
    let dir = OpenOptions::new()
        .read(true)
        .custom_flags(libc::O_DIRECTORY | libc::O_NOFOLLOW)
        .open(path)?;
    let meta = dir.metadata()?;
    if meta.uid() != runtime::uid() || meta.mode() & 0o777 != 0o700 {
        return Err(io::Error::other(
            "manager settings directory must be private and owner-controlled",
        ));
    }
    Ok(dir)
}

fn current(dir: &File) -> io::Result<File> {
    let file = File::from(openat(
        dir,
        NAME,
        OFlags::RDONLY | OFlags::CLOEXEC | OFlags::NOFOLLOW | OFlags::NONBLOCK,
        Mode::empty(),
    )?);
    let meta = file.metadata()?;
    if !meta.is_file()
        || meta.nlink() != 1
        || meta.mode() & 0o777 != 0o600
        || meta.uid() != runtime::uid()
    {
        return Err(io::Error::other(
            "manager settings must be a private owner-only regular file",
        ));
    }
    Ok(file)
}

pub(crate) fn load(root: &Path) -> io::Result<String> {
    let file = match directory(root, false).and_then(|dir| current(&dir)) {
        Ok(file) => file,
        Err(err) if err.kind() == io::ErrorKind::NotFound => return Ok(String::new()),
        Err(err) => return Err(err),
    };
    let mut text = String::new();
    file.take(LIMIT as u64 + 1).read_to_string(&mut text)?;
    validate(&text)?;
    Ok(text)
}

pub(crate) fn save(root: &Path, text: &str) -> io::Result<()> {
    validate(text)?;
    let dir = directory(root, true)?;
    match current(&dir) {
        Ok(_) => {}
        Err(err) if err.kind() == io::ErrorKind::NotFound => {}
        Err(err) => return Err(err),
    }
    let temporary = format!(".save-{}", runtime::random_id()?);
    let mut file = File::from(openat(
        &dir,
        temporary.as_str(),
        OFlags::WRONLY | OFlags::CREATE | OFlags::EXCL | OFlags::CLOEXEC,
        Mode::RUSR | Mode::WUSR,
    )?);
    let result = (|| {
        file.write_all(text.trim().as_bytes())?;
        file.sync_all()?;
        renameat(&dir, temporary.as_str(), &dir, NAME)?;
        dir.sync_all()
    })();
    if result.is_err() {
        let _ = unlinkat(&dir, temporary.as_str(), AtFlags::empty());
    }
    result
}

pub(crate) fn render(prompt: &str, input: &str) -> io::Result<String> {
    validate(prompt)?;
    if input.len() > input_budget(prompt) {
        return Err(io::Error::other(
            "manager input exceeds native dispatch limit",
        ));
    }
    Ok(if prompt.is_empty() {
        input.to_owned()
    } else {
        format!("{HEADER}{prompt}{INPUT}{input}")
    })
}

pub(crate) fn saved(input: &serde_json::Value) -> io::Result<&str> {
    let prompt = match input.get("starting_prompt") {
        None => "",
        Some(value) => value
            .as_str()
            .ok_or_else(|| io::Error::other("invalid saved manager prompt"))?,
    };
    validate(prompt)?;
    Ok(prompt)
}

pub(crate) fn saved_input(input: &serde_json::Value, text: &str) -> io::Result<String> {
    render(saved(input)?, text)
}

#[cfg(test)]
mod tests {
    use super::*;
    use std::fs;
    use std::os::unix::fs::{PermissionsExt, symlink};

    #[test]
    fn go_prompt_file_and_cached_admissions_keep_their_bounds() {
        let root =
            std::env::temp_dir().join(format!("manager-prompt-{}", runtime::random_id().unwrap()));
        runtime::private_dir(&root).unwrap();
        assert_eq!(load(&root).unwrap(), "");
        assert!(!root.join("manager-settings").exists());
        let exact = "é".repeat(512);
        save(&root, &exact).unwrap();
        assert_eq!(load(&root).unwrap(), exact);
        assert!(save(&root, &(exact.clone() + "a")).is_err());
        assert!(save(&root, "invalid\nprompt").is_err());
        assert_eq!(load(&root).unwrap(), exact);
        save(&root, "  first  ").unwrap();
        let saved = serde_json::json!({"starting_prompt":load(&root).unwrap()});
        save(&root, "next").unwrap();
        assert_eq!(
            saved_input(&saved, "hello").unwrap(),
            "Current owner settings (supersede earlier settings)\nfirst\n\nCurrent input\nhello"
        );
        assert_eq!(saved_input(&serde_json::json!({}), "old").unwrap(), "old");
        assert!(saved_input(&serde_json::json!({"starting_prompt":1}), "old").is_err());
        assert!(render("next", &"x".repeat(4096)).is_err());
        let path = root.join("manager-settings").join(NAME);
        fs::set_permissions(&path, fs::Permissions::from_mode(0o644)).unwrap();
        assert!(load(&root).is_err());
        assert!(save(&root, "replace").is_err());
        fs::remove_file(&path).unwrap();
        let unrelated = root.join("unrelated");
        fs::write(&unrelated, "private").unwrap();
        symlink(&unrelated, &path).unwrap();
        assert!(load(&root).is_err());
        assert!(save(&root, "replace").is_err());
        assert_eq!(fs::read_to_string(&unrelated).unwrap(), "private");
        fs::remove_dir_all(&root).unwrap();
    }
}
