// The browser owns the top layer and the dismissal of auto and hint popovers.
import { MuElement } from '../primitives/base.js';
import { el, positionPopup, sheet } from '../render/dom.js';

const SURFACE = sheet(`
  [popover] {
    position: fixed;
    color: var(--mu-color-text); background: var(--mu-color-surface);
    border: 1px solid var(--mu-color-border); border-radius: var(--mu-radius-2);
    font: var(--mu-type-body); overflow-wrap: anywhere; overscroll-behavior: contain;
  }
  .anchored {
    inset: auto; left: var(--popup-left, 0px); top: var(--popup-top, 0px); margin: 0;
    inline-size: max-content; max-inline-size: calc(100% - 16px);
    max-block-size: var(--popup-height, 100%); overflow: auto;
  }
  button {
    min-inline-size: 44px; min-block-size: 44px; max-inline-size: 100%;
    padding: var(--mu-space-2); font: inherit; cursor: pointer; overflow-wrap: anywhere;
    color: inherit; background: var(--mu-color-surface);
    border: 1px solid var(--mu-color-border); border-radius: var(--mu-radius-2);
  }
  @media (forced-colors: active) {
    [popover] { color: CanvasText; background: Canvas; border-color: CanvasText; }
    button { color: ButtonText; background: ButtonFace; border-color: ButtonText; }
  }
`);

const open = (panel) => panel?.matches(':popover-open');
const BUBBLES = { bubbles: true, composed: true };

class MuPopover extends MuElement {
  #panel = null;
  #position = null;

