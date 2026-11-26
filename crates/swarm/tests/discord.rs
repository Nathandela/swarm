mod support;
#[path = "support/temp.rs"]
mod test_temp;

use serde_json::{Value, json};
use sha2::{Digest, Sha256};
use std::fs::{self, DirBuilder};
use std::io::{BufRead, BufReader, Read, Write};
use std::os::unix::fs::{DirBuilderExt, PermissionsExt, symlink};
use std::os::unix::net::{UnixListener, UnixStream};
use std::path::{Path, PathBuf};
use std::process::{Command, Output};
use std::thread;

struct Place(PathBuf);

impl Place {
    fn new(label: &str) -> Self {
        let place = Self(test_temp::root().join(format!("sd-{}-{label}", std::process::id())));
        DirBuilder::new().mode(0o700).create(&place.0).unwrap();
        place
    }

    fn run(&self, args: &[&str]) -> Output {
        Command::new(support::executable())
            .args(["discord"])
            .args(args)
            .env("HOME", &self.0)
            .env("XDG_CONFIG_HOME", self.0.join("config"))
            .env("SWARM_DAEMON_STATE", &self.0)
            .env_remove("SWARM_DAEMON_SOCK")
            .env_remove("SWARM_DAEMON_LOCK")
            .env_remove("SWARM_DAEMON_LOG")
            .output()
            .unwrap()
    }

    fn private(&self, name: &str, data: impl AsRef<[u8]>) -> PathBuf {
        let file = self.0.join(name);
        fs::write(&file, data).unwrap();
        fs::set_permissions(&file, fs::Permissions::from_mode(0o600)).unwrap();
        file
    }

    fn installed(&self) -> PathBuf {
        let connector = self.0.join("existing");
        DirBuilder::new().mode(0o700).create(&connector).unwrap();
        self.private(
            "discord-active.json",
            serde_json::to_vec(&connector).unwrap(),
        );
        let token = self.private("token", "secret fixture token");
        self.private(
            "existing/config.json",
            serde_json::to_vec(&json!({
                "version": 1, "enabled": true, "mode": "personal",
                "installation_id": "historical-personal-id", "name": "Fixture",
                "application_id": "234", "owner_id": "123", "dm_channel_id": "456",
                "guild_ids": null, "channel_ids": null, "user_ids": null,
                "token_file": token, "daemon_socket": self.0.join("daemon.sock"),
                "work_dir": self.0, "manager_cli": "codex",
                "supervisor": {
                    "state_dir": self.0, "lock": self.0.join("daemon.lock"),
                    "log": self.0.join("daemon.log")
                }
            }))
            .unwrap(),
        );
        connector
    }
}

impl Drop for Place {
    fn drop(&mut self) {
        let _ = fs::remove_dir_all(&self.0);
    }
}

fn json_output(output: Output) -> Value {
    assert!(output.status.success(), "{:?}", output);
    serde_json::from_slice(&output.stdout).unwrap()
}

#[test]
fn isolated_legacy_enrollment_replays_and_disconnects() {
    let place = Place::new("config");
    let token = place.private("token", "  token fixture  \n");
    let connector = place.0.join("custom");
    let args = [
        "connect",
        "--state-dir",
        connector.to_str().unwrap(),
        "--name",
        "Local bot",
        "--application-id",
        "123",
        "--owner-id",
        "456",
        "--token-file",
        token.to_str().unwrap(),
        "--workdir",
        place.0.to_str().unwrap(),
    ];
    let first = json_output(place.run(&args));
    let replay = json_output(place.run(&args));
    assert_eq!(replay, first);
    let changed = place.run(&["connect", "--owner-id", "999"]);
    assert_eq!(changed.status.code(), Some(1));
    assert!(String::from_utf8_lossy(&changed.stderr).contains("requires a new --state-dir"));

    assert_eq!(first["installation_id"].as_str().unwrap().len(), 32);
    assert_eq!(first["installed"], false);
    let status = json_output(place.run(&["status"]));
    assert_eq!(status["connected"], false);
    assert_eq!(status["installation_id"], first["installation_id"]);
    assert!(!format!("{status}").contains("token"));
    assert!(!place.run(&["run"]).status.success());
    assert!(place.run(&["disconnect"]).status.success());
    assert!(place.run(&["disconnect"]).status.success());
    let cfg: Value =
        serde_json::from_slice(&fs::read(connector.join("config.json")).unwrap()).unwrap();
    assert_eq!(cfg["enabled"], false);
    assert!(!format!("{cfg}").contains("fixture"));
    assert_eq!(
        fs::metadata(connector.join("config.json"))
            .unwrap()
            .permissions()
            .mode()
            & 0o777,
        0o600
    );
}

