// An editable grid with typed cells (catalog/data-grid.json). A person moves between the cells with the
// keyboard and edits one cell in place. Each saved change goes to the application as one action. The cell
// shows a text marker until the receipt comes back. The kit computes no formula: the application sends each
// new value in a new fill. src/components/data-grid.md gives the rules.
import { MuElement } from '../primitives/base.js';
import { ActionState } from '../actions/action.js';
import { el, sheet } from '../render/dom.js';
import { isDate } from '../render/validate.js';
import { PATIENCE, patienceOf, ReceiptTimer } from './pending.js';
import { REFERENCE_CSS, showReference } from './receipt.js';

const NUMBER = /^[+-]?(\d+\.?\d*|\.\d+)(e[+-]?\d+)?$/i;
// The maximum number of code points of a text value. It is the limit of catalog/text-cell.json.
const MAX_TEXT = 500;
// The maximum number of UTF-16 units that a person can type in an editor.
const MAX_INPUT = 1000;
// The text marker of a changed cell. A pending change never shows as done.
const MARKERS = Object.freeze({ changed: 'Changed', pending: 'Pending', error: 'Not done' });
// The names of the values that a request sends for one change.
const SENT = Object.freeze({ row: 'Row', column: 'Column', value: 'Value' });
const READ_ONLY = 'This cell is read-only.';
const TYPES = Object.freeze(['text', 'number', 'date', 'choice']);
// The keys of a binding.
const KEYS = Object.freeze(['tool', 'arguments', 'title', 'rows', 'columns']);
// The limits of the contracts: 200 rows, 30 columns, 20 choices, a key of 40 characters and a label of 80 characters.
const MAX_ROWS = 200;
const MAX_COLUMNS = 30;
const MAX_CHOICES = 20;
const MAX_KEY = 40;
const MAX_LABEL = 80;
// The label of a grid has 120 characters at most. The application gives it as the title of the binding.
const MAX_TITLE = 120;

const isObject = (value) => value !== null && typeof value === 'object' && !Array.isArray(value);
const short = (value, max) => typeof value === 'string' && value.length > 0 && value.length <= max * 2 && [...value].length <= max;
const sameList = (left, right) => left.length === right.length && left.every((item, index) => item === right[index]);

// The errors of the title, the rows and the columns of a binding. The application gives the title of the grid,
// and the key, the label and the type of each row and each column that a person can edit. Thus a fill cannot
// give a different meaning to a key, or a different name to the target of the bound arguments.
function policyErrors(binding) {
  const errors = [];
  if (!short(binding.title, MAX_TITLE)) errors.push('title must be the title of the grid, with 1 to 120 characters');
  const rows = isObject(binding.rows) ? Object.entries(binding.rows) : null;
  if (!rows || rows.length > MAX_ROWS || !rows.every(([key, label]) => short(key, MAX_KEY) && short(label, MAX_LABEL))) {
    errors.push('rows must map each editable row key to its label');
  }
  const columns = isObject(binding.columns) ? Object.entries(binding.columns) : null;
  const valid = ([key, column]) => short(key, MAX_KEY) && isObject(column)
    && Object.keys(column).every((name) => name === 'label' || name === 'type' || name === 'choices')
    && short(column.label, MAX_LABEL) && TYPES.includes(column.type)
    && (column.type === 'choice'
      ? Array.isArray(column.choices) && column.choices.length >= 1 && column.choices.length <= MAX_CHOICES
        && column.choices.every((choice) => short(choice, MAX_LABEL)) && new Set(column.choices).size === column.choices.length
      : column.choices === undefined);
  if (!columns || columns.length > MAX_COLUMNS || !columns.every(valid)) {
    errors.push('columns must map each editable column key to its label and its type, with the choices of a choice column');
  }
  return errors;
}

