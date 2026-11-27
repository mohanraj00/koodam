// The input primitives of a form. Each one has a contract in catalog/.
// A field shows the reason of a value that its contract does not accept as text. A typed field asks for the
// edit type after a change (catalog/README.md, "Typed edits").
import { MuElement } from './base.js';
import { el, sheet } from '../render/dom.js';
import { CATALOG } from '../render/catalog.js';
import { isDate } from '../render/validate.js';

let fieldCount = 0;

// The edit types of a typed field, with their meaning.
const TYPES = [
  ['judgment', 'Judgment', 'The old value was not wrong. I chose a different value.'],
  ['correction', 'Correction', 'The old value was wrong.'],
  ['override', 'Override', 'I replace a value from a rule, a service or an agent.'],
];

// A decimal number with an optional exponent, as String() writes a number.
const NUMBER = /^[+-]?(\d+\.?\d*|\.\d+)(e[+-]?\d+)?$/i;

const FIELD_CSS = sheet(`
  :host { display: block; color: var(--mu-color-text); font: var(--mu-type-body); }
  .box { display: flex; flex-direction: column; gap: var(--mu-space-1); }
  input, textarea {
    font: var(--mu-type-body); color: var(--mu-color-text); background: var(--mu-color-surface);
    border: 1px solid var(--mu-color-border); border-radius: var(--mu-radius-1); padding: var(--mu-space-2);
    min-height: 2.75rem;
  }
  textarea { min-height: 6rem; resize: vertical; }
  [aria-invalid="true"] { border-color: var(--mu-color-danger); border-width: 2px; }
  .hint { font: var(--mu-type-caption); color: var(--mu-color-text-muted); }
  .error { font: var(--mu-type-caption); color: var(--mu-color-danger); }
  fieldset {
    margin: 0; padding: var(--mu-space-2); border: 1px solid var(--mu-color-border); border-radius: var(--mu-radius-1);
    display: flex; flex-direction: column; gap: var(--mu-space-1);
  }
  fieldset[hidden] { display: none; }
  legend { font: var(--mu-type-caption); padding: 0 var(--mu-space-1); }
  legend.label { font: var(--mu-type-body); }
  /* The radio and its label are one target of 44 by 44 CSS pixels at the minimum. The row shows hover and pressed. */
  .option { display: flex; flex-wrap: wrap; align-items: center; min-height: 2.75rem; border-radius: var(--mu-radius-1);
    transition: box-shadow var(--mu-motion-fast); }
  .option input { min-height: auto; margin: 0; inline-size: 1.25rem; block-size: 1.25rem; cursor: pointer; }
  .option label { display: inline-flex; align-items: center; min-height: 2.75rem; min-width: 2.75rem;
    padding-inline: var(--mu-space-2); cursor: pointer; }
  /* A disabled option shows no hover and no pressed state. */
  .option:hover:not(:has(:disabled)) { box-shadow: inset 0 0 0 100vmax color-mix(in srgb, currentColor 8%, transparent); }
  .option:active:not(:has(:disabled)) { box-shadow: inset 0 0 0 100vmax color-mix(in srgb, currentColor 14%, transparent); }
  .option:has(:disabled) :is(input, label) { cursor: not-allowed; }
  .option:has(:disabled) label { opacity: 0.6; }
  @media (forced-colors: active) {
    input, textarea, fieldset { border-color: FieldText; }
    .option:hover:not(:has(:disabled)) { outline: 1px solid Highlight; }
    .option:has(:disabled) label { color: GrayText; opacity: 1; }
    .hint, .error { color: CanvasText; }
  }
`);

