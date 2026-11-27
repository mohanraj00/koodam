import { el, sheet } from '../render/dom.js';

export const CHOICE_LIST_CSS = sheet(`
  .choice-list, [part="popup"] { display: grid; gap: var(--mu-space-1); min-inline-size: 0; }
  [part="popup"] { padding: var(--mu-space-1); }
  [part="popup"][hidden] { display: none; }
  [part="search"], [part="option"] {
    box-sizing: border-box; min-inline-size: 44px; min-block-size: 44px;
    font: var(--mu-type-body); padding: var(--mu-space-2);
  }
  [part="option"] { overflow-wrap: anywhere; white-space: pre-wrap; }
  [part="search"] { inline-size: 100%; }
  [part="listbox"] {
    max-block-size: 16rem; overflow: auto; background: var(--mu-color-surface);
    border: 1px solid var(--mu-color-border); border-radius: var(--mu-radius-1);
  }
  [part="listbox"]:empty { display: none; }
  [part="option"] {
    display: flex; align-items: center; cursor: pointer; border-radius: var(--mu-radius-1);
    transition: background-color var(--mu-motion-fast);
  }
  [part="option"][aria-selected="true"] { font-weight: var(--mu-type-weight-strong); }
  [part="option"]:hover { background-color: color-mix(in srgb, currentColor 8%, transparent); }
  [part="option"][data-state="active"] { outline: 2px solid var(--mu-color-focus); outline-offset: -2px; }
  @media (forced-colors: active) {
    [part="search"] { color: FieldText; background: Field; border-color: FieldText; }
    [part="listbox"] { border-color: FieldText; }
    [part="option"]:hover { outline: 1px solid Highlight; outline-offset: -2px; }
    [part="option"][data-state="active"] { outline-color: Highlight; }
  }
`);

// The two fields share search, listbox selection state and active option navigation. The field owns its value.
export class ChoiceList {
  #owner;
  #control;
  #search;
  #list;
  #popup;
  #status;
  #options;
  #selected;
  #choose;
  #matches = [];
  #active = -1;
  #outside;

