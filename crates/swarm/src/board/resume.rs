use super::{
    Action, Editor, FormViewport, Key, caller_launch_environment, field,
    launch::{
        Completion, DIM, PLAIN, absolute_directory, block_cursor, field as paint_field,
        label_layout, spans, update_directory,
    },
    navigation::truncation,
    running, scheduled_review,
    session_form::word_wrap,
};
use serde_json::{Value, json};
use std::io;
use std::path::{Path, PathBuf};
use unicode_width::UnicodeWidthStr;

pub(super) const CAP: &str = "resume-relocate";
const FAST: &str = "⌘←/→ home/end · ⌘⌫/ctrl+u clear-left";
const ERROR: &str = "\x1b[0;38;5;173m";

#[derive(Clone)]
pub(super) struct Source {
    pub(super) id: String,
    agent: String,
    label: String,
    cwd: String,
    conversation: String,
    pub(super) scheduled: bool,
}

impl Source {
    pub(super) fn from_row(row: &Value) -> Self {
        Self {
            id: field(row, "id").into(),
            agent: field(row, "agent").into(),
            label: field(row, "name").into(),
            cwd: field(row, "cwd").into(),
            conversation: field(row, "conversation_id").into(),
            scheduled: scheduled_review(row),
        }
    }

    pub(super) fn can_relocate(&self, negotiated: bool) -> bool {
        negotiated && !self.scheduled
    }

    pub(super) fn directory(&self) -> &Path {
        Path::new(&self.cwd)
    }

    pub(super) fn matches(&self, row: &Value) -> bool {
        field(row, "id") == self.id
            && field(row, "agent") == self.agent
            && field(row, "cwd") == self.cwd
            && field(row, "conversation_id") == self.conversation
            && !running(row)
            && scheduled_review(row) == self.scheduled
    }
}

pub(super) struct Request {
    pub(super) source: Source,
    cwd: PathBuf,
    geometry: (u16, u16),
    relocate: bool,
}

impl Request {
    pub(super) fn original(source: Source, geometry: (u16, u16)) -> Self {
        let cwd = source.directory().to_owned();
        Self {
            source,
            cwd,
            geometry,
            relocate: false,
        }
    }

    pub(super) fn message(&self) -> Value {
        let mut options = json!({"resume_from":self.source.id});
        if self.relocate {
            options["resume_relocate"] = json!("true");
        }
        json!({"op":"launch", "launch":{
            "agent":self.source.agent,
            "name":self.source.label,
            "cwd":self.cwd,
            "cols":self.geometry.0, "rows":self.geometry.1,
            "env":caller_launch_environment(&self.source.agent),
            "options":options,
        }})
    }

    pub(super) fn rejected(self, reason: &str) -> Relocation {
        Relocation::special(
            self.source,
            &self.cwd.to_string_lossy(),
            format!("launch failed: {reason}"),
        )
    }
}

pub(super) struct Relocation {
    pub(super) source: Source,
    directory: Editor,
    completion: Option<Completion>,
    reason: String,
    error: String,
    read_stop: usize,
}

impl Relocation {
    pub(super) fn missing(source: Source) -> Self {
        let seed = source.cwd.clone();
        Self::special(source, &seed, String::new())
    }

    fn special(source: Source, seed: &str, reason: String) -> Self {
        let seed = seed.trim();
        let absolute = absolute_directory(if seed.is_empty() { "~" } else { seed })
            .unwrap_or_else(|_| PathBuf::from("/"));
        let initial = absolute
            .ancestors()
            .find(|path| path.is_dir())
            .unwrap_or(Path::new("/"));
        let directory = Editor::new(&initial.to_string_lossy());
        let completion = Some(Completion::from_prefix(&directory.text));
        Self {
            source,
            directory,
            completion,
            reason,
            error: String::new(),
            read_stop: 0,
        }
    }

    pub(super) fn update(
        &mut self,
        key: Key,
        viewport: FormViewport<'_>,
        current: Option<&Value>,
        online: bool,
    ) -> Action {
        let geometry = viewport.geometry;
        let context = self.context(geometry);
        let limit = context.len();
        let budget = Self::budget(viewport);
        if limit > budget {
            match key {
                Key::PageDown => {
                    self.read_stop = (self.read_stop + budget).min(limit);
                    return Action::None;
                }
                Key::PageUp => {
                    self.read_stop = self.read_stop.saturating_sub(budget);
                    return Action::None;
                }
                Key::Enter if self.read_stop < limit => return Action::None,
                _ => {}
            }
        }
        if key == Key::Enter {
            if !super::launch::Launch::readable(geometry) || !online {
                return Action::None;
            }
            if !current.is_some_and(|row| self.source.matches(row)) {
                self.reject("Resume source changed or disappeared. Reopen relocation.");
                return Action::None;
            }
            match self.destination() {
                Ok(cwd) => {
                    return Action::Resume(Box::new(Request {
                        source: self.source.clone(),
                        cwd,
                        geometry,
                        relocate: true,
                    }));
                }
                Err(error) => self.reject(&error.to_string()),
            }
        } else {
            update_directory(&mut self.directory, &mut self.completion, key);
        }
        Action::None
    }

    fn reject(&mut self, error: &str) {
        self.error = error.into();
        self.read_stop = 0;
    }

    fn destination(&self) -> io::Result<PathBuf> {
        let typed = self.directory.text.trim();
        if typed.is_empty() {
            return Err(io::Error::other("directory is required"));
        }
        let cwd = absolute_directory(typed)
            .map_err(|error| io::Error::other(format!("cannot resolve directory: {error}")))?;
        let meta = cwd.metadata().map_err(|error| {
            io::Error::other(format!(
                "cannot access directory {}: {error}",
                cwd.display()
            ))
        })?;
        if !meta.is_dir() {
            return Err(io::Error::other(format!(
                "{} exists but is not a directory",
                cwd.display()
            )));
        }
        Ok(cwd)
    }