const TOGGLE_CSS = sheet(`
  .toggle {
    display: flex; align-items: center; gap: var(--mu-space-2);
    min-inline-size: 44px; min-block-size: 44px; padding-inline: var(--mu-space-2);
    border-radius: var(--mu-radius-1); overflow-wrap: anywhere; cursor: pointer;
    transition: box-shadow var(--mu-motion-fast);
  }
  .toggle input {
    flex: none; margin: 0; padding: 0; min-height: 0; inline-size: 1.25rem; block-size: 1.25rem;
    accent-color: var(--mu-color-accent); cursor: inherit;
  }
  .toggle:hover:not(:has(:disabled)) { box-shadow: inset 0 0 0 100vmax color-mix(in srgb, currentColor 8%, transparent); }
  .toggle:active:not(:has(:disabled)) { box-shadow: inset 0 0 0 100vmax color-mix(in srgb, currentColor 14%, transparent); }
  .toggle:has(:disabled) { cursor: not-allowed; }
  .toggle:has(:disabled) > :is([part="label"], [part="state"]), input:disabled + .thumb { opacity: 0.6; }
  .switch { display: grid; flex: none; }
  .switch input, .thumb { grid-area: 1 / 1; }
  .switch input { appearance: none; inline-size: 2.5rem; border-radius: 1rem; }
  .switch input:checked { background-color: var(--mu-color-accent); }
  .thumb {
    align-self: center; justify-self: start; pointer-events: none; margin-inline: var(--mu-space-1);
    inline-size: 0.75rem; block-size: 0.75rem; border-radius: 50%; background-color: var(--mu-color-text);
  }
  .switch input:checked + .thumb { justify-self: end; background-color: var(--mu-color-on-accent); }
  [part="state"] { flex: none; min-inline-size: 3ch; font: var(--mu-type-caption); }
  @media (forced-colors: active) {
    .toggle:hover:not(:has(:disabled)) { outline: 1px solid Highlight; }
    .switch input, .thumb { forced-color-adjust: none; }
    .switch input { border-color: ButtonText; background-color: ButtonFace; }
    .switch input:checked { border-color: Highlight; background-color: Highlight; }
    .thumb { background-color: ButtonText; }
    .switch input:checked + .thumb { background-color: HighlightText; }
    .switch input:disabled { border-color: GrayText; background-color: ButtonFace; opacity: 1; }
    .switch input:disabled + .thumb { background-color: GrayText; opacity: 1; }
    .toggle:has(:disabled) > :is([part="label"], [part="state"]) { color: GrayText; opacity: 1; }
  }
`);

// Shared code of each input primitive. A subclass gives the control, reads its value and tells why a value
// is not valid. The value of a refused field is null, because a refused field keeps no data.
export class FieldElement extends MuElement {
  static focus = true;
  static styles = [FIELD_CSS];
  // Enter in a single-line text, number or date control submits the form that contains the field.
  static submitOnEnter = true;

  #initial = '';
  #editedAt = null;
  #described = [];
  #targets = [];
  #problem = null;
  #shown = false;
  #group = null;
  #reason = null;

  // The value of the fill. A typed edit compares the value with it.
  get initial() {
    return this.fill ? this.#initial : null;
  }

