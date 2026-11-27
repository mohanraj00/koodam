// The browser validator of a fill and of a contract. It gives the same errors as tools/catalog.py for each
// case in tests/vectors/fills.json and tests/vectors/contracts.json. A fill with an error is refused.
// The validator never changes the fill or the contract.
import CONTRACT_SCHEMA from '../../catalog/schema/contract.schema.json' with { type: 'json' };
import FILL_SCHEMA from '../../catalog/schema/fill.schema.json' with { type: 'json' };
import { isNumberPair, rangeErrors } from './range.js';

export { parseLinkURL } from './rich-text-value.js';

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
const TIME = /^([01][0-9]|2[0-3]):([0-5][0-9])$/;
const TIME_ZONE = /^[A-Za-z_][A-Za-z0-9_+-]*(\/[A-Za-z_][A-Za-z0-9_+-]*)+$/;
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

// Each field of a contract, with the items of a list and the fields of a record.
function fieldsOf(contract) {
  return Object.entries(contract.data).flatMap(([name, field]) => {
    const path = 'data.' + name;
    if (field.type !== 'list') return [[path, field]];
    const item = path + '.items';
    return [[path, field], [item, field.items],
      ...Object.entries(field.items.fields ?? {}).map(([key, nested]) => [item + '.fields.' + key, nested])];
  });
}

// The component names that a component field accepts: its name, or each item of its names.
const targets = (field) => (own(field, 'names') ? field.names : [field.name]);
const references = (contract) => fieldsOf(contract).filter(([, field]) => field.type === 'component' && !field.field)
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
  const partNames = new Set();
  (contract.parts ?? []).forEach(({ name }, index) => {
    if (partNames.has(name)) errors.push(`parts[${index}].name: the part name is not unique`);
    partNames.add(name);
  });
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
    if ((field.min_items ?? 0) > (field.max_items ?? Infinity)) errors.push(path + ': min_items is more than max_items');
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

function outsideLimits(value, { earliest, latest }) {
  return (earliest !== undefined && value < earliest) || (latest !== undefined && value > latest);
}

export function dateRangeProblem([start, end], limits) {
  for (const [index, label, date, message] of [
    [0, 'start', start, 'Give a start date that exists.'],
    [1, 'end', end, 'Give an end date that exists.'],
  ]) {
    if (!isDate(date)) return { index, message };
    if (outsideLimits(date, limits)) return { index, message: 'The ' + label + ' date is outside the range.' };
  }
  return end < start ? { message: 'The end date is before the start date.' } : null;
}

function timeMinutes(value) {
  const match = TIME.exec(value);
  return match ? Number(match[1]) * 60 + Number(match[2]) : null;
}

export function timeProblem(value, limits) {
  const minutes = timeMinutes(value);
  if (minutes === null) return 'must be a time as HH:MM';
  if (outsideLimits(value, limits)) return 'the time is outside the range';
  const start = timeMinutes(limits.earliest) ?? 0;
  return (minutes - start) % (limits.step ?? 1) ? 'the time is off a step' : null;
}

export function isLocalDateTime(value) {
  return value.length === 16 && value[10] === 'T' && isDate(value.slice(0, 10)) && timeMinutes(value.slice(11)) !== null;
}

export function hasTimeZoneForm(name) {
  return typeof name === 'string' && TIME_ZONE.test(name);
}

function dateAndTimeErrors(name, data, prefix) {
  const { value, earliest, latest } = data;
  if (name === 'date-range-field') {
    if (earliest > latest) return [prefix + 'earliest: the earliest date is after the latest date'];
    const problem = dateRangeProblem(value, data);
    if (!problem) return [];
    if (problem.index !== undefined) return [prefix + `value[${problem.index}]: the date is outside the range`];
    return [prefix + 'value: the end date is before the start date'];
  }
  if (name === 'time-field') {
    for (const [key, bound] of [['earliest', earliest], ['latest', latest]]) {
      if (bound !== undefined && timeMinutes(bound) === null) return [prefix + key + ': must be a time as HH:MM'];
    }
    if (earliest > latest) return [prefix + 'earliest: the earliest time is after the latest time'];
    const problem = timeProblem(value, data);
    return problem ? [prefix + 'value: ' + problem] : [];
  }
  if (!isLocalDateTime(value)) return [prefix + 'value: must be a date and time as YYYY-MM-DDTHH:MM'];
  return hasTimeZoneForm(data.time_zone) ? [] : [prefix + 'time_zone: must have the form Area/Location'];
}

