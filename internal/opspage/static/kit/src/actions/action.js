// The action path of the kit. An action is a tool name and its arguments. The application binds them in code.
// A fill never selects the tool. If the binding has a confirm step, the action starts only after a person confirms.
// The state is pending until a receipt of the action comes back. An action has one key for all its tries, and each
// try has a new request id. A pending action with no receipt has an exit: a retry or a cancel. It never shows done.
// docs/contracts/offline-queue.md gives the rules.
import actionButton from '../../catalog/action-button.json' with { type: 'json' };
import { copyArgs, copyValue, isTextList, receiptOf } from '../offline/queue.js';
import { isOrderedPair, sameValue } from '../render/range.js';
import { RICH_TEXT_SCHEMES, richTextValueErrors, sameRichTextValue } from '../render/rich-text.js';
import { hasTimeZoneForm } from '../render/validate.js';

const TOOL = /^[a-z][a-z0-9_]*(\.[a-z][a-z0-9_]*)*$/;
const MAX_MESSAGE = 200;
const FIELD_NAME = /^[a-z][a-z0-9]*(_[a-z0-9]+)*$/;
// A form card has up to 10 sections of 40 fields. The binding accepts an editable name for each of them.
const MAX_EDITABLE = 400;
const MAX_LABEL = 80;
// The editable-form contract gives a title of 120 characters at most.
const MAX_TITLE = 120;
// The action-button contract gives a description of 200 characters at most.
const MAX_DESCRIPTION = 200;
const ACTION_ICONS = [...actionButton.data.icon.values];
const ACTION_DISPLAYS = [...actionButton.data.display.values];
// A confirm step has a message of 200 characters at most and a button label of 40 characters at most.
const MAX_CONFIRM_MESSAGE = 200;
const MAX_CONFIRM_LABEL = 40;
// The edit types of a typed field (catalog/README.md, "Typed edits"). An actor id has 128 characters at most.
const EDIT_TYPES = new Set(['judgment', 'correction', 'override']);
const MAX_ACTOR_ID = 128;
// The detail of a pending action: a try is in flight, the action waits for a connection, or a try got no receipt.
const WAITS = Object.freeze({ sent: '', queued: 'Waiting for a connection.', stalled: 'No receipt came back.' });
const CANCELED = 'Canceled. The request did not leave this device.';
const CANCELED_UNKNOWN = 'Canceled. No receipt came back, so it is possible that the service did the action.';
const UNCONFIRMED = 'The request did not leave this device. Confirm the action and try again.';
const ISO_TIME = /^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}\.\d{3}Z$/;

// Returns true if the text has more than max characters. The loop stops at max + 1, so a very long text
// does not make a copy of all its characters.
function longer(text, max) {
  let count = 0;
  for (const character of text) {
    count += 1;
    if (count > max) return true;
  }
  return false;
}

// A failed call or an invalid or refused result leaves the outcome unknown.
export async function deliverReceipt(element, request, send) {
  try {
    const receipt = receiptOf(await send(), request);
    const state = element.action?.state;
    if (typeof state === 'string' && state !== 'pending') return;
    if (receipt) {
      if (receipt.id === undefined) receipt.id = request.id;
      if (element.receipt(receipt) !== false) return;
    }
  } catch {
    // No receipt came back that the element could accept.
  }
  element.noReceipt?.(request.id);
}

// Returns the first max characters of the text, with no copy of the remaining characters.
function first(text, max) {
  let start = '';
  let count = 0;
  for (const character of text) {
    count += 1;
    if (count > max) break;
    start += character;
  }
  return start;
}

function text(value, max) {
  return typeof value === 'string' && value.length > 0 && !longer(value, max);
}

function scalar(value) {
  return typeof value === 'string' || typeof value === 'boolean' || (typeof value === 'number' && Number.isFinite(value));
}

export function isActionValue(value) {
  return scalar(value) || isOrderedPair(value) || isTextList(value)
    || (value?.component === 'rich-text' && richTextValueErrors(value).length === 0);
}

export { copyValue as copyActionValue };

export function isEditType(value) {
  return EDIT_TYPES.has(value);
}

function isKnownTimeZone(name) {
  if (!hasTimeZoneForm(name)) return false;
  try {
    new Intl.DateTimeFormat(undefined, { timeZone: name });
    return true;
  } catch {
    return false;
  }
}

