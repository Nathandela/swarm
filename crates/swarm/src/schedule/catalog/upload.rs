//! An owner-only connection for saving one template that outgrows a control frame.
use super::{
    CHUNK_BYTES, TEMPLATE_CHANGE, TEMPLATE_CHUNK, TemplateChange, TemplateChunks, template_target,
};
use crate::{
    ipc::{Client, wire},
    schedule::{
        Template,
        store::{
            ByteCount, MAX_TEMPLATE_VALUE, Store, TemplateTarget, template_fingerprint,
            template_size,
        },
        validate_text,
    },
    socket::{DeadlineReader, DeadlineWriter},
    supervisor::runtime::write_control,
};
use base64::{Engine, engine::general_purpose::STANDARD};
use jiff::Timestamp;
use serde::{Deserialize, Serialize, de::DeserializeOwned};
use serde_json::{Value, json};
use std::{
    io::{self, Read, Write},
    net::Shutdown,
    os::unix::net::UnixStream,
    path::Path,
    sync::Mutex,
    time::{Duration, Instant},
};

pub(crate) const UPLOAD_CAP: &str = "schedule-template-upload-v1";
const UPLOAD: &str = "schedule_template_upload";
const BEGIN_TIMEOUT: Duration = Duration::from_secs(10);
static ACTIVE_UPLOAD: Mutex<()> = Mutex::new(());

#[derive(Serialize, Deserialize)]
#[serde(deny_unknown_fields)]
struct Begin {
    op: String,
    endpoint_id: String,
    id: Option<String>,
    create_id: Option<String>,
    expected_updated_at: Option<Timestamp>,
    encoded_bytes: usize,
    name_bytes: usize,
}

#[derive(Deserialize)]
#[serde(deny_unknown_fields)]
struct Chunk {
    op: String,
    endpoint_id: String,
    sequence: u64,
    data: Option<String>,
    complete: Option<bool>,
}

#[derive(Serialize, Deserialize)]
#[serde(deny_unknown_fields)]
struct Saved {
    id: String,
    created_at: Timestamp,
    updated_at: Timestamp,
    fingerprint: String,
}

pub(super) fn required_size(change: &TemplateChange, endpoint: &str) -> io::Result<Option<usize>> {
    let TemplateChange::Save {
        id,
        create_id,
        name,
        prompt,
        ..
    } = change
    else {
        return Ok(None);
    };
    #[derive(Serialize)]
    struct Request<'a> {
        op: &'static str,
        endpoint_id: &'a str,
        change: &'a TemplateChange,
    }
    let mut count = ByteCount::default();
    serde_json::to_writer(
        &mut count,
        &Request {
            op: TEMPLATE_CHANGE,
            endpoint_id: endpoint,
            change,
        },
    )?;
    if count.0 < wire::MAX_FRAME {
        // The successful inline reply can be larger than the request. Reserve full precision for both dates.
        let date = "9999-12-31T23:59:59.999999999Z";
        let reply = json!({"op":TEMPLATE_CHANGE,"endpoint_id":endpoint,"template":{
            "id":id.as_deref().or(create_id.as_deref()).unwrap_or("00000000000000000000000000"),
            "created_at":date,"updated_at":date,"name":"","prompt":""}});
        let mut count = ByteCount::default();
        serde_json::to_writer(&mut count, &(name, prompt))?;
        let empty_strings = b"[\"\",\"\"]".len();
        let projected = serde_json::to_vec(&reply)?
            .len()
            .saturating_add(count.0 - empty_strings);
        if projected < wire::MAX_FRAME {
            return Ok(None);
        }
    }
    let size = name.len().saturating_add(prompt.len());
    if size > MAX_TEMPLATE_VALUE {
        return Err(io::Error::other(
            "personal template exceeds the Go Bolt encoded value limit",
        ));
    }
    Ok(Some(size))
}

fn transfer_time(size: usize) -> Duration {
    // One absolute deadline, with extra time for exceptionally large Bolt values.
    Duration::from_secs(15 + size.div_ceil(8 << 20) as u64)
}

