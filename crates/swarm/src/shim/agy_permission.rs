//! Scheduled ACP approvals use an ordered display offer and the current attach lease.
use super::{ScreenState, State};
use crate::{
    ipc::{field, wire},
    supervisor::runtime,
};
use serde_json::{Value, json};
use std::{
    io,
    os::unix::net::UnixStream,
    sync::mpsc::{self, SyncSender},
    time::{Duration, Instant},
};

pub(super) struct Pending {
    id: String,
    lines: Vec<String>,
    choices: usize,
    answer: SyncSender<Option<usize>>,
    peek: Option<(Value, Instant, u64)>,
}

impl Pending {
    fn visible(&self, screen: &vt100::Screen) -> bool {
        let (_, width) = screen.size();
        let mut rows: Vec<_> = screen.rows(0, width.get()).collect();
        while rows.last().is_some_and(|line| line.trim().is_empty()) {
            rows.pop();
        }
        rows.len() >= self.lines.len()
            && rows[rows.len() - self.lines.len()..]
                .iter()
                .map(|line| line.trim_end())
                .eq(self.lines.iter().map(String::as_str))
    }
}

/// Serialized with terminal output and called again after a new snapshot or resize.
pub(super) fn offer(screen: &mut ScreenState) {
    let Some(subscriber) = &mut screen.subscriber else {
        return;
    };
    if !subscriber.agy_permission {
        return;
    }
    let pending = screen
        .agy_pending
        .as_ref()
        .filter(|request| request.visible(screen.parser.screen()));
    let id = pending.map(|request| request.id.as_str());
    if subscriber.agy_offer.as_deref() == id {
        return;
    }
    subscriber.agy_offer = id.map(str::to_owned);
    subscriber.agy_armed = false;
    let Ok(display) = runtime::random_id() else {
        let _ = subscriber.conn.shutdown(std::net::Shutdown::Both);
        screen.subscriber = None;
        return;
    };
    subscriber.agy_display = display;
    let message = json!({
        "op":"agy_permission","request":id,"display":subscriber.agy_display,"choices":pending.map(|request| request.choices),
    });
    if subscriber
        .output
        .try_send((wire::CONTROL, message.to_string().into_bytes()))
        .is_err()
    {
        let _ = subscriber.conn.shutdown(std::net::Shutdown::Both);
        screen.subscriber = None;
    }
}

pub(super) fn begin(state: &State, peer: &mut UnixStream, request: &Value) -> io::Result<()> {
    let refusal = || io::Error::other("unproved scheduled Antigravity permission request");
    let Some(binding) = state.schedule.as_ref().filter(|_| state.provider == "agy") else {
        return Err(refusal());
    };
    let pid = runtime::peer_pid(peer)?;
    if !state
        .child
        .lock()
        .unwrap()
        .as_ref()
        .is_some_and(|child| child.id() == pid)
    {
        return Err(refusal());
    }
    let observed = runtime::read_json(&state.dir.join("observation.json"))?;
    if observed["token"] != state.token
        || observed["attempt"] != binding.attempt_id
        || observed["phase"] != "permission"
        || observed["operation"] != request["operation"]
        || observed["conversation_id"] != request["conversation_id"]
    {
        return Err(refusal());
    }
    let choices = request["options"]
        .as_array()
        .filter(|choices| (1..=12).contains(&choices.len()))
        .ok_or_else(refusal)?;
    let clean = |text: &str| -> String {
        text.chars()
            .filter(|ch| !ch.is_control())
            .take(240)
            .collect()
    };
    let mut lines = vec![format!(
        "Antigravity requests permission for {}",
        clean(field(request, "title"))
    )];
    for (index, choice) in choices.iter().enumerate() {
        lines.push(format!(
            "{}. {}",
            index + 1,
            clean(choice.as_str().ok_or_else(refusal)?)
        ));
    }
    lines.push("Enter an option number, or 0 to cancel.".into());
    let payload = format!("\r\n{}\r\n", lines.join("\r\n")).into_bytes();
    let (answer, decision) = mpsc::sync_channel(1);
    {
        let mut screen = state.screen.lock().unwrap();
        if screen.agy_pending.is_some() {
            return Err(refusal());
        }
        screen.parser.process(&payload);
        if let Some(sub) = &screen.subscriber
            && sub.output.try_send((runtime::DATA_OUT, payload)).is_err()
        {
            let _ = sub.conn.shutdown(std::net::Shutdown::Both);
            screen.subscriber = None;
        }
        screen.agy_pending = Some(Pending {
            id: runtime::random_id()?,
            lines,
            choices: choices.len(),
            answer,
            peek: None,
        });
        offer(&mut screen);
    }
    let choice = decision.recv().map_err(io::Error::other)?;
    runtime::write_control(
        peer,
        &json!({"type":"agy_permission_result","choice":choice}),
    )
}

