import FILL_SCHEMA from '../catalog/schema/fill.schema.json' with { type: 'json' };
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
const FAILED = 'The request failed.';

export function onAction(root, handler) {
  const listen = async (event) => {
    const element = event.composedPath?.()?.[0] ?? event.target;
    const request = event.detail;
    if (typeof element?.receipt !== 'function' || typeof request?.id !== 'string'
      || typeof request.tool !== 'string' || handled.has(event)) return;
    handled.add(event);

    const { id, tool, arguments: args } = request;
    let receipt;
    try {
      receipt = await handler(tool, args, id, request, element);
    } catch (error) {
      const message = error instanceof Error ? error.message : typeof error === 'string' ? error : '';
      receipt = { ok: false, message: message.slice(0, 200) || FAILED };
    }
    if (receipt && typeof receipt === 'object' && !Array.isArray(receipt) && !Object.hasOwn(receipt, 'id')) {
      receipt = { ...receipt, id };
    }
    if (element.receipt(receipt) !== false) return;
    if (element.receipt({ ok: false, message: FAILED, id }) === false) {
      console.error('onAction: The element refused the failure receipt.', id);
    }
  };
  root.addEventListener('mu-action', listen);
  return () => root.removeEventListener('mu-action', listen);
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
    case 'text': return { title, type: 'string', maxLength: max_length,
      ...(min_length === undefined ? {} : { minLength: min_length }) };
    case 'date': return { title, type: 'string', format: 'date', pattern: '^(?!0000)[0-9]{4}-[0-9]{2}-[0-9]{2}$' };
    case 'enum': return { title, type: 'string', enum: [...field.values] };
    case 'number':
    case 'integer': return { title, type, ...(minimum === undefined ? {} : { minimum }), ...(maximum === undefined ? {} : { maximum }) };
    case 'boolean': return { title, type };
    case 'list': return { title, type: 'array', maxItems: field.max_items, items: fieldSchema(field.items, reference) };
    case 'component': return field.names
      ? { title, oneOf: field.names.map(reference) }
      : { title, ...reference(field.name) };
    default: throw new TypeError(`Unknown field type ${String(type)}`);
  }
}

function dataSchema(contract, reference) {
  const fields = Object.entries(contract.data);
  return objectSchema(Object.fromEntries(fields.map(([key, field]) => [key, fieldSchema(field, reference)])),
    fields.filter(([, field]) => field.required).map(([key]) => key));
}

function fillSchema(contract, reference, outer) {
  const properties = { component: { const: contract.name }, version: { const: contract.version }, data: dataSchema(contract, reference) };
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
    if (!definitions.has(target)) definitions.set(target, fillSchema(contractOf(target), reference, false));
    return { $ref: `#/$defs/${target}` };
  };
  const schema = outer ? fillSchema(contract, reference, true) : dataSchema(contract, reference);
  return { $schema: FILL_SCHEMA.$schema, description: contract.description, ...schema,
    ...(definitions.size ? { $defs: Object.fromEntries(definitions) } : {}) };
}

export function describe(name) {
  return exportSchema(name, false);
}

export function toolSchema(name) {
  return exportSchema(name, true);
}
