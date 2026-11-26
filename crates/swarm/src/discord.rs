mod worker_input;
pub(crate) use worker_input::{
    ConnectorWorkerInputGuard, lock_connector_worker_input, verify_connector_worker_input,
    worker_input_available, worker_input_guild_available, worker_input_installation,
    worker_installation_enrolled,
};
mod call_live;

mod call_runtime;
mod call_speech;
mod call_team;

mod call_floor;
mod call_helper;

mod call_media;
mod call_origin;
mod call_room;

mod config;
mod dispatcher;
mod doctor;
pub(crate) mod enrolled_worker;
mod gateway;
mod guild;
mod legacy_discussions;
mod legacy_ingress;
mod legacy_state;
mod legacy_team;
pub(crate) mod linked_board;
mod local;
mod mail_wake;
mod manager;
mod migrate_supervisor;
pub(crate) mod provenance;
mod receipt;
mod received_files;

mod rest;
mod service;
mod team_history;
mod team_thread;
mod thread_creation;
mod unit;
mod worker;
mod worker_proof;

use crate::ipc;
use config::{Config, Place, invalid};
use serde_json::{Value, json};
use std::collections::BTreeMap;
use std::io::{self, Read};
use std::path::Path;

pub(crate) const MEDIA_PROTOCOL: u64 = 1;

pub(crate) fn check_packaged_media() -> u8 {
    match call_helper::Helper::bundled().and_then(|helper| helper.check()) {
        Ok(()) => 0,
        Err(error) => {
            eprintln!("swarm: packaged media: {error}");
            1
        }
    }
}

const USAGE: &str = "swarm discord connect|migrate-state|migrate-supervisor|run|status|doctor|stop-call|resume-manual <discussion> --session ID --instance INSTANCE --external-input COUNT|disconnect|exclude <target>|include <target>|context <ref>|reply <ref> --body-file <file>|propose <ref> --candidate-ids '[\"target\"]'";

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
pub(crate) fn native_manager_enrollment(launch: &Value) -> io::Result<Value> {
    native_manager_enrollment_at(&ipc::state_path()?, launch)
}

fn native_manager_enrollment_at(supervisor: &Path, launch: &Value) -> io::Result<Value> {
    let auth = Path::new(ipc::field(&launch["manager"], "auth_file"));
    let directory = auth
        .parent()
        .and_then(Path::parent)
        .ok_or_else(|| invalid("missing enrolled manager directory"))?;
    manager_scope_for_directory(Place::enrolled(supervisor)?, directory)
}

fn manager_scope_for_directory(
    places: Vec<(Place, Config)>,
    directory: &Path,
) -> io::Result<Value> {
    for (place, cfg) in places {
        let root = std::fs::canonicalize(&place.dir)?;
        if root == directory {
            return manager_scope(&root, &cfg);
        }
        if directory.parent() == Some(root.as_path()) {
            for (route, selected) in team_thread::Route::additional(&cfg)? {
                if route.directory(&root)? == directory {
                    let mut scope = manager_scope(directory, &selected)?;
                    scope["call_place"] = json!(root);
                    return Ok(scope);
                }
            }
        }
    }
    Err(invalid(
        "manager launch is outside enrolled Discord directories",
    ))
}

pub(crate) fn native_manager_available(socket: &str) -> bool {
    ipc::state_path()
        .and_then(|root| Place::enrolled(&root))
        .is_ok_and(|places| {
            places.into_iter().any(|(place, cfg)| {
                if cfg.text("daemon_socket") != socket {
                    return false;
                }
                manager_scope(&place.dir, &cfg).is_ok()
                    || team_thread::Route::additional(&cfg).is_ok_and(|routes| {
                        routes
                            .into_iter()
                            .any(|(_, child)| manager_scope(&place.dir, &child).is_ok())
                    })
            })
        })
}

