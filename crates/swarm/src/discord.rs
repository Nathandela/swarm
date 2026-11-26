mod worker_input;
pub(crate) use worker_input::{
    lock_connector_worker_input, verify_connector_worker_input, worker_input_guild_available,
    worker_input_installation,
};
mod call_runtime;
mod call_speech;

mod call_floor;
mod call_helper;

mod call_media;
mod call_origin;
mod call_room;

mod config;
mod dispatcher;
mod gateway;
mod guild;
mod local;
mod mail_wake;
mod manager;
pub(crate) mod provenance;
mod receipt;
mod rest;
mod service;
mod worker;
mod worker_proof;

use crate::ipc;
use config::{Config, Place, invalid};
use serde_json::{Value, json};
use std::collections::BTreeMap;
use std::io::{self, Read};
use std::path::Path;

const USAGE: &str = "swarm discord connect|migrate-supervisor|run|status|doctor|stop-call|resume-manual <discussion> --session ID --instance INSTANCE --external-input COUNT|disconnect|exclude <target>|include <target>|context <ref>|reply <ref> --body-file <file>|propose <ref> --candidate-ids '[\"target\"]'";

pub(crate) fn manager_nonce(
    discussion: &str,
    request: &Value,
    revision: &Value,
    binding: &Value,
    external: &Value,
) -> io::Result<String> {
    use sha2::{Digest, Sha256};
    Ok(format!(
        "{:x}",
        Sha256::digest(serde_json::to_vec(&json!([
            discussion, request, revision, binding, external
        ]))?)
    ))
}

/// Re-read the active, owned personal enrollment at the supervisor admission boundary.
pub(crate) fn native_manager_enrollment() -> io::Result<Value> {
    let place = Place::resolve(None)?;
    let cfg = place.load()?;
    place.ensure_owned(&cfg)?;
    if !cfg.enabled()
        || cfg.text("mode") != "personal"
        || cfg.text("project_access") == "vm"
        || cfg.text("manager_cli") != "codex"
    {
        return Err(invalid(
            "qualified Rust manager requires the active enrolled personal Codex place",
        ));
    }
    let owner = manager::Manager::new(&cfg)?;
    let bridge = std::fs::canonicalize(&place.dir)?.join("manager-v1");
    Ok(
        json!({"launch":owner.launch_request(&bridge.join("bridge.sock"), &bridge.join("bridge.auth")),
        "installation_id":cfg.text("installation_id"), "application_id":cfg.text("application_id"), "owner_id":cfg.text("owner_id"),
        "channel_id":cfg.text("dm_channel_id"),"supervisor_socket":cfg.text("daemon_socket")}),
    )
}

pub(crate) fn connector_intent(scope: &Value) -> io::Result<Value> {
    let dir = Path::new(
        scope["launch"]["manager"]["auth_file"]
            .as_str()
            .unwrap_or_default(),
    )
    .to_owned();
    let place = dir
        .parent()
        .and_then(Path::parent)
        .ok_or_else(|| invalid("invalid connector custody directory"))?;
    let data = crate::supervisor::runtime::read_json(&place.join("rust-text-v1.json"))?;
    let identity = &data["identity"];
    if !matches!(data["version"].as_u64(), Some(2 | 3))
        || identity[0] != scope["installation_id"]
        || identity[1] != scope["application_id"]
        || identity[2] != scope["owner_id"]
        || identity[3] != scope["channel_id"]
        || identity[4] != scope["supervisor_socket"]
        || data["manager"]["generation"] != 1
        || data["manager"]["launch"] != scope["launch"]
    {
        return Err(invalid(
            "connector intent does not match the enrolled manager",
        ));
    }
    Ok(data)
}

pub(crate) fn lock_manager_mail_effect(scope: &Value) -> io::Result<std::fs::File> {
    let place = Path::new(ipc::field(&scope["launch"]["manager"], "auth_file"))
        .parent()
        .and_then(Path::parent)
        .ok_or_else(|| invalid("missing manager mailbox custody directory"))?;
    crate::supervisor::runtime::lock(&place.join(".rust-manager-mail-effect.lock"))
}

