// The offline queue of pending actions. docs/contracts/offline-queue.md is the contract.
// The queue keeps each action on the device and sends the actions in order, one at a time. Each try of an action
// has the same key and a new id. The application gives the send function, and it adds its credentials there.
// The queue keeps no header, no cookie and no credential. It never shows an action as done: only a receipt does.
// Each record belongs to one principal, the signed-in person that the application names. A queue takes and sends
// only the records of its principal. The application checks the credentials in the send function.
import { RICH_TEXT_SCHEMES, richTextValueErrors } from '../render/rich-text-value.js';

const TOOL = /^[a-z][a-z0-9_]*(\.[a-z][a-z0-9_]*)*$/;
const ID = /^[A-Za-z0-9._:-]{1,128}$/;
const NAME = /^[a-z0-9_-]{1,64}$/;
// An argument name that looks like a credential. This is a guard, not a proof.
const CREDENTIAL = /token|secret|passw|passphrase|authorization|cookie|credential|bearer|api_?key|access_?key|private_?key/i;
const STATES = ['queued', 'sent', 'stalled', 'rejected'];
const RECORD_VERSION = 1;
const REQUIRED_KEYS = ['key', 'principal', 'owner', 'order', 'tool', 'arguments', 'label', 'queued', 'tries', 'state', 'message'];
const RECORD_KEYS = new Set([...REQUIRED_KEYS, 'version', 'edits']);
const MAX_ACTIONS = 100;
const MAX_ITEMS = 500;
const MAX_TEXT = 80;
const MAX_RECORD = 200000;
const MAX_LABEL = 120;
const MAX_PRINCIPAL = 128;
const MAX_MESSAGE = 200;
const MAX_REFERENCE = 80;
const TIMEOUT = 10000;
const REJECTED = 'The service rejected the action.';
const UNREADABLE = 'A saved action that this version cannot read';
const FULL = 'The device holds 100 pending actions. Wait until the device sends them.';
const NOT_KEPT = 'The device cannot keep the action.';
const CLOSED = 'The queue is closed.';
const NO_CONNECTION = 'Waiting for a connection.';
const EARLIER = 'Waiting for an earlier action.';
const NO_RECEIPT = 'No receipt came back.';
const NOT_REMOVED = 'The device cannot remove the action. Try again.';
const ISO_TIME = /^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}\.\d{3}Z$/;
// The result of a try that did not leave the device, because the queue closed first.
const UNSENT = Symbol('unsent');

const isPlain = (value) => value !== null && typeof value === 'object' && !Array.isArray(value)
  && [Object.prototype, null].includes(Object.getPrototypeOf(value));
const isScalar = (value) => typeof value === 'string' || typeof value === 'boolean' || (typeof value === 'number' && Number.isFinite(value));
const isOrderedPair = (value) => Array.isArray(value) && value.length === 2 && Object.hasOwn(value, 0) && Object.hasOwn(value, 1)
  && Number.isFinite(value[0]) && Number.isFinite(value[1]) && value[0] <= value[1];
const isCount = (value) => Number.isSafeInteger(value) && value >= 0;
const isPrincipal = (value) => typeof value === 'string' && value.length >= 1 && value.length <= MAX_PRINCIPAL;
const isKey = (value) => typeof value === 'string' && ID.test(value);
const shortText = (value, max) => typeof value === 'string' && value.length <= max * 2 && [...value].length <= max;
const refusal = (message) => Object.freeze({ ok: false, message });

// Checks and copies the receipt fields. A transport checks one request id. The action state can also
// accept ids of earlier tries with the same key.
export function receiptOf(value, request, ids) {
  if (!isPlain(value) || !Object.hasOwn(value, 'ok')) return null;
  const { ok } = value;
  if (typeof ok !== 'boolean') return null;
  const receipt = { ok };
  if (Object.hasOwn(value, 'id')) {
    const { id } = value;
    if (typeof id !== 'string' || !(ids ? ids.has(id) : id === request.id)) return null;
    receipt.id = id;
  }
  if (Object.hasOwn(value, 'key')) {
    const { key } = value;
    if (typeof key !== 'string' || key !== request.key) return null;
    receipt.key = key;
  }
  for (const [field, limit] of [['message', MAX_MESSAGE], ['reference', MAX_REFERENCE]]) {
    if (!Object.hasOwn(value, field)) continue;
    const content = value[field];
    if (!shortText(content, limit)) return null;
    receipt[field] = content;
  }
  return receipt;
}

