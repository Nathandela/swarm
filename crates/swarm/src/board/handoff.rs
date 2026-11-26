use super::launch::{DIM, Launch, PLAIN, TITLE, block_cursor, field, label_layout, spans, status};
use super::{Action, Editor, Key, caller_launch_environment, field as wire_field};
use serde_json::{Value, json};
use std::io;
use unicode_width::UnicodeWidthStr;

pub(super) const CAP: &str = "hands-off-handoff";
const MODES: [&str; 3] = ["passive", "manual", "none"];
const TEMPLATE: &str = include_str!("../../../../internal/tui/templates/handoff-source.md.tmpl");

pub(super) struct Handoff {
    pub(super) source_id: String,
    source_agent: String,
    cwd: String,
    was_running: bool,
    target: Launch,
    model: Editor,
    mode: usize,
    hands_off: bool,
    focus: usize,
    confirmation: Option<Selection>,
    error: String,
}

struct Selection {
    agent: &'static str,
    model: String,
}

impl Handoff {
    pub(super) fn new(source: &Value) -> Self {
        Self {
            source_id: wire_field(source, "id").into(),
            source_agent: wire_field(source, "agent").into(),
            cwd: wire_field(source, "cwd").into(),
            was_running: super::running(source),
            target: Launch::new(),
            model: Editor::new(""),
            mode: 0,
            hands_off: eligible(source).is_err(),
            focus: 0,
            confirmation: None,
            error: String::new(),
        }
    }

    pub(super) fn update_detection(&mut self) -> bool {
        self.target.update_detection()
    }

    pub(super) fn update(
        &mut self,
        key: Key,
        source: Option<&Value>,
        geometry: (u16, u16),
        capable: bool,
        online: bool,
    ) -> Action {
        self.error.clear();
        if !online && matches!(key, Key::Enter | Key::Text('y')) {
            self.error = "daemon disconnected; reconnect before handoff".into();
            return Action::None;
        }
        let result = self.act(key, source, geometry, capable);
        match result {
            Ok(action) => action,
            Err(error) => {
                self.error = error.to_string();
                Action::None
            }
        }
    }

    fn act(
        &mut self,
        key: Key,
        source: Option<&Value>,
        (cols, rows): (u16, u16),
        capable: bool,
    ) -> io::Result<Action> {
        if let Some(confirmed) = &self.confirmation {
            match key {
                Key::Text('n') => self.confirmation = None,
                Key::Text('y') if self.confirmation_fits(cols, rows) => {
                    if !capable || source.is_none() {
                        return Err(io::Error::other(
                            "Handoff source or owner capability changed",
                        ));
                    }
                    return Ok(self.launch(confirmed, cols, rows));
                }
                _ => {}
            }
            return Ok(Action::None);
        }
        match key {
            Key::Up => self.focus = (self.focus + 3) % 4,
            Key::Down | Key::Tab => self.focus = (self.focus + 1) % 4,
            Key::Left | Key::Right if self.focus == 0 => {
                self.target.cycle_agent(matches!(key, Key::Left));
                self.model = Editor::new("");
            }
            Key::Left | Key::Right if self.focus == 2 && !self.hands_off => {
                let step = if key == Key::Left { MODES.len() - 1 } else { 1 };
                self.mode = (self.mode + step) % MODES.len();
            }
            Key::Left | Key::Right if self.focus == 3 => self.hands_off = !self.hands_off,
            Key::Enter => {
                if !Launch::readable((cols, rows)) {
                    return Err(io::Error::other("Resize to 32×8"));
                }
                let source = source
                    .ok_or_else(|| io::Error::other("source session is no longer in the roster"))?;
                let agent = self
                    .target
                    .ready_agent()
                    .ok_or_else(|| io::Error::other("no installed, supported target agent"))?;
                let selected = Selection {
                    agent,
                    model: self.model.text.clone(),
                };
                if self.hands_off {
                    if !capable {
                        return Err(io::Error::other(
                            "daemon is too old for hands-off handoff - restart the daemon, or use the supervised method",
                        ));
                    }
                    if selected.agent != self.source_agent {
                        self.confirmation = Some(selected);
                    } else {
                        return Ok(self.launch(&selected, cols, rows));
                    }
                } else {
                    eligible(source)?;
                    let prompt = supervised_prompt(&selected, self.mode)?;
                    return Ok(Action::Request(
                        json!({
                            "op":"send_input", "session_id":self.source_id,
                            "send_input":{"text":prompt,"submit":true},
                        }),
                        "ok",
                    ));
                }
            }
            key if self.focus == 1 => self.model.apply(key),
            _ => {}
        }
        Ok(Action::None)
    }

    fn launch(&self, selection: &Selection, cols: u16, rows: u16) -> Action {
        let mut options = json!({"handoff_from":self.source_id});
        if !selection.model.is_empty() {
            options["model"] = json!(selection.model);
        }
        Action::Request(
            json!({"op":"launch","launch":{
                "agent":selection.agent, "cwd":self.cwd, "options":options,
                "cols":cols, "rows":rows, "env":caller_launch_environment(selection.agent),
            }}),
            "launch",
        )
    }

    fn disclosure(&self, cols: u16) -> Vec<String> {
        let Some(selection) = &self.confirmation else {
            return Vec::new();
        };
        let notice = format!(
            "Give the {} successor access to the {} source conversation? The target may use a different provider.",
            selection.agent, self.source_agent
        );
        let width = usize::from(cols.saturating_sub(4).max(1));
        wrap(&notice, width)
    }

    fn confirmation_fits(&self, cols: u16, rows: u16) -> bool {
        Launch::readable((cols, rows)) && self.disclosure(cols).len() <= usize::from(rows - 3)
    }

