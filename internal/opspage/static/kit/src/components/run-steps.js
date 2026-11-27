import { MuElement } from '../primitives/base.js';
import { el, setDisclosure, sheet } from '../render/dom.js';

const STATE_TEXT = Object.freeze({
  pending: 'Pending', running: 'Running', in_progress: 'In progress', done: 'Done', failed: 'Failed', skipped: 'Skipped',
});

const WORK_LIST_CSS = sheet(`
  :host { display: block; min-inline-size: 0; color: var(--mu-color-text); font: var(--mu-type-body); }
  [part="authorship"] { margin: 0 0 var(--mu-space-1); font: var(--mu-type-caption); overflow-wrap: anywhere; }
  [part="list"] { margin: 0; padding-inline-start: var(--mu-space-2); }
  li { margin-inline-start: 1.25em; padding-block: var(--mu-space-1); overflow-wrap: anywhere; }
  .row {
    display: grid; grid-template-columns: minmax(0, 1fr) auto; align-items: baseline;
    gap: var(--mu-space-2);
  }
  [part="state"] { font: var(--mu-type-caption); }
  [part="announcement"] {
    position: absolute; inline-size: 1px; block-size: 1px; overflow: hidden; clip-path: inset(50%); white-space: nowrap;
  }
`);

class MuWorkList extends MuElement {
  #view = null;
  #states = new Map();

  refuse(errors) {
    this.#view = null;
    this.#states.clear();
    super.refuse(errors);
  }

  updateList(tag, label, items) {
    const firstFill = this.#view === null;
    this.#view ??= {
      byline: el('p', { part: 'authorship' }),
      list: el(tag, { part: 'list', role: 'list' }),
      announcement: el('div', { part: 'announcement', role: 'status', 'aria-live': 'polite' }),
    };
    const { byline, list, announcement } = this.#view;
    const { type, id, name } = this.actor;
    const author = 'Author: ' + (name ? `${name} (${type}, ${id})` : `${type}, ${id}`);
    if (byline.textContent !== author) byline.textContent = author;
    if (list.getAttribute('aria-label') !== label) list.setAttribute('aria-label', label);
    this.renderItems(list, items);

    const changes = [];
    for (const { id, name, state } of items) {
      if (this.#states.has(id) && this.#states.get(id) !== state) {
        changes.push(`${name}: ${STATE_TEXT[state].toLowerCase()}`);
      }
    }
    this.#states = new Map(items.map(({ id, state }) => [id, state]));
    // Keep the live region in the DOM. Write each fill's changes together.
    if (changes.length) announcement.textContent = changes.join('. ');
    return firstFill ? [byline, list, announcement] : null;
  }
}

function toolDetails({ tool_name, tool_arguments, tool_result }) {
  return el('dl', {},
    el('dt', {}, 'Tool'), el('dd', { part: 'tool' }, tool_name),
    tool_arguments === undefined ? null : el('dt', {}, 'Arguments'),
    tool_arguments === undefined ? null : el('dd', {}, el('pre', { part: 'arguments' }, tool_arguments)),
    tool_result === undefined ? null : el('dt', {}, 'Result'),
    tool_result === undefined ? null : el('dd', {}, el('pre', { part: 'result' }, tool_result)));
}

