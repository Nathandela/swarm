//! Read-only checkout ownership report across explicit local supervisor and connector roots.
mod git;
pub(crate) mod state;
use crate::{flags, ipc, supervisor};
use serde::Serialize;
use serde_json::Value;
use std::collections::{BTreeMap, BTreeSet, VecDeque};
use std::fs;
use std::io::{self, Write};
use std::path::{Path, PathBuf};
use std::time::{Duration, Instant};
const USAGE: &str = "usage: swarm checkout inventory [--json] [--supervisor-state DIR] [--discord-state DIR] [PATH ...]";
const DEFAULTS: &str = "  -discord-state value\n    \talso inspect this local Discord connector state directory (repeatable)\n  -json\n    \tprint machine-readable inventory\n  -supervisor-state value\n    \talso inspect this local supervisor state directory (repeatable)";
fn blank(path: &Path) -> bool {
    path.as_os_str().is_empty()
}
fn off(b: &bool) -> bool {
    !*b
}

#[derive(Default, Serialize)]
struct Checkout {
    path: PathBuf,
    path_state: &'static str,
    #[serde(skip_serializing_if = "blank")]
    repo: PathBuf,
    #[serde(skip_serializing_if = "blank")]
    git_common_dir: PathBuf,
    #[serde(skip_serializing_if = "blank")]
    worktree: PathBuf,
    #[serde(skip_serializing_if = "String::is_empty")]
    git_kind: String,
    #[serde(skip_serializing_if = "String::is_empty")]
    branch: String,
    #[serde(skip_serializing_if = "String::is_empty")]
    head: String,
    #[serde(skip_serializing_if = "off")]
    locked: bool,
    #[serde(skip_serializing_if = "off")]
    prunable: bool,
    linked_sessions: Vec<Session>,
    #[serde(skip_serializing_if = "Vec::is_empty")]
    discussions: Vec<String>,
    #[serde(skip_serializing_if = "Vec::is_empty")]
    unknowns: Vec<String>,
    #[serde(skip)]
    unverified: bool,
}
impl Checkout {
    fn new(path: PathBuf) -> Self {
        let mut v = Self {
            path,
            ..Self::default()
        };
        v.path_state = match fs::metadata(&v.path) {
            Ok(m) if m.is_dir() => "directory",
            Ok(_) => "file",
            Err(e) if e.kind() == io::ErrorKind::NotFound => {
                v.unknowns.push("path is missing".into());
                "missing"
            }
            Err(e) => {
                v.unknowns.push(format!("cannot inspect path: {e}"));
                "unknown"
            }
        };
        v
    }
    fn contains(&self, path: &Path) -> bool {
        !blank(path)
            && (path.starts_with(&self.path)
                || !self.unverified && !blank(&self.worktree) && path.starts_with(&self.worktree))
    }
    fn link(
        &mut self,
        rows: &BTreeMap<String, Session>,
        children: &BTreeMap<&str, Vec<(&str, &str)>>,
        discussions: &[Value],
    ) {
        let mut links = BTreeMap::<&str, BTreeSet<&str>>::new();
        let mut queue = VecDeque::new();
        for row in rows.values() {
            let mut relations = BTreeSet::new();
            if self.contains(&row.canonical_launch)
                || self.unverified && row.launch_path.starts_with(&self.path)
            {
                relations.insert("launch_path");
            }
            if self.contains(&row.canonical_provider)
                || self.unverified && row.provider_path.starts_with(&self.path)
            {
                relations.insert("provider_path");
                if !self.unverified && self.worktree == row.canonical_provider && row.isolated {
                    relations.insert("swarm_worktree_record");
                }
            }
            if self.contains(&row.legacy) {
                relations.insert("possible_legacy_worktree");
            }
            if blank(&self.worktree)
                && !blank(&row.canonical_provider)
                && self.path.starts_with(&row.canonical_provider)
            {
                relations.insert("provider_path_ancestor");
            }
            if !relations.is_empty() {
                queue.push_back(row.id.as_str());
                links.insert(&row.id, relations);
            }
        }
        while let Some(id) = queue.pop_front() {
            let prior = rows
                .get(id)
                .map(|r| r.resumed_from.as_str())
                .filter(|p| rows.contains_key(*p));
            let connected = children
                .get(id)
                .into_iter()
                .flatten()
                .copied()
                .chain(prior.map(|p| (p, "resume_predecessor")));
            for (other, relation) in connected {
                let set = links.entry(other).or_default();
                if set.is_empty() {
                    queue.push_back(other);
                }
                set.insert(relation);
            }
        }
        self.linked_sessions = links
            .iter()
            .map(|(id, rels)| {
                let mut row = rows[*id].clone();
                row.links = rels.iter().map(|r| (*r).to_owned()).collect();
                row
            })
            .collect();
        for d in discussions {
            let id = ipc::field(d, "id");
            let by_session = links.contains_key(ipc::field(d, "manager_session"))
                || d["worker_sessions"]
                    .as_array()
                    .into_iter()
                    .flatten()
                    .filter_map(Value::as_str)
                    .any(|id| links.contains_key(id));
            if by_session
                || git::canonical(Path::new(ipc::field(d, "project"))).is_ok_and(|p| {
                    Path::new(ipc::field(d, "project")).is_absolute() && self.contains(&p)
                })
                || d["worker_projects"]
                    .as_array()
                    .into_iter()
                    .flatten()
                    .filter_map(Value::as_str)
                    .any(|p| {
                        Path::new(p).is_absolute()
                            && git::canonical(Path::new(p)).is_ok_and(|p| self.contains(&p))
                    })
            {
                self.discussions.push(id.into());
            }
        }
    }
}
#[derive(Default, Serialize, Clone)]
struct Session {
    id: String,
    #[serde(skip_serializing_if = "String::is_empty")]
    name: String,
    agent: String,
    launch_path: PathBuf,
    provider_path: PathBuf,
    #[serde(skip)]
    canonical_launch: PathBuf,
    #[serde(skip)]
    canonical_provider: PathBuf,
    links: Vec<String>,
    state: String,
    resume: String,
    #[serde(skip_serializing_if = "off")]
    hidden: bool,
    #[serde(skip_serializing_if = "String::is_empty")]
    spawned_from: String,
    #[serde(skip_serializing_if = "String::is_empty")]
    resumed_from: String,
    #[serde(skip_serializing_if = "String::is_empty")]
    discussion: String,
    #[serde(skip_serializing_if = "Vec::is_empty")]
    unresolved_receipts: Vec<Value>,
    #[serde(skip_serializing_if = "Vec::is_empty")]
    unknowns: Vec<String>,
    #[serde(skip)]
    isolated: bool,
    #[serde(skip)]
    legacy: PathBuf,
    #[serde(skip)]
    worker_instance: String,
    #[serde(skip)]
    worker_attempt: String,
    #[serde(skip)]
    resume_claim: String,
    #[serde(skip)]
    retirement: String,
    #[serde(skip)]
    manager_instance: String,
}
#[derive(Default, Serialize)]
struct Report {
    complete: bool,
    supervisors: Vec<PathBuf>,
    paths: Vec<Checkout>,
    #[serde(skip_serializing_if = "Vec::is_empty")]
    discussions: Vec<Value>,
    #[serde(skip_serializing_if = "Vec::is_empty")]
    unlinked_receipts: Vec<Value>,
    #[serde(skip_serializing_if = "Vec::is_empty")]
    unknowns: Vec<String>,
}
impl Report {
    fn text(&self, mut out: impl Write) -> io::Result<()> {
        writeln!(
            out,
            "Checkout inventory (complete={} for selected local state roots)",
            self.complete
        )?;
        for root in &self.supervisors {
            writeln!(out, "Supervisor {root:?}")?;
        }
        for item in &self.paths {
            write!(out, "{:?}  {}", item.path, item.path_state)?;
            if !item.git_kind.is_empty() {
                write!(out, "  {}", item.git_kind)?;
            }
            if !blank(&item.repo) {
                write!(out, "  repo={:?} worktree={:?}", item.repo, item.worktree)?;
            }
            if !item.branch.is_empty() {
                write!(out, "  branch={:?}", item.branch)?;
            }
            writeln!(out)?;
            if item.linked_sessions.is_empty() && item.discussions.is_empty() {
                writeln!(
                    out,
                    "  No linked session or discussion found in this snapshot."
                )?;
            }
            for s in &item.linked_sessions {
                writeln!(
                    out,
                    "  {:?} {:?}  {}  resume={}  {}",
                    s.id,
                    s.name,
                    s.state,
                    s.resume,
                    s.links.join(", ")
                )?;
                for r in &s.unresolved_receipts {
                    receipt_text(&mut out, "    receipt", r)?;
                }
                for msg in &s.unknowns {
                    writeln!(out, "    unknown: {msg:?}")?;
                }
            }
            for d in &item.discussions {
                writeln!(out, "  Discussion {d:?}")?;
            }
            for msg in &item.unknowns {
                writeln!(out, "  unknown: {msg:?}")?;
            }
        }
        for d in &self.discussions {
            writeln!(
                out,
                "Discussion {:?}  {:?}  manager={:?} project={:?}",
                ipc::field(d, "id"),
                ipc::field(d, "state"),
                ipc::field(d, "manager_session"),
                ipc::field(d, "project")
            )?;
            for receipt in d["unresolved_receipts"].as_array().into_iter().flatten() {
                receipt_text(&mut out, "  receipt", receipt)?;
            }
            for msg in d["unknowns"]
                .as_array()
                .into_iter()
                .flatten()
                .filter_map(Value::as_str)
            {
                writeln!(out, "  unknown: {msg:?}")?;
            }
        }
        for receipt in &self.unlinked_receipts {
            receipt_text(&mut out, "Unlinked receipt", receipt)?;
        }
        for msg in &self.unknowns {
            writeln!(out, "Unknown: {msg:?}")?;
        }
        writeln!(
            out,
            "Use the evidence to decide what to retain. Verify Git changes and any other consumers separately."
        )
    }
}
fn receipt_text(out: &mut impl Write, lead: &str, r: &Value) -> io::Result<()> {
    writeln!(
        out,
        "{lead} {:?} {:?} {:?}",
        ipc::field(r, "kind"),
        ipc::field(r, "id"),
        ipc::field(r, "state")
    )
}
fn inspect(
    supervisors: Vec<PathBuf>,
    connectors: Vec<PathBuf>,
    mut paths: Vec<PathBuf>,
) -> io::Result<Report> {
    let end = Instant::now() + Duration::from_secs(15);
    let mut report = Report::default();
    let mut roots = vec![ipc::state_path()?];
    roots.extend(supervisors);
    let mut rows = BTreeMap::new();
    let mut additional = state::connectors(&mut roots, connectors, &mut report, end);
    if ipc::socket_path()? != ipc::state_path()?.join("daemon.sock") {
        report.unknowns.push("configured supervisor socket uses a nondefault path; inventory follows explicit local state roots".into());
    }
    for root in roots {
        if report.supervisors.len() == 81 || Instant::now() >= end {
            report
                .unknowns
                .push("supervisor discovery limit reached".into());
            break;
        }
        let root = match git::canonical(&root) {
            Ok(p) => p,
            Err(e) => {
                report
                    .unknowns
                    .push(format!("cannot resolve supervisor {root:?}: {e}"));
                continue;
            }
        };
        if report.supervisors.contains(&root) {
            continue;
        }
        report.supervisors.push(root.clone());
        state::inspect(
            &root,
            &supervisor::endpoint(&root),
            &mut rows,
            &mut report,
            &mut additional,
            end,
        );
    }
    let ids: BTreeSet<_> = rows.keys().cloned().collect();
    let prior: BTreeSet<_> = rows.values().map(|r| r.resumed_from.clone()).collect();
    for row in rows.values_mut() {
        if prior.contains(&row.id)
            && matches!(row.resume.as_str(), "candidate" | "parked_candidate")
        {
            row.resume = "has_successor".into();
        }
        for id in [&row.spawned_from, &row.resumed_from] {
            if !id.is_empty() && !ids.contains(id) {
                row.unknowns
                    .push(format!("lineage predecessor is absent: {id}"));
            }
        }
    }
    for row in rows.values_mut() {
        if !row.resume_claim.is_empty() && !prior.contains(&row.id) {
            row.resume = "unresolved".into();
            row.unresolved_receipts.push(state::receipt(
                &row.resume_claim,
                "manager_resume_claim",
                "successor_unverified",
                "",
                "",
            ));
        }
    }
    for discussion in &mut report.discussions {
        let id = ipc::field(discussion, "id").to_owned();
        let claimed_manager = ipc::field(discussion, "manager_session").to_owned();
        let claimed_instance = ipc::field(discussion, "manager_instance").to_owned();
        for row in rows.values_mut().filter(|row| row.discussion == id) {
            if row.id == claimed_manager {
                let exact_instance =
                    !claimed_instance.is_empty() && row.manager_instance == claimed_instance;
                let same_retirement =
                    discussion["unresolved_receipts"]
                        .as_array()
                        .is_some_and(|receipts| {
                            receipts.iter().any(|r| {
                                r["kind"] == "manager_retirement" && r["id"] == row.retirement
                            })
                        });
                if exact_instance
                    && same_retirement
                    && row.state == "parked_resume_source"
                    && discussion["state"] == "parking"
                {
                    discussion["state"] = serde_json::json!("parked");
                    if let Some(receipts) = discussion["unresolved_receipts"].as_array_mut() {
                        receipts.retain(|item| {
                            item["kind"] != "manager_retirement" || item["id"] != row.retirement
                        });
                    }
                }
                if discussion["state"] == "parked" {
                    if row.state == "parked_resume_source"
                        && (row.manager_instance.is_empty() || exact_instance)
                    {
                        row.state = "parked".into();
                    } else {
                        row.unknowns.push("connector reports a parked manager without matching physical exit evidence".into());
                    }
                }
            }
            if !row.worker_instance.is_empty() && !row.worker_attempt.is_empty() {
                for (key, value) in [
                    ("worker_sessions", row.id.as_str()),
                    (
                        "worker_projects",
                        row.provider_path.to_str().unwrap_or_default(),
                    ),
                ] {
                    if !value.is_empty() {
                        if discussion[key].is_null() {
                            discussion[key] = serde_json::json!([]);
                        }
                        let list = discussion[key]
                            .as_array_mut()
                            .expect("projected discussion vector");
                        if !list.iter().any(|known| known == value) {
                            list.push(serde_json::json!(value));
                        }
                    }
                }
            }
        }
    }
    if paths.is_empty() {
        let mut known = BTreeSet::new();
        for r in rows.values() {
            for p in [&r.canonical_launch, &r.canonical_provider, &r.legacy] {
                if !blank(p) {
                    known.insert(p.to_owned());
                }
            }
        }
        known.extend(additional);
        known.insert(std::env::current_dir()?);
        paths.extend(known);
    }
    let mut children = BTreeMap::new();
    for r in rows.values() {
        for (id, relation) in [
            (&r.spawned_from, "descendant"),
            (&r.resumed_from, "resume_successor"),
        ] {
            if !id.is_empty() {
                children
                    .entry(id.as_str())
                    .or_insert_with(Vec::new)
                    .push((r.id.as_str(), relation));
            }
        }
    }
    report.paths = git::inspect(paths, end, &mut report.unknowns);
    for item in &mut report.paths {
        item.link(&rows, &children, &report.discussions);
        if Instant::now() >= end {
            report
                .unknowns
                .push("checkout association deadline reached".into());
            break;
        }
    }
    report.unknowns.sort();
    report.complete = report.unknowns.is_empty()
        && report.unlinked_receipts.is_empty()
        && report
            .discussions
            .iter()
            .all(|d| d["unknowns"].as_array().is_none_or(Vec::is_empty))
        && report.paths.iter().all(|p| {
            p.unknowns.is_empty() && p.linked_sessions.iter().all(|s| s.unknowns.is_empty())
        });
    Ok(report)
}
pub(crate) fn run(args: &[String]) -> u8 {
    let Some((verb, args)) = args.split_first() else {
        eprintln!("{USAGE}");
        return 2;
    };
    if matches!(verb.as_str(), "-h" | "--help") && args.is_empty() {
        println!("{USAGE}");
        return 0;
    }
    if verb != "inventory" {
        eprintln!("{USAGE}");
        return 2;
    }
    let (mut json, mut roots, mut connectors) = (false, Vec::new(), Vec::new());
    let parsed = flags::options(
        args,
        &["json", "supervisor-state", "discord-state"],
        &["json"],
        |name, text| {
            if name == "json" {
                json = flags::parse_bool(name, text)?;
            } else {
                let target = if name == "supervisor-state" {
                    &mut roots
                } else {
                    &mut connectors
                };
                if !Path::new(text).is_absolute() || target.len() >= 16 {
                    return Err("invalid inventory state directory".into());
                }
                target.push(PathBuf::from(text));
            }
            Ok(())
        },
    );
    let paths = match parsed {
        Ok(list) => list.iter().map(PathBuf::from).collect(),
        Err(e) => {
            if !e.is_empty() {
                eprintln!("{e}");
            }
            eprintln!("{USAGE}\n{DEFAULTS}");
            return if e.is_empty() { 0 } else { 2 };
        }
    };
    match inspect(roots, connectors, paths).and_then(|r| {
        let mut out = io::stdout().lock();
        if json {
            serde_json::to_writer_pretty(&mut out, &r)?;
            writeln!(out)?;
        } else {
            r.text(out)?;
        }
        Ok(r.complete)
    }) {
        Ok(true) => 0,
        Ok(false) => 1,
        Err(e) => {
            eprintln!("checkout inventory: {e}");
            1
        }
    }
}