#[test]
fn existing_personal_state_stays_readable_and_custody_is_preserved() {
    let place = Place::new("existing");
    let connector = place.installed();
    let cfg = fs::read(connector.join("config.json")).unwrap();
    let status = json_output(place.run(&["status"]));
    assert_eq!(
        status,
        json!({
            "installation_id": "historical-personal-id", "name": "Fixture",
            "mode": "personal", "connected": false, "workers": null, "project_access": "enrolled"
        })
    );
    place.private("token", "invalid\nfixture");
    let doctor = place.run(&["doctor"]);
    assert_eq!(doctor.status.code(), Some(1));
    let checks: Value = serde_json::from_slice(&doctor.stdout).unwrap();
    assert!(
        checks
            .as_array()
            .unwrap()
            .iter()
            .any(|f| f["check"] == "bot credential" && f["status"] == "fail")
    );
    assert!(
        !String::from_utf8(doctor.stdout)
            .unwrap()
            .contains("secret fixture token")
    );
    assert!(!place.run(&["disconnect"]).status.success());
    assert_eq!(fs::read(connector.join("config.json")).unwrap(), cfg);
}

fn read_control(stream: &mut UnixStream) -> (String, Value) {
    let mut reader = BufReader::new(stream);
    let mut first = String::new();
    reader.read_line(&mut first).unwrap();
    let mut len = 0;
    loop {
        let mut line = String::new();
        assert!(reader.read_line(&mut line).unwrap() > 0);
        if line == "\r\n" {
            break;
        }
        if let Some(value) = line.strip_prefix("Content-Length: ") {
            len = value.trim().parse().unwrap();
        }
    }
    let mut data = vec![0; len];
    reader.read_exact(&mut data).unwrap();
    (first, serde_json::from_slice(&data).unwrap())
}

#[test]
fn private_local_control_passes_the_bound_receipt_and_refuses_symlink() {
    let place = Place::new("bridge");
    let connector = place.installed();
    let socket = connector.join("bridge.sock");
    let listener = UnixListener::bind(&socket).unwrap();
    fs::set_permissions(&socket, fs::Permissions::from_mode(0o600)).unwrap();
    let server = thread::spawn(move || {
        let (mut stream, _) = listener.accept().unwrap();
        let (first, request) = read_control(&mut stream);
        let id = Sha256::digest(b"historical-personal-id");
        assert_eq!(
            first,
            format!("POST /installation-v1/{id:x}/resume-manual HTTP/1.0\r\n")
        );
        assert_eq!(
            request,
            json!({
                "discussion": "logical-discussion", "session": "historic/session",
                "instance": "instance", "external_input": 3,
            })
        );
        let body = br#"{"resumed":true}"#;
        write!(
            stream,
            "HTTP/1.0 200 OK\r\nContent-Length: {}\r\n\r\n",
            body.len()
        )
        .unwrap();
        stream.write_all(body).unwrap();
    });
    let mut args = [
        "resume-manual",
        "logical-discussion",
        "--session",
        "historic/session",
        "--instance",
        "instance",
        "--external-input",
        "3",
    ];
    assert_eq!(json_output(place.run(&args)), json!({"resumed":true}));
    server.join().unwrap();
    args[7] = "-1";
    assert_eq!(place.run(&args).status.code(), Some(2));
    let other = connector.join("other.sock");
    fs::rename(&socket, &other).unwrap();
    symlink(&other, &socket).unwrap();
    let result = place.run(&["exclude", "target"]);
    assert_eq!(result.status.code(), Some(1));
    assert!(String::from_utf8_lossy(&result.stderr).contains("unsafe local socket"));
}

