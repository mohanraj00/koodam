// The browser validator of a fill and of a contract. It gives the same errors as tools/catalog.py for each
// case in tests/vectors/fills.json and tests/vectors/contracts.json. A fill with an error is refused.
// The validator never changes the fill or the contract.
import CONTRACT_SCHEMA from '../../catalog/schema/contract.schema.json' with { type: 'json' };
import FILL_SCHEMA from '../../catalog/schema/fill.schema.json' with { type: 'json' };

const KEYWORDS = new Set(['$schema', '$id', '$defs', '$ref', 'title', 'description', 'type', 'properties', 'required',
  'additionalProperties', 'propertyNames', 'minProperties', 'maxProperties', 'items', 'minItems', 'maxItems',
  'uniqueItems', 'enum', 'const', 'pattern', 'minLength', 'maxLength', 'minimum', 'maximum', 'oneOf']);
const TYPE_NAMES = { object: 'an object', array: 'a list', string: 'a string', boolean: 'true or false',
  integer: 'an integer', number: 'a number', null: 'null' };
const MAX_DEPTH = 8;
// The maximum number of unknown keys that get one error each. More unknown keys get one error in all.
const MAX_UNKNOWN = 10;
// A declared name has 40 characters at most. An error shows only the first 40 characters of a longer key,
// because the size of a key can have no limit.
const MAX_NAME = 40;
const shown = (key) => {
  let start = '';
  let count = 0;
  for (const character of key) {
    count += 1;
    if (count > MAX_NAME) return start + '...';
    start += character;
  }
  return key;
};
const DATE = /^([0-9]{4})-([0-9]{2})-([0-9]{2})$/;
const own = (object, key) => Object.prototype.hasOwnProperty.call(object, key);
// Count the code points of text, but stop after limit + 1. Thus a very long text gets its error
// quickly, and the count makes no copy of the text.
const length = (text, limit = Infinity) => {
  let count = 0;
  for (const _ of text) {
    count += 1;
    if (count > limit) break;
  }
  return count;
};

function isObject(value) {
  return value !== null && typeof value === 'object' && !Array.isArray(value);
}

function isType(value, name) {
  switch (name) {
    case 'object': return isObject(value);
    case 'array': return Array.isArray(value);
    case 'string': return typeof value === 'string';
    case 'boolean': return typeof value === 'boolean';
    case 'integer': return Number.isInteger(value);
    case 'number': return typeof value === 'number' && Number.isFinite(value);
    case 'null': return value === null;
    default: throw new Error('unknown schema type ' + name);
  }
}

function canonical(value) {
  if (Array.isArray(value)) return '[' + value.map(canonical).join(',') + ']';
  if (isObject(value)) return '{' + Object.keys(value).sort().map((k) => JSON.stringify(k) + ':' + canonical(value[k])).join(',') + '}';
  return JSON.stringify(value);
}

