// The action path of the kit. An action is a tool name and its arguments. The application binds them in code.
// A fill never selects the tool. If the binding has a confirm step, the action starts only after a person confirms.
// The state is pending until a receipt with the same id comes back.
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
// A confirm step has a message of 200 characters at most and a button label of 40 characters at most.
const MAX_CONFIRM_MESSAGE = 200;
const MAX_CONFIRM_LABEL = 40;
// A receipt reference, for example a record id, has 80 characters at most.
const MAX_REFERENCE = 80;
// The edit types of a typed field (catalog/README.md, "Typed edits"). An actor id has 128 characters at most.
const EDIT_TYPES = new Set(['judgment', 'correction', 'override']);
const MAX_ACTOR_ID = 128;
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

export function bindingErrors(binding) {
  if (binding === null || typeof binding !== 'object' || Array.isArray(binding)) return ['the action must be an object'];
  const errors = [];
  if (typeof binding.tool !== 'string' || binding.tool.length > 80 || !TOOL.test(binding.tool)) errors.push('the tool name is not valid');
  const args = binding.arguments ?? {};
  if (args === null || typeof args !== 'object' || Array.isArray(args)) errors.push('the arguments must be an object');
  else if (!Object.values(args).every(scalar)) errors.push('each argument must be a string, a number or true or false');
  // The application gives the label of the action, so a fill cannot show a different meaning for the tool.
  if (binding.label !== undefined && (typeof binding.label !== 'string' || binding.label.length === 0
    || longer(binding.label, MAX_LABEL))) errors.push('the label must be a text of 1 to 80 characters');
  // The application gives the title of a form, so a fill cannot show a different target for the bound arguments.
  if (binding.title !== undefined && (typeof binding.title !== 'string' || binding.title.length === 0
    || longer(binding.title, MAX_TITLE))) errors.push('the title must be a text of 1 to 120 characters');
  // The application gives the description of an action, so a fill cannot describe a different target.
  if (binding.description !== undefined && (typeof binding.description !== 'string' || binding.description.length === 0
    || longer(binding.description, MAX_DESCRIPTION))) errors.push('the description must be a text of 1 to 200 characters');
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
  // The application gives the typed names and the person who edits, so a fill cannot remove the type of an edit.
  const typed = binding.typed ?? [];
  const names = entries ? new Set(entries.map(([name]) => name)) : new Set();
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
    if (!EDIT_TYPES.has(type) || !scalar(old) || old === values[field]) return null;
    if (typeof time !== 'string' || !ISO_TIME.test(time) || Number.isNaN(Date.parse(time))) return null;
    seen.add(field);
    result.push(Object.freeze({ field, type, old_value: old, new_value: values[field], actor: binding.editor, time }));
  }
  return Object.freeze(result);
}

export class ActionState {
  #binding = null;
  #request = null;
  #state = 'disabled';
  #message = '';
  #reference = '';

  get state() {
    return this.#state;
  }

  get message() {
    return this.#message;
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
      this.#request = null;
      this.#state = 'disabled';
      this.#message = '';
      this.#reference = '';
      return [];
    }
    const errors = bindingErrors(binding);
    if (errors.length) return errors;
    this.#binding = Object.freeze({ tool: binding.tool, label: binding.label ?? '', title: binding.title ?? '', description: binding.description ?? '', arguments: Object.freeze({ ...(binding.arguments ?? {}) }),
      editable: Object.freeze(Object.assign(Object.create(null), binding.editable ?? {})),
      typed: Object.freeze([...(binding.typed ?? [])]),
      editor: binding.editor ? Object.freeze({ type: binding.editor.type, id: binding.editor.id }) : null,
      confirm: binding.confirm ? Object.freeze({ message: binding.confirm.message, label: binding.confirm.label }) : null });
    this.#request = null;
    this.#state = 'default';
    this.#message = '';
    this.#reference = '';
    return [];
  }

  // Returns a request, or null if the action cannot start now. The extra values come from a fill,
  // so they cannot change an argument that the application binds: the bound arguments come last.
  // Each extra value needs a name that the binding gives as editable. If the binding has a confirm step,
  // the action starts only when confirmed is true, after a person confirms. If the binding has typed names,
  // the request needs a list of edits, and the request carries them with the new values and the editor.
  start(extra = {}, confirmed = false, edits = undefined) {
    if (!this.#binding || this.#state === 'pending' || this.#state === 'done') return null;
    if (this.#binding.confirm && confirmed !== true) return null;
    if (Object.keys(extra).some((name) => !Object.hasOwn(this.#binding.editable, name))) return null;
    const typed = this.#binding.typed.length > 0;
    if (!typed && edits !== undefined && !(Array.isArray(edits) && edits.length === 0)) return null;
    const checked = typed ? editsOf(edits, this.#binding, extra) : null;
    if (typed && !checked) return null;
    this.#request = Object.freeze({ id: crypto.randomUUID(), tool: this.#binding.tool,
      arguments: Object.freeze({ ...extra, ...this.#binding.arguments }), ...(typed ? { edits: checked } : {}) });
    this.#state = 'pending';
    this.#message = '';
    this.#reference = '';
    return this.#request;
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

  // Returns true if the receipt belongs to the pending request. Only a valid receipt changes the state.
  receipt(value) {
    if (!this.#request || this.#state !== 'pending' || value === null || typeof value !== 'object') return false;
    if (value.id !== this.#request.id || typeof value.ok !== 'boolean') return false;
    const message = value.message ?? '';
    if (typeof message !== 'string' || longer(message, MAX_MESSAGE)) return false;
    const reference = value.reference ?? '';
    if (typeof reference !== 'string' || longer(reference, MAX_REFERENCE)) return false;
    this.#state = value.ok ? 'done' : 'error';
    this.#message = message;
    this.#reference = reference;
    this.#request = null;
    return true;
  }
}