// The action path and the queue share this text list check. Both also check rich text with richTextValueErrors.
export function isTextList(value) {
  if (!Array.isArray(value) || value.length > MAX_ITEMS) return false;
  for (let index = 0; index < value.length; index += 1) {
    if (!Object.hasOwn(value, index)) return false;
    const item = value[index];
    if (typeof item !== 'string' || item.length > MAX_TEXT * 2 || [...item].length > MAX_TEXT) return false;
  }
  return true;
}

function freezeTree(value) {
  if (value === null || typeof value !== 'object') return value;
  if (Array.isArray(value)) return Object.freeze(Array.from({ length: value.length }, (_, index) => freezeTree(value[index])));
  return Object.freeze(Object.fromEntries(Object.entries(value).map(([key, child]) => [key, freezeTree(child)])));
}

export function copyValue(value) {
  if (value?.component === 'rich-text') return freezeTree(value);
  if (!Array.isArray(value)) return value;
  // Copy only the indexed entries of an array that passed the value check.
  return Object.freeze(Array.from({ length: value.length }, (_, index) => value[index]));
}

export function copyArgs(args) {
  return Object.freeze(Object.fromEntries(Object.entries(args).map(([name, value]) => [name, copyValue(value)])));
}

// The queue uses all supported link schemes. The action path checks the page policy for new values.
function validArguments(args) {
  return isPlain(args) && Object.values(args).every((value) => isScalar(value) || isOrderedPair(value) || isTextList(value)
    || richTextValueErrors(value, RICH_TEXT_SCHEMES).length === 0);
}

// A deep copy of the typed edits of a request, or null.
function editsOf(edits) {
  if (!Array.isArray(edits) || edits.length > 400) return null;
  const copy = [];
  for (const edit of edits) {
    if (!isPlain(edit)) return null;
    const item = {};
    for (const [name, value] of Object.entries(edit)) {
      const fieldValue = name === 'old_value' || name === 'new_value';
      if (isScalar(value)) item[name] = value;
      else if (fieldValue && (isOrderedPair(value) || isTextList(value))) item[name] = copyValue(value);
      else if (fieldValue && value?.component === 'rich-text') {
        if (richTextValueErrors(value, RICH_TEXT_SCHEMES).length) return null;
        item[name] = copyValue(value);
      } else if (isPlain(value) && Object.values(value).every(isScalar)) item[name] = { ...value };
      else return null;
    }
    copy.push(item);
  }
  return copy;
}

// Check and copy a record before the first write and before adoption. A record with no version is a version 1 record.
function checkedRecord(record) {
  if (!isPlain(record) || !REQUIRED_KEYS.every((key) => Object.hasOwn(record, key))
    || !Reflect.ownKeys(record).every((key) => RECORD_KEYS.has(key) && Object.prototype.propertyIsEnumerable.call(record, key))
    || (Object.hasOwn(record, 'version') && record.version !== RECORD_VERSION)) return { error: 'The record is not valid.' };
  const { key, principal, owner, order, tool, arguments: args, label, queued, tries, state, message } = record;
  if (!isKey(key)) return { error: 'The request has no valid key.' };
  if (typeof tool !== 'string' || tool.length > 80 || !TOOL.test(tool)) return { error: 'The tool name is not valid.' };
  if (!validArguments(args)) return { error: 'The arguments are not valid.' };
  const secret = Object.keys(args).find((name) => CREDENTIAL.test(name));
  if (secret !== undefined) return { error: `The queue does not keep the argument ${secret.slice(0, 40)}. Its name looks like a credential.` };
  const hasEdits = Object.hasOwn(record, 'edits');
  const edits = hasEdits ? editsOf(record.edits) : undefined;
  if (edits === null) return { error: 'The edits are not valid.' };
  if (!isPrincipal(principal) || typeof owner !== 'string' || !ID.test(owner) || !isCount(order) || !isCount(tries)
    || typeof label !== 'string' || label.length < 1 || label.length > MAX_LABEL
    || typeof queued !== 'string' || !ISO_TIME.test(queued) || !Number.isFinite(Date.parse(queued))
    || !STATES.includes(state) || !shortText(message, MAX_MESSAGE)) return { error: 'The record is not valid.' };
  const copy = { ...record, arguments: copyArgs(args), ...(hasEdits ? { edits } : {}) };
  if (JSON.stringify(copy).length > MAX_RECORD) return { error: 'The action is too large for the queue.' };
  return { record: copy };
}