    fn budget(viewport: FormViewport<'_>) -> usize {
        usize::from(viewport.body_rows().saturating_sub(2).max(1))
    }

    fn compact((cols, rows): (u16, u16)) -> bool {
        cols < 64 && rows <= 10
    }

    fn label(&self) -> &str {
        if self.source.label.is_empty() {
            &self.source.agent
        } else {
            &self.source.label
        }
    }

    fn context(&self, geometry: (u16, u16)) -> Vec<String> {
        let compact = Self::compact(geometry);
        let mut messages = if compact {
            vec![
                format!("Session: {}", self.label()),
                format!("Agent: {}", self.source.agent),
                format!("ID: {}", self.source.id),
                format!("Directory: {}", self.source.cwd),
            ]
        } else {
            Vec::new()
        };
        messages.extend(
            [self.reason.as_str(), self.error.as_str()]
                .into_iter()
                .filter(|message| !message.is_empty())
                .map(str::to_owned),
        );
        let cols = geometry.0.saturating_sub(if compact { 0 } else { 2 });
        messages
            .iter()
            .flat_map(|message| word_wrap(message, cols))
            .collect()
    }

    pub(super) fn view(&self, out: &mut String, viewport: FormViewport<'_>) {
        let geometry = viewport.geometry;
        let (cols, _) = geometry;
        let body = viewport.body_rows();
        let (_, budget) = label_layout("directory", cols);
        let mut field = vec![(PLAIN, block_cursor(&self.directory, budget))];
        let (choices, ghost) = self.completion.as_ref().map_or((&[][..], ""), |value| {
            (value.choices.as_slice(), value.ghost.as_str())
        });
        if !ghost.is_empty() {
            field.push((DIM, ghost.into()));
        }
        let directory = |out: &mut String, y| paint_field(out, y, cols, "directory", &field, true);
        if !super::launch::Launch::readable(geometry) {
            spans(out, 1, cols, &[(PLAIN, "Resize to 32×8".into())]);
            directory(out, 2);
            if body >= 3 {
                spans(out, 3, cols, &[(PLAIN, "Esc back / n deny".into())]);
            }
            viewport.footer(out, "esc back");
            return;
        }
        let mut context = self.context(geometry);
        let target = format!("Relocate: {}", self.label());
        let read = Self::budget(viewport);
        if context.len() > read {
            spans(out, 1, cols, &[(PLAIN, target)]);
            directory(out, 2);
            let focus = if self.read_stop >= context.len() {
                "▌ "
            } else {
                "  "
            };
            context.push(format!("{focus}Confirm · enter"));
            let start = self.read_stop.min(context.len().saturating_sub(read));
            for (index, line) in context.into_iter().skip(start).take(read).enumerate() {
                spans(out, index as u16 + 3, cols, &[(PLAIN, line)]);
            }
            viewport.footer(out, "pgup/down read · esc back");
            return;
        }
        let (sentence, instruction) = if self.reason.is_empty() {
            (
                format!(
                    "Session {:?} cannot be resumed because its working directory is gone.",
                    self.label()
                ),
                "Select a new one to relocate it.",
            )
        } else {
            (
                format!("Session {:?} cannot be resumed.", self.label()),
                "Select a new working directory to relocate it.",
            )
        };
        let mut paragraph = word_wrap(&sentence, cols - 4);
        paragraph.extend(word_wrap(instruction, cols - 4));
        let extra = usize::from(choices.len() > 1)
            + usize::from(!self.reason.is_empty()) * 2
            + usize::from(!self.error.is_empty()) * 2;
        let compact =
            Self::compact(geometry) && paragraph.len() + extra + 2 >= usize::from(body + 1);
        let mut y = 1;
        if compact {
            spans(out, y, cols, &[(PLAIN, target)]);
            directory(out, 2);
            y = 3;
            for line in context
                .into_iter()
                .take(usize::from((body + 1).saturating_sub(y)))
            {
                spans(out, y, cols, &[(PLAIN, line)]);
                y += 1;
            }
        }
        for line in paragraph {
            if y > body {
                break;
            }
            spans(out, y, cols, &[(PLAIN, format!("  {line}"))]);
            y += 1;
        }
        if !compact {
            y += 1;
            if y <= body {
                directory(out, y);
                y += 1;
            }
        }
        if choices.len() > 1 && y <= body {
            spans(
                out,
                y,
                cols,
                &[(PLAIN, " ".repeat(14)), (DIM, choices.join("  "))],
            );
            y += 1;
        }
        if !compact {
            for message in [&self.reason, &self.error]
                .into_iter()
                .filter(|message| !message.is_empty())
            {
                y += 1;
                if y > body {
                    break;
                }
                spans(
                    out,
                    y,
                    cols,
                    &[(PLAIN, "  ".into()), (ERROR, message.clone())],
                );
                y += 1;
            }
        }
        let editing = if !ghost.is_empty() || choices.len() > 1 {
            "tab/←→ complete"
        } else {
            "type or paste"
        };
        let hint = format!("{editing} · {FAST} · enter relocate · esc cancel");
        let hint = if hint.width() <= usize::from(cols - 2) {
            hint
        } else {
            truncation(
                &format!("esc cancel · enter relocate · {editing} · {FAST}"),
                cols - 2,
            )
        };
        viewport.footer(out, &hint);
    }
}