export function bindingErrors(binding) {
  if (binding === null || typeof binding !== 'object' || Array.isArray(binding)) return ['the action must be an object'];
  const errors = [];
  if (typeof binding.tool !== 'string' || binding.tool.length > 80 || !TOOL.test(binding.tool)) errors.push('the tool name is not valid');
  const args = binding.arguments ?? {};
  if (args === null || typeof args !== 'object' || Array.isArray(args)) errors.push('the arguments must be an object');
  else if (!Object.values(args).every((value) => scalar(value) || isTextList(value))) {
    errors.push('each bound argument must be text, a finite number, true or false, or a list of texts');
  }
  // The application gives the label of the action, so a fill cannot show a different meaning for the tool.
  if (binding.label !== undefined && (typeof binding.label !== 'string' || binding.label.length === 0
    || longer(binding.label, MAX_LABEL))) errors.push('the label must be a text of 1 to 80 characters');
  // The application gives the title of a form, so a fill cannot show a different target for the bound arguments.
  if (binding.title !== undefined && (typeof binding.title !== 'string' || binding.title.length === 0
    || longer(binding.title, MAX_TITLE))) errors.push('the title must be a text of 1 to 120 characters');
  // The application gives the description of an action, so a fill cannot describe a different target.
  if (binding.description !== undefined && (typeof binding.description !== 'string' || binding.description.length === 0
    || longer(binding.description, MAX_DESCRIPTION))) errors.push('the description must be a text of 1 to 200 characters');
  if (binding.icon !== undefined && !ACTION_ICONS.includes(binding.icon)) {
    errors.push(`the icon must be one of ${ACTION_ICONS.join(', ')}`);
  }
  if (binding.display !== undefined && !ACTION_DISPLAYS.includes(binding.display)) {
    errors.push(`the display must be one of ${ACTION_DISPLAYS.join(', ')}`);
  }
  // The application gives the confirm step, so a fill cannot remove it or change its words.
  if (binding.confirm !== undefined) {
    const confirm = binding.confirm;
    const shaped = confirm !== null && typeof confirm === 'object' && !Array.isArray(confirm)
      && Object.keys(confirm).every((key) => key === 'message' || key === 'label');
    if (!shaped || !text(confirm.message, MAX_CONFIRM_MESSAGE) || !text(confirm.label, MAX_CONFIRM_LABEL)) {
      errors.push('confirm must have only a message of 1 to 200 characters and a label of 1 to 40 characters');
    }
  }
  // The application gives the label of each editable field, so a fill cannot change what a field means.
  const editable = binding.editable ?? {};
  const entries = editable !== null && typeof editable === 'object' && !Array.isArray(editable) ? Object.entries(editable) : null;
  if (!entries || entries.length > MAX_EDITABLE || !entries.every(([name, label]) => FIELD_NAME.test(name)
    && typeof label === 'string' && label.length > 0 && !longer(label, MAX_LABEL))) errors.push('editable must map each field name to its label');
  // A form shows the editable value, so a bound value for the same name would go in the request unseen.
  const overlap = entries && args !== null && typeof args === 'object' ? entries.find(([name]) => Object.hasOwn(args, name)) : null;
  if (overlap) errors.push(`a bound argument cannot be editable: ${overlap[0]}`);
  const names = entries ? new Set(entries.map(([name]) => name)) : new Set();
  const zones = binding.time_zones;
  if (zones !== undefined && (zones === null || typeof zones !== 'object' || Array.isArray(zones)
    || ![Object.prototype, null].includes(Object.getPrototypeOf(zones))
    || !Object.entries(zones).every(([name, zone]) => names.has(name) && isKnownTimeZone(zone)))) {
    errors.push('time_zones must map editable field names to IANA time zone names');
  }
  // The application gives the typed names and the person who edits, so a fill cannot remove the type of an edit.
  const typed = binding.typed ?? [];
  if (!Array.isArray(typed) || new Set(typed).size !== typed.length || !typed.every((name) => names.has(name))) {
    errors.push('typed must list names that editable gives, each one time');
  }
  const editor = binding.editor;
  const person = editor !== null && typeof editor === 'object' && !Array.isArray(editor)
    && Object.keys(editor).every((key) => key === 'type' || key === 'id') && editor.type === 'human' && text(editor.id, MAX_ACTOR_ID);
  if ((editor !== undefined || (Array.isArray(typed) && typed.length > 0)) && !person) {
    errors.push('editor must be a person: { type: human, id } with an id of 1 to 128 characters');
  }
  return errors;
}

