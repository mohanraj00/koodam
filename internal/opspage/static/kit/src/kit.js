import FILL_SCHEMA from '../catalog/schema/fill.schema.json' with { type: 'json' };
import CONTRACT_SCHEMA from '../catalog/schema/contract.schema.json' with { type: 'json' };
import { deliverReceipt } from './actions/action.js';
import { CONTRACTS, registeredTag } from './render/registry.js';
import { actorErrors, fillErrors } from './render/validate.js';

function contractOf(name) {
  const contract = typeof name === 'string' ? CONTRACTS[name] : undefined;
  if (!contract) throw new TypeError(`component: no contract has the name ${String(name)}`);
  return contract;
}

function actorOf(actor) {
  return { type: actor.type, id: actor.id, ...(Object.hasOwn(actor, 'name') ? { name: actor.name } : {}) };
}

function buildFill(name, data, { version, slots } = {}, actor = null) {
  const contract = contractOf(name);
  if (actor && contract.slots?.authorship) slots = { ...slots, authorship: actorOf(actor) };
  return { component: name, version: version === undefined ? contract.version : version, data,
    ...(slots === undefined ? {} : { slots }) };
}

export function fill(name, data, options) {
  return buildFill(name, data, options);
}

export function createKit({ actor } = {}) {
  if (actor == null) throw new TypeError('actor: a trusted actor is necessary');
  const errors = actorErrors(actor);
  if (errors.length) throw new TypeError('actor: ' + errors.join('; '));
  const bound = actorOf(actor);
  return {
    fill,
    outer: (name, data, options) => buildFill(name, data, options, bound),
    mount: (name, data, options) => mount(name, data, { ...options, actor: bound }),
  };
}

export function mount(name, data, { actor = null, parent, slot, action, ...options } = {}) {
  const value = buildFill(name, data, options, actor);
  const tag = registeredTag(name) ?? `mu-${name}`;
  if (globalThis.customElements?.get(tag)?.component !== name) {
    throw new TypeError(`mount: no defined element for ${name}`);
  }
  const element = document.createElement(tag);
  element.actor = actor;
  if (slot !== undefined) element.slot = slot;
  if (action !== undefined) {
    if (!('action' in element)) throw new TypeError(`mount: ${name} does not support an action binding`);
    element.action = action;
  }
  element.fill = value;
  parent?.append(element);
  return element;
}

const handled = new WeakSet();

export function onAction(root, handler) {
  const listen = (event) => {
    const element = event.composedPath?.()?.[0] ?? event.target;
    const request = event.detail;
    if (typeof element?.receipt !== 'function' || typeof request?.id !== 'string'
      || typeof request.tool !== 'string' || handled.has(event)) return;
    handled.add(event);

    const { id, tool, arguments: args } = request;
    return deliverReceipt(element, request, () => handler(tool, args, id, request, element));
  };
  root.addEventListener('mu-action', listen);
  return () => root.removeEventListener('mu-action', listen);
}

// Connects the action elements below root to an offline queue (docs/contracts/offline-queue.md). Each action goes
// through the queue, online and offline. The queue gives the pending detail and the receipt to the element.
// Returns a function that removes the listeners.
export function queueActions(root, queue) {
  const stopActions = onAction(root, (tool, args, id, request, element) => queue.perform(request, element));
  const stopSteps = queue.listen(root);
  return () => {
    stopActions();
    stopSteps();
  };
}

export function validateFill(value) {
  return fillErrors(value, CONTRACTS);
}

function objectSchema(properties, required = Object.keys(properties)) {
  return { type: 'object', properties, additionalProperties: false, ...(required.length ? { required } : {}) };
}

function fieldSchema(field, reference) {
  const { type, label: title, min_length, max_length, minimum, maximum } = field;
  switch (type) {
    case 'text': {
      const name = CONTRACT_SCHEMA.$defs.field_name;
      return { title, type: 'string', maxLength: field.format === 'name' ? Math.min(max_length, name.maxLength) : max_length,
        ...(min_length === undefined ? {} : { minLength: min_length }),
        ...(field.format === 'name' ? { pattern: name.pattern } : {}) };
    }
    case 'date': return { title, type: 'string', format: 'date', pattern: '^(?!0000)[0-9]{4}-[0-9]{2}-[0-9]{2}$' };
    case 'enum': return { title, type: 'string', enum: [...field.values] };
    case 'number':
    case 'integer': return { title, type, ...(minimum === undefined ? {} : { minimum }), ...(maximum === undefined ? {} : { maximum }) };
    case 'boolean': return { title, type };
    case 'number-or-pair': return { title, oneOf: [{ type: 'number' },
      { type: 'array', minItems: 2, maxItems: 2, items: { type: 'number' } }] };
    case 'list': return { title, type: 'array', maxItems: field.max_items, items: fieldSchema(field.items, reference),
      ...(field.min_items === undefined ? {} : { minItems: field.min_items }),
      ...(field.unique_items === undefined ? {} : { uniqueItems: field.unique_items }) };
    case 'record': return { title, ...dataSchema(field.fields, reference) };
    case 'component': {
      const names = field.field ? Object.keys(CONTRACTS).filter((name) => CONTRACTS[name].field === true) : field.names;
      return names ? { title, oneOf: names.map(reference) } : { title, ...reference(field.name) };
    }
    default: throw new TypeError(`Unknown field type ${String(type)}`);
  }
}

function dataSchema(data, reference) {
  const fields = Object.entries(data);
  return objectSchema(Object.fromEntries(fields.map(([key, field]) => [key, fieldSchema(field, reference)])),
    fields.filter(([, field]) => field.required).map(([key]) => key));
}

function fillSchema(contract, reference, outer) {
  const properties = { component: { const: contract.name }, version: { const: contract.version }, data: dataSchema(contract.data, reference) };
  const required = Object.keys(properties);
  if (outer) {
    const slots = Object.entries(contract.slots ?? {});
    const requiredSlots = slots.filter(([, rule]) => rule.required).map(([key]) => key);
    properties.slots = objectSchema(Object.fromEntries(slots.map(([key]) =>
      [key, structuredClone(FILL_SCHEMA.properties.slots.properties[key])])), requiredSlots);
    if (requiredSlots.length) required.push('slots');
  }
  return objectSchema(properties, required);
}

function exportSchema(name, outer) {
  const contract = contractOf(name);
  const definitions = new Map();
  const reference = (target) => {
    if (!definitions.has(target)) {
      // A component that accepts fields can refer to a registered field that contains it.
      definitions.set(target, null);
      definitions.set(target, fillSchema(contractOf(target), reference, false));
    }
    return { $ref: `#/$defs/${target}` };
  };
  const schema = outer ? fillSchema(contract, reference, true) : dataSchema(contract.data, reference);
  return { $schema: FILL_SCHEMA.$schema, description: contract.description, ...schema,
    ...(definitions.size ? { $defs: Object.fromEntries(definitions) } : {}) };
}

export function describe(name) {
  return exportSchema(name, false);
}

export function toolSchema(name) {
  return exportSchema(name, true);
}

export { createRunSteps } from './envelope/run-steps-events.js';