  // The form sends the value of the contract type, or the input text if the text is not valid.
  // An empty text input gives an empty text.
  get value() {
    if (!this.fill || !this.#targets.length) return null;
    const input = this.read();
    if (input === '') return '';
    const value = this.parse(input);
    return value === undefined ? input : value;
  }

  // True if the value is not the value of the fill.
  get changed() {
    return this.value !== null && this.value !== this.#initial;
  }

  // The reason why the contract does not accept the value, as text. Else null.
  get problem() {
    if (!this.fill || !this.#targets.length || this.fill.data.disabled) return null;
    const input = this.read();
    return input === '' && !this.#targets[0].required ? null : this.reason(input, this.fill.data);
  }

  get validationMessage() {
    return this.problem ?? '';
  }

  get disabled() {
    return Boolean(this.fill?.data.disabled);
  }

  // The edit type of a changed typed field: judgment, correction or override. Else null.
  get editType() {
    if (!this.#group || !this.changed) return null;
    return this.#group.querySelector('input:checked')?.value ?? null;
  }

  // The time of the last change of the value or the type, as an ISO 8601 text in UTC. Else null.
  get editedAt() {
    return this.fill && this.changed ? this.#editedAt : null;
  }

  // Shows the reason of a value that is not valid, and moves the focus to the control. Returns false if the value is valid.
  needValue() {
    if (!this.problem) return false;
    this.showProblem();
    (this.problemTarget?.() ?? this.#targets[0]).focus();
    return true;
  }

  reportValidity() {
    return !this.needValue();
  }

  showProblem() {
    this.#shown = true;
    this.#check();
  }

  // Shows the reason of a refused submit in the edit type group, and moves the focus to the group.
  // Returns false if the field needs no edit type now.
  needType() {
    if (!this.#group || !this.changed || this.editType) return false;
    this.#mark(true);
    this.#group.querySelector('input').focus();
    return true;
  }

  state(data) {
    if (data.disabled) return 'disabled';
    return data.error ? 'error' : 'default';
  }

  // The fill can give a value or a range that do not agree. The contract cannot tell this, so the element does.
  // The rules read only the data, so a form can check each field before it renders the field.
  static rules() {
    return [];
  }

  check(data) {
    return this.constructor.rules(data);
  }

  // A subclass reads the control and can parse and check its value. control() returns { node, targets }.
  // The node goes after the label. The targets get the description. A control can add descriptionIds.
  // labelled is true if the node has its own label.
  read() {
    return '';
  }

  parse(input) {
    return input;
  }

  reason() {
    return null;
  }

  // Call this after each change of the value.
  changedValue() {
    this.#editedAt = new Date().toISOString();
    this.#check();
    this.#sync();
    this.dispatchEvent(new CustomEvent('mu-edit', { bubbles: true, composed: true, detail: { field: 'value', value: this.value } }));
  }

  render(data) {
    fieldCount += 1;
    const id = 'mu-field-' + fieldCount;
    this.#editedAt = null;
    this.#shown = false;
    this.#problem = el('span', { id: id + '-problem', class: 'error', part: 'problem' });
    const { node, targets, labelled, descriptionIds = [] } = this.control(data, id);
    this.#described = [...descriptionIds, data.hint ? id + '-hint' : '', data.error ? id + '-error' : ''].filter(Boolean);
    this.#targets = targets;
    // The browser can change the value when the control shows it, for example CRLF to LF in a text area.
    // The old value is the value that the control shows, so such a change is not an edit.
    this.#initial = this.value ?? '';
    for (const target of targets) target.disabled = Boolean(data.disabled);
    // A change commits a value. Then a value that is not valid shows its reason.
    node.addEventListener('change', (event) => {
      if (targets.includes(event.target)) this.showProblem();
    });
    this.#check();
    // A typed field asks for the edit type after a change. A disabled field has no edit.
    this.#group = data.typed && !data.disabled ? this.#typeGroup(id) : null;
    return [el('div', { class: 'box' },
      labelled ? null : el('label', { for: targets[0].id, part: 'label' }, data.label),
      node,
      data.hint ? el('span', { id: id + '-hint', class: 'hint' }, data.hint) : null,
      data.error ? el('span', { id: id + '-error', class: 'error' }, data.error) : null,
      this.#problem,
      this.#group)];
  }

  // A description names a hidden element too, so the reason and its description exist only while they apply.
  #check() {
    const reason = this.#shown ? this.problem : null;
    this.#problem.replaceChildren(reason ?? '');
    const ids = [...this.#described, ...(reason ? [this.#problem.id] : [])].join(' ');
    const invalid = Boolean(reason) || Boolean(this.fill?.data.error);
    for (const target of this.#describedTargets()) {
      if (ids) target.setAttribute('aria-describedby', ids);
      else target.removeAttribute('aria-describedby');
      if (invalid) target.setAttribute('aria-invalid', 'true');
      else target.removeAttribute('aria-invalid');
    }
  }

  // The radios of a choice share one description on their group. Each other control gets its own.
  #describedTargets() {
    return this.describedBy?.() ?? this.#targets;
  }

  #mark(invalid) {
    this.#group.setAttribute('aria-invalid', invalid ? 'true' : 'false');
    this.#reason.replaceChildren(invalid ? 'Choose an edit type.' : '');
    if (invalid) this.#group.setAttribute('aria-describedby', this.#reason.id);
    else this.#group.removeAttribute('aria-describedby');
  }

  // The group shows only while the value is not the value of the fill. The old value again clears the type.
  #sync() {
    if (!this.#group) return;
    const open = this.changed;
    this.#group.hidden = !open;
    if (!open) {
      for (const radio of this.#group.querySelectorAll('input')) radio.checked = false;
      this.#mark(false);
    }
  }

  #typeGroup(id) {
    const reason = el('span', { id: id + '-reason', class: 'error', part: 'reason' });
    const radios = TYPES.map(([value, label, meaning]) => {
      const radio = el('input', { type: 'radio', id: `${id}-${value}`, name: id + '-type', 'aria-describedby': `${id}-${value}-meaning` });
      radio.value = value;
      radio.addEventListener('change', () => {
        this.#editedAt = new Date().toISOString();
        this.#mark(false);
        this.dispatchEvent(new CustomEvent('mu-edit', { bubbles: true, composed: true, detail: { field: 'type', value } }));
      });
      return el('div', { class: 'option' }, radio, el('label', { for: radio.id }, label),
        el('span', { id: `${id}-${value}-meaning`, class: 'hint' }, meaning));
    });
    const group = el('fieldset', { part: 'edit-type' }, el('legend', {}, 'Edit type'), ...radios, reason);
    group.hidden = true;
    this.#reason = reason;
    return group;
  }
}

// A text input that cuts a pasted or scripted text to the limit of the contract. maxLength counts UTF-16
// units, so it never permits more code points than the limit.
function textControl(element, node, limit) {
  node.maxLength = limit;
  node.addEventListener('input', () => {
    const points = [...node.value];
    if (points.length > limit) node.value = points.slice(0, limit).join('');
    element.changedValue();
  });
  return node;
}

export class MuField extends FieldElement {
  static component = 'field';
  #input = null;

  read() {
    return this.#input.value;
  }

  control(data, id) {
    const input = el('input', { id, type: 'text', part: 'input' });
    input.value = data.value ?? '';
    this.#input = textControl(this, input, CATALOG.field.data.value.max_length);
    return { node: input, targets: [input] };
  }
}

export class MuLongTextField extends FieldElement {
  static component = 'long-text-field';
  static submitOnEnter = false;
  #input = null;

  read() {
    return this.#input.value;
  }

  control(data, id) {
    const input = el('textarea', { id, part: 'input' });
    input.value = data.value ?? '';
    this.#input = textControl(this, input, CATALOG['long-text-field'].data.value.max_length);
    return { node: input, targets: [input] };
  }
}

// The words of a range, for example "from 1 to 12" or "of 1 or more".
function range(low, high, words) {
  if (low !== undefined && high !== undefined) return `from ${low} to ${high}`;
  if (low !== undefined) return `${words[0]} ${low}`;
  return `${words[1]} ${high}`;
}

export class MuNumberField extends FieldElement {
  static component = 'number-field';
  #input = null;

  static rules(data) {
    if (data.minimum !== undefined && data.maximum !== undefined && data.minimum > data.maximum) {
      return ['data.minimum: the minimum is more than the maximum'];
    }
    const value = data.value;
    if (value !== undefined && ((data.minimum !== undefined && value < data.minimum) || (data.maximum !== undefined && value > data.maximum))) {
      return ['data.value: the number is outside the range'];
    }
    return [];
  }

  read() {
    return this.#input.value.trim();
  }

  parse(text) {
    const number = NUMBER.test(text) ? Number(text) : NaN;
    return Number.isFinite(number) ? number : undefined;
  }

  reason(text, data) {
    const number = this.parse(text);
    if (number === undefined) return 'Give a number, for example 12 or 12.5.';
    const { minimum: low, maximum: high } = data;
    if ((low !== undefined && number < low) || (high !== undefined && number > high)) {
      return `Give a number ${range(low, high, ['of at least', 'of at most'])}.`;
    }
    return null;
  }

  control(data, id) {
    const input = el('input', { id, type: 'text', part: 'input', autocomplete: 'off' });
    input.inputMode = 'decimal';
    input.value = data.value === undefined ? '' : String(data.value);
    input.addEventListener('input', () => this.changedValue());
    this.#input = input;
    return { node: input, targets: [input] };
  }
}

export class MuDateField extends FieldElement {
  static component = 'date-field';
  #input = null;

  // A date as YYYY-MM-DD sorts as text in the order of time.
  static rules(data) {
    if (data.earliest !== undefined && data.latest !== undefined && data.earliest > data.latest) {
      return ['data.earliest: the earliest date is after the latest date'];
    }
    const value = data.value;
    if (value !== undefined && ((data.earliest !== undefined && value < data.earliest) || (data.latest !== undefined && value > data.latest))) {
      return ['data.value: the date is outside the range'];
    }
    return [];
  }

  read() {
    return this.#input.value.trim();
  }

  parse(text) {
    return isDate(text) ? text : undefined;
  }

  reason(text, data) {
    if (!isDate(text)) return 'Give a date that exists, as YYYY-MM-DD, for example 2026-12-01.';
    const { earliest: low, latest: high } = data;
    if ((low !== undefined && text < low) || (high !== undefined && text > high)) {
      return `Give a date ${range(low, high, ['on or after', 'on or before'])}.`;
    }
    return null;
  }

  control(data, id) {
    const input = el('input', { id, type: 'text', part: 'input', autocomplete: 'off' });
    input.value = data.value ?? '';
    input.addEventListener('input', () => this.changedValue());
    this.#input = input;
    return { node: input, targets: [input] };
  }
}

export class MuChoiceField extends FieldElement {
  static component = 'choice-field';
  static submitOnEnter = false;
  #group = null;

  static rules(data) {
    if (data.choices.length === 0) return ['data.choices: give at least one choice'];
    if (new Set(data.choices).size !== data.choices.length) return ['data.choices: the choices must be unique'];
    if (data.value !== undefined && data.value !== '' && !data.choices.includes(data.value)) {
      return ['data.value: the value is not one of the choices'];
    }
    return [];
  }

  read() {
    return this.#group.querySelector('input:checked')?.value ?? '';
  }

  // A radio gives only a choice of the fill, so the value is always valid.
  describedBy() {
    return [this.#group];
  }

  control(data, id) {
    const radios = data.choices.map((choice, index) => {
      const radio = el('input', { type: 'radio', id: `${id}-${index}`, name: id });
      radio.value = choice;
      radio.checked = choice === data.value;
      radio.addEventListener('change', () => this.changedValue());
      return radio;
    });
    this.#group = el('fieldset', { id, part: 'choices' }, el('legend', { class: 'label', part: 'label' }, data.label),
      ...radios.map((radio) => el('div', { class: 'option' }, radio, el('label', { for: radio.id }, radio.value))));
    return { node: this.#group, targets: radios, labelled: true };
  }
}

export class MuToggleField extends FieldElement {
  static component = 'toggle-field';
  static styles = [...FieldElement.styles, TOGGLE_CSS];
  static submitOnEnter = false;

  #input = null;

  read() {
    return this.#input.checked;
  }

  control(data, id) {
    const isSwitch = data.display === 'switch';
    const input = el('input', { id, type: 'checkbox', part: 'input', role: isSwitch ? 'switch' : null });
    input.checked = data.value ?? false;
    this.#input = input;
    // The input gives the checked state to a screen reader. The state text is for sighted readers.
    const state = isSwitch ? el('span', { part: 'state', 'aria-hidden': 'true' }, input.checked ? 'On' : 'Off') : null;
    const control = isSwitch ? el('span', { class: 'switch' }, input, el('span', { class: 'thumb', 'aria-hidden': 'true' })) : input;
    const label = el('label', { for: id, class: 'toggle', part: 'target' }, control, el('span', { part: 'label' }, data.label), state);
    input.addEventListener('change', () => {
      if (state) state.textContent = input.checked ? 'On' : 'Off';
      this.changedValue();
    });
    return { node: label, targets: [input], labelled: true };
  }
}
