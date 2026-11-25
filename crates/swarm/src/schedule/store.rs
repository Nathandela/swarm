use super::{
    Job, Result, Spec, Template,
    calendar::{Rule, Zone},
    id, valid_id, validate_text,
};
use bbolt_rs::{
    Bolt, BucketApi, BucketRwApi, CursorApi, DbApi, DbRwAPI, TxApi, TxRwApi, TxRwRefApi,
};
use jiff::Timestamp;
use serde::{Deserialize, Serialize, de::DeserializeOwned};
use sha2::{Digest, Sha256};
use std::{
    fs::{File, OpenOptions},
    io,
    os::{
        fd::AsRawFd,
        unix::fs::{MetadataExt, OpenOptionsExt},
    },
    path::{Path, PathBuf},
};

const JOBS: &str = "jobs";
const REVISIONS: &str = "revisions";
const EVALUATED: &str = "evaluated";
const RUNS: &str = "occurrences";
const HISTORY: &str = "job_occurrences";
const ACTIVE: &str = "active_occurrences";
const TEMPLATES: &str = "templates";
const NOT_FOUND: &str = "schedule record not found";
const ARCHIVED: &str = "schedule job is archived";
const CONFLICT: &str = "schedule job revision changed";
const ZERO: &str = "0001-01-01T00:00:00Z";

pub(super) struct Store {
    dir: PathBuf,
    pub host: PathBuf,
}

#[derive(Clone, Serialize, Deserialize)]
pub(super) struct Occurrence {
    pub id: String,
    pub job_id: String,
    revision_id: String,
    request_hash: String,
    pub spec: Spec,
    pub due_at: String,
    claimed_at: Timestamp,
    pub manual: bool,
    pub state: String,
    #[serde(default, skip_serializing_if = "String::is_empty")]
    pub detail: String,
    #[serde(default, skip_serializing_if = "String::is_empty")]
    pub workspace: String,
    #[serde(default, skip_serializing_if = "String::is_empty")]
    pub source_directory: String,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub attempts: Vec<Attempt>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub completion: Option<Completion>,
    #[serde(default = "zero_time")]
    pub projected_at: Timestamp,
    #[serde(default = "zero_time")]
    pub released_at: Timestamp,
    #[serde(default = "zero_time")]
    pub completed_at: Timestamp,
    #[serde(flatten)]
    additional: serde_json::Map<String, serde_json::Value>,
}

#[derive(Clone, Debug, PartialEq, Deserialize, Serialize)]
pub(crate) struct Completion {
    pub summary: String,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub links: Vec<String>,
}

#[derive(Clone, Default, Deserialize, Serialize)]
#[serde(default)]
pub(super) struct Attempt {
    pub id: String,
    pub agent: String,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub operation_id: String,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub completion_token_hash: String,
    #[serde(skip_serializing_if = "std::ops::Not::not")]
    pub prompt_started: bool,
    #[serde(skip_serializing_if = "std::ops::Not::not")]
    pub prompt_delivered: bool,
    #[serde(skip_serializing_if = "std::ops::Not::not")]
    pub dispatch_started: bool,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub session_id: String,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub workspace: String,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub detail: String,
    pub state: String,
    pub started_at: Timestamp,
    pub updated_at: Timestamp,
    #[serde(flatten)]
    pub(super) additional: serde_json::Map<String, serde_json::Value>,
}

pub(super) fn zero_time() -> Timestamp {
    ZERO.parse().expect("valid zero time")
}

impl Occurrence {
    fn new(
        job: &Job,
        due: String,
        instant: Timestamp,
        now: Timestamp,
        invocation: Option<&str>,
    ) -> Result<Self> {
        let mut run = Self {
            id: match invocation {
                Some(id) => id.into(),
                None => digest(format!("{}/{instant}", job.id)),
            },
            job_id: job.id.clone(),
            revision_id: job.revision_id.clone(),
            request_hash: String::new(),
            spec: job.spec.clone(),
            due_at: due,
            claimed_at: now,
            manual: invocation.is_some(),
            state: "queued".into(),
            detail: String::new(),
            additional: serde_json::Map::new(),
            workspace: String::new(),
            source_directory: String::new(),
            attempts: Vec::new(),
            completion: None,
            projected_at: zero_time(),
            released_at: zero_time(),
            completed_at: zero_time(),
        };
        run.request_hash = run.hash()?;
        Ok(run)
    }

