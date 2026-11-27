import { MuElement } from '../primitives/base.js';
import { el, setDisclosure, sheet } from '../render/dom.js';

const PANEL_CSS = sheet(`
  :host { display: block; min-inline-size: 0; color: var(--mu-color-text); font: var(--mu-type-body); }
  button {
    min-inline-size: 44px; min-block-size: 44px; max-inline-size: 100%;
    padding: var(--mu-space-2); font: inherit; color: inherit; cursor: pointer;
    overflow-wrap: anywhere; background: var(--mu-color-surface);
    border: 1px solid var(--mu-color-border); border-radius: var(--mu-radius-2);
  }
  [part="panel"] { padding: var(--mu-space-3) var(--mu-space-2); overflow-wrap: anywhere; }
  [part="panel"][hidden] { display: none !important; }
  @media (forced-colors: active) {
    button { color: ButtonText; background: ButtonFace; border-color: ButtonText; }
  }
`);

function itemErrors(label, items, key) {
  const errors = label.trim() ? [] : ['data.label: give text'];
  const names = new Set();
  for (const [index, item] of items.entries()) {
    if (!item.label.trim()) errors.push(`data.${key}[${index}].label: give text`);
    if (names.has(item.name)) errors.push(`data.${key}[${index}].name: must be unique`);
    names.add(item.name);
  }
  return errors;
}

function panelHasFocus(host, { panel, slot }) {
  const active = host.getRootNode().activeElement;
  return panel.contains(host.shadowRoot.activeElement) || slot.assignedElements().some((node) => node.contains(active));
}

const TAB_STOP = 'a[href], area[href], button, input:not([type="hidden"]), select, textarea, iframe, object, embed,'
  + ' audio[controls], video[controls], summary:first-of-type, [tabindex]';

function hasTabStop(node, watchRoot) {
  if (node.hidden || node.inert) return false;
  const candidate = node.matches(TAB_STOP) || (node.isContentEditable && !node.parentElement?.isContentEditable);
  if (candidate && (!node.hasAttribute('tabindex') || node.tabIndex >= 0) && !node.matches(':disabled')
    && node.getClientRects().length && getComputedStyle(node).visibility === 'visible') return true;
  if (node.shadowRoot) watchRoot(node.shadowRoot);
  const children = node.localName === 'slot'
    ? node.assignedElements({ flatten: true }) : (node.shadowRoot ?? node).children;
  return Array.from(children).some((child) => hasTabStop(child, watchRoot));
}

export class MuTabs extends MuElement {
  static component = 'tabs';
  static observedAttributes = ['selected'];
  static styles = [PANEL_CSS, sheet(`
    [part="tablist"] { display: flex; flex-wrap: wrap; gap: var(--mu-space-2); }
    button[aria-selected="true"] { border-block-end: 3px solid var(--mu-color-accent); font-weight: bold; }
    @media (forced-colors: active) {
      button[aria-selected="true"] { border-block-end-color: Highlight; }
    }
  `)];

  #tabs = new Map();
  #selected = null;
  #previous = null;
  #observer = new MutationObserver(() => this.#watchPanel());

  get selected() {
    return this.#selected;
  }

  connectedCallback() {
    this.#watchPanel();
  }

  disconnectedCallback() {
    this.#observer.disconnect();
  }

  attributeChangedCallback(_name, _before, value) {
    this.select(value ?? this.#tabs.keys().next().value);
  }

  check({ label, tabs }) {
    return itemErrors(label, tabs, 'tabs');
  }

  refuse(errors) {
    this.#observer.disconnect();
    this.#tabs.clear();
    this.#selected = null;
    super.refuse(errors);
  }

  select(name) {
    return this.#select(name, false);
  }

  #select(name, focus) {
    const next = this.#tabs.get(name);
    if (!next) return false;
    if (name === this.#selected) {
      if (focus) next.button.focus();
      return false;
    }
    const before = this.#tabs.get(this.#selected);
    const moveFocus = focus || this.shadowRoot.activeElement === before.button || panelHasFocus(this, before);
    this.#selected = name;
    this.#setTab(before, false);
    this.#setTab(next, true);
    this.#watchPanel();
    if (moveFocus) next.button.focus();
    this.#emit(name);
    return true;
  }

