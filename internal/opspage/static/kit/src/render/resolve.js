// The resolver of the short form of a contract (format 3, catalog/schema/README.md). It gives the same
// result and the same errors as resolve in tools/catalog.py for each case in tests/vectors/short.json.
// The catalog, the registry and the validator read only the full form (format 2).
import CONTRACT_SCHEMA from '../../catalog/schema/contract.schema.json' with { type: 'json' };
import DEFAULTS from '../../catalog/schema/defaults.json' with { type: 'json' };
import SHORT_SCHEMA from '../../catalog/schema/short.schema.json' with { type: 'json' };
import { schemaErrors } from './validate.js';

// A list item with this mark extends the default list. A list with no mark replaces the default list.
const MARK = '+';
const own = (object, key) => Object.prototype.hasOwnProperty.call(object, key);
const isObject = (value) => value !== null && typeof value === 'object' && !Array.isArray(value);
const stated = (object, key, fallback) => (own(object, key) ? object[key] : fallback);

// The default label of a field: its key with spaces and a capital first letter.
export const labelOf = (key) => {
  const text = key.replaceAll('_', ' ');
  return text.slice(0, 1).toUpperCase() + text.slice(1);
};

function mergedList(list, base, path, errors) {
  if (list === undefined) return [...base];
  const marked = list.filter((item) => item.startsWith(MARK)).length;
  if (list.length && marked === list.length) return [...base, ...list.map((item) => item.slice(MARK.length))];
  if (marked) errors.push(`${path}: put the ${MARK} mark on each item or on no item`);
  return [...list];
}

// The full form of a short field. The keys type, label and required come first.
function resolvedField(key, field, path, defaults, errors, tier = 'field') {
  const full = [['type', field.type], ['label', stated(field, 'label', labelOf(key))],
    ['required', stated(field, 'required', defaults[tier].required)]];
  for (const [name, value] of Object.entries(field)) {
    if (name === 'type' || name === 'label' || name === 'required') continue;
    if (name === 'size') {
      if (!own(defaults.sizes, value)) errors.push(`${path}.size: no size has the name ${value}`);
      else if (!own(field, 'max_length')) full.push(['max_length', defaults.sizes[value]]);
    } else if (name === 'items') {
      full.push(['items', resolvedField(key, value, path + '.items', defaults, errors, 'item')]);
    } else {
      full.push([name, value]);
    }
  }
  return Object.fromEntries(full);
}

// The full form (format 2) of a contract, and the errors of the short form: { contract, errors }.
// A contract with no format key, or with the format of the defaults file, has the short form. Each other
// contract comes back with no change, and contractErrors validates it as the full form. The result of a
// short contract is a copy with the key order of contract.schema.json. Validate it with contractErrors.
// checked is false only for the catalog files of the kit, because CI validates each of them against
// short.schema.json. Then the page does not validate 45 contracts again on each load.
export function resolveContract(contract, { defaults = DEFAULTS, checked = true } = {}) {
  if (isObject(contract) && own(contract, 'format') && contract.format !== defaults.format) return { contract, errors: [] };
  const errors = checked ? schemaErrors(contract, SHORT_SCHEMA, SHORT_SCHEMA, '', '(contract)') : [];
  if (errors.length) return { contract: undefined, errors };
  const base = defaults.contract;
  const aiContent = stated(contract, 'ai_content', base.ai_content);
  const access = { ...base.accessibility, ...stated(contract, 'accessibility', {}) };
  const values = {
    format: CONTRACT_SCHEMA.properties.format.const,
    name: contract.name,
    version: stated(contract, 'version', base.version),
    kind: stated(contract, 'kind', base.kind),
    description: contract.description,
    ai_content: aiContent,
    slots: { ...(aiContent ? base.slots : {}), ...stated(contract, 'slots', {}) },
    data: Object.fromEntries(Object.entries(contract.data).map(([key, field]) =>
      [key, resolvedField(key, field, 'data.' + key, defaults, errors)])),
    states: mergedList(stated(contract, 'states', undefined), base.states, 'states', errors),
    tokens: mergedList(stated(contract, 'tokens', undefined), base.tokens, 'tokens', errors),
    accessibility: Object.fromEntries(Object.keys(CONTRACT_SCHEMA.properties.accessibility.properties)
      .filter((key) => own(access, key)).map((key) => [key, access[key]])),
  };
  if (errors.length) return { contract: undefined, errors };
  const keys = Object.keys(CONTRACT_SCHEMA.properties).filter((key) => key !== 'slots' || Object.keys(values.slots).length);
  // The result is a copy, so a change to it does not change the defaults or the short contract.
  return { contract: structuredClone(Object.fromEntries(keys.map((key) => [key, values[key]]))), errors: [] };
}