#[test]
fn mutation_refuses_a_wrong_unsupported_or_disabled_installation() {
    let place = Place::new("bound-refusal");
    let connector = place.installed();
    let socket = connector.join("bridge.sock");
    let listener = UnixListener::bind(&socket).unwrap();
    fs::set_permissions(&socket, fs::Permissions::from_mode(0o600)).unwrap();
    let server = thread::spawn(move || {
        for status in ["403 Forbidden", "405 Method Not Allowed"] {
            let (mut stream, _) = listener.accept().unwrap();
            let (first, request) = read_control(&mut stream);
            assert_ne!(first, "POST /resume-manual HTTP/1.0\r\n");
            let serving_id = Sha256::digest(b"different-installation");
            assert_ne!(
                first,
                format!("POST /installation-v1/{serving_id:x}/resume-manual HTTP/1.0\r\n")
            );
            assert_eq!(request["external_input"], 3);
            let body = if status.starts_with("403") {
                br#"{"error":"discord: installation does not match"}"#.as_slice()
            } else {
                br#"{"error":"discord: unknown operation or method"}"#.as_slice()
            };
            write!(
                stream,
                "HTTP/1.0 {status}\r\nContent-Length: {}\r\n\r\n",
                body.len()
            )
            .unwrap();
            stream.write_all(body).unwrap();
        }
    });
    let args = [
        "resume-manual",
        "d",
        "--session",
        "s",
        "--instance",
        "i",
        "--external-input",
        "3",
    ];
    for detail in [
        "installation does not match",
        "lacks installation-bound controls",
    ] {
        let output = place.run(&args);
        assert_eq!(output.status.code(), Some(1));
        assert!(String::from_utf8_lossy(&output.stderr).contains(detail));
    }
    server.join().unwrap();
    let file = connector.join("config.json");
    let mut cfg: Value = serde_json::from_slice(&fs::read(&file).unwrap()).unwrap();
    cfg["enabled"] = json!(false);
    fs::write(file, serde_json::to_vec(&cfg).unwrap()).unwrap();
    let output = place.run(&args);
    assert_eq!(output.status.code(), Some(1));
    assert!(String::from_utf8_lossy(&output.stderr).contains("connector is disabled"));
}

// The private PATH contains the complete fake service manager; this fixture cannot address the installed job.
#[test]
fn service_activation_restores_the_prior_route_and_only_disconnects_the_selected_place() {
    let fixture = Place::new("service-install");
    let tools = fixture.0.join("tools");
    fs::create_dir(&tools).unwrap();
    let binary = tools.join(if cfg!(target_os = "macos") {
        "launchctl"
    } else {
        "systemctl"
    });
    fs::write(&binary, r#"#!/bin/sh
printf '%s\n' "$*" >> "$HOME/operations"
case "$1:$2" in
  print:*) if [ -f "$HOME/active" ]; then exit 0; fi; echo 'Could not find service' >&2; exit 113 ;;
  bootout:*) /bin/rm -f "$HOME/active" ;;
  bootstrap:*) if [ -f "$HOME/fail-once" ]; then /bin/rm "$HOME/fail-once"; exit 1; fi; : > "$HOME/active" ;;
  --user:is-enabled) if [ -f "$HOME/enabled" ]; then echo enabled; else echo disabled; exit 1; fi ;;
  --user:is-active) if [ -f "$HOME/active" ]; then echo active; else echo inactive; exit 3; fi ;;
  --user:daemon-reload) ;;
  --user:enable) : > "$HOME/enabled" ;;
  --user:disable) /bin/rm -f "$HOME/enabled" ;;
  --user:restart) if [ -f "$HOME/fail-once" ]; then /bin/rm "$HOME/fail-once"; exit 1; fi; : > "$HOME/active" ;;
  --user:stop) /bin/rm -f "$HOME/active" ;;
  *) exit 100 ;;
