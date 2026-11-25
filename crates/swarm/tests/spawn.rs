mod support;

use serde_json::{Value, json};
use std::fs;
use std::io::{Read, Write};
use std::os::unix::net::{UnixListener, UnixStream};
use std::path::{Path, PathBuf};
use std::process::Command;
use std::thread;
use std::time::{Duration, Instant};

fn read(stream: &mut UnixStream) -> Value {
    let mut header = [0; 5];
    stream.read_exact(&mut header).unwrap();
    assert_eq!(header[4], 1);
    let len = u32::from_be_bytes(header[..4].try_into().unwrap()) as usize;
    assert!((2..100_000).contains(&len));
    let mut payload = vec![0; len - 1];
    stream.read_exact(&mut payload).unwrap();
    serde_json::from_slice(&payload).unwrap()
}

fn reply(stream: &mut UnixStream, value: Value) {
    let data = serde_json::to_vec(&value).unwrap();
    stream
        .write_all(&((data.len() as u32) + 1).to_be_bytes())
        .unwrap();
    stream.write_all(&[1]).unwrap();
    stream.write_all(&data).unwrap();
}

#[test]
fn handoff_snapshot_lifetime_matches_observed_outcome() {
    let tmp = std::env::temp_dir().join(format!("st-{}", std::process::id()));
    fs::create_dir(&tmp).unwrap();
    let source = tmp.join("instructions");
    fs::write(&source, b"fixture").unwrap();
    let socket = tmp.join("sock");
    let listener = UnixListener::bind(&socket).unwrap();
    for code in [
        Some("invalid_field"),
        Some("outcome_unknown"),
        Some(""),
        None,
    ] {
        let socket_server = listener.try_clone().unwrap();
        let observer = thread::spawn(move || {
            socket_server.set_nonblocking(true).unwrap();
            let deadline = Instant::now() + Duration::from_secs(5);
            let mut stream = loop {
                match socket_server.accept() {
                    Ok((stream, _)) => break stream,
                    Err(e)
                        if e.kind() == std::io::ErrorKind::WouldBlock
                            && Instant::now() < deadline =>
                    {
                        thread::sleep(Duration::from_millis(5))
                    }
                    Err(e) => panic!("candidate did not connect: {e}"),
                }
            };
            stream.set_nonblocking(false).unwrap();
            stream
                .set_read_timeout(Some(Duration::from_secs(3)))
                .unwrap();
            assert_eq!(read(&mut stream)["op"], "hello");
            reply(
                &mut stream,
                json!({"op":"hello","endpoint_id":"fixture","protocol_version":1}),
            );
            let req = read(&mut stream);
            let prompt = req["launch"]["initial_prompt"].as_str().unwrap();
            let path = PathBuf::from(
                prompt
                    .strip_prefix("Read and follow the instructions in ")
                    .unwrap()
                    .strip_suffix('.')
                    .unwrap(),
            );
            assert_eq!(fs::read(&path).unwrap(), b"fixture");
            if let Some(code) = code {
                reply(
                    &mut stream,
                    json!({"op":"error","error":"fixture refusal","error_code":code}),
                );
            }
            path
        });
        let output = Command::new(support::executable())
            .env_clear()
            .env("HOME", &tmp)
            .env("TMPDIR", &tmp)
            .env("SWARM_DAEMON_SOCK", &socket)
            .args(["spawn", "--cli", "claude", "--worktree=false", "--handoff"])
            .arg(&source)
            .output()
            .unwrap();
        let copy = observer.join().unwrap();
        assert_eq!(output.status.code(), Some(1), "{output:?}");
        assert!(copy.starts_with(Path::new(&tmp)));
        assert_eq!(copy.exists(), code != Some("invalid_field"), "{output:?}");
        assert!(source.exists());
    }
    fs::remove_dir_all(tmp).unwrap();
}
