// The message envelope, version 1, and the reader of an AG-UI 1.0 stream.
// docs/contracts/envelope.md gives the rules. tests/vectors/envelope/ gives the conformance cases.
// The reader never changes its input. Each text stays text: the reader never reads it as Markdown or markup.
import { fillErrors } from '../render/validate.js';
import { CONTRACTS } from '../render/registry.js';
import CONTRACT_SCHEMA from '../../catalog/schema/contract.schema.json' with { type: 'json' };

export const ENVELOPE_VERSION = 1;
export const AGUI_VERSION = '1.0';
// One format number covers a contract and a fill (catalog/schema/README.md). The reader keeps no copy of it.
export const FILL_FORMAT = CONTRACT_SCHEMA.properties.format.const;
export const FILL_ACTIVITY = 'mugham.fill';
export const FALLBACK_TEXT = 'The kit does not know this part type, so it does not show the part.';

const ACTOR_TYPES = ['human', 'agent', 'system'];
const ROLES = ['developer', 'system', 'assistant', 'user', 'tool', 'reasoning', 'activity'];
const KNOWN_PARTS = ['text', 'component', 'status', 'refusal', 'fallback'];
const MAX_ID = 128;
const MAX_NAME = 80;
const MAX_TEXT = 100000;
const MAX_STATUS = 1000;
const MAX_CODE = 128;
const MAX_PARTS = 100;
const MAX_ERRORS = 20;
const MAX_ERROR = 300;
const MAX_TYPE = 40;
const MAX_STREAM_ERRORS = 100;
// The stream comes from a model, so the reader keeps a limited quantity of data.
const MAX_RUNS = 1000;
const MAX_MESSAGES = 1000;
const MAX_STREAM_DATA = 1000000;

const own = (object, key) => Object.prototype.hasOwnProperty.call(object, key);
const isObject = (value) => value !== null && typeof value === 'object' && !Array.isArray(value);

// Count the code points of text, but stop after limit + 1. The count makes no copy of the text.
function length(text, limit) {
  let count = 0;
  for (const _ of text) {
    count += 1;
    if (count > limit) break;
  }
  return count;
}

const isText = (value, min, max) => typeof value === 'string' && length(value, max) >= min && length(value, max) <= max;
const isId = (value) => isText(value, 1, MAX_ID);

// Give text with max code points at most. A longer text keeps its first code points and ends with '...'.
function cut(text, max) {
  const start = [];
  for (const character of text) {
    if (start.length === max) return start.slice(0, max - 3).join('') + '...';
    start.push(character);
  }
  return text;
}

const ID_ERROR = 'must be an id of 1 to 128 characters';

function actorErrors(actor, path) {
  if (actor === undefined) return [`${path}: a required field is missing`];
  if (!isObject(actor)) return [`${path}: must be an object`];
  const errors = [];
  if (!ACTOR_TYPES.includes(actor.type)) errors.push(`${path}.type: must be human, agent or system`);
  if (!isId(actor.id)) errors.push(`${path}.id: ${ID_ERROR}`);
  if (own(actor, 'name') && !isText(actor.name, 1, MAX_NAME)) errors.push(`${path}.name: must be a text of 1 to 80 characters`);
  return errors;
}

// A copy of a valid actor with only the declared fields.
function actorCopy(actor) {
  const copy = { type: actor.type, id: actor.id };
  if (own(actor, 'name')) copy.name = actor.name;
  return copy;
}

const sameActor = (a, b) => a.type === b.type && a.id === b.id && a.name === b.name;

function refusalPart(errors) {
  return { type: 'refusal', errors: errors.slice(0, MAX_ERRORS).map((error) => cut(error, MAX_ERROR)) };
}

function fallbackPart(type) {
  return { type: 'fallback', partType: cut(type, MAX_TYPE), text: FALLBACK_TEXT };
}