// Makes the record from selected fields. Nothing else from the request goes into the store.
function recordOf(request, label, { principal, owner, order, queued }) {
  if (!isPlain(request)) return { error: 'The request is not valid.' };
  const { id, key, tool, arguments: args = {}, edits } = request;
  if (typeof id !== 'string' || !ID.test(id)) return { error: 'The request has no valid id.' };
  const record = { version: RECORD_VERSION, key, principal, owner, order, tool, arguments: args,
    label: (typeof label === 'string' && label ? label : typeof tool === 'string' ? tool : '').slice(0, MAX_LABEL),
    queued, tries: 0, state: 'queued', message: '', ...(edits === undefined ? {} : { edits }) };
  return checkedRecord(record);
}

// A store that keeps the records in memory only. A reload loses them.
export function memoryStore() {
  const records = new Map();
  return {
    all: async () => [...records.values()].map((record) => structuredClone(record)),
    allEntries: async () => [...records].map(([key, record]) => ({ key, record: structuredClone(record) })),
    put: async (record) => { records.set(record.key, structuredClone(record)); },
    remove: async (key) => { records.delete(key); },
  };
}

// A store that keeps the records in IndexedDB, so they stay after a reload. The name is the name of the queue.
export function indexedDbStore(name = 'actions', factory = globalThis.indexedDB) {
  if (typeof name !== 'string' || !NAME.test(name)) throw new TypeError('name: 1 to 64 characters of a-z, 0-9, - and _');
  if (!factory) throw new TypeError('store: this device has no IndexedDB. Give a store.');
  let opened = null;
  const database = () => {
    opened ??= new Promise((done, fail) => {
      const request = factory.open('mugham-queue:' + name, 1);
      request.addEventListener('upgradeneeded', () => request.result.createObjectStore('actions', { keyPath: 'key' }));
      request.addEventListener('success', () => done(request.result));
      request.addEventListener('error', () => fail(request.error));
    }).catch((error) => {
      opened = null;
      throw error;
    });
    return opened;
  };
  const run = async (mode, work) => {
    const db = await database();
    return new Promise((done, fail) => {
      const transaction = db.transaction('actions', mode);
      const request = work(transaction.objectStore('actions'));
      transaction.addEventListener('complete', () => done(request.result));
      transaction.addEventListener('error', () => fail(transaction.error));
      transaction.addEventListener('abort', () => fail(transaction.error));
    });
  };
  return {
    all: () => run('readonly', (store) => store.getAll()),
    allEntries: () => run('readonly', (store) => {
      const keys = store.getAllKeys();
      const records = store.getAll();
      return { get result() { return records.result.map((record, index) => ({ key: keys.result[index], record })); } };
    }),
    put: async (record) => { await run('readwrite', (store) => store.put(record)); },
    remove: async (key) => { await run('readwrite', (store) => store.delete(key)); },
  };
}

// IndexedDB gives a new copy of a compound key on each read.
function sameStoreKey(a, b) {
  if (Object.is(a, b)) return true;
  if (a === null || b === null || typeof a !== 'object' || typeof b !== 'object') return false;
  try {
    return globalThis.indexedDB?.cmp(a, b) === 0;
  } catch {
    return false;
  }
}

