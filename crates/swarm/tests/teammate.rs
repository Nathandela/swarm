mod support;

use serde_json::{Value, json};
use std::os::unix::fs::PermissionsExt;
use std::{
    fs,
    io::{Read, Write},
    os::unix::net::UnixListener,
    path::Path,
    process::{Command, Output},
};

fn frame(stream: &mut impl Read) -> Value {
    let mut size = [0; 4];
    stream.read_exact(&mut size).unwrap();
    let mut buffer = vec![0; u32::from_be_bytes(size) as usize];
    stream.read_exact(&mut buffer).unwrap();
    assert_eq!(buffer[0], 1);
    serde_json::from_slice(&buffer[1..]).unwrap()
}
fn write_frame(stream: &mut impl Write, body: Value) {
    let body = serde_json::to_vec(&body).unwrap();
    stream
        .write_all(&((body.len() + 1) as u32).to_be_bytes())
        .unwrap();
    stream.write_all(&[1]).unwrap();
    stream.write_all(&body).unwrap();
}
fn cli(path: &Path, args: &[&str]) -> Output {
    Command::new(support::executable())
        .arg("teammate")
        .args(args)
        .env_clear()
        .env("SWARM_WORKER_MAILBOX_SOCKET", path)
        .env("SWARM_SESSION_ID", "worker")
        .env("SWARM_WORKER_MAILBOX_PROOF", "a".repeat(64))
        .output()
        .unwrap()
}
#[test]
fn ordinary_teammate_cli_uses_exact_go_wire_receipts_and_rejects_uncorrelated_ack() {
    let dir = Path::new("/tmp").join(format!("mail-wire-{}", std::process::id()));
    fs::create_dir(&dir).unwrap();
    fs::set_permissions(&dir, fs::Permissions::from_mode(0o700)).unwrap();
    let path = dir.join("socket");
    let listener = UnixListener::bind(&path).unwrap();
    let server = std::thread::spawn(move || {
        for (position, action) in ["message", "receipt", "reply", "receipt"]
            .into_iter()
            .enumerate()
        {
            let (mut stream, _) = listener.accept().unwrap();
            let hello = frame(&mut stream);
            assert_eq!(
                hello["capabilities"],
                json!(["dispatcher-worker-mail", "dispatcher-worker-mail-files"])
            );
            write_frame(
                &mut stream,
                json!({"op":"hello","endpoint_id":"fixture","protocol_version":1,
                "capabilities":["dispatcher-worker-mail"]}),
            );
            let request = frame(&mut stream);
            assert_eq!(request["op"], "worker_mail");
            assert_eq!(request["session_id"], "fixture/worker");
            assert_eq!(request["endpoint_id"], "fixture");
            let mail = &request["worker_mail"];
            assert_eq!(mail["proof"], "a".repeat(64));
            assert_eq!(mail["id"], "original-id");
            assert_eq!(mail["action"], action);
            if action == "message" {
                assert_eq!(mail["text"], "Which path?");
                assert_eq!(mail["kind"], "needs-input");
            }
            let mut reply = json!({"op":"worker_mail","endpoint_id":"fixture","session_id":"fixture/worker",
                "worker_mail_receipt":{"id":"original-id","state":"queued"}});
            if action == "reply" {
                reply["worker_mail_receipt"] =
                    json!({"id":"original-id","state":"available","text":"Use src"});
            }
            if position == 3 {
                reply["worker_mail_receipt"]["id"] = json!("unrelated-id");
            }
            write_frame(&mut stream, reply);
        }
    });
    for (position, args) in [
        vec![
            "message",
            "--id",
            "original-id",
            "--kind",
            "needs-input",
            "--text",
            "Which path?",
        ],
        vec!["receipt", "--id", "original-id"],
        vec!["reply", "--id", "original-id", "--wait", "2s"],
        vec!["receipt", "--id", "original-id"],
    ]
    .into_iter()
    .enumerate()
    {
        let out = cli(&path, &args);
        if position == 3 {
            assert!(!out.status.success());
            assert!(String::from_utf8_lossy(&out.stderr).contains("uncorrelated receipt"));
            continue;
        }
        assert!(out.status.success(), "{out:?}");
        let wire = serde_json::from_slice::<Value>(&out.stdout).unwrap();
        assert_eq!(wire["id"], "original-id");
        if args[0] == "reply" {
            assert_eq!(wire["text"], "Use src");
        }
    }
    server.join().unwrap();
    fs::remove_dir_all(dir).unwrap();
}