// A component part, or a refusal part if the fill validator refuses the fill.
function componentPart(format, fill, path, catalog) {
  if (format !== FILL_FORMAT) return refusalPart([`${path}.format: must be ${FILL_FORMAT}`]);
  const errors = fillErrors(fill, catalog);
  if (errors.length) return refusalPart(errors);
  return { type: 'component', format: FILL_FORMAT, fill: structuredClone(fill) };
}

// The part that the kit shows for one part of an envelope. It is never markup.
function readPart(part, path, catalog) {
  if (!isObject(part) || typeof part.type !== 'string' || part.type === '') {
    return refusalPart([`${path}: must be an object with a type`]);
  }
  switch (part.type) {
    case 'text':
      if (!isText(part.text, 0, MAX_TEXT)) return refusalPart([`${path}.text: must be a text of 100000 characters maximum`]);
      return { type: 'text', text: part.text };
    case 'component':
      return componentPart(part.format, part.fill, path, catalog);
    case 'status': {
      const errors = [];
      if (part.state !== 'error') errors.push(`${path}.state: must be error`);
      if (!isText(part.text, 0, MAX_STATUS)) errors.push(`${path}.text: must be a text of 1000 characters maximum`);
      if (own(part, 'code') && !isText(part.code, 0, MAX_CODE)) errors.push(`${path}.code: must be a text of 128 characters maximum`);
      if (errors.length) return refusalPart(errors);
      const status = { type: 'status', state: 'error', text: part.text };
      if (own(part, 'code')) status.code = part.code;
      return status;
    }
    case 'refusal': {
      const valid = Array.isArray(part.errors) && part.errors.length >= 1 && part.errors.length <= MAX_ERRORS &&
        part.errors.every((error) => typeof error === 'string');
      return refusalPart(valid ? part.errors : [`${path}.errors: must be a list of 1 to 20 texts`]);
    }
    case 'fallback':
      return fallbackPart(typeof part.partType === 'string' ? part.partType : '');
    default:
      return fallbackPart(part.type);
  }
}

function refused(header, errors) {
  return { ...header, state: 'refused', errors: errors.slice(0, MAX_ERRORS).map((error) => cut(error, MAX_ERROR)) };
}

function checkActor(message, header, actorFor) {
  if (actorFor == null) return message;
  try {
    const actor = actorFor(structuredClone(message));
    if (!actorErrors(actor, 'actor').length && actor.type === message.actor.type && actor.id === message.actor.id) return message;
  } catch {
    // The error from page code can contain private data.
  }
  return refused(header, ['Actor check failed.']);
}

// Read one envelope, for example a message from the history of an application. The result is always an
// envelope that agrees with docs/contracts/envelope-v1.schema.json. An envelope with an error is refused.
export function readEnvelope(value, catalog = CONTRACTS, { actorFor } = {}) {
  const header = { envelope: ENVELOPE_VERSION, agui: AGUI_VERSION };
  if (!isObject(value)) return refused(header, ['(envelope): must be an object']);
  const errors = [];
  if (value.envelope !== ENVELOPE_VERSION) errors.push(`envelope: must be ${ENVELOPE_VERSION}`);
  if (value.agui !== AGUI_VERSION) errors.push(`agui: must be ${AGUI_VERSION}`);
  for (const key of ['threadId', 'runId']) {
    if (own(value, key)) {
      if (isId(value[key])) header[key] = value[key];
      else errors.push(`${key}: ${ID_ERROR}`);
    }
  }
  if (isId(value.messageId)) header.messageId = value.messageId;
  else if (value.state !== 'refused' || own(value, 'messageId')) errors.push(`messageId: ${ID_ERROR}`);
  // A refused envelope, for example from the history. The kit keeps its errors and its ids.
  if (value.state === 'refused') {
    const valid = Array.isArray(value.errors) && value.errors.length >= 1 && value.errors.length <= MAX_ERRORS &&
      value.errors.every((error) => isText(error, 0, MAX_ERROR));
    if (!valid) errors.push(`errors: must be a list of 1 to ${MAX_ERRORS} texts of ${MAX_ERROR} characters maximum`);
    else errors.push(...value.errors);
    return refused(header, errors.slice(0, MAX_ERRORS));
  }
  if (own(value, 'role') && !ROLES.includes(value.role)) errors.push(`role: must be one of ${ROLES.join(', ')}`);
  if (value.state !== 'partial' && value.state !== 'final') errors.push('state: must be partial or final');
  errors.push(...actorErrors(value.actor, 'actor'));
  if (!Array.isArray(value.parts)) errors.push('parts: must be a list');
  else if (value.parts.length > MAX_PARTS) errors.push(`parts: more than ${MAX_PARTS} items`);
  if (errors.length) return refused(header, errors);
  const message = { ...header };
  if (own(value, 'role')) message.role = value.role;
  message.actor = actorCopy(value.actor);
  message.state = value.state;
  message.parts = value.parts.map((part, index) => readPart(part, `parts[${index}]`, catalog));
  return checkActor(message, header, actorFor);
}