fn manager_scope(directory: &Path, cfg: &Config) -> io::Result<Value> {
    if !cfg.enabled()
        || cfg.text("mode") != "personal" && team_thread::Route::configured(cfg)?.is_none()
        || !matches!(cfg.text("manager_cli"), "codex" | "claude")
    {
        return Err(invalid(
            "qualified Rust manager requires an active enrolled discussion",
        ));
    }
    let claude_origin = if cfg.text("manager_cli") == "claude" {
        let cwd = Path::new(cfg.text("work_dir"));
        let home = ipc::env_path("HOME")
            .ok_or_else(|| invalid("Claude manager HOME is missing"))?
            .canonicalize()?;
        // Reject authority overrides before probing or launching the provider.
        let authority = crate::providers::claude::project_authority(&home, cwd)?;
        crate::providers::claude_manager_runtime(cwd)?;
        Some((home, authority))
    } else {
        None
    };
    let owner = manager::Manager::new(cfg)?;
    let bridge = std::fs::canonicalize(directory)?.join("manager-v1");
    let mut scope = json!({"launch":owner.launch_request(&bridge.join("bridge.sock"), &bridge.join("bridge.auth")),
        "installation_id":cfg.text("installation_id"),"application_id":cfg.text("application_id"),
        "owner_id":cfg.text("owner_id"),"channel_id":owner.channel(),"supervisor_socket":cfg.text("daemon_socket")});
    if !owner.guild().is_empty() {
        scope["guild_id"] = json!(owner.guild());
    }
    if let Some((home, authority)) = claude_origin {
        scope["claude_home"] = json!(home);
        scope["project_authority"] = authority;
    }
    if cfg.0["projects"].is_object() {
        scope["projects"] = cfg.0["projects"].clone();
    }
    Ok(scope)
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
        || if ipc::field(scope, "guild_id").is_empty() {
            identity[3] != scope["channel_id"]
        } else {
            identity[3] != ""
                || data["team_discussion"]["guild_id"] != scope["guild_id"]
                || data["team_discussion"]["thread_id"] != scope["channel_id"]
        }
        || identity[4] != scope["supervisor_socket"]
        || !data["manager"]["generation"]
            .as_u64()
            .is_some_and(|generation| generation > 0)
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

pub(crate) fn verify_manager_vm_scope(scope: &Value, supervisor: Option<&Path>) -> io::Result<()> {
    if scope["launch"]["manager"]["native_cli"] != true {
        return Ok(());
    }
    let root = supervisor.ok_or_else(|| invalid("missing VM supervisor custody root"))?;
    let current = native_manager_enrollment_at(root, &scope["launch"])?;
    if current["launch"] != scope["launch"]
        || [
            "installation_id",
            "owner_id",
            "application_id",
            "channel_id",
            "guild_id",
            "supervisor_socket",
        ]
        .iter()
        .any(|coordinate| current[*coordinate] != scope[*coordinate])
    {
        return Err(invalid("VM manager enrollment authority changed"));
    }
    Ok(())
}

pub(crate) fn verify_connector_input(
    scope: &Value,
    operation: &str,
    composer: &Value,
    supervisor_root: Option<&Path>,
) -> io::Result<()> {
    verify_manager_vm_scope(scope, supervisor_root)?;
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
        call_origin::validate(scope, origin, ipc::field(source, "text"))?;
        if source["voice_origin"] != *origin || origin["request_id"] != id {
            return Err(invalid("voice origin does not match the persisted cutover"));
        }
    }
    if input["operation"] != operation
        || input["composer"] != *composer
        || manager["session"] != composer["session"]
        || source["state"] != "sending"
        || !ipc::field(scope, "guild_id").is_empty()
            && (source["route"]["guild_id"] != scope["guild_id"]
                || source["route"]["channel_id"] != scope["channel_id"]
                || source["author"]["id"] != origin["actor_id"]
                || source["authority"] != origin["kind"])
        || composer["text"]
            != crate::manager_prompt::saved_input(
                input,
                ipc::field(source, "text"),
                scope["launch"]["manager"]["native_cli"] == true,
            )?
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
    if !source["received_files"].is_null() {
        let place = Path::new(ipc::field(&scope["launch"]["manager"], "auth_file"))
            .parent()
            .and_then(Path::parent)
            .ok_or_else(|| invalid("missing received attachment custody"))?;
        received_files::verify_receipt(place, &source["received_files"])?;
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
        let call_place = scope["call_place"].as_str().map(Path::new).unwrap_or(place);
        call_runtime::verify_live(call_place, origin)
            .map_err(|_| invalid(call_runtime::WITHDRAWN))?;
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
            "migrate-state" => {
                flags = &[
                    "dry-run",
                    "rollback",
                    "controls",
                    "activate-personal",
                    "activate-personal-vm",
                    "inspect-personal-vm",
                    "stage-personal-vm-wake",
                    "deliver-personal-vm-wake",
                ];
                &[
                    "settle-personal-ingress",
                    "reconcile-personal-vm-reply",
                    "dispatch-personal-ingress",
                    "capture-personal-ingress",
                    "prepare-personal-ingress",
                    "prepare-teammate-ingress",
                    "admit-teammate-ingress",
                    "stage-teammate-publication",
                    "deliver-personal-publication",
                    "read-personal-publication-file",
                    "read-personal-source-attachment",
                    "file-id",
                    "file-sha256",
                    "offset",
                ]
            }
            "migrate-supervisor" => {
                flags = &["dry-run"];
                &["from-state-dir", "from-socket", "from-lock", "from-log"]
            }
            "run" => {
                flags = &[
                    "unattended",
                    "retained-personal",
                    "observe-retained-teammate",
                    "capture-retained-teammate",
                ];
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
        "migrate-state" => {
            type Migration = fn(&Place, &str, bool) -> io::Result<bool>;
            let ingress: [(&str, Migration); 9] = [
                (
                    "reconcile-personal-vm-reply",
                    legacy_ingress::reply_notice::run,
                ),
                ("capture-personal-ingress", legacy_ingress::capture),
                (
                    "deliver-personal-publication",
                    legacy_ingress::publication::run,
                ),
                ("prepare-personal-ingress", legacy_ingress::prepare::run),
                ("prepare-teammate-ingress", legacy_team::prepare_ingress),
                ("admit-teammate-ingress", legacy_team::admit_ingress),
                ("stage-teammate-publication", legacy_team::stage_publication),
                ("dispatch-personal-ingress", legacy_ingress::dispatch),
                ("settle-personal-ingress", legacy_ingress::settle),
            ];
            let export = req.get("read-personal-publication-file");
            let source = req.get("read-personal-source-attachment");
            let vm_modes = [
                "inspect-personal-vm",
                "stage-personal-vm-wake",
                "deliver-personal-vm-wake",
            ];
            let selected_vm = vm_modes.iter().filter(|flag| req.on(flag)).count();
            if selected_vm > 0 {
                if selected_vm != 1
                    || req.on("controls")
                    || req.on("rollback")
                    || req.on("activate-personal")
                    || req.on("activate-personal-vm")
                    || (!req.on("inspect-personal-vm") && req.on("dry-run"))
                    || export.is_some()
                    || source.is_some()
                    || ["file-id", "file-sha256", "offset"]
                        .iter()
                        .any(|flag| req.get(flag).is_some())
                    || ingress.iter().any(|(flag, _)| req.get(flag).is_some())
                {
                    return Err(invalid("select one migration operation"));
                }
                return if req.on("inspect-personal-vm") {
                    legacy_ingress::activation::inspect_vm(&place)
                } else {
                    legacy_ingress::service::stage_vm_wake(
                        &place,
                        req.on("deliver-personal-vm-wake"),
                    )
                };
            }
            if export.is_some() && source.is_some() {
                return Err(invalid("select one original attachment source"));
            }
            if let Some(output) = export.or(source) {
                if req.on("controls")
                    || req.on("rollback")
                    || req.on("dry-run")
                    || req.on("activate-personal")
                    || req.on("activate-personal-vm")
                    || ingress.iter().any(|(flag, _)| req.get(flag).is_some())
                {
                    return Err(invalid("select one migration operation"));
                }
                let id = req
                    .get("file-id")
                    .ok_or_else(|| invalid("missing --file-id"))?;
                let digest = req
                    .get("file-sha256")
                    .ok_or_else(|| invalid("missing --file-sha256"))?;
                let offset = req
                    .get("offset")
                    .unwrap_or("0")
                    .parse()
                    .map_err(|_| invalid("invalid offset"))?;
                let data = if export.is_some() {
                    legacy_ingress::publication::read(&place, output, id, digest, offset)?
                } else {
                    legacy_ingress::source_attachment::read(&place, output, id, digest, offset)?
                };
                println!("{}", serde_json::to_string_pretty(&data)?);
                return Ok(true);
            }
            if ["file-id", "file-sha256", "offset"]
                .iter()
                .any(|flag| req.get(flag).is_some())
            {
                return Err(invalid(
                    "select an original publication or source attachment",
                ));
            }
            if req.on("activate-personal") || req.on("activate-personal-vm") {
                if (req.on("activate-personal") && req.on("activate-personal-vm"))
                    || req.on("controls")
                    || ingress.iter().any(|(flag, _)| req.get(flag).is_some())
                    || req.on("dry-run") && req.on("rollback")
                {
                    return Err(invalid("select one migration operation"));
                }
                return legacy_ingress::activation::migrate(
                    &place,
                    req.on("dry-run"),
                    req.on("rollback"),
                    req.on("activate-personal-vm"),
                );
            }
            let mut selected = ingress
                .iter()
                .filter_map(|(flag, action)| req.get(flag).map(|message| (message, action)));
            if let Some((message, action)) = selected.next() {
                if req.on("controls") || req.on("rollback") || selected.next().is_some() {
                    return Err(invalid("select one migration operation"));
                }
                return action(&place, message, req.on("dry-run"));
            }
            return legacy_state::migrate(
                &place,
                req.on("dry-run"),
                req.on("rollback"),
                req.on("controls"),
            );
        }
        "disconnect" => {
            unit::disconnect(&place)?;
            println!("Discord connector disconnected.");
            return Ok(true);
        }
        "migrate-supervisor" => {
            return migrate_supervisor::migrate(
                &place,
                req.on("dry-run"),
                ["from-state-dir", "from-socket", "from-lock", "from-log"].map(|key| req.get(key)),
            );
        }
        _ => {}
    }
    let cfg = place.load()?;
    if matches!(req.command, "include" | "exclude")
        && let Some(result) =
            legacy_state::audience(&place, &cfg, req.id, req.command == "exclude")?
    {
        println!("{}", serde_json::to_string_pretty(&result)?);
        return Ok(true);
    }
    match req.command {
        "run" if req.on("observe-retained-teammate") || req.on("capture-retained-teammate") => {
            if req.on("unattended")
                || req.on("retained-personal")
                || req.on("observe-retained-teammate") && req.on("capture-retained-teammate")
            {
                return Err(invalid("select one foreground retained teammate mode"));
            }
            legacy_team::run(&place, &cfg, req.on("capture-retained-teammate"))?;
        }
        "run" if legacy_ingress::activation::selected(&place)? => {
            return legacy_ingress::service::run(&place);
        }
        "run" if req.on("retained-personal") => {
            return Err(invalid(
                "activate the retained personal enrollment with migrate-state --activate-personal",
            ));
        }
        "run" => service::run(&place.bound(&cfg)?, &cfg, req.on("unattended"))?,
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
        "doctor" => return doctor(&place.bound(&cfg)?, &cfg, req.get("channel")),
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
    let cfg = place.configure(req)?;
    let installed = req.on("install");
    if installed {
        unit::install(&place, &cfg)?;
    }
    let mut report = json!({
        "name":cfg.text("name"),"installation_id":cfg.text("installation_id"),"state_dir":place.dir,"installed":installed,
        "next":format!("swarm discord run --state-dir '{}'",place.dir.to_string_lossy().replace('\'', "'\\''"))
    });
    if !cfg.text("dm_channel_id").is_empty() {
        report["dm_channel_id"] = cfg.0["dm_channel_id"].clone();
    }
    if cfg.managed() {
        report["project_access"] = json!(if cfg.text("project_access") == "vm" {
            "vm"
        } else {
            "enrolled"
        });
    }
    println!("{}", serde_json::to_string_pretty(&report)?);
    Ok(true)
}

fn doctor(place: &Place, cfg: &Config, channel: Option<&str>) -> io::Result<bool> {
    if !cfg.managed() {
        return Err(io::Error::other(
            "this place uses the legacy connector; native manager checks require --mode teammate or --mode personal",
        ));
    }
    let mut findings = Vec::new();
    let check_path = |root: &str| {
        if Path::new(root).is_dir() {
            Ok(())
        } else {
            Err(invalid("workspace must be a directory"))
        }
    };
    let root = cfg.text("work_dir");
    findings.push(doctor::finding(
        "default workspace",
        check_path(root).map(|_| root),
    ));
    if let Some(projects) = cfg.0["projects"].as_object() {
        let mut projects: Vec<_> = projects.iter().collect();
        projects.sort_by_key(|(name, _)| *name);
        for (project, value) in projects {
            let root = value.as_str().unwrap();
            findings.push(doctor::finding(
                &format!("project {project}"),
                check_path(root).map(|_| root),
            ));
        }
    }
    match rest::Discord::connect(cfg) {
        Err(err) => findings.push(doctor::finding("bot credential", Err(err))),
        Ok(discord) => match discord.verify_app(cfg) {
            Err(err) => findings.push(doctor::finding("Discord app", Err(err))),
            Ok(username) => {
                findings.push(doctor::finding("Discord app", Ok(&username)));
                findings.push(doctor::finding("Gateway", place.probe_gateway(cfg)));
                if cfg.text("mode") == "personal" {
                    findings.push(doctor::finding("owner DM", discord.verify_dm(cfg, channel)));
                } else {
                    findings.extend(doctor::channels(cfg, channel, |path| {
                        guild::lookup(&discord, path)
                    }));
                }
            }
        },
    }
    let caps = cfg.manager_capabilities();
    findings.push(doctor::finding(
        "home daemon",
        ipc::Client::dial(Path::new(cfg.text("daemon_socket")), &caps)
            .and_then(|client| cfg.check_supervisor(&client, &caps))
            .map(|_| "manager capabilities negotiated"),
    ));
    match crate::providers::manager_health(cfg.text("manager_cli"), place.supervisor_root()) {
        Ok((version, login)) => {
            findings.push(doctor::finding("native manager CLI", Ok(&version)));
            findings.push(doctor::finding(
                "native provider login",
                if login {
                    Ok("local CLI reports logged in")
                } else {
                    Err(io::Error::other(
                        "native CLI could not confirm its local login; check the provider account",
                    ))
                },
            ));
        }
        Err(error) => findings.push(doctor::finding("native manager CLI", Err(error))),
    }
    let ok = findings.iter().all(|result| result["status"] != "fail");
    println!("{}", serde_json::to_string_pretty(&Value::Array(findings))?);
    Ok(ok)
}

pub(crate) fn checkout_status(
    dir: &std::path::Path,
    end: std::time::Instant,
) -> std::io::Result<serde_json::Value> {
    local::call_until(dir, "checkout-status", "", None, end)
}

#[cfg(test)]
#[test]
#[ignore = "requires the isolated original Go supervisor and guild REST fixture"]
fn native_go_retained_guild_preparation() {
    legacy_team::native_prepare_witness();
}
