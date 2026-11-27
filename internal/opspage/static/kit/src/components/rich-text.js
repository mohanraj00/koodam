// Rich text as a tree of typed nodes (catalog/rich-text.json). A fill gives data only. The kit makes each
// element from the fixed tables below, and each text goes into a text node. Page code owns the link
// schemes, the first heading level and the approval marks. src/components/rich-text.md gives the rules.
import { ReadMoreElement, READ_MORE_CSS } from '../primitives/read-more.js';
import { APPROVAL_MARK_CSS, APPROVAL_STATES, approvalMark } from '../primitives/badge.js';
import { copyControl, el, sheet, text } from '../render/dom.js';
import { richTextLinkSchemes, richTextRules, setRichTextLinkSchemes } from '../render/rich-text.js';
import { isLocalDateTime, parseLinkURL, schemaErrors } from '../render/validate.js';

// The element of each heading level of the page, from level 2 to level 6.
const HEADINGS = Object.freeze(['h2', 'h3', 'h4', 'h5', 'h6']);
const MARKS = Object.freeze({ emphasis: 'em', strong: 'strong', code: 'code' });
const APPROVAL_KEYS = new Set(['block', 'state', 'by', 'at']);
const UTC_SECONDS = /^:[0-5][0-9](\.[0-9]{1,3})?Z$/;

let first = 2;
// The number of the current page policy. An element renders again if its render used an older policy.
let policy = 0;
const connected = new Set();

