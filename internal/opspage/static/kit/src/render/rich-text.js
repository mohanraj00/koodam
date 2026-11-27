// Helpers for the rich text display, editor and action path.
import { CATALOG } from './catalog.js';

export { RICH_TEXT_EDIT_LIMIT, RICH_TEXT_MAX_BLOCKS, RICH_TEXT_SCHEMES, richHeadingLevel, richTextEditProblem,
  richTextLinkSchemes, richTextRules, richTextValueErrors, setRichTextLinkSchemes, watchRichTextLinkSchemes } from './rich-text-value.js';

export function richNode(component, data) {
  return { component, version: CATALOG[component].version, data };
}

// Compare two validated values. The order of the keys in a fill has no effect.
export function sameRichTextValue(first, second) {
  if (first === second) return true;
  if (!first || !second || first.component !== 'rich-text' || second.component !== 'rich-text') return false;
  function same(a, b) {
    if (a === b) return true;
    if (a === null || b === null || typeof a !== 'object' || typeof b !== 'object') return false;
    const keys = Object.keys(a);
    return keys.length === Object.keys(b).length && keys.every((key) => Object.hasOwn(b, key) && same(a[key], b[key]));
  }
  return same(first, second);
}