    fn hash(&self) -> Result<String> {
        #[derive(Serialize)]
        #[serde(rename_all = "PascalCase")]
        struct Input<'a> {
            #[serde(rename = "ID")]
            id: &'a str,
            #[serde(rename = "JobID")]
            job: &'a str,
            #[serde(rename = "RevisionID")]
            revision: &'a str,
            spec: &'a Spec,
            due_at: &'a str,
            claimed_at: Timestamp,
            manual: bool,
        }
        let data = serde_json::to_string(&Input {
            id: &self.id,
            job: &self.job_id,
            revision: &self.revision_id,
            spec: &self.spec,
            due_at: &self.due_at,
            claimed_at: self.claimed_at,
            manual: self.manual,
        })?;
        Ok(digest(go_json(data)))
    }

    fn preserves_identity(&self, next: &Self) -> bool {
        let stable = |a: &str, b: &str| a.is_empty() || a == b;
        let stable_time = |a: Timestamp, b: Timestamp| a == zero_time() || a == b;
        if self.attempts.len() > next.attempts.len()
            || !stable(&self.workspace, &next.workspace)
            || !stable(&self.source_directory, &next.source_directory)
            || !stable_time(self.completed_at, next.completed_at)
            || !stable_time(self.projected_at, next.projected_at)
            || !stable_time(self.released_at, next.released_at)
            || (self.completion.is_some() && self.completion != next.completion)
        {
            return false;
        }
        let mut ids = std::collections::BTreeSet::new();
        for (index, a) in next.attempts.iter().enumerate() {
            if !valid_id(&a.id) || !ids.insert(&a.id) {
                return false;
            }
            if let Some(b) = self.attempts.get(index)
                && (a.id != b.id
                    || a.agent != b.agent
                    || a.started_at != b.started_at
                    || !stable(&b.operation_id, &a.operation_id)
                    || !stable(&b.completion_token_hash, &a.completion_token_hash)
                    || !stable(&b.session_id, &a.session_id)
                    || !stable(&b.workspace, &a.workspace)
                    || (b.dispatch_started && !a.dispatch_started)
                    || (b.prompt_started && !a.prompt_started)
                    || (b.prompt_delivered && !a.prompt_delivered))
            {
                return false;
            }
        }
        true
    }

    pub(super) fn reserves(&self) -> bool {
        match self.state.as_str() {
            "queued" | "working" | "needs-input" => true,
            "attention" => self.attempts.last().is_some_and(|a| a.state == "unknown"),
            "ready" => self.released_at == zero_time(),
            _ => false,
        }
    }
}

struct Evaluation {
    cursor: Timestamp,
    job_id: String,
    zone: Option<String>,
    wall: Option<String>,
    run: Option<(Timestamp, Occurrence)>,
}

impl Store {
    pub fn open(root: &Path, host: &Path) -> Result<Self> {
        let store = Self {
            dir: root.join("schedules"),
            host: host.into(),
        };
        crate::supervisor::runtime::private_dir(&store.dir)?;
        let _lock = store.lock(libc::LOCK_EX)?;
        let path = store.dir.join("state.db");
        if private_file(&path, false)?.is_some() {
            let db = Bolt::open_ro(&path)?;
            let tx = db.begin()?;
            if [JOBS, REVISIONS, EVALUATED, RUNS, HISTORY, ACTIVE, TEMPLATES]
                .into_iter()
                .all(|name| tx.bucket(name).is_some())
            {
                return Ok(store);
            }
            if [JOBS, REVISIONS, EVALUATED, RUNS, HISTORY, ACTIVE]
                .into_iter()
                .any(|name| tx.bucket(name).is_none())
            {
                return Err("unrecognized schedule catalog buckets".into());
            }
            drop(tx);
            drop(db);
            initialize(&path)?;
        } else {
            // Publish a new catalog only after all buckets are durably committed.
            let temp = store
                .dir
                .join(format!(".catalog-{}", id(Timestamp::now())?));
            let result =
                initialize(&temp).and_then(|()| std::fs::rename(&temp, &path).map_err(Into::into));
            if result.is_err() {
                let _ = std::fs::remove_file(temp);
            }
            result?;
        }
        File::open(&store.dir)?.sync_all()?;
        Ok(store)
    }

    pub fn dispatch_lock(&self) -> Result<File> {
        self.lock_file("dispatch.lock", libc::LOCK_EX)
    }

    fn lock(&self, mode: libc::c_int) -> Result<File> {
        self.lock_file("state.lock", mode)
    }

    fn lock_file(&self, name: &str, mode: libc::c_int) -> Result<File> {
        let lock = private_file(&self.dir.join(name), true)?.ok_or("missing schedule lock file")?;
        loop {
            // The open file owns the lock and releases it when the transaction is over.
            if unsafe { libc::flock(lock.as_raw_fd(), mode) } == 0 {
                return Ok(lock);
            }
            let err = io::Error::last_os_error();
            if err.kind() != io::ErrorKind::Interrupted {
                return Err(err.into());
            }
        }
    }

    fn view<T>(&self, f: impl FnOnce(&dyn ReadBucket) -> Result<T>) -> Result<T> {
        let _lock = self.lock(libc::LOCK_SH)?;
        let path = self.dir.join("state.db");
        private_file(&path, false)?.ok_or("schedule database disappeared")?;
        let db = Bolt::open_ro(&path)?;
        let tx = db.begin()?;
        f(&tx)
    }

    fn write<T>(&self, f: impl FnOnce(&mut dyn WriteBucket) -> Result<T>) -> Result<T> {
        let _lock = self.lock(libc::LOCK_EX)?;
        let path = self.dir.join("state.db");
        private_file(&path, false)?.ok_or("schedule database disappeared")?;
        let mut db = Bolt::open(&path)?;
        let mut tx = db.begin_rw()?;
        let result = f(&mut tx)?;
        tx.commit()?;
        Ok(result)
    }

