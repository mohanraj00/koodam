// A rich text value is a nested fill. The catalog agreement test is in tests/rich-text.test.mjs.
export const RICH_TEXT_SCHEMES = Object.freeze(['https:', 'http:', 'mailto:']);
export const RICH_TEXT_MAX_BLOCKS = 100;
export const RICH_TEXT_EDIT_LIMIT = 'The edit does not fit the rich text rules. Use less text or formatting.';
const VERSION = '0.1.0';
const MAX_HEADING = 3;
const MAX_INLINE = 2000;
const BLOCKS = ['rich-paragraph', 'rich-heading', 'rich-list', 'rich-code', 'rich-quote'];
const INLINE = ['rich-span', 'rich-link'];
const MARKS = ['emphasis', 'strong', 'code'];
const MAILBOX = /^[a-z0-9_+'-]+(?:\.[a-z0-9_+'-]+)*@[a-z0-9](?:[a-z0-9-]*[a-z0-9])?(?:\.[a-z0-9](?:[a-z0-9-]*[a-z0-9])?)*$/i;
const MAIL_SPACE = /[\s\u0000-\u001f\u007f]/u;

const linkWatchers = new Set();
let linkSchemes = Object.freeze(['https:']);

export function richTextLinkSchemes() {
  return linkSchemes;
}

export function setRichTextLinkSchemes(values) {
  if (!Array.isArray(values) || !values.every((value) => RICH_TEXT_SCHEMES.includes(value))) {
    throw new TypeError('link schemes: must be an array of ' + RICH_TEXT_SCHEMES.join(', '));
  }
  linkSchemes = Object.freeze([...new Set(values)]);
  for (const watch of linkWatchers) watch();
}

export function watchRichTextLinkSchemes(watch) {
  linkWatchers.add(watch);
  return () => linkWatchers.delete(watch);
}

