// The registry of application contracts. An application registers its own contract and element with
// register. The frozen CATALOG never changes. CONTRACTS gives the catalog contracts and the registered
// contracts, and a script cannot write it. The elements and the envelope reader validate each fill against
// CONTRACTS. catalog/README.md#application-contracts gives the rules and the limits.
import { CATALOG } from './catalog.js';
import { resolveContract } from './resolve.js';
import { contractErrors } from './validate.js';

// The prefix of the element name of each registered component. No kit element has this prefix.
export const TAG_PREFIX = 'mu-app-';
// MuElement has this static key, so register can find the base class of an element.
export const BRAND = Symbol('mugham element');

const own = (object, key) => Object.prototype.hasOwnProperty.call(object, key);
const registered = new Map();

function lookup(name) {
  if (typeof name !== 'string') return undefined;
  if (own(CATALOG, name)) return CATALOG[name];
  return registered.get(name)?.contract;
}

function refuse() {
  throw new TypeError('the registry of contracts changes only through register');
}

export const CONTRACTS = new Proxy(Object.create(null), {
  get: (_, name) => lookup(name),
  has: (_, name) => lookup(name) !== undefined,
  getOwnPropertyDescriptor: (_, name) => {
    const value = lookup(name);
    return value === undefined ? undefined : { value, writable: false, enumerable: true, configurable: true };
  },
  ownKeys: () => [...Object.keys(CATALOG), ...registered.keys()],
  set: refuse,
  defineProperty: refuse,
  deleteProperty: refuse,
  setPrototypeOf: refuse,
  preventExtensions: refuse,
});

// The element name of a registered component, or undefined.
export function registeredTag(name) {
  return typeof name === 'string' ? registered.get(name)?.tag : undefined;
}

function freeze(value) {
  if (value !== null && typeof value === 'object') {
    for (const item of Object.values(value)) freeze(item);
    Object.freeze(value);
  }
  return value;
}

function copy(contract) {
  try {
    return structuredClone(contract);
  } catch {
    throw new TypeError('register: the contract must be JSON data');
  }
}

// The first class in the chain of element that defines key. A subclass must not replace fill or actor,
// because the setters of MuElement validate each fill and each actor.
function owner(element, key) {
  for (let item = element.prototype; item !== null; item = Object.getPrototypeOf(item)) {
    if (own(item, key)) return item.constructor;
  }
  return undefined;
}

function elementError(element, name) {
  const base = `register: the element of ${name} must be a class that extends MuElement`;
  if (typeof element !== 'function' || element[BRAND] !== true || own(element, BRAND)) return base;
  let root = element;
  while (!own(root, BRAND)) root = Object.getPrototypeOf(root);
  for (const key of ['fill', 'actor']) {
    if (owner(element, key) !== root) return `register: the element of ${name} must not replace ${key}`;
  }
  if (element.component !== name) return `register: the element of ${name} must have the static component ${name}`;
  return '';
}

// Register an application contract and its element. The contract has the short form or the full form, and it
// gets the same resolver, validator and schema as the catalog. The element gets the name mu-app-<name>. The
// call fails with a TypeError and changes nothing if the contract is missing or not valid, if its name is a
// catalog name or a registered name, or if the element is not valid. A registered contract stays until the
// page closes.
export function register(contract, element, elements = globalThis.customElements) {
  if (contract === undefined || contract === null) throw new TypeError('register: a contract is necessary');
  const stated = copy(contract);
  const name = typeof stated?.name === 'string' ? stated.name : '(no name)';
  if (typeof stated?.name === 'string' && own(CATALOG, stated.name)) {
    throw new TypeError(`register: ${stated.name} is the name of a catalog contract`);
  }
  if (registered.has(name)) throw new TypeError(`register: ${name} is already registered`);
  // The registry keeps the full form. A contract in the short form gets the defaults of the catalog first.
  const { contract: value, errors: short } = resolveContract(stated);
  const errors = short.length ? short : contractErrors(value, CONTRACTS);
  if (errors.length) throw new TypeError(`register: the contract ${name} is not valid: ${errors.join('; ')}`);
  const error = elementError(element, name);
  if (error) throw new TypeError(error);
  const tag = TAG_PREFIX + name;
  if (elements.get(tag)) throw new TypeError(`register: the element name ${tag} is already defined`);
  // The contract is in the registry before the definition, because the definition upgrades each element of
  // the page, and an upgrade can set a fill. If the definition fails, the registry removes the contract.
  registered.set(name, { contract: freeze(value), tag });
  try {
    elements.define(tag, element);
  } catch (failure) {
    registered.delete(name);
    throw new TypeError(`register: the element of ${name} cannot be defined: ${failure.message}`);
  }
  return tag;
}
