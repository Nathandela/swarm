use super::*;
use std::collections::BTreeMap;
use std::io::ErrorKind;
use std::os::fd::AsRawFd;
use std::sync::atomic::{AtomicBool, Ordering};

static INTERRUPTED: AtomicBool = AtomicBool::new(false);

extern "C" fn interrupted(_: libc::c_int) {
    INTERRUPTED.store(true, Ordering::Relaxed);
}

struct Signals(Vec<(libc::c_int, libc::sigaction)>);

impl Signals {
    fn install() -> io::Result<Self> {
        INTERRUPTED.store(false, Ordering::Relaxed);
        let mut saved = Self(Vec::new());
        // SAFETY: sigaction structures are initialized and the handler only sets an atomic.
        let mut action: libc::sigaction = unsafe { std::mem::zeroed() };
        action.sa_sigaction = interrupted as *const () as usize;
        unsafe { libc::sigemptyset(&mut action.sa_mask) };
        for signal in [libc::SIGINT, libc::SIGTERM] {
            let mut old = std::mem::MaybeUninit::<libc::sigaction>::uninit();
            if unsafe { libc::sigaction(signal, &action, old.as_mut_ptr()) } < 0 {
                return Err(io::Error::last_os_error());
            }
            saved.0.push((signal, unsafe { old.assume_init() }));
        }
        Ok(saved)
    }
}

impl Drop for Signals {
    fn drop(&mut self) {
        for (signal, saved) in &self.0 {
            // SAFETY: restore the initialized dispositions saved by install.
            unsafe { libc::sigaction(*signal, saved, std::ptr::null_mut()) };
        }
    }
}

fn active(ctx: &Context) -> io::Result<()> {
    if INTERRUPTED.load(Ordering::Relaxed) {
        return Err(io::Error::other("pilot wait interrupted"));
    }
    ctx.check()
}

fn ready(client: &Client, deadline: Instant) -> io::Result<bool> {
    let remaining = deadline
        .saturating_duration_since(Instant::now())
        .as_millis()
        .min(100) as i32;
    let mut fd = libc::pollfd {
        fd: client.stream().as_raw_fd(),
        events: libc::POLLIN,
        revents: 0,
    };
    // SAFETY: client keeps the descriptor open and poll writes into the initialized struct.
    let result = unsafe { libc::poll(&mut fd, 1, remaining) };
    if result >= 0 {
        return Ok(result > 0);
    }
    let err = io::Error::last_os_error();
    if err.kind() == ErrorKind::Interrupted {
        Ok(false)
    } else {
        Err(err)
    }
}

struct Output {
    fd: i32,
    flags: i32,
}

impl Output {
    fn new() -> io::Result<Self> {
        let fd = libc::STDOUT_FILENO;
        // SAFETY: stdout is borrowed from this process and never closed by this guard.
        let flags = unsafe { libc::fcntl(fd, libc::F_GETFL) };
        if flags < 0 || unsafe { libc::fcntl(fd, libc::F_SETFL, flags | libc::O_NONBLOCK) } < 0 {
            return Err(io::Error::last_os_error());
        }
        Ok(Self { fd, flags })
    }

    fn line(
        &self,
        ctx: &Context,
        receipt: &str,
        cursor: u64,
        boundary: u64,
        worker: Value,
        deadline: Instant,
    ) -> io::Result<()> {
        let mut line =
            json!({"trusted_guidance":guidance::REMINDER,"receipt":receipt,"cursor":cursor});
        if boundary > 0 {
            line["boundary_cursor"] = json!(boundary)
        }
        line["worker_data"] = worker;
        let mut bytes = serde_json::to_vec(&line)?;
        bytes.push(b'\n');
        let mut data = bytes.as_slice();
        while !data.is_empty() {
            active(ctx)?;
            if Instant::now() >= deadline {
                return Err(io::Error::from(ErrorKind::TimedOut));
            }
            // SAFETY: data remains readable for the call and stdout is still borrowed.
            let n = unsafe { libc::write(self.fd, data.as_ptr().cast(), data.len()) };
            if n > 0 {
                data = &data[n as usize..];
                continue;
            }
            if n == 0 {
                return Err(io::Error::from(ErrorKind::WriteZero));
            }
            let err = io::Error::last_os_error();
            match err.kind() {
                ErrorKind::Interrupted => continue,
                ErrorKind::WouldBlock => {
                    let mut fd = libc::pollfd {
                        fd: self.fd,
                        events: libc::POLLOUT,
                        revents: 0,
                    };
                    // SAFETY: stdout and the initialized stack entry outlive this call.
                    unsafe { libc::poll(&mut fd, 1, 100) };
                }
                _ => return Err(err),
            }
        }
        Ok(())
    }
}

