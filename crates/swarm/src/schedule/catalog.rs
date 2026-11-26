use super::{
    Job, Result, Spec, Template,
    store::{Occurrence, Store},
};
use crate::ipc::{Client, field};
use serde::{Deserialize, Serialize};
use serde_json::{Value, json};
use std::{io, path::Path};

mod upload;
pub(crate) use upload::{UPLOAD_CAP, serve_upload};

pub(crate) const CAP: &str = "schedule-catalog-v1";
const OP: &str = "schedule_catalog";

#[derive(Serialize, Deserialize)]
#[serde(tag = "kind", rename_all = "snake_case", deny_unknown_fields)]
pub(crate) enum Read {
    List,
    Get { id: String },
    History { id: String },
}

#[derive(Serialize, Deserialize)]
#[serde(tag = "kind", content = "records", rename_all = "snake_case")]
pub(crate) enum Records {
    List(Vec<Job>),
    Get(Box<Job>),
    History(Vec<Occurrence>),
}

impl Read {
    pub(crate) fn remote(self, client: &mut Client) -> io::Result<Records> {
        if !client.supports(CAP) {
            return Err(io::Error::other(
                "selected supervisor does not support the automation catalog",
            ));
        }
        let reply = client.request_body(json!({"op": OP, "query": self}), OP)?;
        if field(&reply, "endpoint_id") != client.endpoint() {
            return Err(io::Error::other(
                "automation catalog reply came from another supervisor",
            ));
        }
        let records: Records = serde_json::from_value(reply["catalog"].clone())?;
        match (&self, &records) {
            (Self::List, Records::List(_)) => Ok(records),
            (Self::Get { id }, Records::Get(job)) if id == &job.id => Ok(records),
            (Self::History { id }, Records::History(runs))
                if runs.iter().all(|run| &run.job_id == id) =>
            {
                Ok(records)
            }
            _ => Err(io::Error::other(
                "automation catalog reply did not match the request",
            )),
        }
    }
}

pub(super) fn project(root: &Path, read: Read) -> Result<Records> {
    if matches!(&read, Read::Get { id } | Read::History { id } if !super::valid_id(id)) {
        return Err("invalid schedule job ID".into());
    }
    Ok(
        match (Store::existing(root, Path::new("/etc/localtime"))?, read) {
            (None, Read::List) => Records::List(Vec::new()),
            (None, _) => return Err("schedule record not found".into()),
            (Some(store), Read::List) => Records::List(store.jobs()?),
            (Some(store), Read::Get { id }) => Records::Get(Box::new(store.job(&id)?)),
            (Some(store), Read::History { id }) => Records::History(store.history(&id)?),
        },
    )
}

const TEMPLATES: &str = "schedule_templates";
const TEMPLATE_CHUNK: &str = "schedule_template_chunk";
const CHUNK_BYTES: usize = 64 << 10;

pub(crate) struct TemplateRead;

impl TemplateRead {
    pub(crate) fn remote(client: &mut Client) -> io::Result<Vec<Template>> {
        if !client.supports(CAP) {
            return Err(io::Error::other(
                "selected supervisor does not support the automation catalog",
            ));
        }
        let reply = client.request_body(json!({"op":TEMPLATES,"stream":true}), TEMPLATES)?;
        if field(&reply, "endpoint_id") != client.endpoint() {
            return Err(io::Error::other(
                "personal template reply came from another supervisor",
            ));
        }
        if reply["stream"] != true {
            return serde_json::from_value(reply["templates"].clone()).map_err(Into::into);
        }
        serde_json::from_reader(io::BufReader::new(TemplateStream {
            owner: client,
            deadline: std::time::Instant::now() + std::time::Duration::from_secs(10),
            chunk: io::Cursor::new(Vec::new()),
            sequence: 0,
            done: false,
        }))
        .map_err(Into::into)
    }
}

struct TemplateStream<'a> {
    owner: &'a mut Client,
    deadline: std::time::Instant,
    chunk: io::Cursor<Vec<u8>>,
    sequence: u64,
    done: bool,
}

impl io::Read for TemplateStream<'_> {
    fn read(&mut self, output: &mut [u8]) -> io::Result<usize> {
        use base64::{Engine, engine::general_purpose::STANDARD};
        if output.is_empty() || self.done {
            return Ok(0);
        }
        let ready = io::Read::read(&mut self.chunk, output)?;
        if ready != 0 {
            return Ok(ready);
        }
        let next = self.owner.receive(self.deadline)?;
        if next["op"] != TEMPLATE_CHUNK
            || next["endpoint_id"] != self.owner.endpoint()
            || next["sequence"] != self.sequence
        {
            return Err(io::Error::other(
                "personal template stream changed identity or order",
            ));
        }
        if next["complete"] == true && next["data"].is_null() {
            self.done = true;
            return Ok(0);
        }
        let data = next["data"]
            .as_str()
            .filter(|data| !data.is_empty() && data.len() <= CHUNK_BYTES.div_ceil(3) * 4)
            .ok_or_else(|| io::Error::other("invalid personal template stream chunk"))?;
        let decoded = STANDARD.decode(data).map_err(io::Error::other)?;
        if decoded.is_empty() || decoded.len() > CHUNK_BYTES || !next["complete"].is_null() {
            return Err(io::Error::other("invalid personal template stream chunk"));
        }
        self.sequence += 1;
        self.chunk = io::Cursor::new(decoded);
        io::Read::read(&mut self.chunk, output)
    }
}