    pub fn jobs(&self) -> Result<Vec<Job>> {
        let mut jobs: Vec<Job> = self.view(|tx| records(tx, JOBS))?;
        jobs.sort_by(|a, b| a.spec.name.cmp(&b.spec.name).then_with(|| a.id.cmp(&b.id)));
        Ok(jobs)
    }

    pub fn job(&self, id: &str) -> Result<Job> {
        self.view(|tx| require(tx, JOBS, id))
    }

    pub fn create(&self, mut spec: Spec, now: Timestamp, draft: bool) -> Result<Job> {
        spec.prepare(&self.host)?;
        if !draft {
            self.check_future(&spec, now)?;
        }
        let job = Job {
            id: id(now)?,
            revision_id: id(now)?,
            spec,
            state: if draft { "paused" } else { "active" }.into(),
            created_at: now,
            updated_at: now,
        };
        self.write(|tx| self.set_revision(tx, &job, now))?;
        Ok(job)
    }

    pub fn edit(
        &self,
        job: &str,
        expected: Option<&str>,
        mut spec: Spec,
        now: Timestamp,
    ) -> Result<Job> {
        spec.prepare(&self.host)?;
        self.write(|tx| {
            let mut job: Job = require(tx, JOBS, job)?;
            if job.state == "archived" {
                return Err(ARCHIVED.into());
            }
            if expected.is_some_and(|id| id != job.revision_id) {
                return Err(CONFLICT.into());
            }
            if spec.calendar != job.spec.calendar {
                self.check_future(&spec, now)?;
            }
            if let Some(old) = self.evaluate(tx, &job, before(now)?, now, &mut None)? {
                save_evaluation(tx, old, None)?;
            }
            job.spec = spec;
            job.revision_id = id(now)?;
            job.updated_at = now;
            self.set_revision(tx, &job, now)?;
            Ok(job)
        })
    }

    pub fn set_state(&self, id: &str, state: &str, now: Timestamp) -> Result<Job> {
        self.write(|tx| {
            let mut job: Job = require(tx, JOBS, id)?;
            if job.state == state {
                return Ok(job);
            }
            if job.state == "archived" {
                return Err(ARCHIVED.into());
            }
            if let Some(old) = self.evaluate(tx, &job, before(now)?, now, &mut None)? {
                let reason = format!("job was {state} before this trigger was claimed");
                save_evaluation(tx, old, Some(&reason))?;
            }
            job.state = state.into();
            job.updated_at = now;
            put(tx, JOBS, &job.id, &job)?;
            self.reset_cursor(tx, &job, now)?;
            Ok(job)
        })
    }

    fn check_future(&self, spec: &Spec, now: Timestamp) -> Result<()> {
        if Rule::compile(&spec.calendar, &self.host)?
            .find(before(now)?, false)
            .is_none()
        {
            return Err("schedule has no future trigger".into());
        }
        Ok(())
    }

    fn set_revision(&self, tx: &mut dyn WriteBucket, job: &Job, now: Timestamp) -> Result<()> {
        put(tx, JOBS, &job.id, job)?;
        put(
            tx,
            REVISIONS,
            &job.revision_id,
            &serde_json::json!({
                "id":job.revision_id, "job_id":job.id, "spec":job.spec, "created_at":now,
            }),
        )?;
        self.reset_cursor(tx, job, now)
    }

    fn reset_cursor(&self, tx: &mut dyn WriteBucket, job: &Job, now: Timestamp) -> Result<()> {
        if job.spec.calendar.time_zone == "host" {
            let zone = super::calendar::Zone::host(&self.host)?.id;
            put(tx, EVALUATED, &format!("{}/zone", job.id), &zone)?;
        }
        put(tx, EVALUATED, &job.id, &before(now)?)
    }

    pub fn templates(&self) -> Result<Vec<Template>> {
        let mut list: Vec<Template> = self.view(|tx| records(tx, TEMPLATES))?;
        list.sort_by(|a, b| a.name.cmp(&b.name).then_with(|| a.id.cmp(&b.id)));
        Ok(list)
    }

    pub fn template(&self, id: &str) -> Result<Template> {
        self.view(|tx| require(tx, TEMPLATES, id))
    }

    pub fn save_template(
        &self,
        existing: Option<&str>,
        name: String,
        prompt: String,
        now: Timestamp,
    ) -> Result<Template> {
        validate_text(&name, &prompt)?;
        self.write(|tx| {
            let mut template: Template = match existing {
                Some(id) => require(tx, TEMPLATES, id)?,
                None => Template {
                    id: id(now)?,
                    name: String::new(),
                    prompt: String::new(),
                    created_at: now,
                    updated_at: now,
                },
            };
            template.name = name;
            template.prompt = prompt;
            template.updated_at = now;
            put(tx, TEMPLATES, &template.id, &template)?;
            Ok(template)
        })
    }

