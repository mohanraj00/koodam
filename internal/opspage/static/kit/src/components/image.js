// The page owns the image origins. A fill supplies data only (catalog/image.json).
import { MuElement } from '../primitives/base.js';
import { el, sheet } from '../render/dom.js';

let origins = Object.freeze([]);
const connected = new Set();

function imageURL(value) {
  try {
    const url = new URL(value, location.origin);
    if (!['http:', 'https:'].includes(url.protocol) || url.username || url.password || !origins.includes(url.origin)) return null;
    return url;
  } catch {
    return null;
  }
}

export class MuImage extends MuElement {
  static component = 'image';
  static styles = [sheet(`
    :host { display: block; width: 100%; min-width: 0; }
    figure { margin: 0; color: var(--mu-color-text); font: var(--mu-type-body); }
    .box { position: relative; width: 100%; }
    canvas { display: block; width: 100%; height: auto; visibility: hidden; }
    .open { position: absolute; inset: 0; width: 100%; height: 100%; padding: 0; }
    button {
      font: var(--mu-type-body); color: var(--mu-color-text); background: var(--mu-color-surface);
      border: 1px solid var(--mu-color-border); border-radius: var(--mu-radius-2); cursor: pointer;
    }
    button:disabled { cursor: default; opacity: 1; }
    /* The image covers the layer of the base style, so the edge and the image show hover and pressed. */
    .open:hover:not(:disabled) { border-color: currentColor; }
    .open:active:not(:disabled) img { opacity: 0.85; }
    .open img { display: block; width: 100%; height: 100%; object-fit: contain; }
    .open img[hidden] { display: none; }
    .notice { position: absolute; inset: 0; display: grid; place-items: center; margin: 0; padding: var(--mu-space-2); overflow: auto; }
    .notice[hidden] { display: none; }
    figcaption, .author { margin: var(--mu-space-1) 0 0; overflow-wrap: anywhere; }
    .author { font: var(--mu-type-caption); color: var(--mu-color-text-muted); }
    dialog {
      max-width: calc(100vw - 2rem); max-height: calc(100vh - 2rem); overflow: auto;
      padding: var(--mu-space-2); color: var(--mu-color-text); background: var(--mu-color-surface);
      border: 1px solid var(--mu-color-border); border-radius: var(--mu-radius-2);
    }
    dialog img { display: block; width: auto; height: auto; max-width: none; }
    dialog button { display: block; min-height: 2.75rem; padding: 0 var(--mu-space-2); margin-bottom: var(--mu-space-2); }
    @media (forced-colors: active) {
      button { color: ButtonText; background: ButtonFace; border-color: ButtonText; }
      dialog { color: CanvasText; background: Canvas; border-color: CanvasText; }
      .author, .notice { color: CanvasText; }
    }
  `)];

  // This policy applies to each image on the page, including images that the thread creates.
  static get allowedOrigins() { return origins; }
  static set allowedOrigins(values) {
    if (!Array.isArray(values)) throw new TypeError('image origins: must be an array of http or https origins');
    const copy = values.map((value) => {
      let url;
      try { url = new URL(value); } catch { /* The error below gives the page a fixed message. */ }
      if (typeof value !== 'string' || !url || !['http:', 'https:'].includes(url.protocol) || url.origin !== value) {
        throw new TypeError('image origins: each value must be an http or https origin');
      }
      return value;
    });
    origins = Object.freeze([...new Set(copy)]);
    for (const node of connected) node.#recheck();
  }

  connectedCallback() {
    connected.add(this);
    this.#recheck();
  }

  disconnectedCallback() {
    connected.delete(this);
    this.shadowRoot.querySelector('dialog[open]')?.close();
  }

  #recheck() {
    if (this.fill && this.check(this.fill.data).length) this.fill = this.fill;
  }

  check(data) {
    if (!data.decorative && !data.alt.trim()) return ['data.alt: a text alternative is required unless decorative is true'];
    if (!imageURL(data.url)) return ['data.url: the page does not permit this image URL'];
    return [];
  }

  state() { return 'loading'; }

  render(data) {
    // A canvas with no drawing context reserves the declared ratio in each state. The natural dimensions
    // of a downloaded image cannot change it. The fill sets no style and no markup.
    const spacer = el('canvas', { 'aria-hidden': 'true' });
    spacer.width = data.width;
    spacer.height = data.height;
    const img = el('img');
    img.alt = data.alt;
    img.referrerPolicy = 'no-referrer';
    img.decoding = 'async';
    img.hidden = true;
    const label = 'Open image at full size' + (data.alt ? ': ' + data.alt : '');
    const open = el('button', { type: 'button', class: 'open', part: 'open', 'aria-label': label }, img);
    open.disabled = true;
    const notice = el('p', { class: 'notice', role: 'status', 'aria-live': 'polite', part: 'status' }, 'Loading image.');
    const box = el('div', { class: 'box', part: 'box' }, spacer, open, notice);
    const close = el('button', { type: 'button' }, 'Close image');
    const dialog = el('dialog', { 'aria-label': 'Full-size image', part: 'dialog' }, close);
    const author = this.fill.slots.authorship;
    const authorName = author.name ? ` (${author.name})` : '';
    const figure = el('figure', { role: 'group', 'aria-label': 'Image', part: 'image' }, box,
      data.caption ? el('figcaption', { part: 'caption' }, data.caption) : null,
      el('p', { class: 'author', part: 'authorship' }, `Author: ${author.type} ${author.id}${authorName}`), dialog);
    const current = () => this.shadowRoot.contains(figure);
    img.addEventListener('load', () => {
      if (!current() || this.dataset.state !== 'loading') return;
      this.dataset.state = 'default';
      img.hidden = false;
      open.disabled = false;
      notice.textContent = 'Image loaded.';
      notice.hidden = true;
    });
    img.addEventListener('error', () => {
      if (!current() || this.dataset.state !== 'loading') return;
      this.dataset.state = 'error';
      notice.textContent = 'Image failed to load.' + (data.alt ? ' ' + data.alt : '');
    });
    open.addEventListener('click', () => {
      if (!current() || this.dataset.state !== 'default' || dialog.open) return;
      if (!imageURL(data.url)) { this.fill = this.fill; return; }
      // Move the loaded image. The dialog starts no second request and preserves the fixed box below it.
      dialog.append(img);
      dialog.showModal();
      close.focus();
    });
    close.addEventListener('click', () => dialog.close());
    dialog.addEventListener('close', () => {
      if (!current()) return;
      open.append(img);
      if (this.isConnected) open.focus();
    });
    // The native dialog handles Escape. It also keeps keyboard focus inside while it is modal.
    // Check again at the only request sink. No attribute or fill can supply a second source.
    const url = imageURL(data.url);
    if (url) img.src = url.href;
    return [figure];
  }
}