export function schemaErrors(value, schema, root = schema, path = '', label = '(value)') {
  for (const key of Object.keys(schema)) {
    if (!KEYWORDS.has(key)) throw new Error('unsupported schema keyword ' + key);
  }
  const at = path || label;
  if (own(schema, '$ref')) {
    const ref = schema.$ref;
    if (!ref.startsWith('#/$defs/')) throw new Error('unsupported $ref ' + ref);
    return schemaErrors(value, root.$defs[ref.slice('#/$defs/'.length)], root, path, label);
  }
  if (own(schema, 'oneOf')) {
    const results = schema.oneOf.map((branch) => schemaErrors(value, branch, root, path, label));
    const passed = results.filter((errors) => errors.length === 0).length;
    if (passed === 1) return [];
    if (passed > 1) return [`${at}: matches more than one form`];
    return results.reduce((best, errors) => (errors.length < best.length ? errors : best));
  }
  if (own(schema, 'const') && canonical(value) !== canonical(schema.const)) {
    return [`${at}: must be ${JSON.stringify(schema.const)}`];
  }
  if (own(schema, 'enum') && !schema.enum.some((item) => canonical(item) === canonical(value))) {
    return [`${at}: must be one of ${schema.enum.map((item) => JSON.stringify(item)).join(', ')}`];
  }
  if (own(schema, 'type') && !isType(value, schema.type)) return [`${at}: must be ${TYPE_NAMES[schema.type]}`];
  const errors = [];
  if (typeof value === 'string') {
    const count = length(value, own(schema, 'maxLength') ? schema.maxLength : (schema.minLength ?? 0));
    if (count < (schema.minLength ?? 0)) errors.push(`${at}: fewer than ${schema.minLength} characters`);
    if (own(schema, 'maxLength') && count > schema.maxLength) errors.push(`${at}: more than ${schema.maxLength} characters`);
    if (own(schema, 'pattern') && !new RegExp(schema.pattern, 'u').test(value)) {
      errors.push(`${at}: does not agree with the pattern ${schema.pattern}`);
    }
  }
  if (isType(value, 'number')) {
    if (own(schema, 'minimum') && value < schema.minimum) errors.push(`${at}: less than the minimum ${schema.minimum}`);
    if (own(schema, 'maximum') && value > schema.maximum) errors.push(`${at}: more than the maximum ${schema.maximum}`);
  }
  if (Array.isArray(value)) {
    if (value.length < (schema.minItems ?? 0)) errors.push(`${at}: fewer than ${schema.minItems} items`);
    // Do not read the items of a list that is too long. Its size can have no limit.
    if (own(schema, 'maxItems') && value.length > schema.maxItems) return [...errors, `${at}: more than ${schema.maxItems} items`];
    if (schema.uniqueItems && new Set(value.map(canonical)).size !== value.length) errors.push(`${at}: the items are not unique`);
    if (own(schema, 'items')) {
      value.forEach((item, index) => errors.push(...schemaErrors(item, schema.items, root, `${path}[${index}]`, label)));
    }
  }
  if (isObject(value)) {
    const keys = Object.keys(value);
    if (keys.length < (schema.minProperties ?? 0)) errors.push(`${at}: fewer than ${schema.minProperties} keys`);
    // Do not read the keys of an object that is too large. Its size can have no limit.
    if (own(schema, 'maxProperties') && keys.length > schema.maxProperties) return [...errors, `${at}: more than ${schema.maxProperties} keys`];
    // An object with no maxProperties can have very many unknown keys. Do not make an error for each of them.
    const properties = schema.properties ?? {};
    if (schema.additionalProperties === false) {
      let unknown = 0;
      for (const key of keys) {
        if (!own(properties, key) && ++unknown > MAX_UNKNOWN) return [...errors, `${at}: more than ${MAX_UNKNOWN} unknown keys`];
      }
    }
    for (const key of schema.required ?? []) {
      if (!own(value, key)) errors.push(`${at}: missing key ${key}`);
    }
    for (const key of keys) {
      const child = path ? `${path}.${shown(key)}` : shown(key);
      if (own(schema, 'propertyNames')) {
        for (const error of schemaErrors(key, schema.propertyNames, root, child, label)) {
          errors.push(`${child}: the key name ${error.split(': ').slice(1).join(': ')}`);
        }
      }
      if (own(properties, key)) errors.push(...schemaErrors(value[key], properties[key], root, child, label));
      else if (schema.additionalProperties === false) errors.push(path ? `${path}: unknown key ${shown(key)}` : `${shown(key)}: unknown key`);
      else if (isObject(schema.additionalProperties)) errors.push(...schemaErrors(value[key], schema.additionalProperties, root, child, label));
    }
  }
  return errors;
}

// Each field of a contract, with the items of a list. The contract must agree with the schema.
function fieldsOf(contract) {
  return Object.entries(contract.data).flatMap(([name, field]) =>
    field.type === 'list' ? [['data.' + name, field], [`data.${name}.items`, field.items]] : [['data.' + name, field]]);
}