    pub fn delete_template(&self, id: &str) -> Result<()> {
        self.write(|tx| {
            require::<Template>(tx, TEMPLATES, id)?;
            tx.delete_raw(TEMPLATES, id)
        })
    }

    pub fn history(&self, job: &str) -> Result<Vec<Occurrence>> {
        let mut list: Vec<(Timestamp, Occurrence)> = self.view(|tx| {
            require::<Job>(tx, JOBS, job)?;
            let mut runs = Vec::new();
            tx.walk(HISTORY, Some(&format!("{job}/")), &mut |_, id| {
                let run: Occurrence = require(tx, RUNS, std::str::from_utf8(id)?)?;
                runs.push((run.due_at.parse()?, run));
                Ok(())
            })?;
            Ok(runs)
        })?;
        list.sort_by(|(at, a), (bt, b)| bt.cmp(at).then_with(|| a.id.cmp(&b.id)));
        Ok(list.into_iter().map(|(_, run)| run).collect())
    }

    pub fn occurrence(&self, id: &str) -> Result<Occurrence> {
        self.view(|tx| require(tx, RUNS, id))
    }

    pub fn active(&self) -> Result<Vec<Occurrence>> {
        let mut runs = self.view(|tx| {
            let mut runs = Vec::new();
            tx.walk(ACTIVE, None, &mut |key, _| {
                let run: Occurrence = require(tx, RUNS, std::str::from_utf8(key)?)?;
                runs.push((run.due_at.parse::<Timestamp>()?, run));
                Ok(())
            })?;
            Ok(runs)
        })?;
        runs.sort_by(|(at, a), (bt, b)| at.cmp(bt).then_with(|| a.id.cmp(&b.id)));
        Ok(runs.into_iter().map(|(_, run)| run).collect())
    }

    pub fn update(
        &self,
        id: &str,
        f: impl FnOnce(&mut Occurrence) -> Result<()>,
    ) -> Result<Occurrence> {
        self.write(|tx| {
            let before: Occurrence = require(tx, RUNS, id)?;
            let mut next = before.clone();
            f(&mut next)?;
            if next.request_hash != before.request_hash || next.hash()? != before.request_hash {
                return Err("schedule occurrence inputs are immutable".into());
            }
            if !before.preserves_identity(&next) {
                return Err("schedule execution identities are immutable".into());
            }
            if serde_json::to_vec(&before)? != serde_json::to_vec(&next)? {
                put_run(tx, &next)?;
            }
            Ok(next)
        })
    }

    pub fn claim_manual(&self, job: &str, invocation: &str, now: Timestamp) -> Result<Occurrence> {
        if !valid_id(invocation) {
            return Err("invalid schedule invocation ID".into());
        }
        self.write(|tx| {
            if let Some(existing) = get::<Occurrence>(tx, RUNS, invocation)? {
                if !existing.manual || existing.job_id != job {
                    return Err(CONFLICT.into());
                }
                return Ok(existing);
            }
            let job: Job = require(tx, JOBS, job)?;
            if job.state == "archived" {
                return Err(ARCHIVED.into());
            }
            let due = if job.spec.calendar.time_zone == "host" {
                let zone = super::calendar::Zone::host(&self.host)?;
                go_wall(now, &zone)
            } else {
                now.to_string()
            };
            let mut run = Occurrence::new(&job, due, now, now, Some(invocation))?;
            check_overlap(tx, &mut run)?;
            put_run(tx, &run)?;
            Ok(run)
        })
    }

    pub fn claim_due(&self, now: Timestamp) -> Result<Vec<Occurrence>> {
        let due = |tx: &dyn ReadBucket| -> Result<Vec<Evaluation>> {
            let mut list = Vec::new();
            let mut host = None;
            for job in records(tx, JOBS)? {
                if let Some(evaluation) = self.evaluate(tx, &job, now, now, &mut host)? {
                    list.push(evaluation);
                }
            }
            list.sort_by(|a, b| {
                a.cursor
                    .cmp(&b.cursor)
                    .then_with(|| a.job_id.cmp(&b.job_id))
            });
            Ok(list)
        };
        if self.view(due)?.is_empty() {
            return Ok(Vec::new());
        }
        self.write(|tx| {
            let mut claimed = Vec::new();
            for item in due(tx)? {
                if let Some(run) = save_evaluation(tx, item, None)?
                    && run.state != "missed"
                {
                    claimed.push(run);
                }
            }
            Ok(claimed)
        })
    }

