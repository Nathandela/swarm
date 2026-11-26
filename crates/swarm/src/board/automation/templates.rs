use super::{Action, Automation, Editor, Key, Template, io, write_list_row};
use crate::schedule::catalog::TemplateChange;

pub(super) const USE: char = '\x14';
pub(super) const SAVE: char = '\x05';

pub(super) struct Picker {
    items: Vec<Template>,
    selected: usize,
    top: usize,
    from_draft: bool,
    owner: Option<String>,
    page: Page,
}

enum Page {
    List,
    Edit(Edit),
    Delete {
        target: Template,
        painted: Option<(u16, u16)>,
    },
}

struct Edit {
    existing: Option<(String, jiff::Timestamp)>,
    created: Option<String>,
    fields: [Editor; 2],
    focus: usize,
}

impl Edit {
    fn new(existing: Option<(String, jiff::Timestamp)>, name: &str, prompt: &str) -> Self {
        Self {
            existing,
            created: None,
            fields: [Editor::new(name), Editor::new(prompt)],
            focus: 0,
        }
    }

    fn change(&mut self) -> Result<TemplateChange, String> {
        let [name, prompt] = self.fields.each_ref().map(|field| &field.text);
        if name.trim().is_empty() || prompt.trim().is_empty() {
            return Err("Enter a template name and instructions.".into());
        }
        if self.existing.is_none() && self.created.is_none() {
            self.created =
                Some(crate::schedule::id(jiff::Timestamp::now()).map_err(|e| e.to_string())?);
        }
        Ok(TemplateChange::Save {
            id: self.existing.as_ref().map(|(id, _)| id.clone()),
            create_id: self.created.clone(),
            name: name.clone(),
            prompt: prompt.clone(),
            expected_updated_at: self.existing.as_ref().map(|(_, at)| *at),
        })
    }

    fn key(&mut self, key: Key) {
        match key {
            Key::Up | Key::BackTab => self.focus = 0,
            Key::Down | Key::Tab => self.focus = 1,
            Key::AltEnter if self.focus == 1 => self.fields[1].insert_text(std::iter::once('\n')),
            Key::Paste(text) if self.focus == 1 => {
                let text = text.replace("\r\n", "\n").replace('\r', "\n");
                self.fields[1].insert_text(
                    text.chars()
                        .filter(|ch| !ch.is_control() || matches!(ch, '\n' | '\t')),
                );
            }
            key => self.fields[self.focus].apply(key),
        }
    }
}