// A frozen copy with no prototype, so a key such as constructor is not in the map.
const frozenMap = (entries) => Object.freeze(Object.assign(Object.create(null), Object.fromEntries(entries)));
const MOVES = Object.freeze({ ArrowRight: [0, 1], ArrowLeft: [0, -1], ArrowDown: [1, 0], ArrowUp: [-1, 0] });

// The value of a typed text, or the reason that the grid refuses it. An empty text gives an empty cell.
function parse(column, raw) {
  // A choice is exact text. A choice can have a space at its start or its end, so the exact text comes first.
  if (column.type === 'choice' && column.choices.includes(raw)) return { value: raw };
  const words = column.type === 'text' ? raw : raw.trim();
  if (words === '') return { value: '' };
  if (column.type === 'text') {
    let count = 0;
    for (const _ of words) count += 1;
    return count > MAX_TEXT ? { reason: `Give a text of ${MAX_TEXT} characters at most.` } : { value: words };
  }
  if (column.type === 'number') {
    const number = NUMBER.test(words) ? Number(words) : NaN;
    return Number.isFinite(number) ? { value: number } : { reason: 'Give a number, for example 12 or 12.5.' };
  }
  if (column.type === 'date') {
    return isDate(words) ? { value: words } : { reason: 'Give a date that exists, as YYYY-MM-DD, for example 2026-12-01.' };
  }
  if (column.choices.includes(words)) return { value: words };
  // A typed text can differ from one choice only by the spaces at its start and its end.
  const near = column.choices.filter((choice) => choice.trim() === words);
  return near.length === 1 ? { value: near[0] } : { reason: `Give one of these choices: ${column.choices.join(', ')}.` };
}

export class MuDataGrid extends MuElement {
  static component = 'data-grid';
  static styles = [REFERENCE_CSS, sheet(`
    :host { display: block; min-width: 0; }
    .grid { color: var(--mu-color-text); font: var(--mu-type-body); display: flex; flex-direction: column; gap: var(--mu-space-2); }
    .scroll {
      overflow: auto; max-block-size: 30rem; background: var(--mu-color-surface);
      border: 1px solid var(--mu-color-border); border-radius: var(--mu-radius-1);
    }
    table { border-collapse: separate; border-spacing: 0; }
    th, td {
      text-align: start; vertical-align: top; padding: var(--mu-space-2) var(--mu-space-3);
      min-width: 5rem; max-width: 16rem; block-size: 2.75rem; overflow-wrap: break-word; background: var(--mu-color-surface);
      border-block-end: 1px solid var(--mu-color-border); border-inline-end: 1px solid var(--mu-color-border);
    }
    th { font-weight: var(--mu-type-weight-strong); }
    thead th, thead td { position: sticky; inset-block-start: 0; z-index: 2; }
    tbody th, thead td { position: sticky; inset-inline-start: 0; z-index: 1; }
    thead td { z-index: 3; }
    td.number { text-align: end; font-variant-numeric: tabular-nums; }
    td[aria-readonly="true"] { color: var(--mu-color-text-muted); }
    td:focus-visible, .editor:focus-visible { outline-offset: -2px; }
    .marker { display: block; font: var(--mu-type-caption); font-weight: var(--mu-type-weight-strong); }
    td[data-state="error"] .marker, .status.error { color: var(--mu-color-danger); }
    .editor {
      font: inherit; color: var(--mu-color-text); background: var(--mu-color-surface); width: 100%; min-width: 4rem;
      border: 1px solid var(--mu-color-border); border-radius: var(--mu-radius-1); padding: var(--mu-space-1);
    }
    p { margin: 0; overflow-wrap: anywhere; }
    .title { font-weight: var(--mu-type-weight-strong); }
    .status { font: var(--mu-type-caption); min-height: 1.5em; }
    .status.done { color: var(--mu-color-success); border-inline-start: 2px solid currentColor; padding-inline-start: var(--mu-space-2); }
    .pending-exits { display: flex; flex-wrap: wrap; gap: var(--mu-space-2); }
    .pending-exits[hidden] { display: none; }
    .pending-exits button {
      min-width: 2.75rem; min-height: 2.75rem; padding: var(--mu-space-2) var(--mu-space-3);
      border: 1px solid var(--mu-color-border); border-radius: var(--mu-radius-1);
      background: var(--mu-color-surface); color: var(--mu-color-text); font: var(--mu-type-body); cursor: pointer;
    }
    .muted { font: var(--mu-type-caption); color: var(--mu-color-text-muted); }
    @media (forced-colors: active) {
      .scroll, th, td, .editor { border-color: CanvasText; }
      th, td { background: Canvas; }
      td[aria-readonly="true"], td[data-state="error"] .marker, .status.done, .status.error, .muted { color: CanvasText; }
      .pending-exits button { background: ButtonFace; color: ButtonText; border-color: ButtonText; forced-color-adjust: none; }
    }
  `)];