    fn evaluate(
        &self,
        tx: &dyn ReadBucket,
        job: &Job,
        through: Timestamp,
        now: Timestamp,
        host: &mut Option<Zone>,
    ) -> Result<Option<Evaluation>> {
        if job.state != "active" {
            return Ok(None);
        }
        let mut from: Timestamp = require(tx, EVALUATED, &job.id)?;
        let minute = Timestamp::from_second(through.as_second().div_euclid(60) * 60)?;
        let mut cursor = None;
        if through < from {
            let rewind = before(minute)?.max(before(job.updated_at)?);
            if rewind < from {
                from = rewind;
                cursor = Some(from);
            }
        }
        let rule = Rule::compile_cached(&job.spec.calendar, &self.host, host)?;
        let mut zone = None;
        if let Some(active_zone) = &rule.zone.id {
            let old = get::<String>(tx, EVALUATED, &format!("{}/zone", job.id))?;
            if old.as_ref() != Some(active_zone) {
                from = from.max(before(minute)?);
                cursor = Some(from);
                zone = Some(active_zone.clone());
            }
        }
        let mut wall = None;
        let mut run = None;
        if let Some(at) = rule.find(through, true)
            && at > from
        {
            cursor = Some(at);
            let due = if rule.zone.id.is_some() {
                let local = at.to_zoned(rule.zone.tz.clone());
                wall = Some(format!(
                    "{}/wall/{}",
                    job.id,
                    local.strftime("%Y-%m-%dT%H:%M")
                ));
                go_wall(at, &rule.zone)
            } else {
                at.to_string()
            };
            run = Some((at, Occurrence::new(job, due, at, now, None)?));
        }
        Ok(cursor.map(|cursor| Evaluation {
            cursor,
            job_id: job.id.clone(),
            zone,
            wall,
            run,
        }))
    }
}

fn go_wall(at: Timestamp, zone: &super::calendar::Zone) -> String {
    let offset = zone.tz.to_offset(at).seconds();
    if offset == 0 {
        return at.to_string();
    }
    // Go retains the civil time, but RFC3339 marshaling truncates sub-minute offsets.
    let minutes = offset / 60;
    let absolute = minutes.abs();
    let sign = if minutes < 0 { '-' } else { '+' };
    let local = at.to_zoned(zone.tz.clone());
    format!(
        "{}{sign}{:02}:{:02}",
        local.strftime("%Y-%m-%dT%H:%M:%S%.f"),
        absolute / 60,
        absolute % 60
    )
}

fn before(at: Timestamp) -> Result<Timestamp> {
    Ok(Timestamp::from_nanosecond(at.as_nanosecond() - 1)?)
}

fn save_evaluation(
    tx: &mut dyn WriteBucket,
    item: Evaluation,
    skip: Option<&str>,
) -> Result<Option<Occurrence>> {
    put(tx, EVALUATED, &item.job_id, &item.cursor)?;
    if let Some(zone) = item.zone {
        put(tx, EVALUATED, &format!("{}/zone", item.job_id), &zone)?;
    }
    if let Some(wall) = item.wall {
        if tx.has_raw(EVALUATED, &wall)? {
            return Ok(None);
        }
        tx.put_raw(EVALUATED, &wall, &[1])?;
    }
    let Some((due, mut run)) = item.run else {
        return Ok(None);
    };
    if tx.has_raw(RUNS, &run.id)? {
        return Ok(None);
    }
    let age = run.claimed_at.as_nanosecond() - due.as_nanosecond();
    let grace = i128::from(run.spec.grace);
    let missed = skip.or_else(|| {
        ((grace == 0 && age >= 60_000_000_000) || (grace > 0 && age > grace))
            .then_some("latest trigger is outside the catch-up window")
    });
    if let Some(reason) = missed {
        run.state = "missed".into();
        run.detail = reason.into();
    } else {
        check_overlap(tx, &mut run)?;
    }
    put_run(tx, &run)?;
    Ok(Some(run))
}

fn check_overlap(tx: &dyn ReadBucket, run: &mut Occurrence) -> Result<()> {
    tx.walk(ACTIVE, None, &mut |key, _| {
        if run.state == "blocked" {
            return Ok(());
        }
        let other: Occurrence = require(tx, RUNS, std::str::from_utf8(key)?)?;
        if !other.reserves() {
            return Ok(());
        }
        let same_job =
            run.job_id == other.job_id && (!run.spec.allow_overlap || !other.spec.allow_overlap);
        let a = Path::new(&run.spec.directory);
        let b = Path::new(&other.spec.directory);
        let related_directory = !run.spec.allow_overlap
            && !other.spec.directory.is_empty()
            && (run.spec.workspace == "directory" || other.spec.workspace == "directory")
            && (a.starts_with(b) || b.starts_with(a));
        if same_job || related_directory {
            run.state = "blocked".into();
            run.detail = format!("overlap with active occurrence {}", other.id);
        }
        Ok(())
    })
}

fn put_run(tx: &mut dyn WriteBucket, run: &Occurrence) -> Result<()> {
    put(tx, RUNS, &run.id, run)?;
    tx.put_raw(
        HISTORY,
        &format!("{}/{}", run.job_id, run.id),
        run.id.as_bytes(),
    )?;
    let retry = run.state == "blocked"
        && run.spec.allow_overlap
        && run
            .attempts
            .last()
            .is_some_and(|a| a.state == "workspace_busy");
    if run.reserves() || retry || (run.state == "ready" && run.projected_at == zero_time()) {
        tx.put_raw(ACTIVE, &run.id, &[1])?;
    } else {
        tx.delete_raw(ACTIVE, &run.id)?;
    }
    Ok(())
}