esac
"#).unwrap();
    fs::set_permissions(&binary, fs::Permissions::from_mode(0o700)).unwrap();
    let token = fixture.private("token", "synthetic-service-fixture");
    let one = fixture.0.join("one");
    let two = fixture.0.join("two");
    let unit = fixture.0.join(if cfg!(target_os = "macos") {
        "Library/LaunchAgents/com.swarm.discord.plist"
    } else {
        ".config/systemd/user/swarm-discord.service"
    });
    let run = |args: &[&str]| {
        Command::new(support::executable())
            .env_clear()
            .env("HOME", &fixture.0)
            .env("PATH", &tools)
            .env("SWARM_DAEMON_STATE", &fixture.0)
            .arg("discord")
            .args(args)
            .output()
            .unwrap()
    };
    let install = |dir: &Path| {
        run(&[
            "connect",
            "--install",
            "--mode",
            "legacy",
            "--name",
            "Install fixture",
            "--application-id",
            "123",
            "--owner-id",
            "456",
            "--token-file",
            token.to_str().unwrap(),
            "--workdir",
            fixture.0.to_str().unwrap(),
            "--state-dir",
            dir.to_str().unwrap(),
        ])
    };
    let first = json_output(install(&one));
    assert_eq!(first["installed"], true);
    assert!(fixture.0.join("active").exists());
    let original = fs::read_to_string(&unit).unwrap();
    if std::env::var_os("SWARM_NATIVE_UNIT_PARSER").is_some() {
        let mut parser = if cfg!(target_os = "macos") {
            let mut parser = Command::new("/usr/bin/plutil");
            parser.args(["-lint", "--"]);
            parser
        } else {
            let mut parser = Command::new("/usr/bin/systemd-analyze");
            parser.args(["--man=no", "verify"]);
            parser
        };
        let result = parser
            .arg(&unit)
            .env("HOME", &fixture.0)
            .env("LC_ALL", "C")
            .output()
            .unwrap();
        assert!(
            result.status.success(),
            "{}",
            String::from_utf8_lossy(&result.stderr)
        );
    }

    assert!(original.contains(one.to_str().unwrap()) && !original.contains("token"));
    fs::set_permissions(&unit, fs::Permissions::from_mode(0o640)).unwrap();
    fs::write(fixture.0.join("fail-once"), []).unwrap();
    assert!(!install(&two).status.success());
    assert_eq!(fs::read_to_string(&unit).unwrap(), original);
    assert_eq!(
        fs::metadata(&unit).unwrap().permissions().mode() & 0o777,
        0o640
    );
    assert!(fixture.0.join("active").exists());

    assert!(
        run(&["disconnect", "--state-dir", two.to_str().unwrap()])
            .status
            .success()
    );
    assert_eq!(fs::read_to_string(&unit).unwrap(), original);
    assert!(fixture.0.join("active").exists());
    assert!(
        run(&["disconnect", "--state-dir", one.to_str().unwrap()])
            .status
            .success()
    );
    assert!(!unit.exists() && !fixture.0.join("active").exists());

    fs::write(&unit, "foreign unit").unwrap();
    assert!(!install(&one).status.success());
    assert_eq!(fs::read_to_string(&unit).unwrap(), "foreign unit");
}