// Returns the frozen edits of a request, or null if an edit is not valid. Each edit names a typed field
// that the request sends, one time, with a type, the old value and the time. The new value comes from the
// request and the actor from the binding, so an edit cannot name a different value or a different person.
function editsOf(edits, binding, values) {
  if (!Array.isArray(edits)) return null;
  const seen = new Set();
  const result = [];
  for (const edit of edits) {
    if (edit === null || typeof edit !== 'object' || Array.isArray(edit)) return null;
    if (!Object.keys(edit).every((key) => ['field', 'type', 'old_value', 'time'].includes(key))) return null;
    const { field, type, old_value: old, time } = edit;
    if (!binding.typed.includes(field) || seen.has(field) || !Object.hasOwn(values, field)) return null;
    const next = values[field];
    if (!isEditType(type) || sameValue(old, next)) return null;
    if (Array.isArray(old) || Array.isArray(next)) {
      const pair = isOrderedPair(old) && isOrderedPair(next);
      const texts = isTextList(old) && isTextList(next);
      if (!pair && !texts) return null;
      if (texts && old.length === next.length && next.every((value, index) => value === old[index])) return null;
    } else if (old?.component === 'rich-text' || next?.component === 'rich-text') {
      if (richTextValueErrors(old, RICH_TEXT_SCHEMES).length || next?.component !== 'rich-text' || sameRichTextValue(old, next)) return null;
    } else if (!scalar(old)) return null;
    if (typeof time !== 'string' || !ISO_TIME.test(time) || Number.isNaN(Date.parse(time))) return null;
    seen.add(field);
    result.push(Object.freeze({ field, type, old_value: copyValue(old), new_value: next, actor: binding.editor, time }));
  }
  return Object.freeze(result);
}

export class ActionState {
  #binding = null;
  #request = null;
  #state = 'disabled';
  #message = '';
  #reference = '';
  // The detail of the pending state, the ids of the tries of the pending action and the number of tries that
  // left the device.
  #wait = '';
  #ids = new Set();
  #tries = 0;

  get state() {
    return this.#state;
  }

  get message() {
    return this.#message;
  }

  // The detail of a pending action: sent, queued or stalled. If the action is not pending, the value is an empty text.
  get wait() {
    return this.#state === 'pending' ? this.#wait : '';
  }

  // The key of the pending action. It stays the same for each try. If the action is not pending, the value is an empty text.
  get key() {
    return this.#state === 'pending' && this.#request ? this.#request.key : '';
  }

  // The reference of the last receipt, for example a record id. The element shows it only as text.
  get reference() {
    return this.#reference;
  }

  // The confirm step of the binding, as { message, label }. Without a binding or a confirm step, the value is null.
  get confirm() {
    return this.#binding ? this.#binding.confirm : null;
  }

  // The label of the action. The application gives it in the binding. A binding with no label gives an empty
  // text. Without a binding, the value is null.
  get label() {
    return this.#binding ? this.#binding.label : null;
  }

  // The title of a form. The application gives it in the binding. A binding with no title gives an empty
  // text. Without a binding, the value is null.
  get title() {
    return this.#binding ? this.#binding.title : null;
  }

  // The description of an action button. The application gives it in the binding. A binding with no
  // description gives an empty text. Without a binding, the value is null.
  get description() {
    return this.#binding ? this.#binding.description : null;
  }

  // The application gives the icon and display. If the binding does not give a value, it is undefined.
  get icon() {
    return this.#binding?.icon;
  }

  get display() {
    return this.#binding?.display;
  }

  // The bound arguments that each request sends. The application gives them in the binding. Without a binding,
  // the value is null.
  get arguments() {
    return this.#binding ? this.#binding.arguments : null;
  }

  // The names of the extra values that a request can carry, each with its label. The application gives
  // them in the binding. Without a binding, the value is null.
  get editable() {
    return this.#binding ? this.#binding.editable : null;
  }

  // The time zones of editable fields. Without a binding, the value is null.
  get timeZones() {
    return this.#binding ? this.#binding.time_zones : null;
  }

  // The editable names that need an edit type. Without a binding, the value is null.
  get typed() {
    return this.#binding ? this.#binding.typed : null;
  }

  // The person who edits a typed form, as { type, id }. Without a binding or an editor, the value is null.
  get editor() {
    return this.#binding ? this.#binding.editor : null;
  }