impl Picker {
    pub(super) fn controls(&self) -> &'static [(&'static str, char)] {
        match &self.page {
            Page::List if self.from_draft => &[("Use", '⏎'), ("Back", '⎋')],
            Page::List => &[("New", 'n'), ("Edit", '⏎'), ("Delete", 'd'), ("Back", '⎋')],
            Page::Edit(_) => &[("Save", '⏎'), ("Back", '⎋')],
            Page::Delete { .. } => &[("Delete template", 'y'), ("Cancel", '⎋')],
        }
    }

    pub(super) fn rows_at(&self, budget: usize) -> (usize, usize, usize) {
        match &self.page {
            Page::Edit(edit) => return ((edit.focus + 1).saturating_sub(budget), budget, 2),
            Page::Delete { .. } => return (0, budget, 0),
            Page::List => {}
        }
        let mut selected = self.selected;
        let mut top = self.top;
        super::super::menu::reveal(&mut selected, &mut top, self.items.len(), budget);
        (top, budget, self.items.len())
    }

    pub(super) fn select(&mut self, index: usize) -> bool {
        match &mut self.page {
            Page::List => {
                let activate = self.selected == index && index < self.items.len();
                self.selected = index;
                activate
            }
            Page::Edit(edit) => {
                edit.focus = index;
                false
            }
            Page::Delete { .. } => false,
        }
    }

    pub(super) fn title(&self) -> String {
        match &self.page {
            Page::List => "Automations / Personal templates".into(),
            Page::Edit(_) => "Personal templates / Edit".into(),
            Page::Delete { target, .. } => format!("Delete {}?", target.name),
        }
    }

    pub(super) fn deleting(&self) -> bool {
        matches!(self.page, Page::Delete { .. })
    }

    pub(super) fn painted(&mut self, geometry: (u16, u16)) {
        if let Page::Delete { painted, .. } = &mut self.page {
            *painted = Some(geometry);
        }
    }

    pub(super) fn back(&mut self) -> bool {
        if self.from_draft || matches!(self.page, Page::List) {
            return true;
        }
        self.page = Page::List;
        false
    }

    fn navigate(&mut self, key: Key, budget: usize) {
        self.selected = match key {
            Key::Up | Key::BackTab | Key::Text('k') => self.selected.saturating_sub(1),
            Key::Down | Key::Tab | Key::Text('j') => self.selected.saturating_add(1),
            Key::PageUp => self.selected.saturating_sub(budget),
            Key::PageDown => self.selected.saturating_add(budget),
            Key::Home => 0,
            Key::End => self.items.len().saturating_sub(1),
            _ => return,
        };
        super::super::menu::reveal(&mut self.selected, &mut self.top, self.items.len(), budget);
    }

    pub(super) fn paint(&self, output: &mut String, cols: u16, budget: usize) {
        let (top, _, _) = self.rows_at(budget);
        match &self.page {
            Page::Edit(edit) => {
                for (index, (label, field)) in ["Template name", "Instructions"]
                    .into_iter()
                    .zip(&edit.fields)
                    .enumerate()
                    .skip(top)
                    .take(budget)
                {
                    let selected = edit.focus == index;
                    let text = field.text.replace('\n', "↵").replace('\t', "⇥");
                    let value = if selected {
                        let before = field.text[..field.cursor]
                            .replace('\n', "↵")
                            .replace('\t', "⇥");
                        let displayed = Editor {
                            text,
                            cursor: before.len(),
                        };
                        super::super::launch::block_cursor(
                            &displayed,
                            cols.saturating_sub(5 + label.len() as u16).max(1),
                        )
                    } else {
                        text
                    };
                    write_list_row(
                        output,
                        (index - top) as u16 + 2,
                        cols,
                        &format!("{label} · {value}"),
                        selected,
                    );
                }
                return;
            }
            Page::Delete { .. } => {
                write_list_row(
                    output,
                    2,
                    cols,
                    "Saved jobs keep their own copy of the instructions.",
                    true,
                );
                return;
            }
            Page::List => {}
        }
        if self.items.is_empty() {
            write_list_row(output, 2, cols, "No personal templates yet.", true);
        }
        for (index, template) in self.items.iter().enumerate().skip(top).take(budget) {
            write_list_row(
                output,
                (index - top) as u16 + 2,
                cols,
                &template.name,
                self.selected == index,
            );
        }
    }
}

impl Automation {
    pub(super) fn open_templates(&mut self, from_draft: bool) -> Action {
        self.error.clear();
        self.templates = Some(Picker {
            items: Vec::new(),
            selected: 0,
            top: 0,
            from_draft,
            owner: None,
            page: Page::List,
        });
        Action::ReadTemplates
    }

    pub(super) fn save_draft_template(&mut self) -> Action {
        let Some(draft) = &self.draft else {
            return Action::None;
        };
        let edit = Edit::new(None, &draft.name.text, &draft.prompt.text);
        let action = self.open_templates(true);
        self.templates.as_mut().unwrap().page = Page::Edit(edit);
        action
    }

    pub(in crate::board) fn receive_templates(
        &mut self,
        result: io::Result<(String, Vec<Template>)>,
    ) {
        self.press = None;
        let Some(picker) = &mut self.templates else {
            return;
        };
        match result {
            Ok((owner, items)) => {
                picker.owner = Some(owner);
                picker.items = items;
                picker.selected = 0;
                picker.top = 0;
                self.error.clear();
            }
            Err(error) => {
                picker.owner = None;
                self.error = error.to_string();
            }
        }
    }

    pub(in crate::board) fn complete_template_change(
        &mut self,
        result: io::Result<Option<Template>>,
    ) -> bool {
        self.press = None;
        let Some(picker) = &mut self.templates else {
            return false;
        };
        if let Err(error) = result {
            if let Page::Delete { painted, .. } = &mut picker.page {
                *painted = None;
            }
            self.error = error.to_string();
            return false;
        }
        self.error.clear();
        if picker.from_draft {
            self.templates = None;
            return false;
        }
        picker.page = Page::List;
        true
    }

