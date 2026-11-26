// Shared visual limit for text. The full text stays in the DOM and the accessibility tree.
import { MuElement } from './base.js';
import { el, sheet } from '../render/dom.js';

export const READ_MORE_LABELS = ['read-more-label', 'read-less-label'];
export const READ_MORE_CSS = sheet(`
  :host([data-read-more]) { display: block; min-width: 0; }
  .read-more-content { display: flow-root; overflow-wrap: anywhere; }
  .read-more-content > [part="text"] { display: block; }
  .read-more-clip {
    overflow: hidden; display: -webkit-box; -webkit-box-orient: vertical;
    -webkit-line-clamp: var(--read-more-lines); line-clamp: var(--read-more-lines);
  }
  .read-more-toggle {
    display: block; width: fit-content; max-width: 100%; align-self: start;
    font: var(--mu-type-caption); color: var(--mu-color-accent); background: transparent;
    border: 0; padding: 0; margin-block-start: var(--mu-space-1);
    min-height: 2.75rem; min-width: 2.75rem; text-align: start;
    text-decoration: underline; text-underline-offset: 0.2em; overflow-wrap: anywhere; cursor: pointer;
  }
  .read-more-toggle[hidden] { display: none; }
  .read-more-toggle:focus-visible { outline: 2px solid var(--mu-color-focus); outline-offset: 2px; }
  @media (forced-colors: active) {
    .read-more-toggle { color: ButtonText; border: 1px solid ButtonText; background: ButtonFace; }
  }
`);

export function clampErrors(value) {
  return Number.isInteger(value) && value >= 1 && value <= 20 ? [] : ['clamp: must be an integer from 1 to 20'];
}

let nextId = 0;

export class ReadMore {
  #host;
  #text;
  #lines;
  #expanded = false;
  #overflow = false;
  #observer;
  content;
  button;

  constructor(host, content, lines) {
    const errors = clampErrors(lines);
    if (errors.length) throw new TypeError(errors[0]);
    this.#host = host;
    this.#text = content;
    this.#lines = lines;
    const id = `read-more-${++nextId}`;
    this.content = el('div', { class: 'read-more-content', part: 'read-more-content', id }, content);
    this.button = el('button', { type: 'button', class: 'read-more-toggle', part: 'read-more-toggle',
      'aria-controls': id, 'aria-expanded': 'false' });
    this.button.hidden = true;
    this.button.addEventListener('click', () => {
      this.#expanded = !this.#expanded;
      this.update();
    });
    // A rich text control must not keep keyboard focus outside the short view.
    this.content.addEventListener('focusin', () => {
      if (this.#expanded || !this.#overflow) return;
      this.#expanded = true;
      this.update();
    });
    this.labels();
    this.#observer = new ResizeObserver(() => this.update());
  }

  connect() {
    this.#observer.observe(this.content);
    this.update();
  }

  disconnect() {
    this.#observer.disconnect();
  }

  labels() {
    const key = this.#expanded ? 'read-less-label' : 'read-more-label';
    this.button.textContent = this.#host.getAttribute(key)?.trim() || (this.#expanded ? 'Read less' : 'Read more');
  }

  update() {
    if (!this.content.isConnected) return;
    const style = getComputedStyle(this.#text);
    const height = (parseFloat(style.lineHeight) || parseFloat(style.fontSize) * 1.2) * this.#lines;
    // scrollHeight gives the full height both with and without the visual limit.
    this.#overflow = this.content.scrollHeight > height + 1;
    const clipped = this.#overflow && !this.#expanded;
    this.content.style.maxHeight = clipped ? `${height}px` : '';
    // The line clamp adds an ellipsis at the end of the last visible line.
    this.content.style.setProperty('--read-more-lines', String(this.#lines));
    this.content.classList.toggle('read-more-clip', clipped);
    this.button.hidden = !this.#overflow;
    this.button.setAttribute('aria-expanded', String(this.#expanded || !this.#overflow));
    this.labels();
  }
}

// The three catalog elements share labels and observer cleanup. Their original states stay the same.
export class ReadMoreElement extends MuElement {
  static observedAttributes = READ_MORE_LABELS;
  #readMore = null;

  attributeChangedCallback() {
    this.#readMore?.labels();
  }

  connectedCallback() {
    this.#readMore?.connect();
  }

  disconnectedCallback() {
    this.#readMore?.disconnect();
  }

  rendered() {
    if (this.isConnected) this.#readMore?.connect();
  }

  refuse(errors) {
    this.#clear();
    super.refuse(errors);
  }

  #clear() {
    this.#readMore?.disconnect();
    this.#readMore = null;
    delete this.dataset.readMore;
  }

  readMore(content, lines) {
    this.#clear();
    if (lines === undefined) return [content];
    this.dataset.readMore = '';
    this.#readMore = new ReadMore(this, content, lines);
    return [this.#readMore.content, this.#readMore.button];
  }
}