#[test]
fn preview_vm_setting_requires_an_explicit_verified_connection() {
    let place = Place::new("vm-preview");
    let connector = place.installed();
    let path = connector.join("config.json");
    let mut config: Value = serde_json::from_slice(&fs::read(&path).unwrap()).unwrap();
    config["project_access"] = json!("vm");
    fs::write(&path, serde_json::to_vec(&config).unwrap()).unwrap();
    place.private(
        "existing/.rust-offline-owner.json",
        b"\"historical-personal-id\"",
    );
    place.private("existing/discord-adoption.json", b"{\"no_effects\":true}");
    let grant = connector.join(".rust-vm-manager-grant-v1.json");
    assert_eq!(
        json_output(place.run(&["status"]))["project_access"],
        "enrolled"
    );
    assert!(!grant.exists());
    assert!(
        place
            .run(&[
                "connect",
                "--state-dir",
                connector.to_str().unwrap(),
                "--project-access",
                "vm"
            ])
            .status
            .success()
    );
    assert_eq!(json_output(place.run(&["status"]))["project_access"], "vm");
    assert!(grant.is_file());
    let mut changed: Value = serde_json::from_slice(&fs::read(&path).unwrap()).unwrap();
    let other = place.0.join("different-project");
    fs::create_dir(&other).unwrap();
    changed["work_dir"] = json!(other.canonicalize().unwrap());
    fs::write(&path, serde_json::to_vec(&changed).unwrap()).unwrap();
    assert_eq!(
        json_output(place.run(&["status"]))["project_access"],
        "enrolled"
    );
}

#[test]
fn new_managed_connection_retains_the_historical_vm_default() {
    let place = Place::new("vm-new");
    let token = place.private("token", "synthetic fixture token");
    let connector = place.0.join("new");
    let configured = json_output(place.run(&[
        "connect",
        "--state-dir",
        connector.to_str().unwrap(),
        "--mode",
        "teammate",
        "--name",
        "Fixture",
        "--application-id",
        "123",
        "--owner-id",
        "456",
        "--guild",
        "234",
        "--token-file",
        token.to_str().unwrap(),
        "--workdir",
        place.0.to_str().unwrap(),
    ]));
    assert_eq!(configured["project_access"], "vm");
    assert!(connector.join(".rust-vm-manager-grant-v1.json").is_file());
    assert_eq!(json_output(place.run(&["status"]))["project_access"], "vm");
}

#[test]
fn doctor_confirms_native_manager_login_only_for_the_selected_executable() {
    let fixture = Place::new("manager-doctor");
    let connector = fixture.installed();
    fs::remove_file(fixture.0.join("token")).unwrap();
    let bin = fixture.0.join("bin");
    fs::create_dir(&bin).unwrap();
    let executable = bin.join("codex");
    fs::write(&executable, b"#!/bin/sh\ncase \"$*\" in\n--version) echo 'codex-cli 0.170.0';;\n'login status') echo private-fixture-details; echo checked >> \"$HOME/login-probes\";;\n*) exit 1;;\nesac\n").unwrap();
    fs::set_permissions(&executable, fs::Permissions::from_mode(0o700)).unwrap();
    let check = || {
        let output = Command::new(support::executable())
            .args(["discord", "doctor", "--state-dir"])
            .arg(&connector)
            .env_clear()
            .env("PATH", &bin)
            .env("HOME", &fixture.0)
            .env("SWARM_DAEMON_STATE", &fixture.0)
            .output()
            .unwrap();
        assert_eq!(output.status.code(), Some(1)); // The deliberately absent bot credential.
        let text = String::from_utf8(output.stdout).unwrap();
        assert!(!text.contains("private-fixture-details"));
        serde_json::from_str::<Vec<Value>>(&text).unwrap()
    };
    let rows = check();
    assert!(
        rows.iter()
            .any(|r| r["check"] == "native manager CLI" && r["detail"] == "codex 0.170.0")
    );
    assert!(
        rows.iter()
            .any(|r| r["check"] == "native provider login" && r["status"] == "ok")
    );
    fixture.private("daemon.env", b"PATH=/no-provider-here\n");
    let rows = check();
    assert!(
        rows.iter()
            .any(|r| r["check"] == "native manager CLI" && r["status"] == "fail")
    );
    assert!(!rows.iter().any(|r| r["check"] == "native provider login"));
    assert_eq!(
        fs::read_to_string(fixture.0.join("login-probes")).unwrap(),
        "checked\n"
    );
}