pub(crate) fn verify_connector_input(
    scope: &Value,
    operation: &str,
    composer: &Value,
    supervisor_root: Option<&Path>,
) -> io::Result<()> {
    let data = connector_intent(scope)?;
    let manager = &data["manager"];
    let input = &manager["input"];
    if !input["worker_wake"].is_null() {
        if input["operation"] != operation
            || input["composer"] != *composer
            || manager["session"] != composer["session"]
        {
            return Err(invalid(
                "worker wake does not match the exact connector cutover",
            ));
        }
        let root = supervisor_root
            .ok_or_else(|| invalid("worker wake requires local supervisor custody"))?;
        return mail_wake::verify(&data, scope, root, composer);
    }
    let id = input["source_id"]
        .as_str()
        .filter(|id| config::snowflake(id) || call_origin::key(id))
        .ok_or_else(|| invalid("connector has no persisted manager cutover"))?;
    let source = &data["messages"][id];
    let origin = &composer["origin"];
    let voice = call_origin::key(id);
    if voice {
        call_origin::validate(scope, origin, composer["text"].as_str().unwrap_or_default())?;
        if source["voice_origin"] != *origin || origin["request_id"] != id {
            return Err(invalid("voice origin does not match the persisted cutover"));
        }
    }
    if input["operation"] != operation
        || input["composer"] != *composer
        || manager["session"] != composer["session"]
        || source["state"] != "sending"
        || source["text"] != composer["text"]
        || source["revision"] != composer["origin"]["revision"]
        || source["operation_id"] != composer["origin"]["request_id"]
        || !voice
            && (!origin["voice"].is_null()
                || source["route"]["message_id"] != id
                || composer["origin"]["message_id"] != id)
        || source["native_input_id"] != composer["manager_admission"]["nonce"]
        || source["native_binding"] != composer["manager_admission"]["binding"]
    {
        return Err(invalid(
            "semantic request does not match the exact connector cutover",
        ));
    }
    if voice {
        let place = Path::new(
            scope["launch"]["manager"]["auth_file"]
                .as_str()
                .unwrap_or_default(),
        )
        .parent()
        .and_then(Path::parent)
        .ok_or_else(|| invalid("invalid connector custody directory"))?;
        call_runtime::verify_live(place, origin).map_err(|_| invalid(call_runtime::WITHDRAWN))?;
    }
    Ok(())
}

pub(crate) fn native_input_enabled() -> bool {
    Place::resolve(None)
        .and_then(|place| place.load())
        .is_ok_and(|config| config.enabled())
}

pub(crate) fn run(args: &[String]) -> u8 {
    if args
        .first()
        .is_some_and(|arg| arg == "-h" || arg == "--help")
    {
        println!("{USAGE}");
        return 0;
    }
    match Request::parse(args) {
        Ok(request) => match execute(&request) {
            Ok(ok) => u8::from(!ok),
            Err(err) => {
                eprintln!("discord: {err}");
                1
            }
        },
        Err(err) => {
            eprintln!("discord: {err}\n{USAGE}");
            2
        }
    }
}

struct Request<'a> {
    command: &'a str,
    id: &'a str,
    options: BTreeMap<&'a str, Vec<&'a str>>,
}

impl<'a> Request<'a> {
    fn parse(args: &'a [String]) -> io::Result<Self> {
        let [command, rest @ ..] = args else {
            return Err(invalid("specify a command"));
        };
        let mut rest = rest.iter().map(String::as_str);
        let mut flags: &[&str] = &[];
        let allowed: &[&str] = match command.as_str() {
            "connect" => {
                flags = &["install", "all-channels"];
                &[
                    "name",
                    "application-id",
                    "owner-id",
                    "token-file",
                    "workdir",
                    "guild",
                    "channel",
                    "allow-user",
                    "mode",
                    "manager",
                    "project-access",
                    "project",
                    "receptionist-cli",
                    "receptionist-model",
                ]
            }
            "migrate-supervisor" => {
                flags = &["dry-run"];
                &["from-state-dir", "from-socket", "from-lock", "from-log"]
            }
            "run" => {
                flags = &["unattended"];
                &[]
            }
            "status" | "stop-call" | "disconnect" | "include" | "exclude" => &[],
            "doctor" => &["channel"],
            "context" => &["offset"],
            "reply" => &["body-file", "publication-id"],
            "propose" => &["candidate-ids"],
            "resume-manual" => &["session", "instance", "external-input"],
            _ => return Err(invalid("unknown command")),
        };
        let id = if matches!(
            command.as_str(),
            "include" | "exclude" | "context" | "reply" | "propose" | "resume-manual"
        ) {
            rest.next()
                .filter(|id| !id.is_empty() && !id.starts_with('-'))
                .ok_or_else(|| invalid("need a reference or target ID first"))?
        } else {
            ""
        };
        let mut options: BTreeMap<&str, Vec<&str>> = BTreeMap::new();
        while let Some(arg) = rest.next() {
            let arg = arg
                .strip_prefix('-')
                .ok_or_else(|| invalid("unexpected positional argument"))?;
            let arg = arg.strip_prefix('-').unwrap_or(arg);
            let (name, value) = arg
                .split_once('=')
                .map_or((arg, None), |(name, value)| (name, Some(value)));
            let value = if flags.contains(&name) {
                match value {
                    None | Some("true") => "true",
                    Some("false") => "false",
                    _ => return Err(invalid("invalid boolean option")),
                }
            } else if name == "state-dir" || allowed.contains(&name) {
                value
                    .or_else(|| rest.next())
                    .ok_or_else(|| invalid("missing option value"))?
            } else {
                return Err(invalid("unknown option"));
            };
            let values = options.entry(name).or_default();
            if !matches!(name, "guild" | "channel" | "allow-user" | "project") {
                values.clear();
            }
            values.push(value);
        }
        let req = Self {
            command,
            id,
            options,
        };
        req.validate()?;
        Ok(req)
    }