  constructor(owner, { label, choices }, id, { control, search, selected, choose, multiple = false }) {
    this.#owner = owner;
    this.#control = control;
    this.#search = search;
    this.#selected = selected;
    this.#choose = choose;
    const listId = id + '-list';
    this.#list = el('div', { id: listId, role: 'listbox', part: 'listbox', 'aria-label': label, tabindex: '-1',
      'aria-multiselectable': multiple ? 'true' : undefined });
    this.#status = el('div', { role: 'status', 'aria-live': 'polite', part: 'status', class: 'hint' });
    const popupSearch = search !== control ? search : null;
    this.#popup = el('div', { part: 'popup' }, popupSearch, this.#list, this.#status);
    this.#popup.hidden = true;
    this.node = el('div', { class: 'choice-list' }, control, this.#popup);

    this.#options = choices.map((value, index) => {
      const node = el('div', { id: listId + '-' + index, role: 'option', part: 'option' }, value);
      node.addEventListener('click', () => this.#select(value));
      return { value, folded: value.toLowerCase(), node };
    });
    this.refresh();
    for (const target of [control, popupSearch].filter(Boolean)) {
      target.setAttribute('aria-controls', listId);
      target.setAttribute('aria-expanded', 'false');
      target.addEventListener('keydown', (event) => this.#key(event));
    }
    if (!search) control.setAttribute('aria-activedescendant', '');
    control.addEventListener('click', () => {
      if (this.#popup.hidden) this.#open();
      else if (control !== search) this.close(true);
    });
    search?.addEventListener('input', () => {
      if (this.#popup.hidden) this.#open(false, true);
      else this.#filter();
    });
    // Keep focus on the combobox when a person clicks an option.
    this.#list.addEventListener('mousedown', (event) => {
      if (event.target !== this.#list) event.preventDefault();
    });
    this.node.addEventListener('focusout', ({ relatedTarget }) => {
      if (!this.node.contains(relatedTarget)) this.close();
    });
  }

  refresh() {
    for (const option of this.#options) {
      option.node.setAttribute('aria-selected', String(this.#selected(option.value)));
    }
  }

  #open(last = false, fromInput = false) {
    if (this.#control.disabled) return;
    this.#popup.hidden = false;
    this.#control.setAttribute('aria-expanded', 'true');
    if (this.#search && this.#search !== this.#control) {
      this.#control.tabIndex = -1;
      this.#search.setAttribute('aria-expanded', 'true');
      this.#search.value = '';
    }
    (this.#search ?? this.#control).focus();
    const selected = fromInput ? -1 : this.#options.findIndex(({ value }) => this.#selected(value));
    const edge = last ? this.#options.length - 1 : 0;
    this.#filter(selected >= 0 ? selected : edge);
    if (!this.#popup.hidden) {
      this.#outside = new AbortController();
      this.#owner.ownerDocument.addEventListener('click', (event) => {
        if (!event.composedPath().includes(this.#owner)) this.close();
      }, { capture: true, signal: this.#outside.signal });
    }
  }

  close(focus = false) {
    if (this.#popup.hidden) return;
    this.#outside?.abort();
    this.#outside = null;
    this.#popup.hidden = true;
    this.#control.setAttribute('aria-expanded', 'false');
    if (this.#search && this.#search !== this.#control) {
      this.#control.tabIndex = 0;
      this.#search.setAttribute('aria-expanded', 'false');
    }
    if (this.#search === this.#control) this.#search.value = '';
    this.#setActive(-1);
    if (focus) this.#control.focus();
  }

  #filter(index = 0) {
    if (this.#popup.hidden) return;
    this.#setActive(-1);
    const query = this.#search?.value.toLowerCase() ?? '';
    this.#matches = this.#options.filter(({ folded }) => folded.includes(query));
    this.#list.replaceChildren(...this.#matches.map(({ node }) => node));
    this.#list.scrollTop = 0;
    this.#status.textContent = this.#matches.length ? '' : 'No match';
    this.#setActive(Math.min(index, this.#matches.length - 1));
  }

  #setActive(index) {
    this.#matches[this.#active]?.node.removeAttribute('data-state');
    this.#active = index;
    const option = this.#matches[index]?.node;
    const control = this.#search ?? this.#control;
    if (option) {
      option.setAttribute('data-state', 'active');
      control.setAttribute('aria-activedescendant', option.id);
      option.scrollIntoView({ block: 'nearest' });
    } else if (this.#search) control.removeAttribute('aria-activedescendant');
    else control.setAttribute('aria-activedescendant', '');
  }

  #select(value) {
    if (!this.#popup.hidden && !this.#control.disabled) this.#choose(value);
  }

  #key(event) {
    if (this.#control.disabled || event.isComposing || event.altKey || event.ctrlKey || event.metaKey || event.shiftKey) return;
    const { key } = event;
    const buttonSpace = key === ' ' && event.currentTarget === this.#control && this.#control !== this.#search;
    if (this.#popup.hidden) {
      if (!buttonSpace && !['Enter', 'ArrowDown', 'ArrowUp'].includes(key)) return;
      event.preventDefault();
      this.#open(key === 'ArrowUp');
      return;
    }
    if (key === 'Escape') {
      event.preventDefault();
      event.stopPropagation();
      this.close(true);
      return;
    }
    if (key === 'Enter' || buttonSpace) {
      event.preventDefault();
      const active = this.#matches[this.#active];
      if (active) this.#select(active.value);
      return;
    }
    const last = this.#matches.length - 1;
    let index;
    switch (key) {
      case 'ArrowDown': index = Math.min(last, this.#active + 1); break;
      case 'ArrowUp': index = Math.max(0, this.#active - 1); break;
      case 'Home': index = 0; break;
      case 'End': index = last; break;
      default: return;
    }
    event.preventDefault();
    if (last >= 0) this.#setActive(index);
  }
}