function valueErrors(value, field, path, catalog, depth) {
  switch (field.type) {
    case 'text':
      if (typeof value !== 'string') return [`${path}: must be a string`];
      if (length(value, field.max_length) > field.max_length) return [`${path}: more than ${field.max_length} characters`];
      if (length(value, field.max_length) < (field.min_length ?? 0)) return [`${path}: fewer than ${field.min_length} characters`];
      if (field.format === 'name') return schemaErrors(value, CONTRACT_SCHEMA.$defs.field_name, CONTRACT_SCHEMA, path);
      return [];
    case 'number':
    case 'integer':
      if (!isType(value, field.type)) return [`${path}: must be ${field.type === 'integer' ? 'an integer' : 'a number'}`];
      if (own(field, 'minimum') && value < field.minimum) return [`${path}: less than the minimum ${field.minimum}`];
      if (own(field, 'maximum') && value > field.maximum) return [`${path}: more than the maximum ${field.maximum}`];
      return [];
    case 'boolean':
      return typeof value === 'boolean' ? [] : [`${path}: must be true or false`];
    case 'number-or-pair':
      return Number.isFinite(value) || isNumberPair(value) ? [] : [`${path}: must be a number or a pair of numbers`];
    case 'date':
      return isDate(value) ? [] : [`${path}: must be a date as YYYY-MM-DD`];
    case 'enum':
      return typeof value === 'string' && field.values.includes(value) ? [] : [`${path}: must be one of ${field.values.join(', ')}`];
    case 'list': {
      if (!Array.isArray(value)) return [`${path}: must be a list`];
      if (value.length < (field.min_items ?? 0)) return [`${path}: fewer than ${field.min_items} items`];
      if (value.length > field.max_items) return [`${path}: more than ${field.max_items} items`];
      const errors = [];
      for (let index = 0; index < value.length; index += 1) {
        errors.push(...valueErrors(own(value, index) ? value[index] : undefined, field.items, `${path}[${index}]`, catalog, depth));
      }
      if (!errors.length && field.unique_items && new Set(value.map(canonical)).size !== value.length) {
        errors.push(`${path}: the items are not unique`);
      }
      return errors;
    }
    case 'record':
      return isObject(value) ? dataErrors(value, field.fields, path + '.', catalog, depth) : [path + ': must be an object'];
    case 'component':
      if (isObject(value)) {
        const name = value.component;
        if (field.field) {
          if (typeof name === 'string' && own(catalog, name) && catalog[name].field !== true) {
            return [`${path}.component: must be a field`];
          }
        } else if (!targets(field).includes(name)) return [`${path}.component: must be ${targets(field).join(' or ')}`];
      }
      return fillErrors(value, catalog, path, depth + 1);
    default:
      throw new Error('unknown field type ' + field.type);
  }
}

export function multiChoiceCountLimit(count, { min_items = 0, max_items = 500 }) {
  if (count < min_items) return 'min_items';
  if (count > max_items) return 'max_items';
  return null;
}