struct TemplateChunks<'a, W> {
    output: &'a mut W,
    endpoint: &'a str,
    sequence: u64,
}

impl<W: io::Write> TemplateChunks<'_, W> {
    fn finish(self) -> io::Result<()> {
        crate::supervisor::runtime::write_control(
            self.output,
            &json!({"op":TEMPLATE_CHUNK,"endpoint_id":self.endpoint,
                "sequence":self.sequence,"complete":true}),
        )
    }
}

impl<W: io::Write> io::Write for TemplateChunks<'_, W> {
    fn write(&mut self, data: &[u8]) -> io::Result<usize> {
        use base64::{Engine, engine::general_purpose::STANDARD};
        let size = data.len().min(CHUNK_BYTES);
        if size == 0 {
            return Ok(0);
        }
        crate::supervisor::runtime::write_control(
            self.output,
            &json!({"op":TEMPLATE_CHUNK,
            "endpoint_id":self.endpoint,"sequence":self.sequence,"data":STANDARD.encode(&data[..size])}),
        )?;
        self.sequence += 1;
        Ok(size)
    }

    fn flush(&mut self) -> io::Result<()> {
        self.output.flush()
    }
}

pub(crate) fn stream_templates(
    output: &mut impl io::Write,
    endpoint: &str,
    templates: &[Template],
) -> io::Result<()> {
    use crate::supervisor::runtime::write_control;
    use io::Write as _;
    write_control(
        output,
        &json!({"op":TEMPLATES,"endpoint_id":endpoint,"stream":true}),
    )?;
    let mut chunks = TemplateChunks {
        output,
        endpoint,
        sequence: 0,
    };
    let mut buffered = io::BufWriter::with_capacity(CHUNK_BYTES, &mut chunks);
    serde_json::to_writer(&mut buffered, templates)?;
    buffered.flush()?;
    drop(buffered);
    chunks.finish()
}

pub(crate) fn templates(root: &Path) -> Result<Vec<Template>> {
    Ok(match Store::existing(root, Path::new("/etc/localtime"))? {
        Some(store) => store.templates()?,
        None => Vec::new(),
    })
}

pub(crate) fn serve_templates(root: &Path) -> Result<Value> {
    let templates = templates(root)?;
    let reply = json!({"op":TEMPLATES,"templates":templates});
    if serde_json::to_vec(&reply)?.len() > (1 << 20) - 1024 {
        return Err("personal template result exceeds the owner socket frame limit".into());
    }
    Ok(reply)
}

pub(crate) fn serve(root: &Path, request: &Value) -> Result<Value> {
    let read = serde_json::from_value(request["query"].clone())?;
    let records = project(root, read)?;
    let reply = json!({"op":OP, "catalog":records});
    if serde_json::to_vec(&reply)?.len() > (1 << 20) - 1024 {
        return Err("automation catalog result exceeds the owner socket frame limit".into());
    }
    Ok(reply)
}

pub(crate) const CONTROL: &str = "schedule-catalog-control-v1";
const CHANGE: &str = "schedule_catalog_change";
const TEMPLATE_CHANGE: &str = "schedule_template_change";

#[derive(Serialize, Deserialize)]
#[serde(tag = "kind", rename_all = "snake_case", deny_unknown_fields)]
pub(crate) enum TemplateChange {
    Save {
        id: Option<String>,
        #[serde(default, skip_serializing_if = "Option::is_none")]
        create_id: Option<String>,
        name: String,
        prompt: String,
        #[serde(default, skip_serializing_if = "Option::is_none")]
        expected_updated_at: Option<jiff::Timestamp>,
    },
    Delete {
        id: String,
        expected_updated_at: Option<jiff::Timestamp>,
    },
}

impl TemplateChange {
    pub(crate) fn remote(self, client: &mut Client, path: &Path) -> io::Result<Option<Template>> {
        if !client.supports(CONTROL) {
            return Err(io::Error::other(
                "selected supervisor does not support template changes",
            ));
        }
        if let Some(length) = upload::required_size(&self, client.endpoint())? {
            return upload::remote(self, path, client.endpoint(), length).map(Some);
        }
        let reply =
            client.request_body(json!({"op":TEMPLATE_CHANGE,"change":self}), TEMPLATE_CHANGE)?;
        if field(&reply, "endpoint_id") != client.endpoint() {
            return Err(io::Error::other(
                "personal template reply came from another supervisor",
            ));
        }
        let template = match self {
            Self::Save {
                id,
                create_id,
                name,
                prompt,
                ..
            } => {
                let template: Template = serde_json::from_value(reply["template"].clone())?;
                if id
                    .as_ref()
                    .or(create_id.as_ref())
                    .is_some_and(|id| id != &template.id)
                    || template.name != name
                    || template.prompt != prompt
                {
                    return Err(io::Error::other(
                        "personal template change reply did not match the request",
                    ));
                }
                Some(template)
            }
            Self::Delete { id, .. } => {
                if field(&reply, "template_id") != id {
                    return Err(io::Error::other(
                        "personal template delete reply did not match the request",
                    ));
                }
                None
            }
        };
        Ok(template)
    }
}