    pub(super) fn template_key(&mut self, key: Key, geometry: (u16, u16)) -> Action {
        let budget = self.body_capacity(geometry);
        let Some(picker) = &mut self.templates else {
            return Action::None;
        };
        let change = match &mut picker.page {
            Page::Edit(edit) if key == Key::Enter => Some(edit.change()),
            Page::Edit(edit) => {
                edit.key(key);
                return Action::None;
            }
            Page::Delete { target, painted }
                if key == Key::Text('y')
                    && *painted == Some(geometry)
                    && geometry.0 >= 32
                    && geometry.1 >= 8 =>
            {
                Some(Ok(TemplateChange::Delete {
                    id: target.id.clone(),
                    expected_updated_at: Some(target.updated_at),
                }))
            }
            Page::Delete { .. } => return Action::None,
            Page::List => None,
        };
        if let Some(change) = change {
            match (change, picker.owner.as_ref()) {
                (Ok(change), Some(owner)) => {
                    return Action::ChangeTemplate {
                        owner: owner.clone(),
                        change,
                    };
                }
                (Err(error), _) => self.error = error,
                (Ok(_), None) => {
                    self.error =
                        "Refresh from the original owner before changing a template.".into()
                }
            }
            return Action::None;
        }
        if key == Key::Enter
            && picker.from_draft
            && let Some(template) = picker.items.get(picker.selected)
            && let Some(draft) = &mut self.draft
        {
            draft.use_template(&template.name, &template.prompt);
            self.templates = None;
            self.error.clear();
        } else if !picker.from_draft && key == Key::Text('n') {
            picker.page = Page::Edit(Edit::new(None, "", ""));
        } else if !picker.from_draft
            && matches!(key, Key::Enter | Key::Text('d'))
            && let Some(item) = picker.items.get(picker.selected)
        {
            picker.page = if key == Key::Enter {
                Page::Edit(Edit::new(
                    Some((item.id.clone(), item.updated_at)),
                    &item.name,
                    &item.prompt,
                ))
            } else {
                Page::Delete {
                    target: item.clone(),
                    painted: None,
                }
            };
        } else {
            picker.navigate(key, budget);
        }
        Action::None
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn removing_a_template_requires_its_painted_selection_and_revision() {
        let now = "2026-01-01T00:00:00Z".parse().unwrap();
        let template = Template {
            id: "template".into(),
            name: "Example".into(),
            prompt: "Instructions".into(),
            created_at: now,
            updated_at: now,
        };
        let mut view = Automation::default();
        view.open_templates(false);
        view.receive_templates(Ok(("owner".into(), vec![template])));
        let wide = (80, 24);
        let tiny = (24, 4);
        assert!(matches!(view.update(Key::Text('d'), wide), Action::None));
        assert!(matches!(view.update(Key::Text('y'), wide), Action::None));
        view.note_confirmation_paint(tiny);
        assert!(matches!(view.update(Key::Text('y'), tiny), Action::None));
        view.note_confirmation_paint(wide);
        let Action::ChangeTemplate {
            owner,
            change:
                TemplateChange::Delete {
                    id,
                    expected_updated_at,
                },
        } = view.update(Key::Text('y'), wide)
        else {
            panic!("painted delete was not actionable")
        };
        assert_eq!(owner, "owner");
        assert_eq!(id, "template");
        assert_eq!(expected_updated_at, Some(now));
        assert!(!view.complete_template_change(Err(io::Error::other("refused"))));
        view.note_confirmation_paint(wide);
        assert!(matches!(view.update(Key::Text('y'), wide), Action::None));
    }

    #[test]
    fn applying_a_template_changes_only_prompt_and_an_empty_name() {
        let now = "2026-01-01T00:00:00Z".parse().unwrap();
        let template = Template {
            id: "template".into(),
            name: "Example".into(),
            prompt: "First line\nSecond line\tDetail".into(),
            created_at: now,
            updated_at: now,
        };
        let geometry = (40, 8);
        for name in ["", "Keep my name"] {
            let mut view = Automation::in_directory("/selected/workspace");
            let draft = view.draft.as_mut().unwrap();
            draft.name = Editor::new(name);
            let before = draft.spec();
            assert!(matches!(
                view.update(Key::CtrlT, geometry),
                Action::ReadTemplates
            ));
            view.receive_templates(Ok(("fixture-owner".into(), vec![template.clone()])));
            assert!(matches!(view.update(Key::Enter, geometry), Action::None));
            assert!(view.templates.is_none());
            let draft = view.draft.as_ref().unwrap();
            assert_eq!(
                draft.name.text,
                if name.is_empty() { "Example" } else { name }
            );
            assert_eq!(draft.prompt.text, template.prompt);
            assert_eq!(draft.focus, 1);
            let after = draft.spec();
            assert_eq!(after.directory, before.directory);
            assert_eq!(after.agents, before.agents);
            assert_eq!(after.authority, before.authority);
            assert_eq!(after.workspace, before.workspace);
            assert!(after.calendar == before.calendar);
        }
    }
}