// The component names that a component field accepts: its name, or each item of its names.
const targets = (field) => (own(field, 'names') ? field.names : [field.name]);
const references = (contract) => fieldsOf(contract).filter(([, field]) => field.type === 'component')
  .flatMap(([path, field]) => targets(field).map((target) => [path, target]));

function reaches(catalog, start, goal) {
  const seen = new Set();
  const todo = [start];
  while (todo.length) {
    const name = todo.pop();
    if (name === goal) return true;
    if (seen.has(name) || !own(catalog, name)) continue;
    seen.add(name);
    todo.push(...references(catalog[name]).map(([, target]) => target));
  }
  return false;
}

// The errors of one contract, with the rules of tools/catalog.py (catalog/schema/README.md). catalog maps a
// component name to a valid contract. Each component field must name a contract in catalog.
export function contractErrors(contract, catalog) {
  const errors = schemaErrors(contract, CONTRACT_SCHEMA, CONTRACT_SCHEMA, '', '(contract)');
  if (errors.length) return errors;
  if (!contract.states.includes('default')) errors.push('states: must include default');
  const { accessibility } = contract;
  if (accessibility.role === 'none' && own(accessibility, 'name')) {
    errors.push('accessibility.name: a component with the role none has no accessible name');
  }
  if (accessibility.role !== 'none' && !own(accessibility, 'name')) {
    errors.push('accessibility.name: a component with a role needs an accessible name');
  }
  if (own(accessibility.name ?? {}, 'field')) {
    const field = own(contract.data, accessibility.name.field) ? contract.data[accessibility.name.field] : undefined;
    if (!field || field.type !== 'text' || !field.required) errors.push('accessibility.name: the field must be a required text field');
  }
  for (const [path, field] of fieldsOf(contract)) {
    if ((field.min_length ?? 0) > (field.max_length ?? Infinity)) errors.push(path + ': min_length is more than max_length');
    if ((field.minimum ?? -Infinity) > (field.maximum ?? Infinity)) errors.push(path + ': minimum is more than maximum');
  }
  if (contract.ai_content && !contract.slots?.authorship?.required) {
    errors.push('slots.authorship: a contract with ai_content must declare a required authorship slot');
  }
  for (const [path, target] of references(contract)) {
    if (!own(catalog, target)) errors.push(`${path}: no contract has the name ${target}`);
    else if (reaches(catalog, target, contract.name)) errors.push(`${path}: the contract contains itself through ${target}`);
  }
  return errors;
}

export function isDate(value) {
  const match = typeof value === 'string' ? DATE.exec(value) : null;
  if (!match) return false;
  const [year, month, day] = match.slice(1).map(Number);
  if (year < 1) return false;
  const date = new Date(Date.UTC(2000, month - 1, day));
  date.setUTCFullYear(year);
  return date.getUTCFullYear() === year && date.getUTCMonth() === month - 1 && date.getUTCDate() === day;
}

function valueErrors(value, field, path, catalog, depth) {
  switch (field.type) {
    case 'text':
      if (typeof value !== 'string') return [`${path}: must be a string`];
      if (length(value, field.max_length) > field.max_length) return [`${path}: more than ${field.max_length} characters`];
      if (length(value, field.max_length) < (field.min_length ?? 0)) return [`${path}: fewer than ${field.min_length} characters`];
      return [];
    case 'number':
    case 'integer':
      if (!isType(value, field.type)) return [`${path}: must be ${field.type === 'integer' ? 'an integer' : 'a number'}`];
      if (own(field, 'minimum') && value < field.minimum) return [`${path}: less than the minimum ${field.minimum}`];
      if (own(field, 'maximum') && value > field.maximum) return [`${path}: more than the maximum ${field.maximum}`];
      return [];
    case 'boolean':
      return typeof value === 'boolean' ? [] : [`${path}: must be true or false`];
    case 'date':
      return isDate(value) ? [] : [`${path}: must be a date as YYYY-MM-DD`];
    case 'enum':
      return typeof value === 'string' && field.values.includes(value) ? [] : [`${path}: must be one of ${field.values.join(', ')}`];
    case 'list': {
      if (!Array.isArray(value)) return [`${path}: must be a list`];
      if (value.length > field.max_items) return [`${path}: more than ${field.max_items} items`];
      return value.flatMap((item, index) => valueErrors(item, field.items, `${path}[${index}]`, catalog, depth));
    }
    case 'component':
      if (isObject(value) && !targets(field).includes(value.component)) return [`${path}.component: must be ${targets(field).join(' or ')}`];
      return fillErrors(value, catalog, path, depth + 1);
    default:
      throw new Error('unknown field type ' + field.type);
  }
}

