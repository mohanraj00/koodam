// An upload control (catalog/upload.json). A person picks or drops files. Page code binds the tool, the
// accepted types, the size limit and the file count. A fill cannot set them. The control checks each file
// from its name, its type and its size only: it never reads the content of a file and shows no preview.
// It sends no data. The application sends each file with its own transport and gives the receipt.
// src/components/upload.md gives the rules.
import { MuElement } from '../primitives/base.js';
import { ActionState, bindingErrors } from '../actions/action.js';
import { el, sheet } from '../render/dom.js';
import { PATIENCE, patienceOf, ReceiptTimer } from './pending.js';
import { ACTION_RESULT_CSS, REFERENCE_CSS, showReference } from './receipt.js';

const KEYS = new Set(['tool', 'arguments', 'label', 'accept', 'max_size', 'max_files']);
// The values of a file that each request sends. A bound argument cannot have one of these names.
const SENT = Object.freeze(['name', 'size', 'type']);
const MIME = /^[a-z0-9][a-z0-9!#$&^_.+-]{0,62}\/[a-z0-9][a-z0-9!#$&^_.+-]{0,62}$/;
const EXTENSION = /^\.[a-z0-9]{1,16}$/;
const MAX_ACCEPT = 20;
const MAX_FILES = 20;
const MAX_SIZE = 1000000000;
// The text of each file state. Done shows only after a receipt with ok.
const STATES = Object.freeze({ pending: 'Pending', uploading: 'Uploading', stalled: 'Pending', done: 'Done', error: 'Not done' });
const UNAVAILABLE = 'This upload is not available.';

const integer = (value, max) => Number.isInteger(value) && value >= 1 && value <= max;
const files = (count) => (count === 1 ? '1 file' : `${count} files`);

function fileState(action) {
  if (action.state === 'default') return 'pending';
  if (action.state === 'pending') return action.wait === 'stalled' ? 'stalled' : 'uploading';
  return action.state;
}

// The size of a file as text, with decimal units.
export function bytes(size) {
  if (size < 1000) return size === 1 ? '1 byte' : `${size} bytes`;
  const units = ['kB', 'MB', 'GB'];
  let value = size / 1000;
  let unit = 0;
  while (value >= 1000 && unit < units.length - 1) {
    value /= 1000;
    unit += 1;
  }
  return `${value >= 100 ? Math.round(value) : Math.round(value * 10) / 10} ${units[unit]}`;
}

function uploadErrors(binding) {
  if (binding === null || typeof binding !== 'object' || Array.isArray(binding)) return ['the action must be an object'];
  if (!Object.keys(binding).every((key) => KEYS.has(key))) return ['an upload accepts only tool, arguments, label, accept, max_size and max_files'];
  const errors = bindingErrors({ tool: binding.tool, arguments: binding.arguments, label: binding.label });
  // The tool and the bound arguments select a target that a person cannot see. The application names it.
  if (binding.label === undefined) errors.push('the label is necessary, because it names the target of the upload');
  const args = binding.arguments;
  const taken = args !== null && typeof args === 'object' ? SENT.find((name) => Object.hasOwn(args, name)) : undefined;
  if (taken) errors.push(`a bound argument cannot have the name of a file value: ${taken}`);
  const accept = binding.accept;
  if (!Array.isArray(accept) || accept.length < 1 || accept.length > MAX_ACCEPT || new Set(accept).size !== accept.length
    || !accept.every((item) => typeof item === 'string' && (MIME.test(item) || EXTENSION.test(item)))) {
    errors.push('accept must list 1 to 20 media types or file extensions in lower case, each one time');
  }
  if (!integer(binding.max_size, MAX_SIZE)) errors.push('max_size must be an integer from 1 to 1000000000');
  if (!integer(binding.max_files, MAX_FILES)) errors.push('max_files must be an integer from 1 to 20');
  return errors;
}

// A file agrees with the accepted types if its media type is in the list, and its name ends with an
// extension of the list. Each rule applies only if the list has an entry of that kind.
function accepted(file, accept) {
  const types = accept.filter((item) => !item.startsWith('.'));
  const extensions = accept.filter((item) => item.startsWith('.'));
  const name = file.name.toLowerCase();
  return (types.length === 0 || types.includes(file.type.toLowerCase()))
    && (extensions.length === 0 || extensions.some((item) => name.length > item.length && name.endsWith(item)));
}

export class MuUpload extends MuElement {
  static component = 'upload';
  static styles = [sheet(`
    :host { display: block; min-width: 0; }
    .upload { color: var(--mu-color-text); font: var(--mu-type-body); display: flex; flex-direction: column; gap: var(--mu-space-2); }
    p, ul { margin: 0; padding: 0; overflow-wrap: anywhere; }
    ul { list-style: none; display: flex; flex-direction: column; gap: var(--mu-space-2); }
    .title { font-weight: var(--mu-type-weight-strong); }
    .muted { font: var(--mu-type-caption); color: var(--mu-color-text-muted); }
    .drop {
      display: flex; flex-wrap: wrap; align-items: center; gap: var(--mu-space-2); padding: var(--mu-space-3);
      background: var(--mu-color-surface); border: 2px dashed var(--mu-color-border); border-radius: var(--mu-radius-2);
    }
    .drop.over { border-style: solid; border-color: var(--mu-color-accent); }
    button {
      font: var(--mu-type-body); min-height: 2.75rem; min-width: 2.75rem; padding: var(--mu-space-2) var(--mu-space-3);
      border-radius: var(--mu-radius-2); border: 1px solid var(--mu-color-accent); cursor: pointer;
      background: transparent; color: var(--mu-color-accent);
    }
    button[part="send"] { background: var(--mu-color-accent); color: var(--mu-color-on-accent); align-self: flex-start; }
    button[hidden] { display: none; }
    .file {
      display: grid; grid-template-columns: 1fr auto; align-items: center; gap: var(--mu-space-1) var(--mu-space-2);
      padding: var(--mu-space-2); border: 1px solid var(--mu-color-border); border-radius: var(--mu-radius-1);
    }
    .file > span { grid-column: 1; min-width: 0; overflow-wrap: anywhere; }
    .file-controls { grid-column: 2; grid-row: 1 / span 3; display: flex; flex-wrap: wrap; gap: var(--mu-space-2); }
    .file[data-state="stalled"] .file-controls { grid-column: 1 / -1; grid-row: auto; }
    .file-controls[hidden] { display: none; }
    .state { font-weight: var(--mu-type-weight-strong); }
    .file[data-state="done"] { border-color: var(--mu-color-success); }
    .file[data-state="done"] .state, .notes .done { color: var(--mu-color-success); }
    .file[data-state="error"] .state, .problems { color: var(--mu-color-danger); }
    .notes { display: flex; flex-direction: column; gap: var(--mu-space-1); font: var(--mu-type-caption); min-height: 1.5em; }
    @media (forced-colors: active) {
      .drop, .file, .file[data-state="done"] { border-color: CanvasText; }
      .drop.over { border-color: Highlight; }
      .muted, .problems, .file[data-state="error"] .state, .file[data-state="done"] .state, .notes .done { color: CanvasText; }
      button, button[part="send"] { background: ButtonFace; color: ButtonText; border-color: ButtonText; forced-color-adjust: none; }
    }
  `), REFERENCE_CSS, ACTION_RESULT_CSS];

  #binding = null;
  // The files of the list. Each entry has the file, its state and the nodes of its row.
  #files = [];
  // The files that the last pick or drop refused, each with its reason.
  #problems = [];
  #view = Object.freeze(Object.defineProperty({}, 'state', { get: () => this.#actionState(), enumerable: true }));
  #nodes = null;
  #patience = PATIENCE;

  // The state of the upload can only be read: disabled with no binding, pending while a file waits for its
  // receipt, and default.
  get action() {
    return this.#view;
  }

  // The application binds the tool, its fixed arguments, the label and the limits. A fill cannot set them.
  set action(binding) {
    if (binding !== null) {
      const errors = uploadErrors(binding);
      if (errors.length) throw new TypeError('action: ' + errors.join('; '));
    }
    this.#binding = binding === null ? null : Object.freeze({ tool: binding.tool, label: binding.label,
      arguments: Object.freeze({ ...(binding.arguments ?? {}) }), accept: Object.freeze([...binding.accept]),
      max_size: binding.max_size, max_files: binding.max_files });
    // A new binding is a new target with new limits, so no file of the old binding stays.
    for (const entry of this.#files) entry.timer.clear();
    this.#files = [];
    this.#problems = [];
    if (this.fill) this.fill = this.fill;
  }

  #actionState() {
    if (!this.#binding) return 'disabled';
    return this.#files.some(({ action }) => action.state === 'pending') ? 'pending' : 'default';
  }

  get patience() {
    return this.#patience;
  }

  set patience(value) {
    this.#patience = patienceOf(value);
  }

  // Returns true if the receipt belongs to a file that waits for it. Only a receipt with ok shows Done.
  receipt(value) {
    const entry = this.#files.find(({ action }) => action.receipt(value));
    if (!entry) return false;
    entry.timer.clear();
    const { state, message, reference } = entry.action;
    this.#refresh();
    this.dispatchEvent(new CustomEvent('mu-receipt', { bubbles: true, composed: true,
      detail: Object.freeze({ id: entry.id, name: entry.file.name, size: entry.file.size, ok: state === 'done', message, reference }) }));
    return true;
  }

  // The fill can repeat the limits, but only the values that the page binds are valid.
  check(data) {
    const binding = this.#binding;
    if (!binding) return [];
    if (data.label !== binding.label) return ['data.label: the label is not the label of the application'];
    const accept = data.accept;
    if (accept !== undefined && (accept.length !== binding.accept.length || !accept.every((item) => binding.accept.includes(item))
      || !binding.accept.every((item) => accept.includes(item)))) {
      return ['data.accept: the accepted types are not the types that the page binds'];
    }
    if (data.max_size !== undefined && data.max_size !== binding.max_size) return ['data.max_size: the size limit is not the limit that the page binds'];
    if (data.max_files !== undefined && data.max_files !== binding.max_files) return ['data.max_files: the file count is not the count that the page binds'];
    return [];
  }

  state() {
    if (!this.#binding) return 'disabled';
    const states = this.#files.map(({ action }) => action.state);
    if (states.includes('pending')) return 'pending';
    if (states.includes('error')) return 'error';
    return states.length > 0 && states.every((item) => item === 'done') ? 'done' : 'default';
  }

  render(data) {
    const binding = this.#binding;
    // New data keeps the focus on the same control.
    const active = this.shadowRoot.activeElement;
    const held = active && this.#files.find((entry) => entry.controls.contains(active));
    const heldPart = active?.getAttribute('part');
    const input = el('input', { type: 'file' });
    input.hidden = true;
    input.multiple = Boolean(binding && binding.max_files > 1);
    if (binding) input.accept = binding.accept.join(',');
    const choose = el('button', { type: 'button', part: 'choose' }, 'Choose files');
    const drop = el('div', { class: 'drop', part: 'drop' }, choose, el('span', {}, 'or drop files here'), input);
    const send = el('button', { type: 'button', part: 'send' }, 'Upload');
    const list = el('ul', { 'aria-label': 'Files', part: 'files' });
    const summary = el('p');
    const problems = el('ul', { class: 'problems', part: 'problems' });
    this.#nodes = { choose, send, list, summary, problems };
    const limits = binding
      ? `Accepted types: ${binding.accept.join(', ')}. Each file: ${bytes(binding.max_size)} maximum. ${files(binding.max_files)} maximum.`
      : UNAVAILABLE;
    // The picker opens only for a press of a person. The browser gives the files to the change event.
    choose.addEventListener('click', () => {
      if (this.#binding && this.dataset.state !== 'refused') input.click();
    });
    input.addEventListener('change', () => {
      this.#take(input.files);
      input.value = '';
    });
    drop.addEventListener('dragover', (event) => {
      // The control always takes the drop, so the browser does not open the file. With no binding it takes no file.
      event.preventDefault();
      const bound = this.#binding !== null;
      if (event.dataTransfer) event.dataTransfer.dropEffect = bound ? 'copy' : 'none';
      drop.classList.toggle('over', bound);
    });
    drop.addEventListener('dragleave', () => drop.classList.remove('over'));
    drop.addEventListener('drop', (event) => {
      event.preventDefault();
      drop.classList.remove('over');
      this.#take(event.dataTransfer?.files ?? []);
    });
    send.addEventListener('click', () => this.#send());
    for (const entry of this.#files) list.append(this.#row(entry));
    this.#refresh();
    if (active) {
      queueMicrotask(() => {
        const node = held ? held[heldPart] : heldPart === 'send' ? send : choose;
        if (!choose.isConnected) return;
        (node?.isConnected && !node.hidden ? node : choose).focus();
      });
    }
    const author = this.fill.slots.authorship;
    const name = author.name ? ` (${author.name})` : '';
    return [el('div', { class: 'upload', role: 'group', 'aria-label': data.label, part: 'upload' },
      el('p', { class: 'title', part: 'label' }, data.label),
      data.help ? el('p', { part: 'help' }, data.help) : null,
      el('p', { class: 'muted', part: 'limits' }, limits), drop,
      el('div', { class: 'notes', role: 'status', 'aria-live': 'polite', part: 'status' }, summary, problems),
      list, send, el('p', { class: 'muted', part: 'authorship' }, `Author: ${author.type} ${author.id}${name}`))];
  }

  // The reason that the control refuses a file, from its name, its type and its size. It reads no content.
  #reason(file) {
    const { accept, max_size: size, max_files: count } = this.#binding;
    if (this.#files.length >= count) return `Too many files. The limit is ${files(count)}.`;
    if (!accepted(file, accept)) return `This file type is not accepted. Accepted types: ${accept.join(', ')}.`;
    if (file.size > size) return `This file is larger than the limit of ${bytes(size)}.`;
    if (this.#files.some((entry) => entry.file.name === file.name && entry.file.size === file.size
      && entry.file.lastModified === file.lastModified)) return 'This file is already in the list.';
    return '';
  }

  // Each check runs before a request. A refused file does not go into the list.
  #take(list) {
    if (!this.#binding || this.dataset.state === 'refused') return;
    this.#problems = [];
    for (const file of list) {
      const reason = this.#reason(file);
      if (reason) {
        this.#problems.push({ name: file.name, reason });
        continue;
      }
      const action = new ActionState();
      const { tool, arguments: args } = this.#binding;
      action.bind({ tool, arguments: args, editable: { name: 'Name', size: 'Size', type: 'Type' } });
      const entry = { file, action, timer: new ReceiptTimer() };
      this.#files.push(entry);
      this.#nodes.list.append(this.#row(entry));
    }
    this.#refresh();
  }

  #row(entry) {
    const remove = el('button', { type: 'button', part: 'remove', 'aria-label': `Remove ${entry.file.name}` }, 'Remove');
    const retry = el('button', { type: 'button', part: 'retry', 'aria-label': `Retry ${entry.file.name}` }, 'Retry');
    const cancel = el('button', { type: 'button', part: 'cancel', 'aria-label': `Cancel ${entry.file.name}` }, 'Cancel');
    remove.addEventListener('click', () => this.#remove(entry));
    retry.addEventListener('click', () => {
      if (!this.#files.includes(entry) || !this.fill) return;
      const request = entry.action.retry();
      if (!request) return;
      this.#refresh();
      this.#dispatch(entry, request);
    });
    cancel.addEventListener('click', () => {
      if (this.#files.includes(entry) && entry.action.cancel()) this.#refresh();
    });
    entry.status = el('span', { class: 'state', part: 'state' });
    entry.detail = el('span', { class: 'reference-row' });
    entry.remove = remove;
    entry.retry = retry;
    entry.cancel = cancel;
    entry.controls = el('div', { class: 'file-controls' }, remove, retry, cancel);
    entry.row = el('li', { class: 'file', part: 'file' }, el('span', { part: 'name' }, entry.file.name),
      el('span', { class: 'muted', part: 'size' }, bytes(entry.file.size)), entry.status, entry.detail, entry.controls);
    return entry.row;
  }

  // A person can remove a file before its upload, and a file that is not done.
  #remove(entry) {
    if (entry.action.state !== 'default' && entry.action.state !== 'error') return;
    const index = this.#files.indexOf(entry);
    if (index < 0) return;
    this.#files.splice(index, 1);
    entry.row.remove();
    this.#problems = [];
    this.#refresh();
    const next = this.#files.slice(index).concat(this.#files.slice(0, index)).find((item) => !item.remove.hidden);
    (next ? next.remove : this.#nodes.choose).focus();
  }

  // Sends one request for each file that waits. The request has the file, and the control reads no content.
  #send() {
    if (!this.#binding || this.dataset.state === 'refused') return;
    const waiting = this.#files.filter(({ action }) => action.state === 'default' || action.state === 'error');
    if (!waiting.length) return;
    const sends = waiting.map((entry) => {
      const { name, size, type } = entry.file;
      return [entry, entry.action.start({ name, size, type })];
    });
    this.#problems = [];
    this.#refresh();
    for (const [entry, request] of sends) this.#dispatch(entry, request);
  }

  #dispatch(entry, request) {
    if (!this.#files.includes(entry) || entry.action.key !== request.key) return;
    entry.id = request.id;
    entry.timer.watch(entry.action.wait, this.#patience, () => {
      if (entry.action.noReceipt(request.id)) this.#refresh();
    });
    const detail = Object.freeze({ ...request, file: entry.file });
    this.dispatchEvent(new CustomEvent('mu-upload', { bubbles: true, composed: true, detail }));
  }

  // Shows the state of each file, the summary and the reasons as text. A row keeps its nodes, so the focus stays.
  #refresh() {
    if (!this.#nodes) return;
    const { choose, send, summary, problems } = this.#nodes;
    const count = { pending: 0, uploading: 0, stalled: 0, done: 0, error: 0 };
    const active = this.shadowRoot.activeElement;
    let next = null;
    for (const entry of this.#files) {
      const state = fileState(entry.action);
      const { message, reference } = entry.action;
      count[state] += 1;
      entry.row.dataset.state = state;
      entry.status.textContent = STATES[state] + (message ? ': ' + message : '');
      showReference(entry.detail, reference);
      entry.remove.hidden = state !== 'pending' && state !== 'error';
      entry.retry.hidden = state !== 'stalled';
      entry.cancel.hidden = entry.retry.hidden;
      entry.controls.hidden = entry.remove.hidden && entry.retry.hidden;
      if (entry.controls.contains(active) && active.hidden) next = entry.remove.hidden ? send : entry.remove;
    }
    const bound = Boolean(this.#binding);
    choose.setAttribute('aria-disabled', bound ? 'false' : 'true');
    send.setAttribute('aria-disabled', bound && count.pending + count.error > 0 ? 'false' : 'true');
    const state = this.state();
    send.dataset.state = state;
    choose.dataset.state = bound ? 'default' : 'disabled';
    summary.classList.toggle('done', state === 'done');
    const words = [];
    if (count.pending) words.push(`${files(count.pending)} to upload.`);
    if (count.uploading) words.push(`Uploading ${files(count.uploading)}.`);
    if (count.stalled) words.push(`No receipt came back for ${files(count.stalled)}.`);
    if (count.done) words.push(`Done: ${files(count.done)}.`);
    if (count.error) words.push(`Not done: ${files(count.error)}.`);
    summary.textContent = words.join(' ');
    problems.replaceChildren(...this.#problems.map((item) => el('li', {}, `${item.name}: ${item.reason}`)));
    if (this.fill) this.dataset.state = state;
    next?.focus();
  }
}