    fn get(&self, flag: &str) -> Option<&str> {
        self.values(flag).last().copied()
    }

    fn values(&self, flag: &str) -> &[&str] {
        self.options.get(flag).map_or(&[], Vec::as_slice)
    }

    fn on(&self, flag: &str) -> bool {
        self.get(flag) == Some("true")
    }

    fn validate(&self) -> io::Result<()> {
        match self.command {
            "context"
                if self
                    .get("offset")
                    .is_some_and(|n| n.parse::<i64>().map_or(true, |n| n < 0)) =>
            {
                Err(invalid("invalid offset"))
            }
            "reply"
                if self.get("body-file").unwrap_or_default().is_empty()
                    || self.get("publication-id") == Some("") =>
            {
                Err(invalid("missing body file/publication ID"))
            }
            "propose" => {
                self.candidates()?;
                Ok(())
            }
            "resume-manual"
                if !["session", "instance"]
                    .iter()
                    .all(|flag| self.get(flag).is_some_and(|v| !v.is_empty()))
                    || self
                        .get("external-input")
                        .is_none_or(|n| n.parse::<u64>().is_err()) =>
            {
                Err(invalid(
                    "specify --session, --instance and --external-input from status",
                ))
            }
            _ => Ok(()),
        }
    }

    fn candidates(&self) -> io::Result<Vec<String>> {
        let raw = self.get("candidate-ids").unwrap_or_default();
        if raw.len() <= 8192
            && let Ok(ids) = serde_json::from_str::<Vec<String>>(raw)
            && (1..=10).contains(&ids.len())
            && ids.iter().all(|id| !id.is_empty() && id.len() <= 512)
        {
            return Ok(ids);
        }
        Err(invalid(
            "--candidate-ids must contain 1 to 10 valid target IDs as JSON",
        ))
    }
}