// catalog maps a component name to its contract. CI validates each contract with tools/catalog.py.
export function fillErrors(fill, catalog, path = '', depth = 0) {
  const prefix = path ? path + '.' : '';
  if (depth > MAX_DEPTH) return [`${path || '(fill)'}: the fill is nested too deep`];
  const errors = schemaErrors(fill, FILL_SCHEMA, FILL_SCHEMA, path, '(fill)');
  if (errors.length) return errors;
  const contract = own(catalog, fill.component) ? catalog[fill.component] : undefined;
  if (contract === undefined) return [`${prefix}component: no contract has the name ${fill.component}`];
  if (fill.version !== contract.version) return [`${prefix}version: must be ${contract.version}`];
  const data = fill.data;
  for (const key of Object.keys(data)) {
    if (!own(contract.data, key)) errors.push(`${prefix}data.${shown(key)}: the contract does not declare this field`);
  }
  for (const [key, field] of Object.entries(contract.data)) {
    if (own(data, key)) errors.push(...valueErrors(data[key], field, `${prefix}data.${key}`, catalog, depth));
    else if (field.required) errors.push(`${prefix}data.${key}: a required field is missing`);
  }
  return [...errors, ...slotErrors(fill, contract, prefix, depth)];
}

// Only the outer fill has slots. A nested fill takes the slots of its outer fill.
// The author in a fill is a claim of the model. Only the actor that the application or the thread gives is
// trusted (CONSTITUTION.md, principle 3). Call this only for a fill that fillErrors accepts.
const ACTOR_TYPES = new Set(['human', 'agent', 'system']);

export function actorErrors(actor) {
  if (actor === null) return [];
  if (actor === undefined || typeof actor !== 'object' || Array.isArray(actor)) return ['must be an object or null'];
  if (!ACTOR_TYPES.has(actor.type)) return ['type: must be human, agent or system'];
  if (typeof actor.id !== 'string' || actor.id === '' || length(actor.id, 128) > 128) return ['id: must be a string of 1 to 128 characters'];
  if (own(actor, 'name') && (typeof actor.name !== 'string' || actor.name === '' || length(actor.name, 80) > 80)) {
    return ['name: must be a string of 1 to 80 characters'];
  }
  return [];
}

export function authorErrors(fill, actor) {
  const author = fill.slots?.authorship;
  if (!author) return [];
  if (!actor) return ['slots.authorship: no actor is bound, so the author cannot be checked'];
  // A fill with no name shows only the type and the id. A fill with a name must give the name of the bound actor.
  const name = own(author, 'name') && author.name !== actor.name;
  if (author.type !== actor.type || author.id !== actor.id || name) return ['slots.authorship: the author is not the bound actor'];
  return [];
}

function slotErrors(fill, contract, prefix, depth) {
  const declared = contract.slots ?? {};
  if (depth > 0) return own(fill, 'slots') ? [`${prefix}slots: a nested fill takes the slots of its outer fill`] : [];
  const filled = fill.slots ?? {};
  return [
    ...Object.keys(filled).filter((key) => !own(declared, key)).map((key) => `${prefix}slots.${key}: the contract does not declare this slot`),
    ...Object.entries(declared).filter(([key, slot]) => slot.required && !own(filled, key)).map(([key]) => `${prefix}slots.${key}: a required slot is missing`),
  ];
}