fn template_target<'a>(
    id: Option<&'a str>,
    create_id: Option<&'a str>,
    expected_updated_at: Option<jiff::Timestamp>,
) -> Result<super::store::TemplateTarget<'a>> {
    use super::store::TemplateTarget;
    if id.or(create_id).is_some_and(|id| !super::valid_id(id)) {
        return Err("invalid personal template ID".into());
    }
    if id.is_some() != expected_updated_at.is_some() {
        return Err("editing a personal template requires its current updated_at".into());
    }
    if id.is_some() && create_id.is_some() {
        return Err("a personal template change cannot create and edit together".into());
    }
    Ok(id.map_or(TemplateTarget::Create(create_id), |id| {
        TemplateTarget::Edit(id, expected_updated_at)
    }))
}

pub(crate) fn change_template(root: &Path, request: &Value, endpoint_id: &str) -> Result<Value> {
    let change: TemplateChange = serde_json::from_value(request["change"].clone())?;
    let store = Store::open(root, Path::new("/etc/localtime"))?;
    match change {
        TemplateChange::Save {
            id,
            create_id,
            name,
            prompt,
            expected_updated_at,
        } => {
            let target = template_target(id.as_deref(), create_id.as_deref(), expected_updated_at)?;
            let template = store.save_template_if_current(
                target,
                name,
                prompt,
                endpoint_id,
                jiff::Timestamp::now(),
            )?;
            Ok(json!({"op":TEMPLATE_CHANGE,"template":template}))
        }
        TemplateChange::Delete {
            id,
            expected_updated_at,
        } => {
            if !super::valid_id(&id) {
                return Err("invalid personal template ID".into());
            }
            let expected_updated_at = expected_updated_at
                .ok_or("deleting a personal template requires its current updated_at")?;
            store.delete_template_if_current(&id, Some(expected_updated_at))?;
            Ok(json!({"op":TEMPLATE_CHANGE,"template_id":id}))
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use std::{
        fs::{self, OpenOptions},
        os::unix::fs::OpenOptionsExt,
        path::PathBuf,
    };

    struct Temp(PathBuf);

    impl Temp {
        fn new() -> Self {
            let path = std::env::temp_dir().join(format!(
                "swarm-template-catalog-{}",
                super::super::id(jiff::Timestamp::now()).unwrap()
            ));
            crate::supervisor::runtime::private_dir(&path).unwrap();
            Self(path)
        }

        fn with_go_catalog() -> Self {
            let temp = Self::new();
            let directory = temp.0.join("schedules");
            crate::supervisor::runtime::private_dir(&directory).unwrap();
            let mut file = OpenOptions::new()
                .write(true)
                .create_new(true)
                .mode(0o600)
                .open(directory.join("state.db"))
                .unwrap();
            std::io::Write::write_all(
                &mut file,
                include_bytes!("../../tests/fixtures/schedule/go-catalog.db"),
            )
            .unwrap();
            temp
        }
    }

    impl Drop for Temp {
        fn drop(&mut self) {
            let _ = fs::remove_dir_all(&self.0);
        }
    }

    fn change(temp: &Temp, request: &Value) -> Result<Value> {
        change_template(&temp.0, request, "fixture-endpoint")
    }

    #[test]
    fn template_catalog_operations_preserve_go_written_template() {
        let temp = Temp::with_go_catalog();
        let store = Store::open(&temp.0, Path::new("/etc/localtime")).unwrap();
        let listed = serve_templates(&temp.0).unwrap();
        let templates: Vec<Template> = serde_json::from_value(listed["templates"].clone()).unwrap();
        assert_eq!(templates.len(), 1);
        let original = &templates[0];
        assert_eq!(original.name, "Existing template");
        assert_eq!(original.prompt, "Copy me");
        assert_eq!(original.created_at, "2026-11-01T05:00:00Z".parse().unwrap());

        let database = temp.0.join("schedules/state.db");
        let unchanged = fs::read(&database).unwrap();
        assert!(change(
            &temp,
            &json!({"change":{"kind":"save","id":original.id,"name":"No fence","prompt":"No fence"}}),
        )
        .is_err());
        assert_eq!(fs::read(&database).unwrap(), unchanged);
        assert!(change(&temp, &json!({"change":{"kind":"delete","id":original.id}}),).is_err());
        assert_eq!(fs::read(&database).unwrap(), unchanged);

        store
            .save_template(
                Some(&original.id),
                "Changed elsewhere".into(),
                "New prompt".into(),
                "2026-11-01T06:00:00Z".parse().unwrap(),
            )
            .unwrap();
        let before_stale = fs::read(&database).unwrap();
        assert!(change(
            &temp,
            &json!({"change":{"kind":"save","id":original.id,"name":"Stale","prompt":"Stale","expected_updated_at":original.updated_at}}),
        )
        .is_err());
        assert_eq!(fs::read(&database).unwrap(), before_stale);
        assert!(change(
            &temp,
            &json!({"change":{"kind":"delete","id":original.id,"expected_updated_at":original.updated_at}}),
        )
        .is_err());
        assert_eq!(fs::read(&database).unwrap(), before_stale);

        let created = change(
            &temp,
            &json!({"change":{"kind":"save","name":"New","prompt":"First\nSecond"}}),
        )
        .unwrap();
        let created: Template = serde_json::from_value(created["template"].clone()).unwrap();
        let edited = change(
            &temp,
            &json!({"change":{"kind":"save","id":created.id,"name":"Edited","prompt":"Updated","expected_updated_at":created.updated_at}}),
        )
        .unwrap();
        let edited: Template = serde_json::from_value(edited["template"].clone()).unwrap();
        assert_eq!(edited.id, created.id);
        assert_eq!(edited.created_at, created.created_at);
        assert_eq!(edited.name, "Edited");

        let deleted = change(
            &temp,
            &json!({"change":{"kind":"delete","id":edited.id,"expected_updated_at":edited.updated_at}}),
        )
        .unwrap();
        assert_eq!(deleted["template_id"], created.id);
        let templates = Store::open(&temp.0, Path::new("/etc/localtime"))
            .unwrap()
            .templates()
            .unwrap();
        assert_eq!(templates.len(), 1);
        assert_eq!(templates[0].id, original.id);
        assert_eq!(templates[0].prompt, "New prompt");
    }

    #[test]
    fn explicit_template_create_identity_recovers_an_unacknowledged_reply() {
        let temp = Temp::with_go_catalog();
        let id = "01M8C694P5EA2PJDV268PQ74XW";
        let change_body = |prompt| {
            json!({"change":{"kind":"save","create_id":id,
            "name":"Retry fixture","prompt":prompt}})
        };
        let first = change(&temp, &change_body("same prompt")).unwrap();
        assert_eq!(first["template"]["id"], id);
        let database = temp.0.join("schedules/state.db");
        let unchanged = fs::read(&database).unwrap();
        assert_eq!(change(&temp, &change_body("same prompt")).unwrap(), first);
        assert_eq!(fs::read(&database).unwrap(), unchanged);
        let large_endpoint = "e".repeat(crate::ipc::wire::MAX_FRAME);
        assert!(
            change_template(&temp.0, &change_body("same prompt"), &large_endpoint)
                .unwrap_err()
                .to_string()
                .contains("reply exceeds")
        );
        assert_eq!(fs::read(&database).unwrap(), unchanged);
        assert!(change(&temp, &change_body("different prompt")).is_err());
        assert_eq!(fs::read(&database).unwrap(), unchanged);
        assert_eq!(
            serve_templates(&temp.0).unwrap()["templates"]
                .as_array()
                .unwrap()
                .len(),
            2
        );
        let store = Store::open(&temp.0, Path::new("/etc/localtime")).unwrap();
        store.delete_template(id).unwrap();
        let deleted = fs::read(&database).unwrap();
        assert!(
            change(&temp, &change_body("same prompt"))
                .unwrap_err()
                .to_string()
                .contains("was deleted")
        );
        assert_eq!(fs::read(&database).unwrap(), deleted);
        assert!(store.template(id).is_err());
    }

    #[test]
    fn oversized_template_create_fails_without_commit_or_retry_duplicate() {
        let temp = Temp::with_go_catalog();
        Store::open(&temp.0, Path::new("/etc/localtime")).unwrap();
        let database = temp.0.join("schedules/state.db");
        let unchanged = fs::read(&database).unwrap();
        let prompt = "x".repeat(crate::ipc::wire::MAX_FRAME - 170);
        let request = json!({
            "op":"schedule_template_change",
            "endpoint_id":"fixture-endpoint",
            "change": {
                "kind":"save",
                "name":"Too large",
                "prompt":prompt,
            }
        });
        assert!(serde_json::to_vec(&request).unwrap().len() < crate::ipc::wire::MAX_FRAME);
        let candidate = Template {
            id: "0".repeat(26),
            name: "Too large".into(),
            prompt: "x".repeat(crate::ipc::wire::MAX_FRAME - 170),
            created_at: "2026-11-01T05:00:00Z".parse().unwrap(),
            updated_at: "2026-11-01T05:00:00Z".parse().unwrap(),
        };
        let success = json!({
            "op":"schedule_template_change",
            "template":candidate.clone(),
            "endpoint_id":"fixture-endpoint",
        });
        assert!(serde_json::to_vec(&success).unwrap().len() >= crate::ipc::wire::MAX_FRAME);
        let list = json!({"op":"schedule_templates","templates":[candidate]});
        assert!(serde_json::to_vec(&list).unwrap().len() > crate::ipc::wire::MAX_FRAME - 1024);

        for _ in 0..2 {
            assert!(change(&temp, &request).is_err());
            assert_eq!(fs::read(&database).unwrap(), unchanged);
            assert_eq!(
                Store::open(&temp.0, Path::new("/etc/localtime"))
                    .unwrap()
                    .templates()
                    .unwrap()
                    .len(),
                1
            );
        }
    }

    #[test]
    fn template_stream_refuses_a_valid_document_without_a_completed_transfer() {
        use crate::supervisor::runtime::{read_control, write_control};
        let temp = crate::test_temp::root().join(format!(
            "ts-{}",
            &crate::supervisor::runtime::random_id().unwrap()[..8]
        ));
        std::fs::create_dir(&temp).unwrap();
        let socket = temp.join("p");
        let listener = std::os::unix::net::UnixListener::bind(&socket).unwrap();
        let server = std::thread::spawn(move || {
            let (mut stream, _) = listener.accept().unwrap();
            stream
                .set_read_timeout(Some(std::time::Duration::from_secs(2)))
                .unwrap();
            assert_eq!(read_control(&mut stream).unwrap()["op"], "hello");
            write_control(
                &mut stream,
                &json!({"op":"hello","endpoint_id":"fixture",
                "protocol_version":1,"capabilities":[CAP]}),
            )
            .unwrap();
            let request = read_control(&mut stream).unwrap();
            assert_eq!(request["stream"], true);
            write_control(
                &mut stream,
                &json!({"op":TEMPLATES,"endpoint_id":"fixture","stream":true}),
            )
            .unwrap();
            // This is a complete JSON array, but the owner closes before acknowledging the transfer.
            write_control(
                &mut stream,
                &json!({"op":TEMPLATE_CHUNK,"endpoint_id":"fixture",
                "sequence":0,"data":"W10="}),
            )
            .unwrap();
        });
        let mut owner = Client::dial(&socket, &[CAP]).unwrap();
        let received = TemplateRead::remote(&mut owner);
        server.join().unwrap();
        std::fs::remove_dir_all(temp).unwrap();
        assert!(received.is_err());
    }

    #[test]
    fn retained_large_template_has_a_complete_bounded_snapshot() {
        use base64::{Engine, engine::general_purpose::STANDARD};
        let temp = Temp::with_go_catalog();
        let store = Store::open(&temp.0, Path::new("/etc/localtime")).unwrap();
        let prompt = "🙂\n".repeat(240_000);
        assert!(prompt.len() > crate::ipc::wire::MAX_FRAME);
        let legacy = store
            .save_template(
                None,
                "Large Go template".into(),
                prompt,
                "2026-11-01T06:00:00Z".parse().unwrap(),
            )
            .unwrap();
        assert!(serve_templates(&temp.0).is_err());
        let snapshot = templates(&temp.0).unwrap();
        let mut bytes = Vec::new();
        stream_templates(&mut bytes, "owner", &snapshot).unwrap();
        let mut wire = io::Cursor::new(bytes);
        let (tag, header) = crate::ipc::wire::read_frame(&mut wire).unwrap();
        assert_eq!(tag, crate::ipc::wire::CONTROL);
        assert_eq!(
            serde_json::from_slice::<Value>(&header).unwrap(),
            json!({"op":TEMPLATES, "endpoint_id":"owner", "stream":true})
        );
        let mut joined = Vec::new();
        for index in 0.. {
            let (tag, payload) = crate::ipc::wire::read_frame(&mut wire).unwrap();
            assert_eq!(tag, crate::ipc::wire::CONTROL);
            let record: Value = serde_json::from_slice(&payload).unwrap();
            assert_eq!(record["endpoint_id"], "owner");
            assert_eq!(record["sequence"], index);
            if record["complete"] == true {
                assert!(index > 1);
                break;
            }
            let part = STANDARD.decode(record["data"].as_str().unwrap()).unwrap();
            assert!(!part.is_empty() && part.len() <= CHUNK_BYTES);
            joined.extend(part);
        }
        assert_eq!(wire.position(), wire.get_ref().len() as u64);
        let rendered: Vec<Template> = serde_json::from_slice(&joined).unwrap();
        assert_eq!(rendered.len(), snapshot.len());
        let large = rendered.iter().find(|item| item.id == legacy.id).unwrap();
        assert!(large.prompt == legacy.prompt);
        assert_eq!(large.updated_at, legacy.updated_at);
    }

    #[test]
    fn local_cli_can_shorten_go_catalog_template_while_list_stays_oversized() {
        let temp = Temp::with_go_catalog();
        let store = Store::open(&temp.0, Path::new("/etc/localtime")).unwrap();
        let prompt = "x".repeat(crate::ipc::wire::MAX_FRAME);
        let first = store
            .save_template(
                None,
                "Large first".into(),
                prompt.clone(),
                "2026-11-01T06:00:00Z".parse().unwrap(),
            )
            .unwrap();
        let second = store
            .save_template(
                None,
                "Large second".into(),
                prompt,
                "2026-11-01T06:00:01Z".parse().unwrap(),
            )
            .unwrap();
        assert!(serve_templates(&temp.0).is_err());

        store
            .save_template(
                Some(&first.id),
                "Shortened locally".into(),
                "short prompt".into(),
                "2026-11-01T06:00:02Z".parse().unwrap(),
            )
            .unwrap();
        assert!(serve_templates(&temp.0).is_err());
        assert_eq!(store.template(&first.id).unwrap().prompt, "short prompt");
        assert_eq!(
            store.template(&second.id).unwrap().prompt.len(),
            crate::ipc::wire::MAX_FRAME
        );
    }
}

#[derive(Clone, Serialize, Deserialize)]
#[serde(rename_all = "snake_case")]
pub(crate) enum State {
    Active,
    Paused,
    Archived,
}

impl State {
    fn token(&self) -> &'static str {
        match self {
            Self::Active => "active",
            Self::Paused => "paused",
            Self::Archived => "archived",
        }
    }
}

#[derive(Clone, Serialize, Deserialize)]
#[serde(tag = "kind", rename_all = "snake_case", deny_unknown_fields)]
pub(crate) enum Change {
    Create {
        operation: String,
        spec: Box<Spec>,
    },
    CreateEnabled {
        operation: String,
        spec: Box<Spec>,
    },
    Edit {
        id: String,
        revision: String,
        spec: Box<Spec>,
        #[serde(default, skip_serializing_if = "Option::is_none")]
        paused: Option<(u64, jiff::Timestamp)>,
    },
    SaveDraft {
        id: String,
        revision: String,
        generation: u64,
        updated_at: jiff::Timestamp,
        spec: Box<Spec>,
    },
    SaveActive {
        id: String,
        revision: String,
        #[serde(default, skip_serializing_if = "Option::is_none")]
        operation: Option<String>,
        generation: u64,
        updated_at: jiff::Timestamp,
        spec: Box<Spec>,
    },
    Enable {
        id: String,
        revision: String,
        generation: u64,
        updated_at: jiff::Timestamp,
        spec: Box<Spec>,
        install_timer: bool,
    },
    State {
        id: String,
        revision: String,
        expected: String,
        generation: u64,
        updated_at: jiff::Timestamp,
        value: State,
    },
    TimerCheck {
        action: Box<Change>,
        status: String,
    },
}

pub(crate) enum ChangeResult {
    Saved(Records),
    ConfirmTimer(Option<String>),
}

impl Change {
    pub(crate) fn checks_activation(&self) -> bool {
        matches!(
            self,
            Self::Enable { .. }
                | Self::CreateEnabled { .. }
                | Self::SaveActive { .. }
                | Self::TimerCheck { .. }
        )
    }

    fn timer_key(&self) -> Option<Value> {
        match self {
            Self::CreateEnabled { operation, .. } => Some(json!({"operation":operation})),
            Self::SaveActive {
                id,
                revision,
                operation: Some(operation),
                generation,
                updated_at,
                ..
            } => Some(
                json!({"id":id,"revision":revision,"operation":operation,"generation":generation,"updated_at":updated_at}),
            ),
            Self::TimerCheck { action, .. } => action.timer_key(),
            _ => None,
        }
    }

    pub(crate) fn check_timer_install(&self, client: &mut Client, status: &str) -> io::Result<()> {
        let request = Self::TimerCheck {
            action: Box::new(self.clone()),
            status: status.into(),
        };
        match request.remote(client)? {
            ChangeResult::ConfirmTimer(Some(observed)) if observed == status => Ok(()),
            _ => Err(io::Error::other("automation timer approval changed")),
        }
    }

    pub(crate) fn remote(&self, client: &mut Client) -> io::Result<ChangeResult> {
        if !client.supports(CONTROL) {
            return Err(io::Error::other(
                "selected supervisor does not support automation changes",
            ));
        }
        let request = json!({"op":CHANGE,"change":self,"inline_timer_confirmation":true});
        let reply = if self.checks_activation() {
            client.exchange(request, CHANGE, std::time::Duration::from_secs(60))?
        } else {
            client.request_body(request, CHANGE)?
        };
        if field(&reply, "endpoint_id") != client.endpoint() {
            return Err(io::Error::other(
                "automation change reply came from another supervisor",
            ));
        }
        if let Self::Enable {
            id,
            revision,
            install_timer: false,
            ..
        } = self
            && reply["timer_confirmation"] == json!({"id":id,"revision":revision})
        {
            return Ok(ChangeResult::ConfirmTimer(None));
        }
        if let Some(scope) = self.timer_key()
            && reply["timer_confirmation"]["scope"] == scope
            && let Some(status) = reply["timer_confirmation"]["status"].as_str()
            && inactive_timer_status(status)
        {
            return Ok(ChangeResult::ConfirmTimer(Some(status.into())));
        }
        let job: Job = serde_json::from_value(reply["job"].clone())?;
        let target = match self {
            Self::Create { operation, .. } => operation == &job.id && job.state == "paused",
            Self::CreateEnabled { operation, .. } => operation == &job.id && job.state == "active",
            Self::Edit { id, .. } => id == &job.id,
            Self::SaveDraft { id, .. } => id == &job.id && job.state == "paused",
            Self::Enable { id, .. } => id == &job.id && job.state == "active",
            Self::SaveActive {
                id,
                operation,
                generation,
                ..
            } => {
                id == &job.id
                    && job.state == "active"
                    && *generation == job.lifecycle_generation
                    && operation
                        .as_ref()
                        .is_none_or(|revision| revision == &job.revision_id)
            }
            Self::State { id, value, .. } => id == &job.id && job.state == value.token(),
            Self::TimerCheck { .. } => false,
        };
        if !target {
            return Err(io::Error::other(
                "automation change reply did not match the request",
            ));
        }
        Ok(ChangeResult::Saved(Records::Get(Box::new(job))))
    }
}

pub(crate) fn change(
    root: &Path,
    request: &Value,
    readiness: impl FnOnce(&Spec) -> Result<()>,
    conflict: impl Fn(&Path) -> Option<String>,
) -> Result<Value> {
    let change: Change = serde_json::from_value(request["change"].clone())?;
    let (change, required_status) = match change {
        Change::TimerCheck { action, status }
            if matches!(
                *action,
                Change::CreateEnabled { .. }
                    | Change::SaveActive {
                        operation: Some(_),
                        ..
                    }
            ) && inactive_timer_status(&status) =>
        {
            (*action, Some(status))
        }
        Change::TimerCheck { .. } => return Err("invalid automation timer approval".into()),
        change => (change, None),
    };
    let scope = (required_status.is_some() || request["inline_timer_confirmation"] == true)
        .then(|| change.timer_key())
        .flatten();
    if matches!(&change, Change::Create { operation, .. } | Change::CreateEnabled { operation, .. } if !super::valid_id(operation))
    {
        return Err("invalid automation creation operation ID".into());
    }
    if matches!(&change,
        Change::Edit { id, revision, .. } | Change::SaveDraft { id, revision, .. } | Change::SaveActive { id, revision, .. } | Change::Enable { id, revision, .. }
            | Change::State { id, revision, .. }
            if !super::valid_id(id) || !super::valid_id(revision))
    {
        return Err("invalid automation job or revision ID".into());
    }
    if matches!(&change, Change::SaveActive { id, revision, operation:Some(operation), .. }
        if !super::valid_id(operation) || operation == revision || operation == id)
    {
        return Err("invalid automation revision operation ID".into());
    }
    let host_zone = Path::new("/etc/localtime");
    if let Change::CreateEnabled {
        operation,
        mut spec,
    } = change
    {
        spec.prepare(host_zone)?;
        let store = Store::existing(root, host_zone)?;
        if let Some(store) = &store
            && let Some(saved) = store.creation_receipt(&operation, &spec, "active")?
        {
            if required_status.is_some() {
                return Err("automation draft was already saved".into());
            }
            return Ok(json!({"op":CHANGE,"job":saved}));
        }
        Store::check_future_at(host_zone, &spec, jiff::Timestamp::now())?;
        let active = store
            .as_ref()
            .map(Store::active)
            .transpose()?
            .unwrap_or_default();
        qualify_enabled_save(root, &operation, &spec, &active, readiness, conflict)?;
        if let Some(confirmation) = timer_confirmation(scope.as_ref(), required_status.as_deref())?
        {
            // A timer check can wait while another caller creates the catalog or claims the ID.
            if required_status.is_some()
                && let Some(current) = Store::existing(root, host_zone)?
                && current
                    .creation_receipt(&operation, &spec, "active")?
                    .is_some()
            {
                return Err("automation draft was already saved".into());
            }
            return Ok(confirmation);
        }
        let store = match store {
            Some(store) => store,
            None => Store::open(root, host_zone)?,
        };
        let job = store.create_bound(*spec, jiff::Timestamp::now(), false, Some(&operation))?;
        return Ok(json!({"op":CHANGE,"job":job}));
    }
    let store = if matches!(change, Change::SaveActive { .. }) {
        Store::existing(root, host_zone)?.ok_or("automation catalog is unavailable")?
    } else {
        Store::open(root, host_zone)?
    };
    let now = jiff::Timestamp::now();
    let job = match change {
        Change::Create { operation, spec } => {
            store.create_bound(*spec, now, true, Some(&operation))?
        }
        Change::CreateEnabled { .. } => {
            unreachable!("enabled create handled before opening catalog")
        }
        Change::TimerCheck { .. } => unreachable!("timer check unwrapped before opening catalog"),
        Change::Edit {
            id,
            revision,
            spec,
            paused,
        } => store.edit_bound(
            &id,
            Some(&revision),
            *spec,
            paused.map(|(generation, updated)| ("paused", generation, updated, "paused")),
            now,
        )?,
        Change::SaveDraft {
            id,
            revision,
            generation,
            updated_at,
            spec,
        } => store.edit_bound(
            &id,
            Some(&revision),
            *spec,
            Some(("active", generation, updated_at, "paused")),
            now,
        )?,
        Change::SaveActive {
            id,
            revision,
            operation,
            generation,
            updated_at,
            mut spec,
        } => {
            let target = store.job(&id)?;
            spec.prepare(host_zone)?;
            if let Some(operation) = operation.as_ref()
                && target.revision_id == *operation
                && target.spec == *spec
                && target.state == "active"
                && target.lifecycle_generation == generation
            {
                if required_status.is_some() {
                    return Err("automation draft was already saved".into());
                }
                return Ok(json!({"op":CHANGE,"job":target}));
            }
            require_active_revision(&target, &revision, generation, updated_at)?;
            if spec.calendar != target.spec.calendar {
                store.check_future(&spec, now)?;
            }
            qualify_enabled_save(root, &id, &spec, &store.active()?, readiness, conflict)?;
            if let Some(confirmation) =
                timer_confirmation(scope.as_ref(), required_status.as_deref())?
            {
                if required_status.is_some() {
                    require_active_revision(&store.job(&id)?, &revision, generation, updated_at)?;
                }
                return Ok(confirmation);
            }
            store.edit_to_revision(
                &id,
                Some(&revision),
                *spec,
                Some(("active", generation, updated_at, "active")),
                operation.as_deref(),
                jiff::Timestamp::now(),
            )?
        }
        Change::Enable {
            id,
            revision,
            generation,
            updated_at,
            mut spec,
            install_timer,
        } => {
            let target = store.job(&id)?;
            if target.revision_id != revision || !target.paused_revision(generation, updated_at) {
                return Err("automation revision or state changed".into());
            }
            spec.prepare(Path::new("/etc/localtime"))?;
            store.check_future(&spec, now)?;
            readiness(&spec)?;
            super::workspace::preview(root, &id, &spec, &store.active()?, conflict)?;
            let installed =
                super::timer::command("status")?.as_deref() != Some("installed, active");
            if installed {
                if !install_timer {
                    return Ok(
                        json!({"op":CHANGE,"timer_confirmation":{"id":id,"revision":revision}}),
                    );
                }
                super::timer::command("install")?;
                if super::timer::command("status")?.as_deref() != Some("installed, active") {
                    return Err("host timer is not active; retry or save paused".into());
                }
            }
            store
                .edit_bound(
                    &id,
                    Some(&revision),
                    *spec,
                    Some(("paused", generation, updated_at, "active")),
                    jiff::Timestamp::now(),
                )
                .map_err(|error| {
                    if installed {
                        format!("host timer enabled; job save failed: {error}").into()
                    } else {
                        error
                    }
                })?
        }
        Change::State {
            id,
            revision,
            expected,
            generation,
            updated_at,
            value,
        } => store.set_state_bound(
            &id,
            value.token(),
            Some((&revision, &expected, generation, updated_at)),
            now,
        )?,
    };
    Ok(json!({"op":CHANGE,"job":job}))
}

fn require_active_revision(
    job: &Job,
    revision: &str,
    generation: u64,
    updated_at: jiff::Timestamp,
) -> Result<()> {
    if job.revision_id != revision
        || job.state != "active"
        || job.lifecycle_generation != generation
        || job.updated_at != updated_at
    {
        return Err("automation revision or state changed".into());
    }
    Ok(())
}

fn qualify_enabled_save(
    root: &Path,
    id: &str,
    spec: &Spec,
    active: &[super::Occurrence],
    readiness: impl FnOnce(&Spec) -> Result<()>,
    conflict: impl Fn(&Path) -> Option<String>,
) -> Result<()> {
    readiness(spec)?;
    super::workspace::preview(root, id, spec, active, conflict)
}

fn inactive_timer_status(status: &str) -> bool {
    matches!(
        status,
        "not installed, inactive" | "partially installed, inactive" | "installed, inactive"
    )
}

fn timer_confirmation(
    scope: Option<&Value>,
    required_status: Option<&str>,
) -> Result<Option<Value>> {
    let cancel = std::sync::atomic::AtomicBool::new(false);
    let status = super::timer::board_status(&cancel)?;
    if required_status.is_some_and(|expected| expected != status) {
        return Err("timer status changed; reopen the automation before enabling the timer".into());
    }
    if status == "installed, active" {
        return Ok(None);
    }
    if let Some(scope) = scope
        && inactive_timer_status(&status)
    {
        return Ok(Some(
            json!({"op":CHANGE,"timer_confirmation":{"scope":scope,"status":status}}),
        ));
    }
    Err("Enable the timer from Automations before saving enabled.".into())
}

pub(crate) const MANUAL_CAP: &str = "schedule-board-manual-v1";
pub(crate) const MANUAL_OP: &str = "schedule_board_manual";

#[derive(Serialize, Deserialize)]
#[serde(tag = "kind", rename_all = "snake_case", deny_unknown_fields)]
pub(crate) enum Manual {
    Status,
    Run { previous: Option<String> },
}

#[derive(Serialize, Deserialize)]
pub(crate) struct RunState {
    pub(crate) id: String,
    pub(crate) state: String,
    pub(crate) pending: bool,
}

impl Manual {
    pub(crate) fn remote(self, client: &mut Client, job: &str) -> io::Result<Option<RunState>> {
        if !client.supports(MANUAL_CAP) {
            return Err(io::Error::other(
                "selected supervisor does not support durable board runs",
            ));
        }
        let reply = client.exchange(
            json!({"op":MANUAL_OP,"schedule_id":job,"intent":self}),
            MANUAL_OP,
            std::time::Duration::from_secs(110),
        )?;
        if field(&reply, "endpoint_id") != client.endpoint() || field(&reply, "schedule_id") != job
        {
            return Err(io::Error::other(
                "automation run reply did not match the request",
            ));
        }
        Ok(serde_json::from_value(reply["run"].clone())?)
    }
}
