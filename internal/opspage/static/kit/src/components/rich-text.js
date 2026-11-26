// Rich text as a tree of typed nodes (catalog/rich-text.json). A fill gives data only. The kit makes each
// element from the fixed tables below, and each text goes into a text node. Page code owns the link
// schemes and the first heading level. src/components/rich-text.md gives the rules.
import { ReadMoreElement, READ_MORE_CSS } from '../primitives/read-more.js';
import { copyControl, el, sheet, text } from '../render/dom.js';

// The schemes that page code can permit. No other scheme can open.
const SCHEMES = Object.freeze(['https:', 'http:', 'mailto:']);
// The element of each heading level of the page, from level 2 to level 6.
const HEADINGS = Object.freeze(['h2', 'h3', 'h4', 'h5', 'h6']);
const MARKS = Object.freeze({ emphasis: 'em', strong: 'strong', code: 'code' });
// The maximum number of inline nodes in one rich text. The contract limits each list, not the total.
const MAX_INLINE = 2000;

let schemes = Object.freeze(['https:']);
let first = 2;
// The number of the current page policy. An element renders again if its render used an older policy.
let policy = 0;
const connected = new Set();

// The target of a link, or null if the page does not permit it. The kit opens only the parsed address.
function linkURL(value) {
  let url;
  try {
    url = new URL(value);
  } catch {
    return null;
  }
  if (!schemes.includes(url.protocol)) return null;
  // A mail link has an address only. It cannot set a subject, a body or a second receiver.
  if (url.protocol === 'mailto:') return url.pathname && !url.search && !url.hash ? url : null;
  return url.hostname && !url.username && !url.password ? url : null;
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

export class MuRichText extends ReadMoreElement {
  static component = 'rich-text';
  static styles = [READ_MORE_CSS, sheet(`
    :host { display: block; min-width: 0; }
    .rich {
      color: var(--mu-color-text); font: var(--mu-type-body); overflow-wrap: anywhere;
      display: flex; flex-direction: column; gap: var(--mu-space-2);
    }
    p, h2, h3, h4, h5, h6, ul, ol, pre, blockquote { margin: 0; }
    p, li, .heading { white-space: pre-wrap; }
    .heading { font: var(--mu-type-body); font-weight: var(--mu-type-weight-strong); }
    .level-1 { font-size: 1.25em; }
    .level-2 { font-size: 1.125em; }
    ul, ol { padding-inline-start: calc(var(--mu-space-4) + var(--mu-space-2)); }
    li + li { margin-block-start: var(--mu-space-1); }
    pre {
      padding: var(--mu-space-2); border: 1px solid var(--mu-color-border); border-radius: var(--mu-radius-1);
      white-space: pre-wrap; flex: 1; min-width: 0;
    }
    .copy { border-radius: var(--mu-radius-1); }
    .code { display: flex; flex-wrap: wrap; align-items: flex-start; gap: var(--mu-space-1) var(--mu-space-2); }
    code { font-family: ui-monospace, SFMono-Regular, Menlo, Consolas, monospace; font-size: 0.9em; }
    :not(pre) > code { padding-inline: var(--mu-space-1); border: 1px solid var(--mu-color-border); border-radius: var(--mu-radius-1); }
    blockquote {
      padding-inline-start: var(--mu-space-3); border-inline-start: 3px solid var(--mu-color-border);
      display: flex; flex-direction: column; gap: var(--mu-space-2);
    }
    strong { font-weight: var(--mu-type-weight-strong); }
    em { font-style: italic; }
    .muted { font: var(--mu-type-caption); color: var(--mu-color-text-muted); }
    .anchor { position: relative; }
    .link {
      font: inherit; color: var(--mu-color-accent); background: none; border: 0; padding: 0; margin: 0;
      border-radius: var(--mu-radius-1); text-decoration: underline; text-align: start; white-space: inherit;
      overflow-wrap: anywhere; cursor: pointer;
    }
    .link:hover { text-decoration-thickness: 2px; }
    .target {
      position: absolute; inset-block-start: 100%; inset-inline-start: 0; z-index: 1;
      width: max-content; max-width: min(20rem, 80vw); padding: var(--mu-space-1) var(--mu-space-2);
      font: var(--mu-type-caption); color: var(--mu-color-text); background: var(--mu-color-surface);
      border: 1px solid var(--mu-color-border); border-radius: var(--mu-radius-1); white-space: normal;
    }
    .target[hidden] { display: none; }
    @media (forced-colors: active) {
      pre, blockquote, code, .target { border-color: CanvasText; }
      .muted { color: CanvasText; }
      .link { color: LinkText; }
      .target { color: CanvasText; background: Canvas; }
    }
  `)];

  // The policy that this element used for its last render.
  #policy = -1;
  #targets = 0;

  // The schemes that a link can have. This policy applies to each rich text on the page, including the
  // rich text of a thread. A fill cannot set it.
  static get linkSchemes() {
    return schemes;
  }

  static set linkSchemes(values) {
    if (!Array.isArray(values) || !values.every((value) => SCHEMES.includes(value))) {
      throw new TypeError('link schemes: must be an array of ' + SCHEMES.join(', '));
    }
    schemes = Object.freeze([...new Set(values)]);
    MuRichText.#changed();
  }

  // The page level of a heading node with level 1. A fill cannot set it.
  static get headingLevel() {
    return first;
  }

  static set headingLevel(value) {
    if (!Number.isInteger(value) || value < 2 || value > 6) throw new TypeError('heading level: must be an integer from 2 to 6');
    first = value;
    MuRichText.#changed();
  }

  static #changed() {
    policy += 1;
    for (const node of connected) node.#refresh();
  }

  connectedCallback() {
    super.connectedCallback();
    connected.add(this);
    this.#refresh();
  }

  disconnectedCallback() {
    super.disconnectedCallback();
    connected.delete(this);
  }

  // Validates and renders the fill again, if the page policy changed after the last render.
  #refresh() {
    if (this.fill && this.#policy !== policy) this.fill = this.fill;
  }

  // The contract format cannot give these rules: the heading order, the total size and the page policy of a link.
  check(data) {
    let level = 0;
    for (const [index, block] of data.blocks.entries()) {
      if (block.component !== 'rich-heading') continue;
      if (block.data.level > level + 1) return [`data.blocks[${index}].data.level: a heading cannot skip a level`];
      level = block.data.level;
    }
    let count = 0;
    for (const [at, content] of runs(data.blocks)) {
      count += content.length;
      if (count > MAX_INLINE) return [`data.blocks: more than ${MAX_INLINE} inline nodes`];
      for (const [index, node] of content.entries()) {
        if (node.component === 'rich-link' && !linkURL(node.data.url)) {
          return [`${at}.content[${index}].data.url: the page does not permit this link`];
        }
      }
    }
    return [];
  }

  state(data) {
    return data.blocks.length ? 'default' : 'empty';
  }

  render(data) {
    this.#policy = policy;
    this.#targets = 0;
    const author = this.fill.slots.authorship;
    const name = author.name ? ` (${author.name})` : '';
    const blocks = data.blocks.length ? data.blocks.map((block) => this.#block(block))
      : [el('p', { class: 'muted', part: 'empty' }, 'No text. Ask for the text again.')];
    const authorText = el('p', { class: 'muted', part: 'authorship' }, `Author: ${author.type} ${author.id}${name}`);
    const content = el('div', { class: 'rich', part: 'rich-text' }, ...blocks);
    if (data.clamp === undefined) {
      this.readMore(content);
      content.append(authorText);
      return [content];
    }
    return [el('div', { class: 'rich' }, ...this.readMore(content, data.clamp), authorText)];
  }

  // The validator accepts only the five block types and the two inline types. Each element name here is fixed.
  #block({ component, data }) {
    switch (component) {
      case 'rich-paragraph':
        return el('p', {}, ...this.#inline(data.content));
      case 'rich-heading':
        return el(HEADINGS[Math.min(first + data.level - 1, 6) - 2], { class: 'heading level-' + data.level }, ...this.#inline(data.content));
      case 'rich-list':
        return el(data.ordered ? 'ol' : 'ul', {}, ...data.items.map((item) => el('li', {}, ...this.#inline(item.data.content))));
      case 'rich-code':
        return el('div', { class: 'code' }, el('pre', { part: 'code' }, el('code', {}, data.text)), copyControl(data.text, 'code'));
      case 'rich-quote':
        return el('blockquote', {}, ...data.paragraphs.map((item) => el('p', {}, ...this.#inline(item.data.content))));
      default:
        return el('p', { class: 'refused' }, 'Refused: the kit has no element for this node.');
    }
  }

  #inline(content) {
    return content.map(({ component, data }) => {
      if (component === 'rich-link') return this.#link(data);
      return Object.hasOwn(MARKS, data.mark ?? '') ? el(MARKS[data.mark], {}, data.text) : text(data.text);
    });
  }

  // A link is a button, and no element has an address attribute. The target shows as text on hover and
  // on focus. The target opens only after a person presses the button, and the application can cancel it.
  #link(data) {
    const url = linkURL(data.url);
    if (!url) return text(data.text);
    const id = `target-${++this.#targets}`;
    const target = el('span', { class: 'target', id, part: 'target' }, `Opens ${url.href}`);
    target.hidden = true;
    const button = el('button', { type: 'button', class: 'link', part: 'link', 'aria-describedby': id }, data.text);
    const anchor = el('span', { class: 'anchor' }, button, target);
    const shown = { hover: false, focus: false };
    const show = (key, value) => {
      shown[key] = value;
      target.hidden = !(shown.hover || shown.focus);
    };
    anchor.addEventListener('mouseenter', () => show('hover', true));
    anchor.addEventListener('mouseleave', () => show('hover', false));
    button.addEventListener('focus', () => show('focus', true));
    button.addEventListener('blur', () => show('focus', false));
    // Escape hides the target text, and the pointer and the focus stay where they are.
    button.addEventListener('keydown', (event) => {
      if (event.key !== 'Escape' || target.hidden) return;
      show('hover', false);
      show('focus', false);
    });
    button.addEventListener('click', () => {
      // Check the policy again at the only sink.
      const now = linkURL(data.url);
      if (!now) {
        this.fill = this.fill;
        return;
      }
      const event = new CustomEvent('mu-open', { bubbles: true, composed: true, cancelable: true, detail: { locator: now.href } });
      if (button.dispatchEvent(event)) window.open(now.href, '_blank', 'noopener,noreferrer');
    });
    return anchor;
  }
}