  #emit(name) {
    this.dispatchEvent(new CustomEvent('mu-tabs-change', { bubbles: true, composed: true, detail: { name } }));
  }

  #setTab({ button, panel }, selected) {
    button.tabIndex = selected ? 0 : -1;
    button.setAttribute('aria-selected', String(selected));
    panel.hidden = !selected;
    if (!selected) panel.removeAttribute('tabindex');
  }

  #watchPanel() {
    this.#observer.disconnect();
    const current = this.#tabs.get(this.#selected);
    if (!current) return;
    const { panel, slot } = current;
    const watch = (root) => {
      if (this.isConnected) this.#observer.observe(root, {
        childList: true, subtree: true, attributes: true,
        attributeFilter: ['class', 'contenteditable', 'controls', 'disabled', 'hidden', 'href', 'inert', 'open', 'slot', 'style', 'tabindex', 'type'],
      });
    };
    watch(this);
    if (hasTabStop(slot, watch)) panel.removeAttribute('tabindex');
    else panel.tabIndex = 0;
  }

  #onKey(event, name, button) {
    const key = event.key;
    if (key === 'Tab') this.#watchPanel();
    if (!['ArrowLeft', 'ArrowRight', 'Home', 'End'].includes(key) || event.altKey || event.ctrlKey || event.metaKey) return;
    const names = [...this.#tabs.keys()];
    let index = 0;
    if (key === 'End') index = names.length - 1;
    if (key === 'ArrowLeft' || key === 'ArrowRight') {
      const forward = (key === 'ArrowRight') !== button.matches(':dir(rtl)');
      index = (names.indexOf(name) + (forward ? 1 : -1) + names.length) % names.length;
    }
    event.preventDefault();
    this.#select(names[index], true);
  }

  render({ label, tabs }) {
    this.#previous = this.#selected;
    this.#tabs.clear();
    const list = el('div', { part: 'tablist', role: 'tablist', 'aria-label': label });
    for (const { label, name } of tabs) {
      const tabId = 'tab-' + name;
      const panelId = 'tab-panel-' + name;
      const button = el('button', { type: 'button', id: tabId, part: 'tab', role: 'tab', 'aria-controls': panelId }, label);
      const slot = el('slot', { name });
      const panel = el('div', { id: panelId, part: 'panel', role: 'tabpanel', 'aria-labelledby': tabId }, slot);
      button.addEventListener('click', () => this.select(name));
      button.addEventListener('focus', () => this.select(name));
      button.addEventListener('keydown', (event) => this.#onKey(event, name, button));
      slot.addEventListener('slotchange', () => {
        if (name === this.#selected) this.#watchPanel();
      });
      list.append(button);
      this.#tabs.set(name, { button, panel, slot });
    }
    const requested = this.#previous ?? this.getAttribute('selected');
    this.#selected = this.#tabs.has(requested) ? requested : tabs[0].name;
    for (const [name, tab] of this.#tabs) this.#setTab(tab, name === this.#selected);
    return [list, ...Array.from(this.#tabs.values(), ({ panel }) => panel)];
  }

  rendered() {
    this.#watchPanel();
    if (this.#previous !== null && this.#previous !== this.#selected) this.#emit(this.#selected);
  }
}

export class MuAccordion extends MuElement {
  static component = 'accordion';
  static styles = [PANEL_CSS, sheet(`
    [part="heading"] { margin: 0; }
    [part="heading"]:not(:first-child) { margin-block-start: var(--mu-space-2); }
    [part="toggle"] {
      display: flex; inline-size: 100%; align-items: center; justify-content: space-between;
      gap: var(--mu-space-2); text-align: start; font-weight: bold;
    }
    [part="toggle"]::after {
      content: ""; flex: none; inline-size: 0.5em; block-size: 0.5em;
      border: solid currentColor; border-width: 0 2px 2px 0; transform: rotate(45deg);
    }
    [part="toggle"][aria-expanded="true"]::after { transform: rotate(225deg); }
  `)];

  #sections = new Map();

  check({ label, sections }) {
    return itemErrors(label, sections, 'sections');
  }

  state() {
    return 'collapsed';
  }

  refuse(errors) {
    this.#sections.clear();
    super.refuse(errors);
  }

  setExpanded(name, expanded) {
    if (typeof expanded !== 'boolean') throw new TypeError('expanded: must be a boolean');
    const section = this.#sections.get(name);
    if (!section || section.expanded === expanded) return false;
    const { button, panel } = section;
    const moveFocus = !expanded && panelHasFocus(this, section);
    section.expanded = expanded;
    setDisclosure(button, panel, expanded);
    this.dataset.state = [...this.#sections.values()].some((item) => item.expanded) ? 'expanded' : 'collapsed';
    if (moveFocus) button.focus();
    this.dispatchEvent(new CustomEvent('mu-accordion-toggle', { bubbles: true, composed: true, detail: { name, expanded } }));
    return true;
  }

  render({ label, sections }) {
    this.#sections.clear();
    const level = this.getAttribute('heading-level');
    const heading = /^[2-6]$/.test(level) ? 'h' + level : 'h2';
    const group = el('div', { role: 'group', 'aria-label': label });
    for (const { label, name } of sections) {
      const panelId = 'section-panel-' + name;
      const button = el('button', { type: 'button', id: 'section-' + name, part: 'toggle', 'aria-controls': panelId }, label);
      const slot = el('slot', { name });
      const panel = el('div', { id: panelId, part: 'panel' }, slot);
      const section = { button, panel, slot, expanded: false };
      setDisclosure(button, panel, false);
      button.addEventListener('click', () => this.setExpanded(name, !section.expanded));
      group.append(el(heading, { part: 'heading' }, button), panel);
      this.#sections.set(name, section);
    }
    return [group];
  }
}
