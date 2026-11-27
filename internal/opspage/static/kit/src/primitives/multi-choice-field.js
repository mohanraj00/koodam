import { el, sheet } from '../render/dom.js';
import { multiChoiceCountLimit } from '../render/validate.js';
import { ChoiceList, CHOICE_LIST_CSS } from './choice-list.js';
import { FieldElement } from './fields.js';

const MULTI_CHOICE_CSS = sheet(`
  :host { min-inline-size: 0; overflow-wrap: anywhere; }
  [part="group"] { display: grid; gap: var(--mu-space-1); min-inline-size: 0; }
  [part="checkbox-label"] {
    display: flex; align-items: center; gap: var(--mu-space-2);
    min-inline-size: 44px; min-block-size: 44px; cursor: pointer;
  }
  [part="checkbox"] {
    flex: none; inline-size: 44px; block-size: 44px; margin: 0; padding: 0;
    accent-color: var(--mu-color-accent); cursor: inherit;
  }
  [part="tags"] { display: flex; flex-wrap: wrap; gap: var(--mu-space-1); }
  [part="tags"]:empty { display: none; }
  [part="tag"] {
    display: flex; align-items: center; gap: var(--mu-space-2); max-inline-size: 100%;
    padding-inline-start: var(--mu-space-2); background: var(--mu-color-surface);
    border: 1px solid var(--mu-color-border); border-radius: var(--mu-radius-1);
  }
  [part="tag-text"], [part="checkbox-label"] > span { min-inline-size: 0; white-space: pre-wrap; }
  [part="remove"] {
    flex: none; min-inline-size: 44px; min-block-size: 44px; padding: var(--mu-space-1);
    font: var(--mu-type-body); color: var(--mu-color-text); background: transparent;
    border: 1px solid transparent; border-radius: var(--mu-radius-1); cursor: pointer;
  }
  [part="option"] { justify-content: space-between; gap: var(--mu-space-2); }
  [part="option"]::after {
    content: ""; flex: none; inline-size: 0.55em; block-size: 0.85em; transform: rotate(45deg);
  }
  [part="option"][aria-selected="true"]::after { border-inline-end: 2px solid; border-block-end: 2px solid; }
  @media (forced-colors: active) {
    [part="tag"] { border-color: FieldText; }
    [part="remove"] { background: ButtonFace; color: ButtonText; border-color: ButtonText; }
    [part="remove"]:disabled { color: GrayText; border-color: GrayText; }
  }
`);

export class MuMultiChoiceField extends FieldElement {
  static component = 'multi-choice-field';
  static styles = [...FieldElement.styles, CHOICE_LIST_CSS, MULTI_CHOICE_CSS];
  static submitOnEnter = false;

  #choices = [];
  #selected = new Set();
  #group;
  #picker;

  read() {
    return Object.freeze(this.#choices.filter((choice) => this.#selected.has(choice)));
  }

  get changed() {
    const value = this.value;
    const initial = this.initial;
    return value !== null && initial !== null
      && (value.length !== initial.length || value.some((choice, index) => choice !== initial[index]));
  }

  reason(value, data) {
    const limit = multiChoiceCountLimit(value.length, data);
    if (!limit) return null;
    const count = data[limit];
    return `Select at ${limit === 'min_items' ? 'least' : 'most'} ${count} ${count === 1 ? 'choice' : 'choices'}.`;
  }

  disconnectedCallback() {
    this.#picker?.close();
  }

  refuse(errors) {
    this.#picker?.close();
    super.refuse(errors);
  }

  describedBy() {
    return this.#group ? [this.#group] : undefined;
  }

  control(data, id) {
    this.#picker?.close();
    this.#picker = null;
    this.#group = null;
    this.#choices = data.choices;
    this.#selected = new Set(data.value ?? []);
    const group = el('div', { part: 'group', role: 'group', 'aria-labelledby': id + '-label' });
    if (this.#choices.length <= 7) {
      this.#group = group;
      const label = el('span', { id: id + '-label', part: 'label' }, data.label);
      const targets = this.#choices.map((choice, index) => {
        const box = el('input', { id: id + '-' + index, type: 'checkbox', part: 'checkbox' });
        box.checked = this.#selected.has(choice);
        box.addEventListener('change', () => {
          if (box.disabled) return;
          this.#setChoice(choice, box.checked);
          this.#reportChange();
        });
        group.append(el('label', { for: box.id, part: 'checkbox-label' }, box, el('span', {}, choice)));
        return box;
      });
      return { node: el('div', { class: 'box' }, label, group), targets, labelled: true };
    }
    const label = el('label', { id: id + '-label', for: id, part: 'label' }, data.label);
    const search = el('input', { id, type: 'text', role: 'combobox', part: 'search', autocomplete: 'off', 'aria-autocomplete': 'list' });
    const tags = el('div', { part: 'tags' });
    const updateTags = () => {
      const buttons = [];
      const nodes = this.read().map((choice, index) => {
        const button = el('button', { type: 'button', part: 'remove', 'aria-label': 'Remove ' + choice }, '×');
        button.disabled = Boolean(data.disabled);
        button.addEventListener('click', () => {
          if (button.disabled || !this.#selected.has(choice)) return;
          this.#setChoice(choice, false);
          this.#picker.refresh();
          const remaining = updateTags();
          (remaining[index] ?? remaining.at(-1) ?? search).focus();
          this.#reportChange();
        });
        buttons.push(button);
        return el('span', { part: 'tag' }, el('span', { part: 'tag-text' }, choice), button);
      });
      tags.replaceChildren(...nodes);
      return buttons;
    };
    this.#picker = new ChoiceList(this, data, id, {
      control: search, search, multiple: true, selected: (choice) => this.#selected.has(choice),
      choose: (choice) => {
        this.#setChoice(choice, !this.#selected.has(choice));
        this.#picker.refresh();
        updateTags();
        this.#reportChange();
      },
    });
    updateTags();
    group.append(tags, this.#picker.node);
    return { node: el('div', { class: 'box' }, label, group), targets: [search], labelled: true };
  }

  #setChoice(choice, checked) {
    if (checked) this.#selected.add(choice);
    else this.#selected.delete(choice);
  }

  #reportChange() {
    this.showProblem();
    this.changedValue();
  }
}