  setPanel(panel, mode) {
    panel.popover = mode;
    // Older browsers have auto popovers and treat the hint value as manual.
    if (mode === 'hint' && panel.popover !== 'hint') panel.popover = 'auto';
    panel.addEventListener('beforetoggle', ({ newState }) => {
      if (newState === 'closed' && panel === this.#panel) this.#closed();
    });
    panel.addEventListener('toggle', ({ oldState, newState }) => {
      if (oldState === newState) return;
      this.dispatchEvent(this.constructor.events[newState]());
    });
    this.#panel = panel;
    return panel;
  }

  showPanel(anchor) {
    const panel = this.#panel;
    if (!this.isConnected || !panel || open(panel)) return false;
    panel.showPopover(anchor ? { source: anchor } : undefined);
    if (panel !== this.#panel || !open(panel)) return false;
    this.dataset.state = 'open';
    if (anchor) this.#position = positionPopup(panel, anchor);
    return true;
  }

  hidePanel() {
    if (this.isConnected && open(this.#panel)) this.#panel.hidePopover();
    this.#closed();
  }

  #closed() {
    this.#position?.();
    this.#position = null;
    if (this.dataset.state === 'open') this.dataset.state = 'default';
    this.panelClosed(this.#panel);
  }

  panelClosed() {}

  disconnectedCallback() {
    this.hidePanel();
  }

  refuse(errors) {
    this.hidePanel();
    this.#panel = null;
    super.refuse(errors);
  }
}

export class MuMenu extends MuPopover {
  static component = 'menu';
  static events = {
    open: () => new CustomEvent('mu-menu-open', BUBBLES),
    closed: () => new CustomEvent('mu-menu-close', BUBBLES),
  };
  static styles = [SURFACE, sheet(`
    [part="menu"] { padding: max(5px, var(--mu-space-2)); }
    [role="menuitem"] { display: block; inline-size: 100%; text-align: start; }
  `)];

  #trigger = null;

  check({ label, items }) {
    const errors = label.trim() ? [] : ['data.label: give text'];
    const names = new Set();
    items.forEach((item, index) => {
      if (!item.label.trim()) errors.push('data.items[' + index + '].label: give text');
      if (names.has(item.name)) errors.push('data.items[' + index + '].name: give a unique name');
      names.add(item.name);
    });
    return errors;
  }

  panelClosed(panel) {
    this.#trigger?.setAttribute('aria-expanded', 'false');
    // Escape closes the auto popover. If focus is in the menu, return it to the trigger before the panel hides.
    if (panel?.contains(this.shadowRoot.activeElement)) this.#trigger.focus({ preventScroll: true });
  }

  render(data) {
    this.hidePanel();
    const trigger = el('button', { type: 'button', id: 'trigger', part: 'trigger', 'aria-haspopup': 'menu',
      'aria-expanded': 'false', 'aria-controls': 'menu' }, data.label);
    const items = data.items.map(({ label }) => el('button', { type: 'button', part: 'item', role: 'menuitem', tabindex: -1 }, label));
    const panel = this.setPanel(el('div', { id: 'menu', class: 'anchored', part: 'menu', role: 'menu',
      'aria-labelledby': 'trigger' }, ...items), 'auto');
    this.#trigger = trigger;
    trigger.popoverTargetElement = panel;

    const show = (index) => {
      trigger.focus({ preventScroll: true });
      if (!this.showPanel(trigger)) return;
      trigger.setAttribute('aria-expanded', 'true');
      items[index].focus();
    };
    trigger.addEventListener('keydown', (event) => {
      if (event.key !== 'ArrowDown' && event.key !== 'ArrowUp') return;
      event.preventDefault();
      show(event.key === 'ArrowDown' ? 0 : items.length - 1);
    });
    trigger.addEventListener('click', (event) => {
      event.preventDefault();
      if (open(panel)) this.hidePanel();
      else show(0);
    });
    panel.addEventListener('keydown', (event) => {
      if (event.key !== 'Tab') return;
      // Close first so the browser moves focus from the trigger in the page tab order.
      this.hidePanel();
    });

    items.forEach((item, index) => {
      item.addEventListener('keydown', (event) => {
        const last = items.length - 1;
        let next;
        switch (event.key) {
          case 'ArrowDown': next = index === last ? 0 : index + 1; break;
          case 'ArrowUp': next = index === 0 ? last : index - 1; break;
          case 'Home': next = 0; break;
          case 'End': next = last; break;
          default: return;
        }
        event.preventDefault();
        items[next].focus();
      });
      item.addEventListener('click', () => {
        if (!open(panel)) return;
        this.hidePanel();
        trigger.focus();
        this.dispatchEvent(new CustomEvent('mu-menu-select', { ...BUBBLES, detail: { name: data.items[index].name } }));
      });
    });
    return [trigger, panel];
  }
}

let descriptionCount = 0;

export class MuTooltip extends MuPopover {
  static component = 'tooltip';
  static events = {
    open: () => new CustomEvent('mu-tooltip-open', BUBBLES),
    closed: () => new CustomEvent('mu-tooltip-close', BUBBLES),
  };
  static styles = [SURFACE, sheet(`
    .target { display: inline-block; max-inline-size: 100%; vertical-align: middle; }
    ::slotted(*) { min-inline-size: 44px; min-block-size: 44px; max-inline-size: 100%; }
    [role="tooltip"] {
      padding: var(--mu-space-2); max-inline-size: min(20rem, calc(100% - 16px)); overflow: clip;
    }
  `)];

  #slot = null;
  #target = null;
  #description = null;
  #listeners = null;

  check({ text }) {
    return text.trim() ? [] : ['data.text: give text'];
  }

  #unbind() {
    this.#listeners?.abort();
    this.#listeners = null;
    if (!this.#target) return;
    const id = this.#description.getAttribute('id');
    const remaining = this.#references(this.#target).filter((reference) => reference !== id);
    if (remaining.length) this.#target.setAttribute('aria-describedby', remaining.join(' '));
    else this.#target.removeAttribute('aria-describedby');
    this.#description.remove();
    this.#description = null;
    this.#target = null;
  }

  #references(target) {
    return (target.getAttribute('aria-describedby') ?? '').split(/\s+/).filter(Boolean);
  }

  #bind() {
    if (!this.isConnected || !this.#slot) return;
    const [target] = this.#slot.assignedElements();
    if (target === this.#target) return;
    this.hidePanel();
    this.#unbind();
    if (!target) return;

    // A slotted control and its ARIA reference must be in the same node tree.
    let id;
    const root = target.getRootNode();
    do { id = 'mu-tooltip-description-' + ++descriptionCount; } while (root.getElementById(id));
    const description = el('span', { id }, this.fill.data.text);
    description.hidden = true;
    description.slot = 'description';
    this.append(description);
    target.setAttribute('aria-describedby', [...this.#references(target), id].join(' '));
    this.#description = description;
    this.#target = target;

    const anchor = this.#slot.parentNode;
    const listeners = new AbortController();
    this.#listeners = listeners;
    const options = { signal: listeners.signal };
    let hover = false;
    let focused = false;
    anchor.addEventListener('pointerenter', (event) => {
      if (event.pointerType === 'touch') return;
      hover = true;
      this.showPanel(anchor);
    }, options);
    anchor.addEventListener('pointerleave', () => {
      hover = false;
      if (!focused) this.hidePanel();
    }, options);
    const showOnFocus = () => {
      if (focused) return;
      focused = true;
      this.showPanel(anchor);
    };
    target.addEventListener('focus', showOnFocus, options);
    target.addEventListener('blur', (event) => {
      // The page can lose focus while the target stays active in its root.
      if (event.relatedTarget === null && root.activeElement === target) return;
      focused = false;
      if (!hover) this.hidePanel();
    }, options);
    if (root.activeElement === target) showOnFocus();
  }

  connectedCallback() { this.#bind(); }
  rendered() { this.#bind(); }

  disconnectedCallback() {
    this.#unbind();
    super.disconnectedCallback();
  }

  refuse(errors) {
    this.#unbind();
    this.#slot = null;
    super.refuse(errors);
  }

  render(data) {
    this.hidePanel();
    this.#unbind();
    const slot = el('slot');
    slot.addEventListener('slotchange', () => this.#bind());
    this.#slot = slot;
    const panel = this.setPanel(el('span', { part: 'tooltip', class: 'anchored', role: 'tooltip' }, data.text), 'hint');
    // The named slot makes the hidden description available in the flat tree.
    return [el('span', { class: 'target' }, slot, panel), el('slot', { name: 'description' })];
  }
}

const TOAST_TIME = 8000;
const ANNOUNCE_DELAY = 50;

export class MuToast extends MuPopover {
  static component = 'toast';
  static events = {
    open: () => new CustomEvent('mu-toast-open', BUBBLES),
    closed: () => new CustomEvent('mu-toast-close', BUBBLES),
  };
  static styles = [SURFACE, sheet(`
    [part="toast"] {
      inset: auto; inset-inline-end: var(--mu-space-3); inset-block-end: var(--mu-space-3);
      margin: 0; inline-size: max-content; max-inline-size: min(24rem, calc(100% - 2 * var(--mu-space-3)));
      max-block-size: calc(100% - 2 * var(--mu-space-3)); overflow: auto; padding: var(--mu-space-3);
    }
    [part="toast"]:popover-open { display: flex; align-items: center; gap: var(--mu-space-2); }
    [part="toast"][data-state="error"] { border-color: var(--mu-color-danger); }
    [part="message"] { min-inline-size: 0; }
    [part="close"] { max-inline-size: 50%; }
    .live { position: absolute; inline-size: 1px; block-size: 1px; overflow: hidden; clip-path: inset(50%); }
    @media (forced-colors: active) {
      [part="toast"][data-state="error"] { border-color: CanvasText; }
    }
  `)];

  #announce = null;
  #expiry = null;
  #region = null;
  #origin = null;

  check({ message, close_label }) {
    const errors = message.trim() ? [] : ['data.message: give text'];
    if (close_label !== undefined && !close_label.trim()) errors.push('data.close_label: give text');
    return errors;
  }

  show() {
    if (!this.showPanel()) return;
    const { message, close_label } = this.fill.data;
    let origin = this.getRootNode().activeElement;
    while (origin?.shadowRoot?.activeElement) origin = origin.shadowRoot.activeElement;
    this.#origin = origin;
    // Let the browser expose the empty live region before it receives the message.
    this.#announce = setTimeout(() => { this.#region.textContent = message; }, ANNOUNCE_DELAY);
    if (!close_label) this.#expiry = setTimeout(() => this.hidePanel(), TOAST_TIME);
  }

  panelClosed() {
    clearTimeout(this.#announce);
    clearTimeout(this.#expiry);
    if (this.#region) this.#region.textContent = '';
    this.#origin = null;
  }

  render(data) {
    this.hidePanel();
    const error = data.kind === 'error';
    this.#region = el('span', { class: 'live', role: error ? 'alert' : 'status', 'aria-label': 'Notification' });
    const close = data.close_label ? el('button', { type: 'button', part: 'close' }, data.close_label) : null;
    close?.addEventListener('click', () => {
      const origin = this.shadowRoot.activeElement === close ? this.#origin : null;
      this.hidePanel();
      if (origin?.isConnected) origin.focus();
    });
    const panel = this.setPanel(el('div', { part: 'toast', 'data-state': error ? 'error' : null },
      el('span', { part: 'message', 'aria-hidden': 'true' }, data.message), close), 'manual');
    return [this.#region, panel];
  }
}
