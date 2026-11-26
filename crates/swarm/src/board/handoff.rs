use super::launch::{
    ACCENT, DIM, Detected, Detection, ERROR, FAST_EDIT, Launch, LaunchOption, PLAIN, PROVIDERS,
    TITLE, block_cursor, field_spans, label_layout, model_option, spans,
};
use super::{Action, FormViewport, Key, caller_launch_environment, field as wire_field, name};
use serde_json::{Value, json};
use std::io;
use unicode_width::{UnicodeWidthChar, UnicodeWidthStr};

pub(super) const CAP: &str = "hands-off-handoff";
const MODES: [&str; 3] = ["passive", "manual", "none"];
const TEMPLATE: &str = include_str!("../../assets/handoff/source.md.tmpl");
const RUNNING_WARNING: &str = "the source may still be running and editing this checkout - the successor is told to check git status first";
type Line = Vec<(&'static str, String)>;

pub(super) struct Handoff {
    pub(super) source_id: String,
    source_agent: String,
    cwd: String,
    was_running: bool,
    available: Option<[bool; PROVIDERS.len()]>,
    target: Option<usize>,
    model: Option<LaunchOption>,
    mode: usize,
    hands_off: bool,
    focus: usize,
    confirmation: Option<Selection>,
    reading: Option<(String, usize)>,
    presented: Option<((u16, u16), bool)>,
    error: String,
}

struct Selection {
    agent: &'static str,
    model: String,
}

impl Handoff {
    pub(super) fn new(source: &Value, cached: Option<&Detected>) -> Self {
        let mut form = Self {
            source_id: wire_field(source, "id").into(),
            source_agent: wire_field(source, "agent").into(),
            cwd: wire_field(source, "cwd").into(),
            was_running: super::running(source),
            available: None,
            target: None,
            model: None,
            mode: 0,
            hands_off: eligible(source).is_err(),
            focus: 0,
            confirmation: None,
            reading: None,
            presented: None,
            error: String::new(),
        };
        if let Some(snapshot) = cached {
            form.apply_detection(snapshot);
        }
        form
    }

    pub(super) fn apply_detection(&mut self, snapshot: &Detected) {
        let available = snapshot
            .each_ref()
            .map(|status| *status == Detection::Ready);
        self.available = Some(available);
        let target = self
            .target
            .filter(|&index| available[index])
            .or_else(|| available.iter().position(|&ready| ready));
        if self.target != target {
            self.select_target(target);
        }
        if self.target.is_some() {
            self.error.clear();
        }
        if self
            .confirmation
            .as_ref()
            .is_some_and(|selection| Some(selection.agent) != self.target_name())
        {
            self.cancel_confirmation();
            self.error = "target changed while the cross-CLI confirmation was open - nothing launched, review and submit again".into();
        }
    }

    fn select_target(&mut self, target: Option<usize>) {
        self.target = target;
        self.model = self.target_name().and_then(model_option);
    }

    fn target_name(&self) -> Option<&'static str> {
        self.target.map(|index| PROVIDERS[index])
    }

    fn cycle_target(&mut self, forward: bool) {
        let (Some(available), Some(current)) = (self.available, self.target) else {
            return;
        };
        let count = PROVIDERS.len();
        let step = if forward { 1 } else { count - 1 };
        let next = (1..=count)
            .map(|offset| (current + step * offset) % count)
            .find(|&index| available[index]);
        self.select_target(next);
        self.error.clear();
    }

    pub(super) fn confirm_pending(&self) -> bool {
        self.confirmation.is_some()
    }

    fn cancel_confirmation(&mut self) {
        self.confirmation = None;
        self.reading = None;
        self.presented = None;
    }

    pub(super) fn note_paint(&mut self, geometry: (u16, u16), notice: bool) {
        if self.confirm_pending() {
            self.presented = Some((geometry, notice));
        }
    }

    pub(super) fn update(
        &mut self,
        key: Key,
        source: Option<&Value>,
        viewport: FormViewport<'_>,
        capable: bool,
        online: bool,
    ) -> Action {
        let decision = self.confirm_pending();
        if decision && matches!(key, Key::Text('n') | Key::Escape) {
            self.cancel_confirmation();
            return Action::None;
        }
        let submit = key == Key::Enter || decision && key == Key::Text('y');
        if !Launch::readable(viewport.geometry) {
            if submit {
                return Action::None;
            }
        } else if self.read_context(&key, source, viewport, submit) {
            return Action::None;
        }
        // A single read can deliver Enter+y or End+y before the next paint.
        if decision
            && key == Key::Text('y')
            && self.presented != Some((viewport.geometry, viewport.notice.is_some()))
        {
            return Action::None;
        }
        if submit && !online {
            self.error = "daemon disconnected; reconnect before handoff".into();
            return Action::None;
        }
        match self.act(key, source, viewport, capable) {
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
        viewport: FormViewport<'_>,
        capable: bool,
    ) -> io::Result<Action> {
        let (cols, rows) = viewport.geometry;
        if let Some(confirmed) = &self.confirmation {
            if key == Key::Text('y') {
                if !capable || source.is_none() {
                    return Err(io::Error::other(
                        "Handoff source or owner capability changed",
                    ));
                }
                return Ok(self.launch(confirmed, cols, rows));
            }
            return Ok(Action::None);
        }
        match key {
            Key::Up => self.focus = (self.focus + 3) % 4,
            Key::Down | Key::Tab => self.focus = (self.focus + 1) % 4,
            Key::Left | Key::Right if self.focus == 0 => self.cycle_target(key == Key::Right),
            Key::Left | Key::Right if self.focus == 2 => {
                let step = if key == Key::Left { MODES.len() - 1 } else { 1 };
                self.mode = (self.mode + step) % MODES.len();
                self.error.clear();
            }
            Key::Left | Key::Right if self.focus == 3 => {
                self.hands_off = !self.hands_off;
                self.error.clear();
            }
            Key::Enter => {
                let source = source
                    .ok_or_else(|| io::Error::other("source session is no longer in the roster"))?;
                if !self.hands_off {
                    eligible(source)?;
                }
                let agent = self
                    .target_name()
                    .ok_or_else(|| io::Error::other("no installed, supported target agent"))?;
                let selected = Selection {
                    agent,
                    model: self
                        .model
                        .as_ref()
                        .map_or_else(String::new, |option| option.value.text.clone()),
                };
                if self.hands_off {
                    if !capable {
                        return Err(io::Error::other(
                            "daemon is too old for hands-off handoff - restart the daemon, or use the supervised method",
                        ));
                    }
                    if selected.agent != self.source_agent {
                        self.confirmation = Some(selected);
                        self.reading = None;
                        self.presented = None;
                    } else {
                        return Ok(self.launch(&selected, cols, rows));
                    }
                } else {
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
            key if self.focus == 1 => {
                if let Some(model) = &mut self.model {
                    let cycle = matches!(key, Key::Left | Key::Right) && model.suggestions() > 0;
                    let previous = model.value.text.clone();
                    model.apply(key);
                    if cycle || previous != model.value.text {
                        self.error.clear();
                    }
                }
            }
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

    fn target_value(&self) -> Line {
        if self.available.is_none() {
            vec![(DIM, "checking...".into())]
        } else if let Some(target) = self.target_name() {
            arrows(target)
        } else {
            vec![(ERROR, "no installed, supported agent".into())]
        }
    }

    fn model_value(&self, cols: u16) -> Line {
        let Some(model) = &self.model else {
            return vec![(DIM, "(agent default)".into())];
        };
        let editor = &model.value;
        if self.focus == 1 {
            let budget = label_layout("model", cols).1;
            let cursor = block_cursor(editor, budget);
            let mut value = vec![(PLAIN, cursor)];
            if editor.text.is_empty() {
                value.push((DIM, " (agent default)".into()));
            }
            return value;
        }
        let mut value = vec![(
            PLAIN,
            if editor.text.is_empty() {
                "(agent default)".into()
            } else {
                editor.text.clone()
            },
        )];
        if model.suggestions() > 1 {
            value.push((PLAIN, " ".into()));
            value.push((DIM, "◂ ▸".into()));
        }
        value
    }

    fn fields(&self, cols: u16) -> Vec<Line> {
        let supervision = if self.hands_off {
            vec![(DIM, "not applicable - no supervisor exists".into())]
        } else {
            arrows(MODES[self.mode])
        };
        let method = if self.hands_off {
            "hands-off"
        } else {
            "supervised"
        };
        [
            ("target", self.target_value()),
            ("model", self.model_value(cols)),
            ("supervision", supervision),
            ("method", arrows(method)),
        ]
        .into_iter()
        .enumerate()
        .map(|(i, (label, value))| field_spans(cols, label, &value, self.focus == i))
        .collect()
    }

    fn hint(&self, cols: u16) -> String {
        if self.confirm_pending() {
            let hint = "y confirm the cross-CLI transcript disclosure · n or esc cancel";
            return if hint.width() <= usize::from(cols.saturating_sub(2)) {
                hint.into()
            } else {
                "esc cancel · y confirm the cross-CLI transcript disclosure".into()
            };
        }
        let edit = match self.focus {
            0 => "arrows change target",
            2 => "arrows change supervision",
            3 => "arrows change method",
            _ => return self.model_hint(cols),
        };
        ordered_hint(edit, cols)
    }

    fn model_hint(&self, cols: u16) -> String {
        let Some(model) = &self.model else {
            return ordered_hint("arrows change model", cols);
        };
        let arrows = if model.suggestions() > 0 {
            "suggestions"
        } else {
            "move"
        };
        ordered_hint(&format!("type or paste · ←→ {arrows} · {FAST_EDIT}"), cols)
    }

    fn context(&self, compact: bool, source: Option<&Value>) -> Vec<(&'static str, String)> {
        let mut messages = Vec::new();
        if compact {
            messages.extend([
                (PLAIN, format!("Session: {}", self.source_name(source))),
                (PLAIN, format!("Agent: {}", self.source_agent)),
                (PLAIN, format!("ID: {}", self.source_id)),
                (PLAIN, format!("Directory: {}", self.cwd)),
            ]);
        }
        if self.hands_off && self.was_running {
            messages.push((DIM, RUNNING_WARNING.into()));
        }
        if let Some(selection) = &self.confirmation {
            messages.push((ACCENT, format!(
                "cross-CLI handoff: {} will be told to read a transcript written by {} - y confirm, n cancel",
                selection.agent, self.source_agent
            )));
        }
        if !self.error.is_empty() {
            messages.push((ERROR, self.error.clone()));
        }
        messages
    }

    fn source_name<'a>(&'a self, source: Option<&'a Value>) -> &'a str {
        source.map_or(&self.source_id, name)
    }

    fn reading_key(&self, source: Option<&Value>, messages: &[(&str, String)]) -> String {
        format!(
            "{}\0{}",
            self.source_name(source),
            messages
                .iter()
                .map(|(_, text)| text.as_str())
                .collect::<Vec<_>>()
                .join("\n")
        )
    }

    fn read_context(
        &mut self,
        key: &Key,
        source: Option<&Value>,
        viewport: FormViewport<'_>,
        submit: bool,
    ) -> bool {
        let (compact, width, budget) = reading_layout(viewport);
        let messages = self.context(compact, source);
        let count = messages
            .iter()
            .map(|(_, message)| wrap(message, width).len())
            .sum();
        if count <= budget {
            self.reading = None;
            return false;
        }
        let decision = self.confirm_pending();
        let context = self.reading_key(source, &messages);
        let stop = self.stop(&context, count);
        let next = match key {
            Key::Up if decision => stop.saturating_sub(1),
            Key::Down | Key::Tab if decision => (stop + 1).min(count),
            Key::PageUp => stop.saturating_sub(budget),
            Key::PageDown => (stop + budget).min(count),
            Key::Home if decision => 0,
            Key::End if decision => count,
            _ => return submit && stop != count,
        };
        if next != stop {
            self.presented = None;
        }
        self.reading = Some((context, next));
        true
    }

    fn stop(&self, context: &str, count: usize) -> usize {
        self.reading
            .as_ref()
            .filter(|(key, _)| key == context)
            .map_or(0, |(_, stop)| (*stop).min(count))
    }

    pub(super) fn view(
        &self,
        screen: &mut String,
        viewport: FormViewport<'_>,
        source: Option<&Value>,
    ) {
        let (cols, _) = viewport.geometry;
        let (compact, width, budget) = reading_layout(viewport);
        let body = usize::from(viewport.body_rows());
        let fields = self.fields(cols);
        let decision = self.confirm_pending();
        let messages = self.context(compact, source);
        let reading = messages
            .iter()
            .flat_map(|(_, text)| wrap(text, width))
            .collect::<Vec<_>>();
        let hint;
        let lines = if !Launch::readable(viewport.geometry) {
            hint = "esc back".into();
            vec![
                vec![(PLAIN, "Resize to 32×8".into())],
                fields[self.focus].clone(),
                vec![(PLAIN, "Esc back / n deny".into())],
            ]
        } else if reading.len() > budget {
            hint = if decision {
                "↑↓/tab read · end confirm · esc back"
            } else {
                "pgup/down read · esc back"
            }
            .into();
            let count = reading.len();
            let stop = self.stop(&self.reading_key(source, &messages), count);
            let action = if decision {
                "Confirm · y"
            } else {
                "Confirm · enter"
            };
            let marker = if stop == count { "▌ " } else { "  " };
            let mut lines = self.summary(fields[self.focus].clone(), source);
            lines.extend(
                reading
                    .into_iter()
                    .chain([format!("{marker}{action}")])
                    .skip(stop.min(count + 1 - budget))
                    .take(budget)
                    .map(|text| vec![(PLAIN, text)]),
            );
            lines
        } else if compact {
            hint = self.hint(cols);
            let mut lines = self.summary(fields[self.focus].clone(), source);
            lines.extend(reading.into_iter().map(|text| vec![(PLAIN, text)]));
            if !decision {
                lines.extend(
                    fields
                        .into_iter()
                        .enumerate()
                        .filter_map(|(i, line)| (i != self.focus).then_some(line)),
                );
            }
            lines
        } else {
            hint = self.hint(cols);
            self.full_view(fields, messages, width, body)
        };
        for (offset, pieces) in lines.into_iter().take(body).enumerate() {
            spans(screen, offset as u16 + 1, cols, &pieces);
        }
        viewport.footer(screen, &hint);
    }

    fn summary(&self, focused: Line, source: Option<&Value>) -> Vec<Line> {
        let title = format!(
            "Handoff: {} → {}",
            self.source_name(source),
            self.target_name().unwrap_or("")
        );
        vec![vec![(PLAIN, title)], focused]
    }

    fn full_view(
        &self,
        fields: Vec<Line>,
        messages: Vec<(&'static str, String)>,
        width: usize,
        body: usize,
    ) -> Vec<Line> {
        let mut title = vec![vec![(TITLE, "swarm".into()), (DIM, " · handoff".into())]];
        let mut context = Vec::new();
        for (style, message) in messages {
            context.push(Vec::new());
            context.extend(
                wrap(&message, width)
                    .into_iter()
                    .map(|line| vec![(PLAIN, "  ".into()), (style, line)]),
            );
        }
        if title.len() + 1 + fields.len() + context.len() <= body {
            title.push(Vec::new());
            title.extend(fields);
        } else {
            if context.len() > body.saturating_sub(2) {
                context.retain(|line| !line.is_empty());
            }
            let window = body
                .saturating_sub(1 + context.len())
                .max(1)
                .min(fields.len());
            let end = (self.focus + 1 + usize::from(window > 1)).min(fields.len());
            let start = end.saturating_sub(window).min(self.focus);
            title.extend(fields.into_iter().skip(start).take(window));
        }
        title.extend(context);
        title
    }
}

fn arrows(value: &str) -> Line {
    vec![
        (DIM, "◂ ".into()),
        (ACCENT, value.into()),
        (DIM, " ▸".into()),
    ]
}

fn ordered_hint(edit: &str, cols: u16) -> String {
    let hint = format!("{edit} · tab/↑↓ next · enter continue · esc cancel");
    if hint.width() <= usize::from(cols.saturating_sub(2)) {
        hint
    } else {
        format!("esc cancel · enter continue · {edit} · tab/↑↓ next")
    }
}

fn reading_layout(viewport: FormViewport<'_>) -> (bool, usize, usize) {
    let (cols, rows) = viewport.geometry;
    let compact = cols < 64 && rows <= 10;
    let width = usize::from(cols.saturating_sub(if compact { 0 } else { 2 }).max(1));
    let budget = usize::from(viewport.body_rows().saturating_sub(2).max(1));
    (compact, width, budget)
}

fn wrap(text: &str, width: usize) -> Vec<String> {
    let mut lines = Vec::new();
    let mut line = String::new();
    let mut space = String::new();
    let mut word = String::new();
    let add_word = |line: &mut String, space: &mut String, word: &mut String| {
        if !word.is_empty() {
            line.push_str(space);
            space.clear();
            line.push_str(word);
            word.clear();
        }
    };
    for ch in text.chars() {
        if ch.is_whitespace() && ch != '\u{a0}' {
            add_word(&mut line, &mut space, &mut word);
            space.push(ch);
            continue;
        }
        let cells = ch.width().unwrap_or(0);
        if word.width() + cells > width {
            add_word(&mut line, &mut space, &mut word);
        }
        word.push(ch);
        if line.width() + space.width() + word.width() > width {
            lines.push(std::mem::take(&mut line));
            space.clear();
        }
        if ch == '-' || word.width() == width {
            add_word(&mut line, &mut space, &mut word);
        }
    }
    add_word(&mut line, &mut space, &mut word);
    if !line.is_empty() {
        lines.push(line);
    }
    lines
}

fn eligible(source: &Value) -> io::Result<()> {
    let state = &source["status"];
    let reason = if !super::running(source) {
        "session has ended - handoff requires a running source"
    } else if wire_field(state, "Turn") != "idle" {
        "session is busy - wait for its current turn"
    } else if wire_field(state, "Interaction") == "permission" {
        "resolve the permission request before handoff"
    } else if !matches!(
        wire_field(state, "Interaction"),
        "none" | "prompt" | "unknown"
    ) {
        return Err(io::Error::other(format!(
            "session interaction state {:?} is not safe for handoff",
            wire_field(state, "Interaction")
        )));
    } else {
        return Ok(());
    };
    Err(io::Error::other(reason))
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

    fn source() -> Value {
        json!({
            "id":"owner/session", "name":"amber", "agent":"claude", "cwd":"/tmp/source-checkout",
            "status":{"Process":"running","Turn":"idle","Interaction":"permission"},
        })
    }

    fn detected(available: &[&str]) -> Detected {
        PROVIDERS.map(|provider| {
            if available.contains(&provider) {
                Detection::Ready
            } else {
                Detection::Disabled {
                    reason: "not installed".into(),
                    installed: false,
                }
            }
        })
    }

    #[test]
    fn cross_cli_approval_requires_the_current_disclosure_to_have_painted() {
        let source = source();
        let mut form = Handoff::new(&source, Some(&detected(&["agy", "claude"])));
        let wide = FormViewport {
            geometry: (118, 26),
            notice: None,
        };
        let compact = FormViewport {
            geometry: (40, 8),
            notice: None,
        };
        let tiny = FormViewport {
            geometry: (24, 4),
            notice: None,
        };
        let input = |form: &mut Handoff, key, viewport| {
            form.update(key, Some(&source), viewport, true, true)
        };
        input(&mut form, Key::Tab, wide);
        input(&mut form, Key::Right, wide);
        input(&mut form, Key::Enter, wide);
        assert!(form.confirm_pending());
        assert!(matches!(
            input(&mut form, Key::Text('y'), wide),
            Action::None
        ));

        form.note_paint(wide.geometry, false);
        assert!(matches!(
            input(&mut form, Key::Text('y'), tiny),
            Action::None
        ));
        assert!(matches!(
            input(&mut form, Key::Text('y'), compact),
            Action::None
        ));
        form.note_paint(compact.geometry, false);
        assert!(matches!(
            input(&mut form, Key::Text('y'), compact),
            Action::None
        ));
        input(&mut form, Key::End, compact);
        assert!(matches!(
            input(&mut form, Key::Text('y'), compact),
            Action::None
        ));
        form.note_paint(compact.geometry, false);
        input(&mut form, Key::Paste("different model".into()), compact);
        let Action::Request(request, "launch") = input(&mut form, Key::Text('y'), compact) else {
            panic!("expected the reviewed launch");
        };
        assert_eq!(request["launch"]["agent"], "agy");
        assert_eq!(request["launch"]["cwd"], "/tmp/source-checkout");
        assert_eq!(
            request["launch"]["options"],
            json!({"handoff_from":"owner/session", "model":"Orchestrator (Carbon)"})
        );
    }

    #[test]
    fn handoff_target_refresh_preserves_edits_and_invalidates_changed_disclosure() {
        let source = source();
        let available = detected(&["agy", "claude", "opencode"]);
        let wide = FormViewport {
            geometry: (118, 26),
            notice: None,
        };
        let mut form = Handoff::new(&source, Some(&available));
        let input = |form: &mut Handoff, key| form.update(key, Some(&source), wide, true, true);

        input(&mut form, Key::Left);
        assert_eq!(form.target_name(), Some("opencode"));
        input(&mut form, Key::Tab);
        input(&mut form, Key::Text('x'));
        input(&mut form, Key::Left);
        input(&mut form, Key::Text('y'));
        form.apply_detection(&available);
        assert_eq!(form.model.as_ref().unwrap().value.text, "yx");
        input(&mut form, Key::Enter);
        assert!(form.confirm_pending());
        form.apply_detection(&detected(&["claude"]));
        assert!(!form.confirm_pending());
        assert_eq!(form.target_name(), Some("claude"));
        assert!(form.error.contains("target changed"));
        assert!(matches!(input(&mut form, Key::Text('y')), Action::None));
    }

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