    pub(super) fn view(&self, screen: &mut String, cols: u16, rows: u16) {
        spans(
            screen,
            1,
            cols,
            &[(TITLE, "swarm".into()), (DIM, " · handoff".into())],
        );
        if self.confirmation.is_some() {
            for (offset, line) in self
                .disclosure(cols)
                .into_iter()
                .take(usize::from(rows.saturating_sub(3)))
                .enumerate()
            {
                spans(
                    screen,
                    offset as u16 + 3,
                    cols,
                    &[(DIM, format!("  {line}"))],
                );
            }
            let hint = if self.confirmation_fits(cols, rows) {
                "y confirm · n / esc cancel"
            } else {
                "resize to read · n / esc cancel"
            };
            status(screen, rows, cols, hint);
            return;
        }
        let model = if self.focus == 1 {
            vec![(
                PLAIN,
                block_cursor(&self.model, label_layout("model", cols).1),
            )]
        } else if self.model.text.is_empty() {
            vec![(DIM, "(default)".into())]
        } else {
            vec![(PLAIN, self.model.text.clone())]
        };
        let fields = [
            ("target", self.target.picker(cols)),
            ("model", model),
            (
                "supervision",
                vec![(
                    DIM,
                    if self.hands_off {
                        "(none for hands-off)"
                    } else {
                        MODES[self.mode]
                    }
                    .into(),
                )],
            ),
            (
                "method",
                vec![(
                    PLAIN,
                    if self.hands_off {
                        "◂ hands-off ▸"
                    } else {
                        "◂ supervised ▸"
                    }
                    .into(),
                )],
            ),
        ];
        for (index, (label, value)) in fields
            .iter()
            .take(usize::from(rows.saturating_sub(3)))
            .enumerate()
        {
            field(
                screen,
                index as u16 + 3,
                cols,
                label,
                value,
                self.focus == index,
            );
        }
        if !self.error.is_empty() && rows >= 8 {
            spans(screen, 7, cols, &[("\x1b[0;38;5;167m", self.error.clone())]);
        } else if self.hands_off && self.was_running && rows >= 8 {
            spans(
                screen,
                7,
                cols,
                &[(
                    DIM,
                    "The source may still be running in the same checkout.".into(),
                )],
            );
        }
        status(
            screen,
            rows,
            cols,
            "arrows change · ↑↓ next · enter continue · esc cancel",
        );
    }
}

fn eligible(source: &Value) -> io::Result<()> {
    let state = &source["status"];
    let reason = if !super::running(source) {
        "session has ended - handoff requires a running source"
    } else if wire_field(state, "Turn") != "idle" {
        "session is busy - wait for its current turn"
    } else if wire_field(state, "Interaction") == "permission" {
        "session has a pending permission request - resolve it before handoff"
    } else if !matches!(
        wire_field(state, "Interaction"),
        "none" | "prompt" | "unknown"
    ) {
        "session interaction is not safe for handoff"
    } else {
        return Ok(());
    };
    Err(io::Error::other(reason))
}

fn wrap(text: &str, width: usize) -> Vec<String> {
    let mut lines = Vec::new();
    let mut line = String::new();
    for word in text.split_whitespace() {
        if !line.is_empty() && line.width() + 1 + word.width() > width {
            lines.push(std::mem::take(&mut line));
        }
        if !line.is_empty() {
            line.push(' ');
        }
        line.push_str(word);
    }
    if !line.is_empty() {
        lines.push(line);
    }
    lines
}

fn supervised_prompt(selection: &Selection, mode: usize) -> io::Result<String> {
    let (body, tails) = TEMPLATE
        .split_once("{{if eq .Supervision \"passive\" -}}\n")
        .ok_or_else(|| io::Error::other("invalid embedded handoff template"))?;
    let (passive, other) = tails
        .split_once("{{- else if eq .Supervision \"manual\" -}}")
        .ok_or_else(|| io::Error::other("invalid embedded handoff template"))?;
    let (manual, none) = other
        .split_once("{{- else -}}")
        .ok_or_else(|| io::Error::other("invalid embedded handoff template"))?;
    let none = none
        .split_once("{{- end}}")
        .map(|(text, _)| text)
        .ok_or_else(|| io::Error::other("invalid embedded handoff template"))?;
    let marker = "{{shellQuote .Target}}{{if .Model}} --model {{shellQuote .Model}}{{end}} --supervision {{shellQuote .Supervision}}";
    let (prefix, suffix) = body
        .split_once(marker)
        .ok_or_else(|| io::Error::other("invalid embedded handoff command"))?;
    let quote = |value: &str| format!("'{}'", value.replace('\'', "'\"'\"'"));
    let model = if selection.model.is_empty() {
        String::new()
    } else {
        format!(" --model {}", quote(&selection.model))
    };
    let tail = [passive, manual, none][mode].trim();
    let command = format!(
        "{}{model} --supervision {}",
        quote(selection.agent),
        quote(MODES[mode])
    );
    let result = format!("{prefix}{command}{suffix}{tail}\n");
    if result.len() > 16_384 {
        return Err(io::Error::other("handoff prompt exceeds send limit"));
    }
    Ok(result)
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn supervised_template_quotes_model_data_and_selects_one_mode() {
        let selection = Selection {
            agent: "claude",
            model: "model ' with spaces".into(),
        };
        for (index, mode) in MODES.iter().enumerate() {
            let text = supervised_prompt(&selection, index).unwrap();
            assert!(text.contains(&format!("--supervision '{mode}'")));
            assert!(text.contains("--model 'model '\"'\"' with spaces'"));
            assert!(!text.contains("{{"));
            assert_eq!(text.matches("After launch:").count(), 1);
        }
    }
}