impl Drop for Output {
    fn drop(&mut self) {
        // SAFETY: restore the inherited flags of the borrowed descriptor.
        unsafe { libc::fcntl(self.fd, libc::F_SETFL, self.flags) };
    }
}

fn duration_flag(raw: &str) -> Result<Duration> {
    duration::parse(raw)
        .filter(|n| *n > 0)
        .map(|n| Duration::from_nanos(n as u64))
        .ok_or_else(|| misuse("timeout requires a positive duration"))
}

fn expiry(ctx: &Context) -> Instant {
    Instant::now() + Duration::try_from(ctx.expires - OffsetDateTime::now_utc()).unwrap_or_default()
}

fn relevant(record: &Value) -> bool {
    match field(record, "type") {
        "structured_gap" | "exited" | "lost" | "deleted" => true,
        "group_transition" => matches!(
            field(record, "group"),
            "needs_input" | "ready_for_review" | "completed"
        ),
        "interaction" => {
            let item = &record["item"];
            (item["kind"] == "approval_request" && item["status"] == "in_progress")
                || terminal_reply(item)
        }
        _ => false,
    }
}

fn terminal_reply(item: &Value) -> bool {
    item["kind"] == "agent_message"
        && matches!(field(item, "status"), "completed" | "failed" | "declined")
}

fn records(frame: &Value, key: &str) -> Result<Vec<Value>> {
    match frame.get(key) {
        None | Some(Value::Null) => Ok(Vec::new()),
        Some(Value::Array(values)) => Ok(values.clone()),
        _ => Err(fail("invalid journal records")),
    }
}