pub(super) fn remote(
    change: TemplateChange,
    path: &Path,
    endpoint: &str,
    encoded_bytes: usize,
) -> io::Result<Template> {
    let TemplateChange::Save {
        id,
        create_id,
        name,
        prompt,
        expected_updated_at,
    } = change
    else {
        return Err(io::Error::other("only a template save can be uploaded"));
    };
    let expected_id = id
        .as_deref()
        .or(create_id.as_deref())
        .ok_or_else(|| io::Error::other("template upload requires an exact ID"))?;
    validate_text(&name, &prompt).map_err(explain)?;
    let begin_deadline = Instant::now() + BEGIN_TIMEOUT;
    let mut owner = Client::dial_before(path, &[UPLOAD_CAP], begin_deadline)?;
    if owner.endpoint() != endpoint {
        return Err(io::Error::other(
            "template owner changed; reopen templates before saving",
        ));
    }
    if !owner.supports(UPLOAD_CAP) {
        return Err(io::Error::other(
            "selected supervisor does not support oversized template saves",
        ));
    }
    let header = Begin {
        op: UPLOAD.into(),
        endpoint_id: endpoint.into(),
        id: id.clone(),
        create_id: create_id.clone(),
        expected_updated_at,
        encoded_bytes,
        name_bytes: name.len(),
    };
    write_control(
        &mut DeadlineWriter(owner.stream(), begin_deadline),
        &serde_json::to_value(header)?,
    )?;
    let reply = checked_reply(&mut owner, begin_deadline, endpoint)?;
    if reply != json!({"op":UPLOAD,"endpoint_id":endpoint,"ready":true}) {
        return Err(invalid_wire());
    }
    let deadline = Instant::now() + transfer_time(encoded_bytes);
    let mut output = DeadlineWriter(owner.stream(), deadline);
    let mut chunks = TemplateChunks {
        output: &mut output,
        endpoint,
        sequence: 0,
    };
    chunks.write_all(name.as_bytes())?;
    chunks.write_all(prompt.as_bytes())?;
    chunks.finish()?;
    owner.stream().shutdown(Shutdown::Write)?;
    let reply = checked_reply(&mut owner, deadline, endpoint)?;
    let saved: Saved = serde_json::from_value(reply["saved"].clone())?;
    if reply["op"] != UPLOAD
        || saved.id != expected_id
        || saved.fingerprint != template_fingerprint(&name, &prompt)?
    {
        return Err(invalid_wire());
    }
    Ok(Template {
        id: saved.id,
        name,
        prompt,
        created_at: saved.created_at,
        updated_at: saved.updated_at,
    })
}

fn checked_reply(owner: &mut Client, deadline: Instant, endpoint: &str) -> io::Result<Value> {
    let reply = owner.receive(deadline)?;
    if reply["endpoint_id"] != endpoint {
        return Err(invalid_wire());
    }
    if reply["op"] == "error" {
        return Err(io::Error::other(
            crate::ipc::field(&reply, "error").to_owned(),
        ));
    }
    Ok(reply)
}

fn invalid_wire() -> io::Error {
    io::Error::other("invalid personal template upload")
}

fn explain(error: Box<dyn std::error::Error>) -> io::Error {
    io::Error::other(error.to_string())
}

fn read_control<T: DeserializeOwned>(input: &mut impl Read) -> io::Result<T> {
    let (tag, bytes) = wire::read_frame(input)?;
    if tag != wire::CONTROL {
        return Err(invalid_wire());
    }
    serde_json::from_slice(&bytes).map_err(|_| invalid_wire())
}

fn extend(bytes: &mut Vec<u8>, data: &[u8], limit: usize) -> io::Result<()> {
    let length = bytes.len() + data.len();
    if length > bytes.capacity() {
        let capacity = length.max(bytes.capacity().saturating_mul(2)).min(limit);
        bytes
            .try_reserve_exact(capacity - bytes.len())
            .map_err(io::Error::other)?;
    }
    bytes.extend_from_slice(data);
    Ok(())
}

fn read_content(
    input: &mut impl Read,
    endpoint: &str,
    size: usize,
    name_size: usize,
) -> io::Result<(String, String)> {
    let (mut name, mut prompt) = (Vec::new(), Vec::new());
    for sequence in 0u64.. {
        let packet: Chunk = read_control(input)?;
        if packet.op != TEMPLATE_CHUNK
            || packet.endpoint_id != endpoint
            || packet.sequence != sequence
        {
            return Err(invalid_wire());
        }
        let remaining = size - name.len() - prompt.len();
        if packet.complete == Some(true) && packet.data.is_none() && remaining == 0 {
            break;
        }
        let data = packet
            .data
            .as_deref()
            .filter(|s| !s.is_empty() && s.len() <= CHUNK_BYTES.div_ceil(3) * 4)
            .ok_or_else(invalid_wire)?;
        let bytes = STANDARD.decode(data).map_err(|_| invalid_wire())?;
        if packet.complete.is_some() || bytes.is_empty() || bytes.len() > remaining.min(CHUNK_BYTES)
        {
            return Err(invalid_wire());
        }
        let (a, b) = bytes.split_at(bytes.len().min(name_size - name.len()));
        extend(&mut name, a, name_size)?;
        extend(&mut prompt, b, size - name_size)?;
    }
    // A dedicated sender half-closes after the completion marker. No trailing frames are allowed.
    if input.read(&mut [0])? != 0 {
        return Err(invalid_wire());
    }
    Ok((
        String::from_utf8(name).map_err(|_| invalid_wire())?,
        String::from_utf8(prompt).map_err(|_| invalid_wire())?,
    ))
}