// Use the same code points and grammar as is_navigation_href in tools/catalog.py.
const NAVIGATION_FORBIDDEN = /[\x00-\x20\x7f-\x9f\u00a0\u1680\u2000-\u200a\u2028\u2029\u202f\u205f\u3000\ufeff\\]/;
const NAVIGATION_HTTPS = /^https:\/\/([^:/?#]+)(?::([0-9]{1,5}))?(?:[/?#].*)?$/i;
const DNS_LABEL = /^[A-Za-z0-9](?:[A-Za-z0-9-]{0,61}[A-Za-z0-9])?$/;

export function navigationHref(value) {
  if (typeof value !== 'string' || !value || NAVIGATION_FORBIDDEN.test(value)) return null;
  const address = NAVIGATION_HTTPS.exec(value);
  if (!address) {
    return value.startsWith('//') || /^[A-Za-z][A-Za-z0-9+.-]*:/.test(value) ? null : value;
  }
  const [, host, port] = address;
  if (/^[0-9.]+$/.test(host) || !host.split('.').every((label) => DNS_LABEL.test(label))) return null;
  return port === undefined || Number(port) <= 65535 ? value : null;
}

function multiChoiceErrors({ choices, value = [], ...limits }, prefix) {
  if (limits.min_items > limits.max_items) return [prefix + 'min_items: must be at most max_items'];
  const limit = multiChoiceCountLimit(value.length, limits);
  if (limit) return [prefix + 'value: ' + (limit === 'min_items' ? 'fewer' : 'more') + ' than ' + limits[limit] + ' items'];
  const positions = new Map(choices.map((choice, index) => [choice, index]));
  let previous = -1;
  for (const [index, choice] of value.entries()) {
    const position = positions.get(choice);
    if (position === undefined) return [prefix + `value[${index}]: the value is not one of the choices`];
    if (position <= previous) return [prefix + 'value: use the order of the choices'];
    previous = position;
  }
  return [];
}

function dataErrors(data, fields, prefix, catalog, depth) {
  const keys = Object.keys(data);
  const max = FILL_SCHEMA.properties.data.maxProperties;
  if (keys.length > max) return [`${prefix.slice(0, -1)}: more than ${max} keys`];
  const errors = [];
  for (const key of keys) {
    if (!own(fields, key)) errors.push(`${prefix}${shown(key)}: the contract does not declare this field`);
  }
  for (const [key, field] of Object.entries(fields)) {
    if (own(data, key)) errors.push(...valueErrors(data[key], field, prefix + key, catalog, depth));
    else if (field.required) errors.push(`${prefix}${key}: a required field is missing`);
  }
  return errors;
}

const WORK_ITEMS = new Map([
  ['run-steps', ['steps', 'name']],
  ['plan', ['tasks', 'name']],
  ['approval-queue', ['items', 'title', 'summary']],
]);

const PAGE_TEXT_FIELDS = new Map([
  ['empty-state', ['title', 'text']], ['skeleton', ['label']], ['badge', ['text']], ['avatar', ['name']],
]);

// catalog maps a component name to its contract. CI validates each contract with tools/catalog.py.
export function fillErrors(fill, catalog, path = '', depth = 0) {
  const prefix = path ? path + '.' : '';
  if (depth > MAX_DEPTH) return [`${path || '(fill)'}: the fill is nested too deep`];
  const schema = schemaErrors(fill, FILL_SCHEMA, FILL_SCHEMA, path, '(fill)');
  if (schema.length) return schema;
  const contract = own(catalog, fill.component) ? catalog[fill.component] : undefined;
  if (contract === undefined) return [`${prefix}component: no contract has the name ${fill.component}`];
  if (fill.version !== contract.version) return [`${prefix}version: must be ${contract.version}`];
  const data = fill.data;
  const errors = dataErrors(data, contract.data, prefix + 'data.', catalog, depth);
  if (!errors.length && fill.component === 'range-field' && contract.data.value?.type === 'number-or-pair'
    && [data.minimum, data.maximum, data.step].every(Number.isFinite) && own(data, 'value')) {
    errors.push(...rangeErrors(data, prefix + 'data.'));
  }
  if (!errors.length && fill.component === 'select-field' && contract.data.choices?.items?.type === 'text' && Array.isArray(data.choices)) {
    if (data.choices.length === 0) errors.push(prefix + 'data.choices: give at least one choice');
    else if (new Set(data.choices).size !== data.choices.length) errors.push(prefix + 'data.choices: the choices must be unique');
    else if (own(data, 'value') && !data.choices.includes(data.value)) errors.push(prefix + 'data.value: the value is not one of the choices');
  }
  if (!errors.length && fill.component === 'multi-choice-field' && contract.data.value?.type === 'list'
    && Array.isArray(data.choices)) {
    errors.push(...multiChoiceErrors(data, prefix + 'data.'));
  }
  if (!errors.length && ['date-range-field', 'time-field', 'date-time-field'].includes(fill.component)) {
    errors.push(...dateAndTimeErrors(fill.component, data, prefix + 'data.'));
  }
  if (!errors.length && fill.component === 'breadcrumbs') {
    for (const [index, { href }] of data.items.entries()) {
      if ((index < data.items.length - 1 || href !== undefined) && navigationHref(href) === null) {
        errors.push(prefix + `data.items[${index}].href: give a relative path or an https address`);
      }
    }
  }
  if (!errors.length && fill.component === 'pagination' && data.page > data.page_count) {
    errors.push(prefix + 'data.page: must be at most page_count');
  }
  if (!errors.length && (fill.component === 'steps' || fill.component === 'progress')) {
    if (!data.label.trim()) errors.push(prefix + 'data.label: give text');
    if (fill.component === 'steps') {
      for (const [index, label] of data.labels.entries()) {
        if (!label.trim()) errors.push(prefix + `data.labels[${index}]: give text`);
      }
      if (data.current >= data.labels.length) errors.push(prefix + 'data.current: must be within labels');
    } else if (own(data, 'value') && data.value > data.maximum) {
      errors.push(prefix + 'data.value: must be at most maximum');
    }
  }
  const workItems = WORK_ITEMS.get(fill.component);
  if (!errors.length && workItems) {
    const [key, ...fields] = workItems;
    if (!data.label.trim()) errors.push(prefix + 'data.label: give text');
    const ids = new Set();
    for (const [index, item] of data[key].entries()) {
      const at = prefix + `data.${key}[${index}].`;
      if (!item.id.trim()) errors.push(at + 'id: give text');
      else if (ids.has(item.id)) errors.push(at + 'id: must be unique');
      ids.add(item.id);
      for (const field of fields) {
        if (!item[field].trim()) errors.push(at + field + ': give text');
      }
      if (key === 'steps' && ['tool_name', 'tool_arguments', 'tool_result'].some((field) => own(item, field))
        && !(own(item, 'tool_name') && item.tool_name.trim())) errors.push(at + 'tool_name: give a name for the tool call');
    }
  }
  if (!errors.length) {
    for (const key of PAGE_TEXT_FIELDS.get(fill.component) ?? []) {
      if (!data[key].trim()) errors.push(prefix + `data.${key}: give text`);
    }
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