  bind(binding) {
    if (binding === null) {
      this.#binding = null;
      this.#clear();
      this.#state = 'disabled';
      this.#message = '';
      this.#reference = '';
      return [];
    }
    const errors = bindingErrors(binding);
    if (errors.length) return errors;
    this.#binding = Object.freeze({ tool: binding.tool, label: binding.label ?? '', title: binding.title ?? '',
      description: binding.description ?? '', icon: binding.icon, display: binding.display,
      arguments: copyArgs(binding.arguments ?? {}),
      editable: Object.freeze(Object.assign(Object.create(null), binding.editable ?? {})),
      time_zones: Object.freeze(Object.assign(Object.create(null), binding.time_zones ?? {})),
      typed: Object.freeze([...(binding.typed ?? [])]),
      editor: binding.editor ? Object.freeze({ type: binding.editor.type, id: binding.editor.id }) : null,
      confirm: binding.confirm ? Object.freeze({ message: binding.confirm.message, label: binding.confirm.label }) : null });
    this.#clear();
    this.#state = 'default';
    this.#message = '';
    this.#reference = '';
    return [];
  }

  #clear() {
    this.#request = null;
    this.#wait = '';
    this.#ids.clear();
    this.#tries = 0;
  }

  // Returns a request, or null if the action cannot start now. The request has a new key and a new id. The extra values come from a fill,
  // so they cannot change an argument that the application binds: the bound arguments come last.
  // Each extra value needs a name that the binding gives as editable. If the binding has a confirm step,
  // the action starts only when confirmed is true, after a person confirms. If the binding has typed names,
  // the request needs a list of edits, and the request carries them with the new values and the editor.
  start(extra = {}, confirmed = false, edits = undefined) {
    if (!this.#binding || this.#state === 'pending' || this.#state === 'done') return null;
    if (this.#binding.confirm && confirmed !== true) {
      this.#state = 'error';
      this.#message = UNCONFIRMED;
      this.#reference = '';
      return null;
    }
    if (Object.keys(extra).some((name) => !Object.hasOwn(this.#binding.editable, name))) return null;
    if (!Object.values(extra).every(isActionValue)) return null;
    const values = Object.fromEntries(Object.entries(extra).map(([name, value]) => [name, copyValue(value)]));
    const typed = this.#binding.typed.length > 0;
    if (!typed && edits !== undefined && !(Array.isArray(edits) && edits.length === 0)) return null;
    const checked = typed ? editsOf(edits, this.#binding, values) : null;
    if (typed && !checked) return null;
    this.#clear();
    this.#request = Object.freeze({ id: crypto.randomUUID(), key: crypto.randomUUID(), tool: this.#binding.tool,
      arguments: Object.freeze({ ...values, ...this.#binding.arguments }), ...(typed ? { edits: checked } : {}) });
    this.#ids.add(this.#request.id);
    this.#state = 'pending';
    this.#wait = 'sent';
    this.#tries = 1;
    this.#message = '';
    this.#reference = '';
    return this.#request;
  }

  // Sets the detail of the pending action: { key, wait, tries, message }. The key must be the key of the pending
  // action, and wait is sent, queued or stalled. tries is the number of tries that left the device. Returns true
  // if the detail changed the state. The state stays pending.
  hold(update) {
    if (this.#state !== 'pending' || update === null || typeof update !== 'object') return false;
    const { key, wait, tries, message } = update;
    if (key !== this.#request.key || !Object.hasOwn(WAITS, wait)) return false;
    if (tries !== undefined && !(Number.isInteger(tries) && tries >= 0)) return false;
    if (message !== undefined && typeof message !== 'string') return false;
    this.#wait = wait;
    if (tries !== undefined) this.#tries = tries;
    this.#message = message ? first(message, MAX_MESSAGE) : WAITS[wait];
    return true;
  }

  // Only the current try can stall the action. A late failure of an earlier try changes nothing.
  noReceipt(id) {
    if (this.#state !== 'pending' || this.#wait !== 'sent' || id !== this.#request.id) return false;
    return this.hold({ key: this.#request.key, wait: 'stalled' });
  }

  // Returns the request of a new try of a stalled action, or null. The key stays the same, and the id is new.
  retry() {
    if (this.#state !== 'pending' || this.#wait !== 'stalled') return null;
    this.#request = Object.freeze({ ...this.#request, id: crypto.randomUUID() });
    this.#ids.add(this.#request.id);
    this.#wait = 'sent';
    this.#tries += 1;
    this.#message = '';
    return this.#request;
  }

  // Cancels a pending action that waits for a connection or that got no receipt. An action with a try in flight
  // cannot cancel. The action can start again. After a try, the kit does not know if the service did the action,
  // and the message says so.
  cancel() {
    if (this.#state !== 'pending' || (this.#wait !== 'queued' && this.#wait !== 'stalled')) return false;
    const sent = this.#tries > 0;
    this.#clear();
    this.#state = 'default';
    this.#message = sent ? CANCELED_UNKNOWN : CANCELED;
    this.#reference = '';
    return true;
  }

  // After done or error, the action can start again. The message says why. A pending action cannot reopen,
  // because its receipt has not come back.
  reopen(message = '') {
    if (this.#state !== 'done' && this.#state !== 'error') return false;
    this.#state = 'default';
    this.#message = first(String(message), MAX_MESSAGE);
    this.#reference = '';
    return true;
  }

  // A receipt must match the key of the action, the id of one of its tries, or both when both are present.
  receipt(value) {
    if (this.#state !== 'pending') return false;
    const receipt = receiptOf(value, this.#request, this.#ids);
    if (!receipt || (receipt.key === undefined && receipt.id === undefined)) return false;
    this.#state = receipt.ok ? 'done' : 'error';
    this.#message = receipt.message ?? '';
    this.#reference = receipt.reference ?? '';
    this.#clear();
    return true;
  }
}