pub(super) fn snapshot_offer(screen: &ScreenState) -> Option<Value> {
    let pending = screen.agy_pending.as_ref()?;
    pending
        .visible(screen.parser.screen())
        .then(|| json!({"request":pending.id,"lines":pending.lines}))
}

pub(super) fn acknowledge_peek(screen: &mut ScreenState, request: &Value) -> bool {
    let Some(pending) = &mut screen.agy_pending else {
        return false;
    };
    let context = &request["context"];
    if request["request"] != pending.id
        || !pending.visible(screen.parser.screen())
        || !context["scope"].is_array()
        || !context["born"].is_u64()
    {
        return false;
    }
    let Ok(cutoff) = runtime::birth_clock() else {
        return false;
    };
    pending.peek = Some((context["scope"].clone(), Instant::now(), cutoff));
    true
}

pub(super) fn cli_selection(screen: &mut ScreenState, request: &Value) -> bool {
    let Some(pending) = &screen.agy_pending else {
        return false;
    };
    let Some((scope, when, cutoff)) = &pending.peek else {
        return false;
    };
    let contexts = request["permission_contexts"].as_array();
    let matched = contexts.is_some_and(|contexts| {
        contexts.iter().any(|context| {
            context["scope"] == *scope
                && context["born"].as_u64().is_some_and(|born| born > *cutoff)
        })
    });
    let Some(choice) = field(request, "data")
        .trim()
        .parse::<usize>()
        .ok()
        .filter(|&choice| choice <= pending.choices)
    else {
        return false;
    };
    if request["submit"] != true
        || !matched
        || when.elapsed() > Duration::from_secs(45)
        || !pending.visible(screen.parser.screen())
    {
        return false;
    }
    respond(screen, (choice != 0).then_some(choice))
}

pub(super) fn revoke_peek(screen: &mut ScreenState) {
    if let Some(request) = &mut screen.agy_pending {
        request.peek = None;
    }
}

pub(super) fn control(screen: &mut ScreenState, request: &Value) {
    let Some(pending) = &screen.agy_pending else {
        return;
    };
    let Some(lease) = &mut screen.subscriber else {
        return;
    };
    if lease.agy_offer.as_deref() != Some(pending.id.as_str())
        || request["request"] != pending.id
        || request["display"] != lease.agy_display
        || !pending.visible(screen.parser.screen())
    {
        return;
    }
    match field(request, "type") {
        "agy_permission_displayed" => lease.agy_armed = true,
        "agy_permission_reply" if lease.agy_armed => {
            let choice = request["choice"]
                .as_u64()
                .and_then(|number| usize::try_from(number).ok())
                .filter(|&number| number <= pending.choices);
            if let Some(number) = choice {
                respond(screen, (number != 0).then_some(number));
            }
        }
        _ => {}
    }
}

pub(super) fn respond(screen: &mut ScreenState, choice: Option<usize>) -> bool {
    let Some(pending) = screen.agy_pending.take() else {
        return false;
    };
    offer(screen);
    let _ = pending.answer.try_send(choice);
    true
}