  #action = new ActionState();
  // The rows and the columns that the application binds, or null.
  #policy = null;
  #columns = [];
  #rows = [];
  // The changes that have no receipt with ok, by cell id. A change is changed, pending or error.
  #changes = new Map();
  // The change of the pending request.
  #sent = null;
  // The values that a receipt confirmed, by cell id, until the application sends new data.
  #confirmed = new Map();
  #seen = '';
  // The keys of the cell that has the Tab stop, and the open editor.
  #current = null;
  #editing = null;
  #armed = false;
  #scroll = null;
  #status = null;
  #reference = null;
  #success = null;
  #pending = null;
  #timer = new ReceiptTimer();
  #patience = PATIENCE;

  // The state of the action can only be read. Only the binding, a saved change and a receipt change it.
  get action() {
    return this.#action;
  }

  // The application binds the tool, its fixed arguments, and the rows and the columns that a person can edit.
  // Each request also sends row, column and value. The grid has no label, no confirm step and no typed edit,
  // so the binding has no other key.
  set action(binding) {
    let errors = ['the action must be an object'];
    let policy = null;
    if (binding === null) errors = this.#action.bind(null);
    else if (isObject(binding)) {
      errors = Object.keys(binding).every((key) => KEYS.includes(key))
        ? policyErrors(binding) : ['a data grid accepts only tool, arguments, title, rows and columns'];
      // The policy is valid before the action changes, so a refused binding changes nothing.
      if (!errors.length) errors = this.#action.bind({ tool: binding.tool, arguments: binding.arguments, editable: SENT });
      if (!errors.length) {
        policy = Object.freeze({ title: binding.title, rows: frozenMap(Object.entries(binding.rows)),
          columns: frozenMap(Object.entries(binding.columns).map(([key, column]) => [key, Object.freeze({ label: column.label,
            type: column.type, choices: Object.freeze([...(column.choices ?? [])]) })])) });
      }
    }
    if (errors.length) throw new TypeError('action: ' + errors.join('; '));
    this.#policy = policy;
    // A new binding is a new target, so no change of the old target stays.
    this.#changes.clear();
    this.#confirmed.clear();
    this.#sent = null;
    this.#success = null;
    this.#updatePending();
    if (this.fill) this.fill = this.fill;
  }

  get patience() {
    return this.#patience;
  }

  set patience(value) {
    this.#patience = patienceOf(value);
  }