fn receive_upload(
    root: &Path,
    stream: &UnixStream,
    endpoint: &str,
    deadline: &mut Instant,
) -> io::Result<Value> {
    let _permit = ACTIVE_UPLOAD
        .try_lock()
        .map_err(|_| io::Error::other("another oversized personal template is uploading"))?;
    let begin: Begin = read_control(&mut DeadlineReader(stream, *deadline))?;
    if begin.op != UPLOAD
        || begin.endpoint_id != endpoint
        || begin.encoded_bytes > MAX_TEMPLATE_VALUE
        || begin.name_bytes == 0
        || begin.name_bytes >= begin.encoded_bytes
    {
        return Err(invalid_wire());
    }
    let target = template_target(
        begin.id.as_deref(),
        begin.create_id.as_deref(),
        begin.expected_updated_at,
    )
    .map_err(explain)?;
    if matches!(target, TemplateTarget::Create(None)) {
        return Err(invalid_wire());
    }
    write_control(
        &mut DeadlineWriter(stream, *deadline),
        &json!({"op":UPLOAD,"endpoint_id":endpoint,"ready":true}),
    )?;
    *deadline = Instant::now() + transfer_time(begin.encoded_bytes);
    let (name, prompt) = read_content(
        &mut DeadlineReader(stream, *deadline),
        endpoint,
        begin.encoded_bytes,
        begin.name_bytes,
    )?;
    validate_text(&name, &prompt).map_err(explain)?;
    let now = Timestamp::now();
    let host = Path::new("/etc/localtime");
    if let TemplateTarget::Create(Some(id)) = target {
        template_size(id, &name, &prompt, now, now)?;
    }
    if Instant::now() >= *deadline {
        return Err(io::ErrorKind::TimedOut.into());
    }
    let catalog = match target {
        TemplateTarget::Create(Some(_)) => Store::open(root, host).map_err(explain)?,
        TemplateTarget::Edit(..) => Store::existing(root, host)
            .map_err(explain)?
            .ok_or_else(|| io::Error::other("schedule record not found"))?,
        TemplateTarget::Create(None) => return Err(invalid_wire()),
    };
    if Instant::now() >= *deadline {
        return Err(io::ErrorKind::TimedOut.into());
    }
    let saved = catalog
        .save_uploaded_template(target, name, prompt, now)
        .map_err(explain)?;
    let reply = Saved {
        id: saved.id,
        created_at: saved.created_at,
        updated_at: saved.updated_at,
        fingerprint: template_fingerprint(&saved.name, &saved.prompt)?,
    };
    Ok(json!({"op":UPLOAD,"endpoint_id":endpoint,"saved":reply}))
}

pub(crate) fn serve_upload(root: &Path, stream: &UnixStream, endpoint: &str) -> io::Result<()> {
    let mut deadline = Instant::now() + BEGIN_TIMEOUT;
    let reply = receive_upload(root, stream, endpoint, &mut deadline).unwrap_or_else(|error| {
        json!({"op":"error","endpoint_id":endpoint,"error_code":"invalid_field","error":error.to_string()})
    });
    write_control(&mut DeadlineWriter(stream, deadline), &reply)
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn chunks_require_the_exact_utf8_length_and_half_close() {
        let name = "🙂 legacy";
        let prompt = "a".repeat(CHUNK_BYTES - name.len() - 1) + "é and a last line\n";
        let words = format!("{name}{prompt}");
        let mut frames = Vec::new();
        let mut chunks = TemplateChunks {
            output: &mut frames,
            endpoint: "owner",
            sequence: 0,
        };
        chunks.write_all(words.as_bytes()).unwrap();
        chunks.finish().unwrap();
        assert_eq!(
            read_content(&mut frames.as_slice(), "owner", words.len(), name.len()).unwrap(),
            (name.to_owned(), prompt)
        );
        assert!(read_content(&mut frames.as_slice(), "other", words.len(), name.len()).is_err());
        assert!(
            read_content(&mut frames.as_slice(), "owner", words.len() + 1, name.len()).is_err()
        );
        frames.push(0);
        assert!(read_content(&mut frames.as_slice(), "owner", words.len(), name.len()).is_err());
    }

    #[test]
    fn partial_upload_io_cannot_extend_the_absolute_deadline() {
        let (client, mut server) = UnixStream::pair().unwrap();
        let drip = std::thread::spawn(move || {
            for _ in 0..20 {
                if server.write_all(&[0]).is_err() {
                    return;
                }
                std::thread::sleep(Duration::from_millis(20));
            }
        });
        let error = read_control::<Chunk>(&mut DeadlineReader(
            &client,
            Instant::now() + Duration::from_millis(45),
        ))
        .err()
        .unwrap();
        assert_eq!(error.kind(), io::ErrorKind::TimedOut);
        drop(client);
        drip.join().unwrap();
    }
}