// Parse a link under the scheme policy of its component. A web address has a host and no credentials.
export function parseLinkURL(value, schemes, base) {
  let url;
  try {
    url = new URL(value, base);
  } catch {
    return null;
  }
  if (!schemes.includes(url.protocol)) return null;
  if (url.protocol === 'mailto:') {
    // Check the decoded mailbox and the URL separators, including an empty query or fragment.
    if (MAIL_SPACE.test(value) || url.host || /[?#]/u.test(url.href)) return null;
    try {
      const address = decodeURIComponent(url.pathname);
      return !MAIL_SPACE.test(address) && MAILBOX.test(address) ? url : null;
    } catch {
      return null;
    }
  }
  return url.hostname && !url.username && !url.password ? url : null;
}

const isObject = (value) => value !== null && typeof value === 'object' && !Array.isArray(value);
const own = Object.hasOwn;
const FILL_KEYS = ['component', 'version', 'data'];

function text(maximum) {
  return (value, path) => {
    if (typeof value !== 'string') return `${path}: must be a string`;
    if (!value) return `${path}: fewer than 1 characters`;
    let count = 0;
    for (const _ of value) {
      if (++count > maximum) return `${path}: more than ${maximum} characters`;
    }
    return '';
  };
}

function integer(maximum) {
  return (value, path) => {
    if (!Number.isInteger(value)) return `${path}: must be an integer`;
    if (value < 1) return `${path}: less than the minimum 1`;
    return value > maximum ? `${path}: more than the maximum ${maximum}` : '';
  };
}

function nodes(names, maximum) {
  return (list, path) => {
    if (!Array.isArray(list)) return `${path}: must be a list`;
    if (list.length > maximum) return `${path}: more than ${maximum} items`;
    for (let index = 0; index < list.length; index += 1) {
      const node = own(list, index) ? list[index] : undefined;
      const error = nodeError(node, names, `${path}[${index}]`);
      if (error) return error;
    }
    return '';
  };
}

const content = nodes(INLINE, 50);
const paragraphs = (maximum) => nodes(['rich-paragraph'], maximum);
const mark = (value, path) => MARKS.includes(value) ? '' : `${path}: must be one of ${MARKS.join(', ')}`;
const boolean = (value, path) => typeof value === 'boolean' ? '' : `${path}: must be true or false`;

// The third entry of a field marks it as optional.
const FIELDS = {
  'rich-text': [['blocks', nodes(BLOCKS, RICH_TEXT_MAX_BLOCKS)], ['clamp', integer(20), true]],
  'rich-paragraph': [['content', content]],
  'rich-heading': [['level', integer(MAX_HEADING)], ['content', content]],
  'rich-list': [['ordered', boolean, true], ['items', paragraphs(50)]],
  'rich-code': [['text', text(10000)]],
  'rich-quote': [['paragraphs', paragraphs(20)]],
  'rich-span': [['text', text(2000)], ['mark', mark, true]],
  'rich-link': [['text', text(200)], ['url', text(2000)]],
};

function nodeError(node, names, path) {
  if (!isObject(node)) return `${path}: must be an object`;
  if (!names.includes(node.component)) return `${path}.component: must be ${names.join(' or ')}`;
  for (const key of FILL_KEYS) {
    if (!own(node, key)) return `${path}: missing key ${key}`;
  }
  if (node.version !== VERSION) return `${path}.version: must be ${VERSION}`;
  const data = node.data;
  if (!isObject(data)) return `${path}.data: must be an object`;
  if (own(node, 'slots')) return `${path}.slots: a nested fill takes the slots of its outer fill`;
  for (const key of Object.keys(node)) {
    if (!FILL_KEYS.includes(key)) return `${path}: unknown key ${key.slice(0, 40)}`;
  }
  const fields = FIELDS[node.component];
  for (const key of Object.keys(data)) {
    if (!fields.some(([name]) => key === name)) return `${path}.data.${key.slice(0, 40)}: the contract does not declare this field`;
  }
  for (const [name, check, optional] of fields) {
    const at = `${path}.data.${name}`;
    if (own(data, name)) {
      const error = check(data[name], at);
      if (error) return error;
    } else if (!optional) return `${at}: a required field is missing`;
  }
  return '';
}

export function richHeadingLevel(level, previous) {
  return Math.min(level, previous + 1, MAX_HEADING);
}

// The inline content of each paragraph, heading, list item and quote paragraph, with its path in the fill.
function* runs(blocks) {
  for (const [index, { component, data }] of blocks.entries()) {
    const at = `data.blocks[${index}].data`;
    if (component === 'rich-paragraph' || component === 'rich-heading') yield [at, data.content];
    if (component === 'rich-list') {
      for (const [item, node] of data.items.entries()) yield [`${at}.items[${item}].data`, node.data.content];
    }
    if (component === 'rich-quote') {
      for (const [item, node] of data.paragraphs.entries()) yield [`${at}.paragraphs[${item}].data`, node.data.content];
    }
  }
}

function skippedHeading(blocks) {
  let level = 0;
  for (const [index, block] of blocks.entries()) {
    if (block.component !== 'rich-heading') continue;
    const maximum = richHeadingLevel(block.data.level, level);
    if (block.data.level !== maximum) return { index, maximum };
    level = block.data.level;
  }
  return null;
}

function contentError(data, schemes) {
  let count = 0;
  for (const [at, content] of runs(data.blocks)) {
    count += content.length;
    if (count > MAX_INLINE) return `data.blocks: more than ${MAX_INLINE} inline nodes`;
    for (const [index, node] of content.entries()) {
      if (node.component === 'rich-link' && !parseLinkURL(node.data.url, schemes)) {
        return `${at}.content[${index}].data.url: the page does not permit this link`;
      }
    }
  }
  return '';
}

// The contract format does not give the heading order, total inline count or link policy.
export function richTextRules(data, schemes = richTextLinkSchemes()) {
  const heading = skippedHeading(data.blocks);
  const error = heading ? `data.blocks[${heading.index}].data.level: a heading cannot skip a level` : contentError(data, schemes);
  return error ? [error] : [];
}

function shapeError(value) {
  return value?.component === 'rich-text' ? nodeError(value, ['rich-text'], 'value') : 'value.component: must be rich-text';
}

export function richTextValueErrors(value, schemes = richTextLinkSchemes()) {
  const error = shapeError(value);
  return error ? [error] : richTextRules(value.data, schemes);
}

export function richTextEditProblem(value) {
  if (shapeError(value) || contentError(value.data, richTextLinkSchemes())) return RICH_TEXT_EDIT_LIMIT;
  const heading = skippedHeading(value.data.blocks);
  return heading ? `A heading cannot skip a level. Use Heading ${heading.maximum} first.` : '';
}