// A reader of one AG-UI 1.0 stream. Call push for each event in the order of the transport.
// result gives a copy of the runs, the messages in the order of their first event, the count of
// ignored events and the errors of the stream. push gives false for an event that changed no run, no
// message and no error. A reader of the stream can then skip its update.
export function createReader(catalog = CONTRACTS) {
  const runs = [];
  const messages = [];
  // The messageId of each message gives its index in messages, its kind and its text length.
  const index = new Map();
  const errors = [];
  let ignored = 0;
  let active = null;
  let count = 0;
  let dataSize = 0;
  const runIds = new Set();
  // Each path that changes no state calls fail or skip. changed is true if a call of fail adds an error.
  let changed = false;
  let unchanged = false;

  const fail = (error) => {
    unchanged = true;
    if (errors.length < MAX_STREAM_ERRORS) {
      errors.push(error);
      changed = true;
    }
  };
  const skip = () => {
    unchanged = true;
    ignored += 1;
  };
  const header = (messageId) => {
    const value = { envelope: ENVELOPE_VERSION, agui: AGUI_VERSION };
    if (active) Object.assign(value, { threadId: active.threadId, runId: active.runId });
    value.messageId = messageId;
    return value;
  };
  const actorOf = (event) => {
    const metadata = isObject(event.metadata) ? event.metadata : {};
    const mugham = isObject(metadata.mugham) ? metadata.mugham : {};
    return mugham.actor;
  };
  const add = (messageId, kind, message) => {
    index.set(messageId, { at: messages.length, kind, size: 0 });
    messages.push(message);
  };
  // Find the open text message of an event. If the event cannot change the message, give null.
  const openText = (event, at) => {
    if (!isId(event.messageId)) {
      fail(`${at}.messageId: ${ID_ERROR}`);
      return null;
    }
    const entry = index.get(event.messageId);
    if (!entry) {
      fail(`${at}.messageId: no message has this id`);
      return null;
    }
    if (entry.kind !== 'text') {
      fail(`${at}.messageId: the message is not a text message`);
      return null;
    }
    const message = messages[entry.at];
    if (message.state === 'refused') {
      skip();
      return null;
    }
    if (message.state === 'final') {
      fail(`${at}.messageId: the message has ended`);
      return null;
    }
    return { entry, message };
  };

  const handlers = {
    RUN_STARTED(event, at) {
      const bad = ['threadId', 'runId'].filter((key) => !isId(event[key]));
      if (bad.length) return bad.forEach((key) => fail(`${at}.${key}: ${ID_ERROR}`));
      if (active) return fail(`${at}: a run is already active`);
      if (runIds.has(event.runId)) return fail(`${at}.runId: a run has this runId already`);
      if (runs.length >= MAX_RUNS) return fail(`${at}: the stream has ${MAX_RUNS} runs`);
      runIds.add(event.runId);
      active = { threadId: event.threadId, runId: event.runId, state: 'active' };
      runs.push(active);
    },
    RUN_FINISHED(event, at) {
      const bad = ['threadId', 'runId'].filter((key) => !isId(event[key]));
      if (bad.length) return bad.forEach((key) => fail(`${at}.${key}: ${ID_ERROR}`));
      if (!active || active.threadId !== event.threadId || active.runId !== event.runId) {
        return fail(`${at}: no active run has this threadId and runId`);
      }
      active.state = 'finished';
      active = null;
    },
    RUN_ERROR(event, at) {
      const bad = [];
      if (!isText(event.message, 0, MAX_STATUS)) bad.push(`${at}.message: must be a text of 1000 characters maximum`);
      if (own(event, 'code') && !isText(event.code, 0, MAX_CODE)) bad.push(`${at}.code: must be a text of 128 characters maximum`);
      if (bad.length) return bad.forEach(fail);
      if (!active) return fail(`${at}: no run is active`);
      const status = { type: 'status', state: 'error', text: event.message };
      if (own(event, 'code')) status.code = event.code;
      for (const message of messages) {
        if (message.state === 'partial' && message.runId === active.runId && message.parts.length < MAX_PARTS) {
          message.parts.push({ ...status });
        }
      }
      active.state = 'error';
      active.error = { message: event.message };
      if (own(event, 'code')) active.error.code = event.code;
      active = null;
    },
    TEXT_MESSAGE_START(event, at, actorFor) {
      if (!isId(event.messageId)) return fail(`${at}.messageId: ${ID_ERROR}`);
      if (index.has(event.messageId)) return fail(`${at}.messageId: the message exists already`);
      if (messages.length >= MAX_MESSAGES) return fail(`${at}: the stream has ${MAX_MESSAGES} messages`);
      if (own(event, 'role') && !ROLES.includes(event.role)) return fail(`${at}.role: must be one of ${ROLES.join(', ')}`);
      const actor = actorOf(event);
      const bad = actorErrors(actor, 'metadata.mugham.actor');
      if (bad.length) return add(event.messageId, 'text', refused(header(event.messageId), bad));
      const head = header(event.messageId);
      const message = { ...head };
      if (own(event, 'role')) message.role = event.role;
      Object.assign(message, { actor: actorCopy(actor), state: 'partial', parts: [{ type: 'text', text: '' }] });
      add(event.messageId, 'text', checkActor(message, head, actorFor));
    },
    TEXT_MESSAGE_CONTENT(event, at) {
      const open = openText(event, at);
      if (!open) return;
      if (typeof event.delta !== 'string' || event.delta === '') return fail(`${at}.delta: must be a text of 1 character minimum`);
      // A delta after a status part starts a new text part, so the parts keep the order of the events.
      const parts = open.message.parts;
      const last = parts[parts.length - 1];
      if (last.type !== 'text' && parts.length >= MAX_PARTS) return fail(`${at}.messageId: the message has ${MAX_PARTS} parts`);
      const size = length(event.delta, MAX_TEXT);
      if (dataSize + size > MAX_STREAM_DATA) return fail(`${at}.delta: the stream has ${MAX_STREAM_DATA} characters of data`);
      open.entry.size += size;
      if (open.entry.size > MAX_TEXT) {
        // The kit does not keep the text of a refused message.
        dataSize -= open.entry.size - size;
        const { envelope, agui, threadId, runId, messageId } = open.message;
        const value = { envelope, agui, threadId, runId, messageId };
        for (const key of ['threadId', 'runId']) if (value[key] === undefined) delete value[key];
        messages[open.entry.at] = refused(value, [`text: more than ${MAX_TEXT} characters`]);
        return;
      }
      dataSize += size;
      if (last.type === 'text') last.text += event.delta;
      else parts.push({ type: 'text', text: event.delta });
    },
    TEXT_MESSAGE_END(event, at) {
      const open = openText(event, at);
      if (open) open.message.state = 'final';
    },
    ACTIVITY_SNAPSHOT(event, at, actorFor) {
      if (event.activityType !== FILL_ACTIVITY) {
        skip();
        return;
      }
      if (!isId(event.messageId)) return fail(`${at}.messageId: ${ID_ERROR}`);
      if (own(event, 'replace') && typeof event.replace !== 'boolean') return fail(`${at}.replace: must be true or false`);
      const entry = index.get(event.messageId);
      if (entry && entry.kind !== 'activity') return fail(`${at}.messageId: the message is a text message`);
      if (!entry && messages.length >= MAX_MESSAGES) return fail(`${at}: the stream has ${MAX_MESSAGES} messages`);
      if (entry && event.replace === false) {
        skip();
        return;
      }
      const old = entry ? messages[entry.at] : undefined;
      // A refused message stays refused.
      if (old && old.state === 'refused') {
        skip();
        return;
      }
      const actor = actorOf(event);
      const bad = actorErrors(actor, 'metadata.mugham.actor');
      if (old && bad.length) return bad.forEach((error) => fail(`${at}.${error}`));
      if (old && !sameActor(old.actor, actor)) {
        return fail(`${at}.metadata.mugham.actor: must not change`);
      }
      // A replaced message keeps its threadId and its runId.
      const head = old ? { envelope: old.envelope, agui: old.agui } : header(event.messageId);
      if (old) {
        for (const key of ['threadId', 'runId']) if (own(old, key)) head[key] = old[key];
        head.messageId = old.messageId;
      }
      let message;
      if (bad.length) message = refused(head, bad);
      else {
        const content = event.content;
        const part = isObject(content) ? componentPart(content.format, content.fill, 'content', catalog)
          : refusalPart(['content: must be an object']);
        message = checkActor({ ...head, role: 'activity', actor: actorCopy(actor), state: 'final', parts: [part] }, head, actorFor);
      }
      // The JSON text of a fill counts in the data of the stream. A replaced fill gives back its size.
      const part = message.parts ? message.parts[0] : undefined;
      const size = part && part.type === 'component' ? JSON.stringify(part.fill).length : 0;
      const before = entry ? entry.size : 0;
      if (dataSize - before + size > MAX_STREAM_DATA) return fail(`${at}.content: the stream has ${MAX_STREAM_DATA} characters of data`);
      dataSize += size - before;
      if (entry) messages[entry.at] = message;
      else add(event.messageId, 'activity', message);
      const stored = index.get(event.messageId);
      stored.size = size;
      // Keep the name for the display of the refusal. It is not a field of the envelope.
      const component = part?.type === 'refusal' ? event.content?.fill?.component : undefined;
      stored.refusedComponent = typeof component === 'string' ? cut(component, MAX_ERROR) : undefined;
    },
  };

  return {
    push(event, { actorFor } = {}) {
      const at = `events[${count}]`;
      count += 1;
      changed = false;
      unchanged = false;
      if (!isObject(event) || typeof event.type !== 'string') fail(`${at}: must be an object with a type`);
      else if (!own(handlers, event.type)) skip();
      else handlers[event.type](event, at, actorFor);
      return changed || !unchanged;
    },
    result() {
      return structuredClone({ runs, messages, ignored, errors });
    },
    refusedComponent(messageId) {
      return index.get(messageId)?.refusedComponent;
    },
  };
}

// Read a full list of AG-UI 1.0 events.
export function readStream(events, catalog = CONTRACTS) {
  const reader = createReader(catalog);
  for (const event of events) reader.push(event);
  return reader.result();
}

// The event adapter uses the same checks and limits as the reader.
export { MAX_ID, MAX_NAME, actorCopy, actorErrors, cut, isId };