pub(super) fn go_json(data: String) -> String {
    // Go's encoding/json escapes HTML and the JavaScript line separators.
    data.replace('<', "\\u003c")
        .replace('>', "\\u003e")
        .replace('&', "\\u0026")
        .replace('\u{2028}', "\\u2028")
        .replace('\u{2029}', "\\u2029")
}

pub(super) fn digest(data: impl AsRef<[u8]>) -> String {
    format!("{:x}", Sha256::digest(data))
}

fn initialize(path: &Path) -> Result<()> {
    let mut db = Bolt::open(path)?;
    let mut tx = db.begin_rw()?;
    for bucket in [JOBS, REVISIONS, EVALUATED, RUNS, HISTORY, ACTIVE, TEMPLATES] {
        tx.create_bucket_if_not_exists(bucket)?;
    }
    tx.commit()?;
    Ok(())
}

fn private_file(path: &Path, create: bool) -> Result<Option<File>> {
    let file = match OpenOptions::new()
        .read(true)
        .write(create)
        .create(create)
        .mode(0o600)
        .custom_flags(libc::O_NOFOLLOW | libc::O_NONBLOCK)
        .open(path)
    {
        Ok(file) => file,
        Err(e) if !create && e.kind() == io::ErrorKind::NotFound => return Ok(None),
        Err(e) => return Err(e.into()),
    };
    let meta = file.metadata()?;
    // The effective uid identifies the owner of the local scheduler state.
    if !meta.is_file()
        || meta.mode() & 0o077 != 0
        || meta.nlink() != 1
        || meta.uid() != unsafe { libc::geteuid() }
    {
        return Err(format!("schedule state file must be private: {}", path.display()).into());
    }
    Ok(Some(file))
}

type EntryVisitor<'a> = dyn FnMut(&[u8], &[u8]) -> Result<()> + 'a;

trait ReadBucket {
    fn get_raw(&self, bucket: &str, key: &str) -> Result<Option<Vec<u8>>>;
    fn has_raw(&self, bucket: &str, key: &str) -> Result<bool>;
    fn walk(&self, bucket: &str, prefix: Option<&str>, f: &mut EntryVisitor<'_>) -> Result<()>;
}

impl<'tx, T: TxApi<'tx>> ReadBucket for T {
    fn get_raw(&self, name: &str, key: &str) -> Result<Option<Vec<u8>>> {
        Ok(self
            .bucket(name)
            .ok_or("missing schedule bucket")?
            .get(key)
            .map(<[u8]>::to_vec))
    }

    fn has_raw(&self, name: &str, key: &str) -> Result<bool> {
        Ok(self
            .bucket(name)
            .ok_or("missing schedule bucket")?
            .get(key)
            .is_some())
    }

    fn walk(&self, name: &str, prefix: Option<&str>, f: &mut EntryVisitor<'_>) -> Result<()> {
        let bucket = self.bucket(name).ok_or("missing schedule bucket")?;
        if let Some(prefix) = prefix {
            let mut cursor = bucket.cursor();
            let mut entry = cursor.seek(prefix);
            while let Some((key, value)) = entry {
                if !key.starts_with(prefix.as_bytes()) {
                    break;
                }
                f(key, value.ok_or("unexpected nested schedule bucket")?)?;
                entry = cursor.next();
            }
        } else {
            for (key, value) in bucket.iter_entries() {
                f(key, value)?;
            }
        }
        Ok(())
    }
}

trait WriteBucket: ReadBucket {
    fn put_raw(&mut self, bucket: &str, key: &str, value: &[u8]) -> Result<()>;
    fn delete_raw(&mut self, bucket: &str, key: &str) -> Result<()>;
}

impl<'tx, T: TxRwRefApi<'tx>> WriteBucket for T {
    fn put_raw(&mut self, name: &str, key: &str, value: &[u8]) -> Result<()> {
        Ok(self
            .bucket_mut(name)
            .ok_or("missing schedule bucket")?
            .put(key, value)?)
    }