export class ActionQueue {
  #send;
  #store;
  #name;
  #principal;
  #timeout;
  #online;
  #events;
  #locks;
  #now;
  #owner = crypto.randomUUID();
  // The records of this owner, in order. Each record is also in the store.
  #list = [];
  // The local recovery key names the store key. The saved content stays in the store.
  #unreadable = new Map();
  #nextRecovery = 0;
  // The page parts that wait for a receipt: key -> { id, element, promise, resolve }.
  #waiters = new Map();
  // The keys of the records that this queue removed. The queue never takes them from the store again.
  #gone = new Set();
  // The keys of the actions whose cancel waits for the store, and of the actions that the store did not remove.
  #leaving = new Set();
  #stuck = new Set();
  // True after a pass stopped at an action whose cancel waits for the store.
  #blocked = false;
  // True if the store has more records of this principal than the queue had room for.
  #more = false;
  #watchers = new Set();
  #adds = Promise.resolve();
  #running = null;
  #again = false;
  #order = 0;
  #loaded = null;
  #closed = false;
  #release = null;
  #reconnect = () => { this.flush(); };
  #disconnect = () => { this.#changed(); };

  constructor({ send, principal, name = 'actions', store, timeout = TIMEOUT, online, events, locks, now } = {}) {
    if (typeof send !== 'function') throw new TypeError('send: a function is necessary');
    // The application names the person whose credentials the send function uses. An application with no sign-in
    // gives one fixed text.
    if (!isPrincipal(principal)) throw new TypeError('principal: a text of 1 to 128 characters is necessary');
    if (typeof name !== 'string' || !NAME.test(name)) throw new TypeError('name: 1 to 64 characters of a-z, 0-9, - and _');
    if (!(Number.isFinite(timeout) && timeout > 0)) throw new TypeError('timeout: a number of milliseconds above 0');
    this.#send = send;
    this.#principal = principal;
    this.#name = name;
    this.#store = store ?? indexedDbStore(name);
    for (const method of ['all', 'put', 'remove']) {
      if (typeof this.#store?.[method] !== 'function') throw new TypeError(`store: the method ${method} is necessary`);
    }
    this.#timeout = timeout;
    this.#online = online ?? (() => globalThis.navigator?.onLine !== false);
    this.#events = events === undefined ? (typeof globalThis.addEventListener === 'function' ? globalThis : null) : events;
    this.#locks = locks === undefined ? (globalThis.navigator?.locks ?? null) : locks;
    this.#now = now ?? Date.now;
  }

  // Reads the store, takes the records with no live owner and starts the first flush. openQueue calls it.
  load() {
    this.#loaded ??= (async () => {
      await this.#own();
      try {
        await this.#adopt();
      } catch (error) {
        this.close();
        throw error;
      }
      this.#events?.addEventListener('online', this.#reconnect);
      this.#events?.addEventListener('offline', this.#disconnect);
      this.flush();
      this.#changed();
    })();
    return this.#loaded;
  }

  // The actions of the queue, in order. No state of the queue is done.
  get entries() {
    const actions = this.#list.map((record) => Object.freeze({ key: record.key, tool: record.tool, label: record.label,
      arguments: copyArgs(record.arguments), queued: record.queued, tries: record.tries, ...this.#shown(record) }));
    const recovery = [...this.#unreadable.keys()].map((key) => Object.freeze({ key, label: UNREADABLE, state: 'unreadable' }));
    return Object.freeze([...actions, ...recovery]);
  }

  // Calls the listener with the entries after each change. A receipt gives { key, receipt } as the second argument.
  watch(listener) {
    this.#watchers.add(listener);
    return () => this.#watchers.delete(listener);
  }

  // Adds a request to the queue and returns a promise of its receipt. A cancel gives null. The element, if given,
  // gets the detail of the pending state through hold(). A second request with the same key is a retry of a
  // person: it gives the same promise, and the queue sends the action again.
  perform(request, element = null) {
    if (isPlain(request) && Object.hasOwn(request, 'file')) return Promise.resolve(refusal('The queue cannot keep a file.'));
    const key = isPlain(request) ? request.key : undefined;
    const waiting = this.#waiters.get(key);
    if (waiting) {
      this.flush();
      return waiting.promise;
    }
    if (this.#closed) return Promise.resolve(refusal(CLOSED));
    const waiter = { id: request?.id, element };
    waiter.promise = new Promise((resolve) => { waiter.resolve = resolve; });
    const add = this.#adds.then(async () => {
      if (this.#find(key) || this.#unreadable.has(key)) return 'The queue has an action with this key.';
      if (this.#list.length >= MAX_ACTIONS) return FULL;
      this.#order = Math.max(this.#order + 1, this.#now());
      const label = element?.action?.label || element?.action?.title || '';
      const { record, error } = recordOf(request, label, { principal: this.#principal, owner: this.#owner, order: this.#order,
        queued: new Date(this.#now()).toISOString() });
      if (error) return error;
      try {
        await this.#store.put({ ...record });
      } catch {
        return NOT_KEPT;
      }
      this.#list.push(record);
      return '';
    });
    this.#adds = add.catch(() => {});
    // The waiter is in the map before the record is in the store, so a retry or a cancel in that time finds it.
    if (typeof key === 'string') this.#waiters.set(key, waiter);
    const added = (error) => {
      if (error) {
        this.#waiters.delete(key);
        waiter.resolve(refusal(error));
        return;
      }
      this.flush();
      this.#changed();
    };
    add.then(added, () => added(NOT_KEPT));
    return waiter.promise;
  }

  // Sends the queue now. One flush runs at a time. A flush that starts during a flush runs one more pass after it.
  flush() {
    if (this.#closed) return this.#running ?? Promise.resolve();
    this.#again = true;
    this.#running ??= (async () => {
      try {
        while (this.#again && !this.#closed) {
          this.#again = false;
          await this.#pass();
        }
      } finally {
        this.#running = null;
        if (this.#closed) this.#releaseOwner();
      }
    })();
    return this.#running;
  }

  // Sends the queue again after a try with no receipt. The key of each action stays the same. Returns false if the
  // action does not wait.
  retry(key) {
    const state = this.#find(key)?.state;
    if (state !== 'stalled' && state !== 'queued') return false;
    this.flush();
    return true;
  }

  // Starts the cancel of an action that waits for a connection or that got no receipt. Returns false for each
  // other action, also for an action with a try in flight. The cancel is complete only after the store removed the
  // record: then the promise of perform gives null, and the element shows the cancel. Thus a canceled action
  // cannot go out after a reload. If the store cannot remove the record, the action stays pending.
  cancel(key) {
    const record = this.#find(key);
    // The queue cancels only an action that shows a cancel: not a try in flight, and not the action that it sends next.
    const shown = record ? this.#shown(record).state : '';
    if ((shown !== 'queued' && shown !== 'stalled') || this.#leaving.has(key)) return false;
    this.#withdraw(record);
    return true;
  }

  // Removes each action with no try in flight, for example when a person signs out. An action with a try in
  // flight stays until its receipt comes back. Returns a promise of the number of removed actions. The promise
  // settles after the store removed the records.
  async clear() {
    // An action that perform still writes to the store is in the list after this wait.
    await this.#adds;
    const waiting = this.#list.filter((record) => record.state !== 'sent' && !this.#leaving.has(record.key));
    const removed = await Promise.all(waiting.map((record) => this.#withdraw(record)));
    return removed.filter(Boolean).length;
  }

  // Removes a rejected action or starts removal of an unreadable entry.
  dismiss(key) {
    const unreadable = this.#unreadable.get(key);
    if (unreadable) {
      if (unreadable.dismissing) return false;
      unreadable.dismissing = true;
      void this.#dismissUnreadable(key, unreadable);
      return true;
    }
    const record = this.#find(key);
    if (record?.state !== 'rejected') return false;
    this.#drop(record);
    this.#changed();
    return true;
  }

  // Connects the cancel events of the action elements and the steps of mu-action-queue below root to the queue.
  // Returns a function that removes the listeners.
  listen(root) {
    const cancel = (event) => {
      const key = event.detail?.key;
      if (!this.#waiters.has(key) && !this.#find(key)) return;
      // The queue has the action, so the element must not cancel it by itself. The queue cancels the element
      // after the store removed the record.
      event.preventDefault();
      this.cancel(key);
    };
    const step = (event) => {
      const { key, step: name } = event.detail ?? {};
      if (name === 'retry') this.retry(key);
      else if (name === 'cancel') this.cancel(key);
      else if (name === 'dismiss') this.dismiss(key);
    };
    root.addEventListener('mu-action-cancel', cancel);
    root.addEventListener('mu-queue-step', step);
    return () => {
      root.removeEventListener('mu-action-cancel', cancel);
      root.removeEventListener('mu-queue-step', step);
    };
  }

  // Stops the queue. The records stay in the store, and a later queue takes them.
  close() {
    this.#closed = true;
    this.#events?.removeEventListener('online', this.#reconnect);
    this.#events?.removeEventListener('offline', this.#disconnect);
    if (!this.#running) this.#releaseOwner();
  }

  #releaseOwner() {
    this.#release?.();
    this.#release = null;
  }

  #find(key) {
    return this.#list.find((record) => record.key === key);
  }

  // Removes a record from the store, then ends the wait of its page part: the element cancels, and the promise
  // gives null. While the store works, the queue does not send this record or a record after it. Returns true
  // if the store removed the record.
  async #withdraw(record) {
    const { key } = record;
    this.#leaving.add(key);
    this.#stuck.delete(key);
    let removed = true;
    try {
      await this.#store.remove(key);
    } catch {
      removed = false;
    }
    this.#leaving.delete(key);
    if (removed) {
      this.#list = this.#list.filter((item) => item !== record);
      this.#gone.add(key);
      const waiter = this.#waiters.get(key);
      this.#waiters.delete(key);
      try {
        waiter?.element?.cancel?.(key);
      } catch {
        // An element of the application cannot stop the queue.
      }
      waiter?.resolve(null);
    } else {
      this.#stuck.add(key);
    }
    // After a cancel, the queue continues with the next action. After a failed cancel, the action waits as
    // before. The queue sends it again only if a pass stopped at it, or at the next flush.
    if (removed || this.#blocked) {
      this.#blocked = false;
      this.flush();
    }
    this.#changed();
    return removed;
  }

  #drop(record) {
    this.#list = this.#list.filter((item) => item !== record);
    this.#gone.add(record.key);
    return this.#store.remove(record.key).catch(() => {});
  }

  // The state and the message that the page shows for a record. Offline, an action waits for a connection. Online,
  // a running flush sends the first action next, and each other action waits for the action before it. With no
  // running flush, the first action waits too, so the page never shows a try that is not in flight.
  #shown(record) {
    if (record.state === 'rejected') return { state: 'rejected', message: record.message };
    if (record.state === 'sent') return { state: 'sent', message: '' };
    // The store did not remove this action after a cancel, so it still waits. The message tells the person.
    const stuck = this.#stuck.has(record.key);
    if (!this.#online()) return { state: 'queued', message: stuck ? NOT_REMOVED : NO_CONNECTION };
    if (record.state === 'stalled') return { state: 'stalled', message: stuck ? NOT_REMOVED : NO_RECEIPT };
    if (this.#list.find((item) => item.state !== 'rejected') !== record) return { state: 'queued', message: stuck ? NOT_REMOVED : EARLIER };
    if (this.#running && !this.#closed && !this.#leaving.has(record.key)) return { state: 'sent', message: '' };
    return { state: 'queued', message: stuck ? NOT_REMOVED : NO_CONNECTION };
  }

  // A failed write does not stop the queue. The key of the action protects against a second send after a reload.
  async #keep(record) {
    try {
      await this.#store.put({ ...record });
    } catch {
      // The record stays in memory.
    }
  }

  // Gives the detail of each pending action to its element, then calls the listeners.
  #changed(change) {
    for (const record of this.#list) {
      const element = this.#waiters.get(record.key)?.element;
      if (typeof element?.hold !== 'function' || record.state === 'rejected') continue;
      const { state, message } = this.#shown(record);
      try {
        element.hold({ key: record.key, wait: state, tries: record.tries, message });
      } catch {
        // An element of the application cannot stop the queue.
      }
    }
    const entries = this.entries;
    for (const listener of this.#watchers) {
      try {
        listener(entries, change);
      } catch {
        // A listener of the application cannot stop the queue.
      }
    }
  }

  // Holds the owner lock until the queue closes and the running flush finishes its storage work.
  #own() {
    if (!this.#locks) return Promise.resolve();
    return new Promise((ready) => {
      this.#locks.request(`mugham-queue:${this.#name}:${this.#owner}`, () => {
        ready();
        return new Promise((release) => { this.#release = release; });
      }).catch(() => {
        // With no lock, a different queue cannot know that this owner is live. Then the queue uses no locks.
        this.#locks = null;
        ready();
      });
    });
  }

  #readStored() {
    if (typeof this.#store.allEntries === 'function') return this.#store.allEntries();
    return this.#store.all().then((records) => records.map((record) => ({ key: record?.key, record })));
  }

  #withStoreLock(work) {
    if (this.#locks) return this.#locks.request(`mugham-queue:${this.#name}`, work);
    return work();
  }

  #isMine(record) {
    return record != null && Object.hasOwn(record, 'principal') && record.principal === this.#principal;
  }

  async #dismissUnreadable(key, entry) {
    try {
      await this.#withStoreLock(async () => {
        // Check the current record. Another page can dismiss it after this queue reads it.
        const found = (await this.#readStored()).find((item) => sameStoreKey(item.key, entry.storeKey));
        if (found && this.#isMine(found.record) && !(found.key === found.record.key && checkedRecord(found.record).record)) {
          await this.#store.remove(entry.storeKey);
        }
        this.#unreadable.delete(key);
      });
    } catch {
      // The entry stays available for dismissal.
    } finally {
      entry.dismissing = false;
      this.#changed();
    }
  }

  #readRecords(entries) {
    const stored = [];
    const recovery = new Map();
    const prior = new Map([...this.#unreadable].map(([key, entry]) => [entry.storeKey, key]));
    const compound = [...prior].filter(([key]) => key !== null && typeof key === 'object');
    for (const { key, record } of entries) {
      if (!this.#isMine(record)) continue;
      const checked = key === record.key ? checkedRecord(record).record : undefined;
      if (checked) {
        stored.push(checked);
        continue;
      }
      const oldKey = prior.get(key) ?? (key !== null && typeof key === 'object'
        ? compound.find(([old]) => sameStoreKey(old, key))?.[1]
        : undefined);
      const shownKey = oldKey ?? (isKey(key) && key === record.key && !this.#find(key) ? key : `unreadable/${++this.#nextRecovery}`);
      recovery.set(shownKey, this.#unreadable.get(oldKey) ?? { storeKey: key, dismissing: false });
    }
    const changed = recovery.size !== this.#unreadable.size || [...recovery.keys()].some((key) => !this.#unreadable.has(key));
    this.#unreadable = recovery;
    if (changed) this.#changed();
    return stored.sort((a, b) => a.order - b.order);
  }

  // Takes each record of its principal whose owner holds no lock, for example after a reload. With no Web Locks API,
  // the queue takes each record of its principal. The lock of the queue name lets only one queue take records at a time.
  async #adopt() {
    // The queue reads this again in each pass. A store that fails gives no reason for one more pass.
    this.#more = false;
    const take = async () => {
      const held = this.#locks ? new Set(((await this.#locks.query()).held ?? []).map((lock) => lock.name)) : null;
      // The oldest records come first, so the queue takes them first if it has no room for all of them.
      const entries = await this.#readStored();
      if (this.#closed) return;
      const stored = this.#readRecords(entries);
      for (const record of stored) {
        if (this.#closed) break;
        if (this.#find(record.key) || this.#gone.has(record.key)) continue;
        // A record of this owner that is not in the list is in the middle of perform. perform adds it to the list.
        if (record.owner === this.#owner || held?.has(`mugham-queue:${this.#name}:${record.owner}`)) continue;
        // A full queue takes the other records in a later pass, when it has room.
        if (this.#list.length >= MAX_ACTIONS) {
          this.#more = true;
          break;
        }
        // A try that was in flight has no receipt now. The record waits, and the next try has the same key.
        const mine = { ...record, owner: this.#owner, state: record.state === 'rejected' ? 'rejected' : 'queued' };
        if (mine.state === 'queued') mine.message = '';
        await this.#store.put({ ...mine });
        this.#list.push(mine);
      }
      this.#list.sort((a, b) => a.order - b.order || (a.queued < b.queued ? -1 : a.queued > b.queued ? 1 : 0));
      this.#order = Math.max(this.#order, ...this.#list.map((record) => record.order));
    };
    await this.#withStoreLock(take);
  }

  // One pass: sends each action in order until the queue is empty, the device is offline or a try gets no receipt.
  async #pass() {
    try {
      await this.#adopt();
    } catch {
      // The store cannot give its records now. The pass sends the records in memory.
    }
    while (!this.#closed && this.#online()) {
      const record = this.#list.find((item) => item.state !== 'rejected');
      // A cancel of the first action waits for the store. The queue sends nothing until the cancel is complete.
      if (!record) {
        // The queue sent each action that it had. One more pass takes the records that had no room.
        if (this.#more && this.#list.length < MAX_ACTIONS) this.#again = true;
        return;
      }
      if (this.#leaving.has(record.key)) {
        this.#blocked = true;
        return;
      }
      const before = record.state;
      record.tries += 1;
      record.state = 'sent';
      record.message = '';
      this.#stuck.delete(record.key);
      this.#changed();
      // The store knows about the try before the request leaves the device.
      await this.#keep(record);
      // Keep the link schemes that passed the check when the queue took the record.
      const edits = record.edits ? editsOf(record.edits) : undefined;
      const request = Object.freeze({ id: crypto.randomUUID(), key: record.key, tool: record.tool,
        arguments: copyArgs(record.arguments), ...(edits ? { edits: Object.freeze(edits) } : {}) });
      const receipt = edits === null || !validArguments(record.arguments) ? null : await this.#try(request);
      if (receipt === UNSENT) {
        // The queue closed before the request left the device, for example at a sign-out. The record stays for
        // its principal as it was before this try.
        record.tries -= 1;
        record.state = before;
        await this.#keep(record);
        this.#changed();
        return;
      }
      if (!receipt) {
        record.state = this.#online() ? 'stalled' : 'queued';
        await this.#keep(record);
        this.#changed();
        return;
      }
      const waiter = this.#waiters.get(record.key);
      // Only an element that is in the page now can show a rejection. A call of perform with no element has none.
      const shows = Boolean(waiter?.element) && waiter.element.isConnected !== false;
      this.#waiters.delete(record.key);
      const removal = receipt.ok || shows ? this.#drop(record) : null;
      if (!removal) {
        // The page has no element for this action. The rejection stays until a person dismisses it.
        record.state = 'rejected';
        record.message = receipt.message || REJECTED;
        await this.#keep(record);
      }
      // The caller knows the id of its request. The queue has already checked its transport id.
      if (waiter) waiter.resolve(Object.freeze({ ...receipt, id: waiter.id }));
      this.#changed({ key: record.key, receipt });
      if (removal) await removal;
    }
  }

  // One try. The result is a receipt, null if no valid receipt came back in the time limit, or UNSENT if the
  // queue closed before the request left the device.
  async #try(request) {
    const control = new AbortController();
    let timer;
    // The queue reads its closed state in the same task as the call of the send function. After a close, the
    // credentials of the send function can be the credentials of a different person.
    if (this.#closed) return UNSENT;
    let answer;
    try {
      answer = Promise.resolve(this.#send(request, { signal: control.signal, principal: this.#principal }));
    } catch {
      return null;
    }
    try {
      const late = {};
      const clock = new Promise((done) => { timer = setTimeout(() => done(late), this.#timeout); });
      const value = await Promise.race([answer, clock]);
      if (value !== late) {
        const receipt = receiptOf(value, request);
        if (!receipt) return null;
        const { ok, message = '', reference = '' } = receipt;
        return Object.freeze({ id: request.id, key: request.key, ok, message, reference });
      }
      control.abort();
      return null;
    } catch {
      return null;
    } finally {
      clearTimeout(timer);
    }
  }
}

// Opens a queue: reads the store and starts the first flush. If the store cannot give its records, the promise rejects.
export async function openQueue(options) {
  const queue = new ActionQueue(options);
  await queue.load();
  return queue;
}

// A fill of the action-queue component from the entries of a queue.
export function queueFill(label, entries) {
  const actions = [];
  const recovery = [];
  for (const { key, label, state, tries, queued, message } of entries) {
    const row = { component: 'queued-action', version: '0.1.0', data: { key, label, state } };
    if (state === 'unreadable') {
      if (recovery.length < MAX_ACTIONS) recovery.push(row);
    } else {
      Object.assign(row.data, { tries, queued, ...(message ? { message } : {}) });
      actions.push(row);
    }
  }
  return { component: 'action-queue', version: '0.1.0', data: { label, actions, ...(recovery.length ? { recovery } : {}) } };
}