pub(super) fn run(ctx: &Context, client: &mut Client, args: &[String]) -> Result<u8> {
    let mut after = None;
    let mut once = false;
    let mut timeout = None;
    let mut ids = BTreeMap::new();
    let mut it = args.iter();
    while let Some(arg) = it.next() {
        match arg.as_str() {
            "--after" if after.is_none() => {
                after = Some(
                    it.next()
                        .and_then(|n| n.parse::<u64>().ok())
                        .ok_or_else(|| misuse("watch --after needs a numeric cursor"))?,
                )
            }
            "--once" if !once => once = true,
            "--timeout" if timeout.is_none() => {
                timeout = Some(duration_flag(it.next().ok_or_else(|| {
                    misuse("watch --timeout needs one positive duration")
                })?)?)
            }
            arg if !arg.is_empty() && !arg.starts_with('-') => {
                ids.insert(arg.to_owned(), arg.to_owned());
            }
            _ => {
                return Err(misuse(
                    "watch needs explicit discussion IDs and unambiguous flags",
                ));
            }
        }
    }
    if ids.is_empty() || timeout.is_some() && !once {
        return Err(misuse(
            "watch needs explicit discussion IDs and --timeout requires --once",
        ));
    }
    if !client.supports("journal") || !client.supports("journal-subscribe-from") {
        return Err(fail("watch requires atomic journal subscription"));
    }
    let rows = client.sessions().map_err(fail)?;
    for id in ids.keys().cloned().collect::<Vec<_>>() {
        let present = rows.iter().any(|row| row["id"] == id);
        let scoped = id.split_once('/').filter(|(endpoint, local)| {
            *endpoint == client.endpoint() && !local.is_empty() && !local.contains('/')
        });
        // An explicit replay can name a retired discussion. A fresh snapshot
        // still requires a current row rather than waiting on a mistyped ID.
        if !present && (after.is_none() || scoped.is_none()) {
            return Err(misuse(format!(
                "watch: discussion {id:?} is absent; refresh roster"
            )));
        }
        if let Some((_, local)) = scoped {
            ids.insert(local.to_owned(), id);
        }
    }
    let normalize = |mut record: Value| -> Option<Value> {
        record["session_id"] = json!(ids.get(field(&record, "session_id"))?);
        Some(record)
    };
    let context_ends = expiry(ctx);
    let deadline = if once {
        context_ends.min(Instant::now() + timeout.unwrap_or(TEN_MINUTES))
    } else {
        context_ends
    };
    let _signals = Signals::install().map_err(fail)?;
    let output = Output::new().map_err(fail)?;
    ctx.check().map_err(fail)?;
    client
        .send(
            &json!({"op":"journal_subscribe_from","endpoint_id":client.endpoint(),
        "cursor":after.unwrap_or(u64::MAX-1),"journal_max_bytes":1048575}),
        )
        .map_err(fail)?;
    let mut head = None;
    let mut full = false;
    let mut snapshot = Vec::new();
    let mut replay = Vec::new();
    let mut size = 0;
    loop {
        let page_deadline = deadline.min(Instant::now() + Duration::from_secs(10));
        loop {
            active(ctx).map_err(fail)?;
            if Instant::now() >= page_deadline {
                return Err(fail("watch: journal snapshot timed out"));
            }
            if ready(client, page_deadline).map_err(fail)? {
                break;
            }
        }
        if Instant::now() >= deadline {
            return Err(fail("watch: journal snapshot timed out"));
        }
        let frame = client.receive(page_deadline).map_err(fail)?;
        if frame["op"] == "error" {
            return Err(fail(field(&frame, "error")));
        }
        if frame["op"] != "journal_subscribe_from" || frame["endpoint_id"] != client.endpoint() {
            return Err(fail("invalid journal snapshot page"));
        }
        let cursor = frame["cursor"].as_u64().unwrap_or(0);
        let page_full = frame["full_resync"] == true;
        if head.is_some() && (head != Some(cursor) || page_full != full) {
            return Err(fail("journal boundary changed during snapshot"));
        }
        head = Some(cursor);
        full = page_full;
        size += frame.to_string().len();
        if size > 32 << 20 {
            return Err(fail("journal snapshot exceeds client limit"));
        }
        snapshot.extend(records(&frame, "roster")?);
        replay.extend(records(&frame, "journal")?);
        if frame["journal_more"] != true {
            break;
        }
    }
    let head = head.unwrap_or_default();
    if !full && let Some(from) = after.filter(|from| *from <= head) {
        let mut previous = from;
        for record in &replay {
            let next = record["cursor"]
                .as_u64()
                .filter(|next| *next > previous && *next <= head)
                .ok_or_else(|| fail("invalid journal replay sequence"))?;
            previous = next;
        }
    }
    let mut cursor = after.unwrap_or(head);
    output
        .line(
            ctx,
            "snapshot",
            cursor,
            head,
            json!(
                snapshot
                    .into_iter()
                    .filter_map(normalize)
                    .collect::<Vec<_>>()
            ),
            deadline,
        )
        .map_err(fail)?;
    if full || after.is_some_and(|from| from > head) {
        output
            .line(
                ctx,
                "history_gap",
                head,
                0,
                json!({"full_resync":full,"cursor_ahead":cursor>head}),
                deadline,
            )
            .map_err(fail)?;
        cursor = head;
        if once {
            return Ok(0);
        }
    } else if after.is_some() {
        for record in replay.into_iter().filter_map(normalize).filter(relevant) {
            let sequence = record["cursor"]
                .as_u64()
                .ok_or_else(|| fail("invalid event cursor"))?;
            if sequence <= cursor || sequence > head {
                continue;
            }
            output
                .line(ctx, "event", sequence, 0, record, deadline)
                .map_err(fail)?;
            cursor = sequence;
            if once {
                return Ok(0);
            }
        }
        cursor = head;
    }
    loop {
        active(ctx).map_err(fail)?;
        if Instant::now() >= deadline {
            if !once || deadline == context_ends {
                return Err(fail("watch stopped: pilot context expired"));
            }
            output
                .line(
                    ctx,
                    "waiting",
                    cursor,
                    0,
                    json!({"reason":"timeout"}),
                    context_ends.min(Instant::now() + Duration::from_secs(5)),
                )
                .map_err(fail)?;
            return Ok(2);
        }
        if !ready(client, deadline).map_err(fail)? {
            continue;
        }
        match client.receive(deadline.min(Instant::now() + Duration::from_secs(10))) {
            Ok(frame)
                if frame["op"] == "journal_event" && frame["endpoint_id"] == client.endpoint() =>
            {
                for record in records(&frame, "journal")? {
                    let sequence = record["cursor"]
                        .as_u64()
                        .ok_or_else(|| fail("invalid live event cursor"))?;
                    if sequence <= cursor {
                        continue;
                    }
                    if let Some(record) = normalize(record).filter(relevant) {
                        output
                            .line(ctx, "event", sequence, 0, record, deadline)
                            .map_err(fail)?;
                        if once {
                            return Ok(0);
                        }
                    }
                    cursor = sequence;
                }
            }
            _ => {
                output
                    .line(
                        ctx,
                        "disconnected",
                        cursor,
                        0,
                        json!({"reason":"journal connection closed"}),
                        context_ends.min(Instant::now() + Duration::from_secs(5)),
                    )
                    .map_err(fail)?;
                return Ok(1);
            }
        }
    }
}