export class MuRunSteps extends MuWorkList {
  static component = 'run-steps';
  static styles = [WORK_LIST_CSS, sheet(`
    [part="toggle"] {
      display: flex; align-items: center; justify-content: space-between; gap: var(--mu-space-2);
      min-inline-size: 44px; min-block-size: 44px; inline-size: 100%; padding: var(--mu-space-1) var(--mu-space-2);
      color: inherit; background: var(--mu-color-surface); border: 1px solid var(--mu-color-border);
      border-radius: var(--mu-radius-2); font: inherit; text-align: start; cursor: pointer;
      overflow-wrap: anywhere;
    }
    [part="toggle"]::after {
      content: ""; flex: none; inline-size: 0.5em; block-size: 0.5em;
      border: solid currentColor; border-width: 0 2px 2px 0; transform: rotate(45deg);
    }
    [part="toggle"][aria-expanded="true"]::after { transform: rotate(225deg); }
    [part="panel"] {
      margin-block-start: var(--mu-space-1); padding: var(--mu-space-2);
      background: var(--mu-color-surface); border: 1px solid var(--mu-color-border); border-radius: var(--mu-radius-2);
    }
    p, dl, dd, pre { margin: 0; }
    dt { font: var(--mu-type-caption); }
    dt:not(:first-child), p + dl { margin-block-start: var(--mu-space-2); }
    p, pre { white-space: pre-wrap; }
    pre { font: var(--mu-type-code); }
    @media (forced-colors: active) {
      [part="toggle"] { color: ButtonText; background: ButtonFace; border-color: ButtonText; }
      [part="panel"] { color: CanvasText; background: Canvas; border-color: CanvasText; }
    }
  `)];

  #rows = new Map();
  #controlCount = 0;

  refuse(errors) {
    this.#rows.clear();
    super.refuse(errors);
  }

  render({ label, steps }) {
    return this.updateList('ol', label, steps);
  }

  #makeRow(hasDetails) {
    const title = el('span', { part: 'name' });
    const state = el('span', { part: 'state' });
    let button = null;
    let panel = null;
    if (hasDetails) {
      const number = ++this.#controlCount;
      title.id = 'step-name-' + number;
      panel = el('div', { id: 'step-panel-' + number, part: 'panel' });
      button = el('button', { type: 'button', part: 'toggle', 'aria-labelledby': title.id, 'aria-controls': panel.id }, title);
      setDisclosure(button, panel, false);
      button.addEventListener('click', () => setDisclosure(button, panel, panel.hidden));
    }
    const row = el('li', { part: 'step' }, el('div', { class: 'row' }, button ?? title, state), panel);
    return { row, title, state, button, panel };
  }

  renderItems(list, steps) {
    const next = new Map();
    for (const step of steps) {
      const hasDetails = step.detail !== undefined || step.tool_name !== undefined;
      const old = this.#rows.get(step.id);
      const entry = old && Boolean(old.button) === hasDetails ? old : this.#makeRow(hasDetails);
      if (entry.title.textContent !== step.name) entry.title.textContent = step.name;
      const state = STATE_TEXT[step.state];
      if (entry.state.textContent !== state) entry.state.textContent = state;
      if (entry.panel && ['detail', 'tool_name', 'tool_arguments', 'tool_result'].some((key) => entry.step?.[key] !== step[key])) {
        entry.panel.replaceChildren(
          ...(step.detail === undefined ? [] : [el('p', { part: 'detail' }, step.detail)]),
          ...(step.tool_name === undefined ? [] : [toolDetails(step)]));
      }
      entry.step = step;
      next.set(step.id, entry);
    }
    for (const [id, entry] of this.#rows) {
      if (next.get(id) !== entry) entry.row.remove();
    }
    const active = this.shadowRoot.activeElement;
    let before = null;
    for (const { row, button } of [...next.values()].reverse()) {
      // Move other rows around a focused button so it stays in the DOM and keeps focus.
      if ((!active || active !== button) && (row.parentNode !== list || row.nextElementSibling !== before)) {
        list.insertBefore(row, before);
      }
      before = row;
    }
    this.#rows = next;
  }
}

export class MuPlan extends MuWorkList {
  static component = 'plan';
  static styles = [WORK_LIST_CSS];

  render({ label, tasks }) {
    return this.updateList('ul', label, tasks);
  }

  renderItems(list, tasks) {
    list.replaceChildren(...tasks.map(({ name, state }) => el('li', { part: 'task' },
      el('div', { class: 'row' }, el('span', { part: 'name' }, name), el('span', { part: 'state' }, STATE_TEXT[state])))));
  }
}