export class MuRichText extends ReadMoreElement {
  static component = 'rich-text';
  static styles = [READ_MORE_CSS, APPROVAL_MARK_CSS, sheet(`
    :host { display: block; min-width: 0; }
    .rich {
      color: var(--mu-color-text); font: var(--mu-type-body); overflow-wrap: anywhere;
      display: flex; flex-direction: column; gap: var(--mu-space-2);
    }
    p, h2, h3, h4, h5, h6, ul, ol, pre, blockquote { margin: 0; }
    p, li, .heading { white-space: pre-wrap; }
    [part="approval-mark"] { margin-block-end: calc(var(--mu-space-1) - var(--mu-space-2)); }
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
  #renderedFill = null;
  #refreshing = false;
  #fillRevision = 0;
  #targets = 0;
  #blocks = [];
  #marks = [];
  #approvals = new Map();

  get fillRevision() {
    return this.#fillRevision;
  }

  setApprovals(list, revision) {
    if (!this.#checkRevision(revision)) return;
    const errors = [];
    const approvals = new Map();
    if (!Array.isArray(list)) errors.push('approvals: must be a list');
    else {
      for (let index = 0; index < list.length; index += 1) {
        const item = list[index];
        const path = `approvals[${index}]`;
        if (item === null || typeof item !== 'object' || Array.isArray(item)) {
          errors.push(path + ': must be an object');
          continue;
        }
        for (const key of Reflect.ownKeys(item)) {
          if (!APPROVAL_KEYS.has(key)) errors.push(path + '.' + String(key) + ': unknown key');
        }
        const { block, state } = item;
        const by = Object.hasOwn(item, 'by') ? item.by : undefined;
        const at = Object.hasOwn(item, 'at') ? item.at : undefined;
        if (!Object.hasOwn(item, 'block') || !Number.isInteger(block) || block < 0 || block >= this.#blocks.length) {
          errors.push(path + '.block: must be an index of a top-level block');
        } else if (approvals.has(block)) {
          errors.push(path + '.block: must be unique');
        } else {
          approvals.set(block, { state, by, at });
        }
        if (!Object.hasOwn(item, 'state') || typeof state !== 'string' || !Object.hasOwn(APPROVAL_STATES, state)) {
          errors.push(path + '.state: must be approved, pending or rejected');
        }
        if (Object.hasOwn(item, 'by')) {
          errors.push(...schemaErrors(by, { type: 'string', maxLength: 80 }, undefined, path + '.by'));
        }
        if (Object.hasOwn(item, 'at') && (typeof at !== 'string' || !at.endsWith('Z')
          || !isLocalDateTime(at.slice(0, 16)) || !UTC_SECONDS.test(at.slice(16)))) {
          errors.push(path + '.at: must be an ISO 8601 date and time in UTC');
        }
      }
    }

    // Page code can set a new fill while the kit reads the list.
    if (!this.#checkRevision(revision)) return;
    for (const mark of this.#marks) mark.remove();
    this.#marks = [];
    if (errors.length) {
      this.#approvals.clear();
      this.dispatchEvent(new CustomEvent('mu-refused', { bubbles: true, composed: true, detail: { errors } }));
      return;
    }
    this.#approvals = approvals;
    this.#showApprovals();
  }

  #checkRevision(revision) {
    if (this.#renderedFill && revision === this.#fillRevision) return true;
    const errors = [`revision: stale, the current revision is ${this.#fillRevision}`];
    this.dispatchEvent(new CustomEvent('mu-refused', { bubbles: true, composed: true, detail: { errors } }));
    return false;
  }

  #showApprovals() {
    for (const [index, approval] of this.#approvals) {
      const block = this.#blocks[index];
      const mark = approvalMark(approval);
      block.parentNode.insertBefore(mark, block);
      this.#marks.push(mark);
    }
  }

  // The schemes that a link can have. This policy applies to each rich text on the page, including the
  // rich text of a thread. A fill cannot set it.
  static get linkSchemes() {
    return richTextLinkSchemes();
  }

  static set linkSchemes(values) {
    setRichTextLinkSchemes(values);
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
    if (!this.#renderedFill || this.#policy === policy) return;
    this.#refreshing = true;
    try {
      this.fill = this.#renderedFill;
    } finally {
      this.#refreshing = false;
    }
  }

  check(data) {
    return richTextRules(data);
  }

  state(data) {
    return data.blocks.length ? 'default' : 'empty';
  }

  refuse(errors) {
    this.#fillRevision += 1;
    this.#renderedFill = null;
    this.#refreshing = false;
    this.#approvals.clear();
    this.#blocks = [];
    this.#marks = [];
    super.refuse(errors);
  }

  render(data) {
    this.#policy = policy;
    this.#targets = 0;
    // A new fill clears the marks. A change of page policy keeps the revision and marks.
    if (!this.#refreshing) {
      this.#fillRevision += 1;
      this.#approvals.clear();
      // Keep the fill that supplied the displayed text for the next policy change.
      this.#renderedFill = structuredClone(this.fill);
    }
    this.#refreshing = false;
    this.#marks = [];
    this.#blocks = data.blocks.map((block) => this.#block(block));
    const author = this.fill.slots.authorship;
    const name = author.name ? ` (${author.name})` : '';
    const blocks = this.#blocks.length ? this.#blocks
      : [el('p', { class: 'muted', part: 'empty' }, 'No text. Ask for the text again.')];
    const authorText = el('p', { class: 'muted', part: 'authorship' }, `Author: ${author.type} ${author.id}${name}`);
    const content = el('div', { class: 'rich', part: 'rich-text' }, ...blocks);
    this.#showApprovals();
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
        return el(data.ordered ? 'ol' : 'ul', {}, ...data.items.map(({ data }) =>
          el('li', {}, ...this.#inline(data.content))));
      case 'rich-code':
        return el('div', { class: 'code' }, el('pre', { part: 'code' }, el('code', {}, data.text)), copyControl(data.text, 'code'));
      case 'rich-quote':
        return el('blockquote', {}, ...data.paragraphs.map((item) => this.#block(item)));
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
    const url = parseLinkURL(data.url, richTextLinkSchemes());
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
      const now = parseLinkURL(data.url, richTextLinkSchemes());
      if (!now) {
        this.#refresh();
        return;
      }
      const event = new CustomEvent('mu-open', { bubbles: true, composed: true, cancelable: true, detail: { locator: now.href } });
      if (button.dispatchEvent(event)) window.open(now.href, '_blank', 'noopener,noreferrer');
    });
    return anchor;
  }
}