    fn delete_raw(&mut self, name: &str, key: &str) -> Result<()> {
        Ok(self
            .bucket_mut(name)
            .ok_or("missing schedule bucket")?
            .delete(key)?)
    }
}

fn records<T: DeserializeOwned>(tx: &dyn ReadBucket, name: &str) -> Result<Vec<T>> {
    let mut list = Vec::new();
    tx.walk(name, None, &mut |_, bytes| {
        list.push(serde_json::from_slice(bytes)?);
        Ok(())
    })?;
    Ok(list)
}

fn get<T: DeserializeOwned>(tx: &dyn ReadBucket, bucket: &str, key: &str) -> Result<Option<T>> {
    Ok(tx
        .get_raw(bucket, key)?
        .as_deref()
        .map(serde_json::from_slice)
        .transpose()?)
}

fn require<T: DeserializeOwned>(tx: &dyn ReadBucket, bucket: &str, key: &str) -> Result<T> {
    get(tx, bucket, key)?.ok_or_else(|| NOT_FOUND.into())
}

fn put(tx: &mut dyn WriteBucket, bucket: &str, key: &str, value: &impl Serialize) -> Result<()> {
    tx.put_raw(bucket, key, &serde_json::to_vec(value)?)
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::schedule::{Calendar, Temp};
    use std::thread;
    use std::{
        fs,
        os::unix::fs::PermissionsExt,
        sync::{Arc, Barrier},
    };

    fn ts(s: &str) -> Timestamp {
        s.parse().unwrap()
    }

    fn spec(dir: &Path) -> Spec {
        Spec {
            name: "Revision one".into(),
            prompt: "Inspect changes\nRésumé <yes> & results".into(),
            directory: dir.to_str().unwrap().into(),
            authority: "observe".into(),
            workspace: "directory".into(),
            agents: vec!["codex".into()],
            calendar: Calendar {
                kind: "cron".into(),
                time_zone: "UTC".into(),
                cron: "* * * * *".into(),
                ..Calendar::default()
            },
            allow_overlap: true,
            ..Spec::default()
        }
    }

    #[test]
    fn competing_ticks_claim_once_with_rewind_and_latest_catch_up() {
        let dir = Temp::new();
        let store = Store::open(&dir.0, Path::new("/etc/localtime")).unwrap();
        let job = store
            .create(spec(&dir.0), ts("2026-01-01T10:00:01Z"), false)
            .unwrap();
        let due = ts("2026-01-01T10:01:00Z");
        let barrier = Arc::new(Barrier::new(4));
        thread::scope(|threads| {
            let mut workers = Vec::new();
            for _ in 0..4 {
                let (dir, barrier) = (&dir.0, Arc::clone(&barrier));
                workers.push(threads.spawn(move || {
                    let store = Store::open(dir, Path::new("/etc/localtime")).unwrap();
                    barrier.wait();
                    store.claim_due(due).unwrap().len()
                }));
            }
            assert_eq!(
                workers
                    .into_iter()
                    .map(|t| t.join().unwrap())
                    .sum::<usize>(),
                1
            );
        });
        let reopened = Store::open(&dir.0, Path::new("/etc/localtime")).unwrap();
        assert!(reopened.claim_due(due).unwrap().is_empty());
        assert!(
            reopened
                .claim_due(ts("2026-01-01T10:00:48Z"))
                .unwrap()
                .is_empty()
        );
        assert!(reopened.claim_due(due).unwrap().is_empty());
        assert_eq!(
            reopened
                .claim_due(ts("2026-01-01T10:58:04Z"))
                .unwrap()
                .len(),
            1
        );
        let runs = reopened.history(&job.id).unwrap();
        assert_eq!(runs.len(), 2);
        assert_eq!(runs[0].due_at, "2026-01-01T10:58:00Z");
        assert_eq!(runs[1].due_at, "2026-01-01T10:01:00Z");
    }

    #[test]
    fn manual_retry_survives_pause_archive_and_stale_revision() {
        let dir = Temp::new();
        let store = Store::open(&dir.0, Path::new("/etc/localtime")).unwrap();
        let now = ts("2026-01-01T10:00:00Z");
        let mut spec = spec(&dir.0);
        spec.allow_overlap = false;
        let job = store.create(spec.clone(), now, true).unwrap();
        assert!(store.claim_due(now).unwrap().is_empty());
        let invocation = id(now).unwrap();
        let first = store.claim_manual(&job.id, &invocation, now).unwrap();
        assert_eq!(first.state, "queued");
        assert_eq!(
            store
                .claim_manual(&job.id, &id(now).unwrap(), now)
                .unwrap()
                .state,
            "blocked"
        );
        spec.name = "Revision two".into();
        let edited = store
            .edit(&job.id, Some(&job.revision_id), spec.clone(), now)
            .unwrap();
        assert!(
            matches!(store.edit(&job.id, Some(&job.revision_id), spec, now), Err(e) if e.to_string() == CONFLICT)
        );
        assert_eq!(edited.state, "paused");
        store.set_state(&job.id, "archived", now).unwrap();
        let replay = store
            .claim_manual(&job.id, &invocation, ts("2027-01-01T00:00:00Z"))
            .unwrap();
        assert_eq!(replay.request_hash, first.request_hash);
        assert_eq!(replay.revision_id, job.revision_id);
        assert_eq!(replay.spec.name, "Revision one");
        assert!(
            matches!(store.claim_manual(&job.id, &id(now).unwrap(), now), Err(e) if e.to_string() == ARCHIVED)
        );
    }

    #[test]
    fn host_zone_changes_preserve_the_first_wall_slot() {
        let dir = Temp::new();
        let copied = dir.0.join("zone");
        let timezone = |name: &str| {
            // Copy a TZif file, leaving the workstation's timezone untouched.
            fs::copy(Path::new("/usr/share/zoneinfo").join(name), &copied).unwrap();
        };
        timezone("Europe/Zurich");
        let store = Store::open(&dir.0, &copied).unwrap();
        let mut spec = spec(&dir.0);
        spec.calendar.kind = "daily".into();
        spec.calendar.time_zone = "host".into();
        spec.calendar.time = "12:00".into();
        let job = store
            .create(spec, ts("2026-01-01T10:00:00Z"), false)
            .unwrap();
        let first = store.claim_due(ts("2026-01-01T11:00:00Z")).unwrap();
        assert_eq!(first[0].due_at, "2026-01-01T12:00:00+01:00");
        timezone("Europe/London");
        assert!(
            store
                .claim_due(ts("2026-01-01T12:00:00Z"))
                .unwrap()
                .is_empty()
        );
        assert_eq!(store.history(&job.id).unwrap().len(), 1);
        assert_eq!(
            store.claim_due(ts("2026-01-02T12:00:00Z")).unwrap().len(),
            1
        );
    }

    #[test]
    fn historical_subminute_offset_keeps_exact_claim_identity() {
        let at = ts("1900-01-01T00:00:00.000000003Z");
        let zone = super::super::calendar::Zone {
            id: Some("fixture".into()),
            tz: jiff::tz::Offset::from_seconds(1172).unwrap().to_time_zone(),
        };
        assert_eq!(go_wall(at, &zone), "1900-01-01T00:19:32.000000003+00:19");
        let dir = Temp::new();
        let store = Store::open(&dir.0, Path::new("/etc/localtime")).unwrap();
        let job = store.create(spec(&dir.0), at, true).unwrap();
        let run = Occurrence::new(&job, go_wall(at, &zone), at, at, None).unwrap();
        assert_eq!(
            run.id,
            digest(format!("{}/1900-01-01T00:00:00.000000003Z", job.id))
        );
    }

    #[test]
    fn historical_go_catalog_retains_completion_and_hash() {
        let dir = Temp::new();
        let state = dir.0.join("schedules");
        crate::supervisor::runtime::private_dir(&state).unwrap();
        let path = state.join("state.db");
        let fixture = include_bytes!("../../tests/fixtures/schedule/go-catalog.db");
        let mut db = OpenOptions::new()
            .write(true)
            .create_new(true)
            .mode(0o600)
            .open(&path)
            .unwrap();
        std::io::Write::write_all(&mut db, fixture).unwrap();
        drop(db);

        let store = Store::open(&dir.0, Path::new("/etc/localtime")).unwrap();
        let job = store.jobs().unwrap().pop().unwrap();
        let before = store.history(&job.id).unwrap().pop().unwrap();
        assert_eq!(before.request_hash, before.hash().unwrap());
        assert_eq!(before.state, "ready");
        assert_eq!(
            before.completion.as_ref().unwrap().summary,
            "Fixture retained"
        );
        assert_eq!(before.attempts[0].session_id, "fixture-session");
        assert_eq!(store.templates().unwrap()[0].prompt, "Copy me");
        assert_eq!(fs::read(&path).unwrap(), fixture);

        let now = ts("2026-11-01T05:30:10.000000001Z");
        store.claim_due(now).unwrap();
        let reopened = Store::open(&dir.0, Path::new("/etc/localtime")).unwrap();
        let history = reopened.history(&job.id).unwrap();
        assert_eq!(history[0].due_at, "2026-11-01T05:30:00Z");
        assert_eq!(history[0].claimed_at, now);
        assert_eq!(history[1].request_hash, before.request_hash);
        assert_eq!(history[1].completion, before.completion);
        assert_eq!(
            history[1].attempts[0].operation_id,
            before.attempts[0].operation_id
        );
    }

    #[test]
    fn idle_claims_leave_large_archived_history_unread() {
        let dir = Temp::new();
        let store = Store::open(&dir.0, Path::new("/etc/localtime")).unwrap();
        let now = ts("2026-01-01T10:00:00Z");
        let job = store.create(spec(&dir.0), now, true).unwrap();
        store
            .write(|tx| {
                for index in 0..1200 {
                    let when = Timestamp::from_second(now.as_second() - i64::from(index))?;
                    let mut run = Occurrence::new(&job, when.to_string(), when, now, None)?;
                    run.state = "incomplete".into();
                    put_run(tx, &run)?;
                }
                Ok(())
            })
            .unwrap();
        store.set_state(&job.id, "archived", now).unwrap();
        assert_eq!(store.history(&job.id).unwrap().len(), 1200);
        let started = std::time::Instant::now();
        for _ in 0..20 {
            assert!(store.active().unwrap().is_empty());
            assert!(store.claim_due(now).unwrap().is_empty());
        }
        eprintln!(
            "20 idle claim and active-index reads with 1200 terminal rows: {:?}",
            started.elapsed()
        );
    }

    #[test]
    fn catalog_rejects_insecure_state_file() {
        let dir = Temp::new();
        let store = Store::open(&dir.0, Path::new("/etc/localtime")).unwrap();
        fs::set_permissions(
            store.dir.join("state.db"),
            fs::Permissions::from_mode(0o644),
        )
        .unwrap();
        assert!(matches!(store.jobs(), Err(e) if e.to_string().contains("must be private")));
    }
}
