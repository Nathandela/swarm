mod support;

use std::ffi::OsStr;
use std::fs;
use std::os::unix::ffi::OsStrExt;
use std::os::unix::fs::{PermissionsExt, symlink};
use std::path::{Path, PathBuf};
use std::process::{Command, Output};

struct Fixture(PathBuf);

impl Fixture {
    fn new() -> Self {
        let path = std::env::temp_dir().join(format!("swarm-trust-test-{}", std::process::id()));
        fs::create_dir(&path).unwrap();
        Self(path.canonicalize().unwrap())
    }

    fn run(&self, action: &str, workspace: &Path, options: &[&str]) -> Output {
        Command::new(support::executable())
            .env_clear()
            .env("HOME", &self.0)
            .env("XDG_CONFIG_HOME", self.0.join(".config"))
            .args(["setup-trust", action])
            .arg(workspace)
            .args(options)
            .output()
            .unwrap()
    }
}

impl Drop for Fixture {
    fn drop(&mut self) {
        fs::remove_dir_all(&self.0).unwrap();
    }
}

#[test]
fn consent_follows_only_the_enrolled_physical_directory() {
    let f = Fixture::new();
    let workspace = f.0.join("workspace");
    let alias = f.0.join("alias");
    fs::create_dir(&workspace).unwrap();
    symlink(&workspace, &alias).unwrap();
    let check = |path: &Path, state: &str| {
        let output = f.run("status", path, &[]);
        assert!(output.status.success());
        assert!(output.stderr.is_empty());
        assert!(
            String::from_utf8(output.stdout)
                .unwrap()
                .starts_with(&format!("Automatic startup trust is {state} for "))
        );
    };
    let raw = f.0.join(OsStr::from_bytes(b"non-utf8-\xff"));
    let out = f.run("status", &raw, &[]);
    assert!(out.status.success());
    assert_eq!(
        out.stdout,
        [
            b"Automatic startup trust is off for ",
            raw.as_os_str().as_bytes(),
            b".\n"
        ]
        .concat()
    );
    check(&workspace, "off");
    let enrollment = f.run("on", &alias, &["--profile-hooks"]);
    assert!(enrollment.status.success(), "{enrollment:?}");
    assert!(
        String::from_utf8(enrollment.stdout)
            .unwrap()
            .contains("new or changed user and plugin hooks")
    );
    check(&workspace, "on");

    let config = if cfg!(target_os = "macos") {
        "Library/Application Support"
    } else {
        ".config"
    };
    let dir = f.0.join(config).join("swarm/setup-trust");
    let marker = fs::read_dir(&dir).unwrap().next().unwrap().unwrap().path();
    assert_eq!(marker.metadata().unwrap().permissions().mode() & 0o077, 0);
    fs::set_permissions(&marker, fs::Permissions::from_mode(0o644)).unwrap();
    check(&alias, "off");
    fs::set_permissions(&marker, fs::Permissions::from_mode(0o600)).unwrap();
    check(&alias, "on");
    let moved = f.0.join("moved");
    fs::rename(&workspace, &moved).unwrap();
    fs::create_dir(&workspace).unwrap();
    check(&workspace, "off");
    fs::remove_dir(&workspace).unwrap();
    fs::rename(&moved, &workspace).unwrap();
    check(&alias, "on");

    fs::rename(&marker, dir.join("saved")).unwrap();
    symlink(OsStr::new("saved"), &marker).unwrap();
    check(&workspace, "off");
    assert!(f.run("off", &workspace, &[]).status.success());
    assert!(!marker.exists());
    assert!(dir.join("saved").is_file());
}