  hold(update) {
    if (!this.#action.hold(update)) return false;
    this.#updatePending();
    return true;
  }

  noReceipt(id) {
    if (this.#action.noReceipt(id)) this.#updatePending();
  }

  cancel(key) {
    const sent = this.#sent;
    if (!sent || !key || key !== this.#action.key || !this.#action.cancel()) return false;
    this.#sent = null;
    if (this.#changes.get(sent.id) === sent) {
      // The sent edit stays Changed until a person saves it again.
      sent.state = 'changed';
      sent.canceled = true;
      const cell = this.#find(sent.row, sent.column);
      if (cell) this.#paint(cell);
    }
    if (this.fill) this.#say(this.#action.message);
    this.#updatePending(sent);
    this.#flush();
    this.#sync();
    return true;
  }

  // Returns true if the receipt belongs to the pending request. Only a receipt with ok removes the marker.
  receipt(value) {
    if (!this.#action.receipt(value)) return false;
    const change = this.#sent;
    const ok = this.#action.state === 'done';
    const message = this.#action.message;
    const reference = this.#action.reference;
    this.#sent = null;
    // The action can start again for the next change.
    this.#action.reopen();
    this.#updatePending(change);
    if (!change) return true;
    const cell = this.#find(change.row, change.column);
    // A newer change of the same cell stays. It goes in the next request.
    const latest = this.#changes.get(change.id) === change;
    if (ok) {
      this.#confirmed.set(change.id, change.value);
      if (latest) this.#changes.delete(change.id);
    } else if (latest) {
      change.state = 'error';
    }
    // A newer change with the value that is now confirmed needs no request.
    const newer = this.#changes.get(change.id);
    if (cell && newer && newer !== change && newer.value === this.#base(cell)) this.#changes.delete(change.id);
    if (cell) this.#paint(cell);
    const name = cell ? `${cell.row.label}, ${cell.column.label}` : 'a cell';
    // If a newer change of this cell stays, its marker shows the current state.
    if (ok && this.#changes.has(change.id)) this.#say('');
    else this.#say(`${ok ? 'Done' : 'Not done'}: ${name}.${message ? ' ' + message : ''}`, ok ? 'done' : 'error', reference);
    this.#flush();
    this.#sync();
    return true;
  }

  // The contract format cannot compare a cell with its column, or a key with a different key. The application
  // gives the label and the type of each key that a person can edit. A fill with a different label or type for
  // such a key is refused, so a fill cannot show a different meaning for a key that a request sends.
  check(data) {
    if (!data.columns.length) return ['data.columns: at least one column is necessary'];
    const policy = this.#policy;
    // The bound arguments select a target that a person cannot see. The application names it.
    if (policy && data.label !== policy.title) return ['data.label: the label is not the title of the application'];
    const keys = new Set();
    for (const [index, { data: column }] of data.columns.entries()) {
      const at = `data.columns[${index}].data`;
      if (keys.has(column.key)) return [`${at}.key: two columns have the same key`];
      keys.add(column.key);
      const choices = column.choices;
      if (column.type === 'choice' && !choices?.length) return [`${at}.choices: a choice column needs its choices`];
      if (column.type !== 'choice' && choices !== undefined) return [`${at}.choices: only a choice column has choices`];
      if (choices && new Set(choices).size !== choices.length) return [`${at}.choices: two choices are the same`];
      const bound = policy && Object.hasOwn(policy.columns, column.key) ? policy.columns[column.key] : null;
      if (bound && column.label !== bound.label) return [`${at}.label: the label is not the label of the application`];
      if (bound && column.type !== bound.type) return [`${at}.type: the type is not the type of the application`];
      if (bound && !sameList(choices ?? [], bound.choices)) return [`${at}.choices: the choices are not the choices of the application`];
    }
    const rows = new Set();
    for (const [index, { data: row }] of data.rows.entries()) {
      const at = `data.rows[${index}].data`;
      if (rows.has(row.key)) return [`${at}.key: two rows have the same key`];
      rows.add(row.key);
      if (policy && Object.hasOwn(policy.rows, row.key) && row.label !== policy.rows[row.key]) {
        return [`${at}.label: the label is not the label of the application`];
      }
      if (row.cells.length !== data.columns.length) return [`${at}.cells: the cell count must equal the column count`];
      for (const [position, cell] of row.cells.entries()) {
        const column = data.columns[position].data;
        if (cell.component !== `${column.type}-cell`) return [`${at}.cells[${position}].component: must be ${column.type}-cell`];
        if (column.type === 'choice' && cell.data.value !== undefined && !column.choices.includes(cell.data.value)) {
          return [`${at}.cells[${position}].data.value: the value is not one of the choices`];
        }
      }
    }
    return [];
  }

  state(data) {
    if (this.#sent) return 'pending';
    if ([...this.#changes.values()].some((change) => change.state === 'error')) return 'error';
    if (!data.rows.length) return 'empty';
    return this.#action.state === 'disabled' ? 'disabled' : 'default';
  }

  refuse(errors) {
    this.#editing = null;
    super.refuse(errors);
  }

  rendered() {
    this.#flush();
  }

  render(data) {
    // New data keeps the focus on the same cell, and an open editor keeps its text.
    const active = this.shadowRoot.activeElement;
    const keep = active && this.#current && this.#scroll?.contains(active)
      ? { ...this.#current, draft: this.#editing ? this.#editing.input.value : undefined } : null;
    const heldExit = this.#pending?.exits.contains(active) ? active.getAttribute('part') : null;
    this.#editing = null;
    const json = JSON.stringify(data);
    const fresh = json !== this.#seen;
    if (fresh) this.#confirmed.clear();
    this.#seen = json;
    const policy = this.#policy;
    const bound = policy !== null;
    // A person can edit a cell only if the application binds its row key and its column key.
    this.#columns = data.columns.map(({ data: column }) => ({ key: column.key, label: column.label, type: column.type,
      locked: column.read_only === true || !bound || !Object.hasOwn(policy.columns, column.key), choices: column.choices ?? [] }));
    const body = el('tbody');
    this.#rows = data.rows.map(({ data: item }) => {
      const row = { key: item.key, label: item.label, cells: [] };
      const line = el('tr', {}, el('th', { scope: 'row' }, item.label));
      for (const [index, column] of this.#columns.entries()) {
        const source = item.cells[index].data;
        const editable = bound && Object.hasOwn(policy.rows, row.key) && !column.locked && source.read_only !== true;
        const td = el('td', { class: column.type, part: 'cell', tabindex: '-1', 'aria-readonly': editable ? 'false' : 'true' });
        const cell = { id: JSON.stringify([row.key, column.key]), row, column, td, editable, value: source.value ?? '' };
        row.cells.push(cell);
        line.append(td);
      }
      body.append(line);
      return row;
    });
    // A change stays if its cell is in the new data and a person can edit it. A change that waits for its
    // receipt also stays on a cell that is now read-only, so the receipt can show its result. A change that
    // is not done on such a cell stays until the application sends new data.
    for (const [id, change] of this.#changes) {
      const cell = this.#find(change.row, change.column);
      const waits = change === this.#sent || (change.state === 'error' && !fresh);
      if (!cell || !(waits || (cell.editable && cell.column.type === change.type))) this.#changes.delete(id);
    }
    // The state of the grid agrees with the changes that stay.
    this.dataset.state = this.state(data);
    for (const row of this.#rows) for (const cell of row.cells) this.#paint(cell);
    const start = (this.#current && this.#find(this.#current.row, this.#current.column)) ?? this.#rows[0]?.cells[0];
    if (start) {
      start.td.setAttribute('tabindex', '0');
      this.#current = { row: start.row.key, column: start.column.key };
    } else {
      this.#current = null;
    }
    const table = el('table', { role: 'grid', 'aria-label': data.label, 'aria-readonly': bound ? 'false' : 'true' },
      el('thead', {}, el('tr', {}, el('td'), ...this.#columns.map((column) => el('th', { scope: 'col' }, column.label)))), body);
    table.addEventListener('keydown', (event) => this.#key(event));
    table.addEventListener('focusin', (event) => this.#focused(event));
    table.addEventListener('focusout', (event) => {
      // The focus left the editor, for example with Tab or a click. A valid value is saved.
      if (this.#editing && event.target === this.#editing.input) this.#commit(false);
    });
    table.addEventListener('mousedown', (event) => {
      const cell = this.#cellOf(event.target);
      this.#armed = Boolean(cell) && this.shadowRoot.activeElement === cell.td;
    });
    // The first click moves the focus to a cell. A click on the cell that has the focus opens its editor.
    table.addEventListener('click', (event) => {
      const cell = this.#cellOf(event.target);
      if (cell && this.#armed && !this.#editing) this.#open(cell);
      this.#armed = false;
    });
    this.#scroll = el('div', { class: 'scroll', part: 'scroll' }, table);
    this.#status = el('p', { class: 'status', id: 'status', role: 'status', 'aria-live': 'polite', part: 'status' });
    this.#reference = el('div', { class: 'reference-row' });
    const detail = el('p', { class: 'muted', role: 'status', part: 'pending' });
    const retry = el('button', { type: 'button', part: 'retry' }, 'Retry');
    const cancel = el('button', { type: 'button', part: 'cancel' }, 'Cancel');
    retry.addEventListener('click', () => {
      if (!this.fill || this.#pending?.retry !== retry) return;
      const request = this.#action.retry();
      if (request) this.#send(request);
    });
    cancel.addEventListener('click', () => {
      if (!this.fill || this.#pending?.cancel !== cancel) return;
      const key = this.#action.key;
      if (!key || cancel.hidden) return;
      const event = new CustomEvent('mu-action-cancel', { bubbles: true, composed: true, cancelable: true, detail: Object.freeze({ key }) });
      if (this.dispatchEvent(event)) this.cancel(key);
    });
    const exits = el('div', { class: 'pending-exits', part: 'exit' }, retry, cancel);
    this.#pending = { detail, retry, cancel, exits };
    this.#updatePending();
    if (this.#success) this.#say(this.#success.words, 'done', this.#success.reference);
    if (keep) queueMicrotask(() => this.#restore(keep));
    if (heldExit) queueMicrotask(() => {
      const button = this.#pending[heldExit];
      if (button.isConnected && !button.hidden) button.focus();
    });
    const author = this.fill.slots.authorship;
    const name = author.name ? ` (${author.name})` : '';
    return [el('div', { class: 'grid', role: 'group', 'aria-label': data.label, part: 'data-grid' },
      el('p', { class: 'title', part: 'title' }, data.label), this.#scroll,
      data.rows.length ? null : el('p', { part: 'empty' }, 'No rows. Change the request, or try again later.'),
      this.#status, this.#reference, detail, exits,
      el('p', { class: 'muted', part: 'authorship' }, `Author: ${author.type} ${author.id}${name}`))];
  }

  #restore(keep) {
    const cell = this.#find(keep.row, keep.column);
    if (!cell || !cell.td.isConnected) return;
    if (keep.draft !== undefined && cell.editable) this.#open(cell, keep.draft);
    else cell.td.focus();
  }

  #find(rowKey, columnKey) {
    const column = this.#columns.findIndex((item) => item.key === columnKey);
    return column < 0 ? undefined : this.#rows.find((row) => row.key === rowKey)?.cells[column];
  }

  #cellOf(target) {
    const td = target instanceof Element ? target.closest('td') : null;
    if (!td) return undefined;
    for (const row of this.#rows) {
      const cell = row.cells.find((item) => item.td === td);
      if (cell) return cell;
    }
    return undefined;
  }

  // The value that the application confirmed for the cell, then the value that the cell shows.
  #base(cell) {
    return this.#confirmed.has(cell.id) ? this.#confirmed.get(cell.id) : cell.value;
  }

  #shown(cell) {
    const change = this.#changes.get(cell.id);
    return change ? change.value : this.#base(cell);
  }

  // Each value and each marker goes into a text node.
  #paint(cell) {
    if (this.#editing?.cell === cell) return;
    const change = this.#changes.get(cell.id);
    cell.td.dataset.state = change ? change.state : 'default';
    cell.td.replaceChildren(el('span', { part: 'value' }, String(this.#shown(cell))),
      change ? el('span', { class: 'marker', part: 'changed' }, MARKERS[change.state]) : '');
  }

  #say(words, state = '', reference = '') {
    this.#success = state === 'done' ? { words, reference } : null;
    this.#status.className = 'status ' + state;
    this.#status.textContent = words;
    showReference(this.#reference, reference);
  }

  #sync() {
    if (this.fill) this.dataset.state = this.state(this.fill.data);
  }

  #updatePending(change = this.#sent) {
    const { key, wait, message } = this.#action;
    this.#timer.watch(wait, this.#patience, () => this.hold({ key, wait: 'stalled' }));
    if (wait) this.dataset.wait = wait;
    else delete this.dataset.wait;
    if (!this.fill || !this.#pending) return;
    const { detail, retry, cancel, exits } = this.#pending;
    const active = this.shadowRoot.activeElement;
    const cell = change && this.#find(change.row, change.column);
    const name = cell ? `${cell.row.label}, ${cell.column.label}` : this.fill.data.label;
    const words = wait ? (message ? `Pending: ${message}` : 'Pending') : '';
    if (detail.textContent !== words) detail.textContent = words;
    detail.hidden = !wait;
    retry.hidden = wait !== 'stalled';
    cancel.hidden = wait !== 'stalled' && wait !== 'queued';
    exits.hidden = cancel.hidden;
    retry.setAttribute('aria-label', `Retry ${name}`);
    cancel.setAttribute('aria-label', `Cancel ${name}`);
    if ((active === retry || active === cancel) && active.hidden) {
      if (!cancel.hidden) cancel.focus();
      else if (cell) this.#focus(cell);
      else if (this.#current) this.#find(this.#current.row, this.#current.column)?.td.focus();
    }
  }

  // One cell has the Tab stop. The arrow keys, Home and End move it.
  #focus(cell) {
    const old = this.#current && this.#find(this.#current.row, this.#current.column);
    if (old) old.td.setAttribute('tabindex', '-1');
    cell.td.setAttribute('tabindex', '0');
    this.#current = { row: cell.row.key, column: cell.column.key };
    cell.td.focus();
    this.#reveal(cell.td);
  }

  // The fixed headers can cover a cell after the browser scrolls to it. Move the cell out from below them.
  #reveal(td) {
    const area = this.#scroll;
    const corner = area.querySelector('thead td').getBoundingClientRect();
    const box = td.getBoundingClientRect();
    if (box.left < corner.right) area.scrollLeft -= corner.right - box.left;
    if (box.top < corner.bottom) area.scrollTop -= corner.bottom - box.top;
  }

  #focused(event) {
    const cell = this.#cellOf(event.target);
    if (!cell || event.target !== cell.td) return;
    const old = this.#current && this.#find(this.#current.row, this.#current.column);
    if (old && old !== cell) old.td.setAttribute('tabindex', '-1');
    cell.td.setAttribute('tabindex', '0');
    this.#current = { row: cell.row.key, column: cell.column.key };
  }

  #key(event) {
    if (this.#editing && event.target === this.#editing.input) {
      this.#editorKey(event);
      return;
    }
    const cell = this.#cellOf(event.target);
    if (!cell || event.target !== cell.td) return;
    const row = this.#rows.indexOf(cell.row);
    const column = this.#columns.indexOf(cell.column);
    const last = [this.#rows.length - 1, this.#columns.length - 1];
    let next = null;
    if (Object.hasOwn(MOVES, event.key)) next = [row + MOVES[event.key][0], column + MOVES[event.key][1]];
    else if (event.key === 'Home') next = [event.ctrlKey ? 0 : row, 0];
    else if (event.key === 'End') next = [event.ctrlKey ? last[0] : row, last[1]];
    if (next) {
      event.preventDefault();
      const target = this.#rows[Math.min(Math.max(next[0], 0), last[0])].cells[Math.min(Math.max(next[1], 0), last[1])];
      if (target !== cell) this.#focus(target);
      return;
    }
    // Enter opens the editor with the value. A typed character opens the editor with that character.
    const typed = event.key.length === 1 && !event.ctrlKey && !event.metaKey && !event.altKey;
    if (event.key !== 'Enter' && !typed) return;
    event.preventDefault();
    this.#open(cell, typed ? event.key : undefined);
  }

  #editorKey(event) {
    const { cell, input } = this.#editing;
    if (event.key === 'Enter') {
      event.preventDefault();
      this.#commit(true);
    } else if (event.key === 'Escape') {
      event.preventDefault();
      event.stopPropagation();
      this.#editing = null;
      this.#paint(cell);
      this.#say('');
      cell.td.focus();
    } else if (cell.column.type === 'choice' && (event.key === 'ArrowDown' || event.key === 'ArrowUp')) {
      // The arrow keys give the next or the previous choice.
      event.preventDefault();
      const choices = cell.column.choices;
      // The text of the editor gives the same choice here as it gives at the save.
      const index = choices.indexOf(parse(cell.column, input.value).value);
      const step = event.key === 'ArrowDown' ? 1 : -1;
      input.value = choices[index < 0 ? (step > 0 ? 0 : choices.length - 1) : (index + step + choices.length) % choices.length];
    }
  }

  #open(cell, text) {
    if (this.#editing) return;
    if (!cell.editable) {
      this.#say(READ_ONLY);
      return;
    }
    const input = el('input', { type: 'text', class: 'editor', part: 'editor', autocomplete: 'off',
      'aria-label': `${cell.row.label}, ${cell.column.label}`, 'aria-describedby': 'status' });
    input.maxLength = MAX_INPUT;
    input.value = text ?? String(this.#shown(cell));
    this.#editing = { cell, input };
    cell.td.replaceChildren(input);
    this.#say(cell.column.type === 'choice' ? `Choices: ${cell.column.choices.join(', ')}.` : '');
    input.focus();
    if (text === undefined) input.select();
  }

  // Saves the value of the editor. With stay, a value that is not valid keeps the editor open and shows the
  // reason. Without stay, the focus left the editor: a value that is not valid is not saved.
  #commit(stay) {
    const { cell, input } = this.#editing;
    const result = parse(cell.column, input.value);
    if (result.reason !== undefined && stay) {
      input.setAttribute('aria-invalid', 'true');
      this.#say(result.reason, 'error');
      return;
    }
    this.#editing = null;
    if (result.reason !== undefined) {
      this.#paint(cell);
      this.#say('Not saved. ' + result.reason, 'error');
      return;
    }
    this.#say('');
    this.#save(cell, result.value);
    this.#paint(cell);
    if (stay) cell.td.focus();
    this.#flush();
    this.#sync();
  }

  #save(cell, value) {
    const change = this.#changes.get(cell.id);
    const base = this.#base(cell);
    // A save of the same value sends the change again after a rejection or a cancel.
    if (value === (change ? change.value : base) && change?.state !== 'error' && !change?.canceled) return;
    // A change back to the confirmed value needs no request. But while a request of this cell is pending,
    // its receipt can confirm a different value. Then the change stays until that receipt comes back.
    if (value === base && this.#sent?.id !== cell.id) {
      this.#changes.delete(cell.id);
      return;
    }
    this.#changes.set(cell.id, { id: cell.id, row: cell.row.key, column: cell.column.key, type: cell.column.type, value, state: 'changed' });
  }

  // Sends the oldest change that has no request. One request is pending at a time.
  #flush() {
    if (!this.fill || this.#sent || this.#action.state !== 'default') return;
    for (const change of this.#changes.values()) {
      if (change.state !== 'changed' || change.canceled) continue;
      const request = this.#action.start({ row: change.row, column: change.column, value: change.value });
      if (!request) return;
      change.state = 'pending';
      this.#sent = change;
      const cell = this.#find(change.row, change.column);
      if (cell) this.#paint(cell);
      this.#send(request);
      return;
    }
  }

  #send(request) {
    this.#sync();
    this.#updatePending();
    this.dispatchEvent(new CustomEvent('mu-action', { bubbles: true, composed: true, detail: request }));
  }
}