fn execute(req: &Request<'_>) -> io::Result<bool> {
    let place = Place::resolve(req.get("state-dir"))?;
    match req.command {
        "connect" => return connect(req, place),
        "disconnect" => {
            place.disconnect()?;
            println!("Discord connector disconnected.");
            return Ok(true);
        }
        "migrate-supervisor" => {
            return Err(io::Error::other(
                "supervisor migration is unavailable in this Rust build",
            ));
        }
        _ => {}
    }
    let cfg = place.load()?;
    match req.command {
        "run" => service::run(&place, &cfg)?,
        "status" => {
            let status = local::call(&place.dir, "status", "", None)
                .ok()
                .filter(|status| status["installation_id"] == cfg.text("installation_id"))
                .unwrap_or_else(|| cfg.status());
            println!("{}", serde_json::to_string_pretty(&status)?);
        }
        "stop-call" => {
            call_runtime::stop(&place.dir, cfg.text("installation_id"))?;
            println!("Discord calls suspended until restart.");
        }
        "doctor" => return doctor(&place, &cfg, req.get("channel")),
        _ => {
            let mut query = String::new();
            let request = match req.command {
                "context" => {
                    query = format!(
                        "?offset={}&ref={}",
                        req.get("offset").unwrap_or("0").parse::<i64>().unwrap(),
                        local::escape(req.id)
                    );
                    None
                }
                "include" | "exclude" => Some(json!({"target_id": req.id})),
                "reply" => {
                    let mut body = String::new();
                    std::fs::File::open(req.get("body-file").unwrap())?
                        .take(65537)
                        .read_to_string(&mut body)?;
                    if body.is_empty() || body.len() > 65536 {
                        return Err(invalid("reply body must be 1 to 65536 bytes"));
                    }
                    Some(
                        json!({"ref": req.id, "id": req.get("publication-id").unwrap_or("final"), "body": body}),
                    )
                }
                "propose" => Some(json!({"ref": req.id, "candidate_ids": req.candidates()?})),
                "resume-manual" => Some(
                    json!({"discussion": req.id, "session": req.get("session"), "instance": req.get("instance"), "external_input": req.get("external-input").unwrap().parse::<u64>().unwrap()}),
                ),
                _ => unreachable!(),
            };
            if request.is_some() && !cfg.enabled() {
                return Err(io::Error::other("connector is disabled"));
            }
            let request = request
                .as_ref()
                .map(|body| (cfg.text("installation_id"), body));
            let result = local::call(&place.dir, req.command, &query, request)?;
            println!("{}", serde_json::to_string_pretty(&result)?);
        }
    }
    Ok(true)
}

fn connect(req: &Request<'_>, place: Place) -> io::Result<bool> {
    if req.on("install") {
        return Err(io::Error::other(
            "service activation is unavailable in this Rust build",
        ));
    }
    let cfg = place.configure(req)?;
    println!(
        "{}",
        serde_json::to_string_pretty(&json!({
            "name": cfg.text("name"), "installation_id": cfg.text("installation_id"),
            "state_dir": place.dir, "installed": false,
            "next": format!("swarm discord run --state-dir '{}'", place.dir.to_string_lossy().replace('\'', "'\\''"))
        }))?
    );
    Ok(true)
}

fn doctor(place: &Place, cfg: &Config, channel: Option<&str>) -> io::Result<bool> {
    if !cfg.managed() {
        return Err(io::Error::other(
            "this place uses the legacy connector; native manager checks require --mode teammate or --mode personal",
        ));
    }
    let mut findings = Vec::new();
    let mut check = |name: &str, result: io::Result<&str>| {
        findings.push(match result {
            Ok(detail) => json!({"check": name, "status": "ok", "detail": detail}),
            Err(err) => json!({"check": name, "status": "fail", "detail": err.to_string()}),
        });
    };
    let check_path = |root: &str| {
        if Path::new(root).is_dir() {
            Ok(())
        } else {
            Err(invalid("workspace must be a directory"))
        }
    };
    let root = cfg.text("work_dir");
    check("default workspace", check_path(root).map(|_| root));
    if let Some(projects) = cfg.0["projects"].as_object() {
        let mut projects: Vec<_> = projects.iter().collect();
        projects.sort_by_key(|(name, _)| *name);
        for (project, value) in projects {
            let root = value.as_str().unwrap();
            check(
                &format!("project {project}"),
                check_path(root).map(|_| root),
            );
        }
    }
    match rest::Discord::connect(cfg) {
        Err(err) => check("bot credential", Err(err)),
        Ok(discord) => match discord.verify_app(cfg) {
            Err(err) => check("Discord app", Err(err)),
            Ok(username) => {
                check("Discord app", Ok(&username));
                check("Gateway", place.probe_gateway(cfg));
                if cfg.text("mode") == "personal" {
                    check("owner DM", discord.verify_dm(cfg, channel));
                } else {
                    check(
                        "discussion access",
                        Err(io::Error::other(
                            "guild discussion permissions are unavailable in this Rust build",
                        )),
                    );
                }
            }
        },
    }
    let caps = cfg.manager_capabilities();
    check(
        "home daemon",
        ipc::Client::dial(Path::new(cfg.text("daemon_socket")), &caps)
            .and_then(|client| cfg.check_supervisor(&client, &caps))
            .map(|_| "manager capabilities negotiated"),
    );
    let ok = findings.iter().all(|result| result["status"] != "fail");
    println!("{}", serde_json::to_string_pretty(&Value::Array(findings))?);
    Ok(ok)
}