pub(super) fn await_reply(
    ctx: &Context,
    client: &mut Client,
    args: &[String],
) -> Result<(Value, bool)> {
    let Some((id, args)) = args
        .split_first()
        .filter(|(id, _)| !id.is_empty() && !id.starts_with('-'))
    else {
        return Err(misuse("await needs an explicit discussion ID"));
    };
    let mut limit = TEN_MINUTES;
    let extra = options(args, &["timeout"], &[], |_, value| {
        limit = duration_flag(value).map_err(|(_, msg)| msg)?;
        Ok(())
    })
    .map_err(misuse)?;
    if !extra.is_empty() {
        return Err(misuse("await accepts --timeout <positive duration>"));
    }
    let _signals = Signals::install().map_err(fail)?;
    let deadline = expiry(ctx).min(Instant::now() + limit);
    let path = pending_path(ctx, id);
    let saved = read_optional(&path)?;
    let no_saved_operation = saved.is_none();
    let mut pending = saved.unwrap_or_else(|| json!({}));
    let sent = OffsetDateTime::parse(field(&pending, "sent_at"), &Rfc3339)
        .ok()
        .unwrap_or_else(OffsetDateTime::now_utc);
    let lower = if no_saved_operation {
        history(client, id).ok().map(|items| {
            items
                .iter()
                .filter_map(|item| item["cursor"].as_u64())
                .max()
                .unwrap_or(0)
        })
    } else if pending["history_ready"] == true {
        pending["cursor"].as_u64()
    } else {
        None
    };
    client.request("subscribe", None, "ok").map_err(fail)?;
    let mut row = target(client, id)?;
    loop {
        active(ctx).map_err(fail)?;
        let op = field(&pending, "operation_id").to_owned();
        if !op.is_empty()
            && let Ok(receipt) = operation_status(client, id, &op)
            && native_input(&receipt)
        {
            let saved = &pending["receipt"];
            let same = ["native_message_id", "native_turn_id"]
                .into_iter()
                .all(|key| field(saved, key).is_empty() || saved[key] == receipt[key]);
            if same {
                pending["receipt"] = receipt;
                if pending["uncertain"] == true
                    && (native_terminal(&pending["receipt"])
                        || matches!(
                            field(&pending["receipt"], "state"),
                            "native_accepted" | "admitted" | "running"
                        ))
                {
                    let _guard = lock(&ctx.path).map_err(fail)?;
                    active(ctx).map_err(fail)?;
                    if read_optional(&path)?.is_some_and(|r| r["operation_id"] == op) {
                        pending["uncertain"] = json!(false);
                        save(&path, &pending)?;
                    }
                }
            }
        }
        if native_terminal(&pending["receipt"]) {
            return Ok((
                json!({"state":row,"operation":pending["receipt"],"reply_confirmed":true}),
                false,
            ));
        }
        if pending["uncertain"] != true && !native_input(&pending["receipt"]) {
            let replied = lower.is_some_and(|lower| {
                history(client, id).ok().is_some_and(|items| {
                    items.iter().any(|item| {
                        item["cursor"].as_u64().is_some_and(|n| n > lower)
                            && terminal_reply(&item["item"])
                    })
                })
            });
            let later = OffsetDateTime::parse(field(&row, "group_entered_at"), &Rfc3339)
                .ok()
                .is_some_and(|at| at > sent);
            if replied
                || (later
                    && matches!(
                        field(&row, "group"),
                        "needs_input" | "ready_for_review" | "completed"
                    ))
            {
                let mut data = match view(client, id) {
                    Ok(view) => view,
                    Err(_) if row["status"]["Process"] != "running" => {
                        json!({"state":row,"source":"observed_process_state"})
                    }
                    Err(err) => return Err(err),
                };
                data["reply_confirmed"] = json!(replied);
                return Ok((data, false));
            }
        }
        if Instant::now() >= deadline {
            let mut data = json!({"state":target(client,id)?,"status":"no_fresh_reply_confirmed"});
            if pending["uncertain"] == true {
                data["status"] = json!("delivery_uncertain");
                data["operation_id"] = pending["operation_id"].clone()
            }
            if pending["receipt"].is_object() {
                data["operation"] = pending["receipt"].clone()
            }
            return Ok((data, true));
        }
        let tick = (Instant::now() + Duration::from_secs(1)).min(deadline);
        loop {
            active(ctx).map_err(fail)?;
            if Instant::now() >= tick {
                break;
            }
            if !client.has_push() && !ready(client, tick).map_err(fail)? {
                continue;
            }
            let event = client.push(deadline).map_err(fail)?;
            if event["op"] == "event" && event["session"]["id"] == id.as_str() {
                row = event["session"].clone();
                break;
            }
        }
    }
}
