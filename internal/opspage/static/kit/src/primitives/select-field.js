import { el, sheet } from '../render/dom.js';
import { ChoiceList, CHOICE_LIST_CSS } from './choice-list.js';
import { FieldElement } from './fields.js';

const SELECT_CSS = sheet(`
  :host { min-inline-size: 0; overflow-wrap: anywhere; }
  [part="control"] {
    box-sizing: border-box; min-inline-size: 44px; min-block-size: 44px;
    font: var(--mu-type-body); padding: var(--mu-space-2);
  }
  [part="control"] {
    display: flex; align-items: center; justify-content: space-between; gap: var(--mu-space-2);
    text-align: start; color: var(--mu-color-text); background: var(--mu-color-surface);
    border: 1px solid var(--mu-color-border); border-radius: var(--mu-radius-1); cursor: pointer;
  }
  [part="control"]::after {
    content: ""; flex: none; inline-size: 0.45em; block-size: 0.45em;
    border-inline-end: 2px solid; border-block-end: 2px solid; transform: rotate(45deg);
  }
  [part="control"][aria-expanded="true"]::after { transform: rotate(225deg); }
  [part="control"][aria-invalid="true"] { border-color: var(--mu-color-danger); border-width: 2px; }
  [part="value"] { overflow-wrap: anywhere; white-space: pre-wrap; }
  @media (forced-colors: active) {
    [part="control"] { color: ButtonText; background: ButtonFace; border-color: ButtonText; }
    [part="control"]:disabled { color: GrayText; border-color: GrayText; }
  }
`);

export class MuSelectField extends FieldElement {
  static component = 'select-field';
  static styles = [...FieldElement.styles, CHOICE_LIST_CSS, SELECT_CSS];
  static submitOnEnter = false;

  #value = '';
  #list;

  read() {
    return this.#value;
  }

  disconnectedCallback() {
    this.#list?.close();
  }

  refuse(errors) {
    this.#list?.close();
    super.refuse(errors);
  }

  control(data, id) {
    this.#list?.close();
    this.#value = data.value ?? '';
    const valueText = el('span', { part: 'value' }, this.#value || 'Choose an option');
    const button = el('button', { id, type: 'button', part: 'control', role: 'combobox' }, valueText);
    const search = data.choices.length > 7 ? el('input', { type: 'text', part: 'search', autocomplete: 'off', role: 'combobox',
      'aria-label': 'Search ' + data.label, 'aria-autocomplete': 'list' }) : null;
    this.#list = new ChoiceList(this, data, id, {
      control: button, search, selected: (choice) => choice === this.#value,
      choose: (choice) => {
        const changed = choice !== this.#value;
        this.#value = choice;
        valueText.textContent = choice;
        this.#list.refresh();
        this.#list.close(true);
        if (changed) this.changedValue();
      },
    });
    return { node: this.#list.node, targets: search ? [button, search] : [button] };
  }
}
