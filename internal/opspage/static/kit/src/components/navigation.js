import { fill } from '../kit.js';
import { MuElement } from '../primitives/base.js';
import { el, sheet } from '../render/dom.js';
import { navigationHref } from '../render/validate.js';

const NAVIGATION_CSS = sheet(`
  :host { display: block; min-inline-size: 0; color: var(--mu-color-text); font: var(--mu-type-body); }
  a, slot::slotted(a) {
    color: var(--mu-color-accent); text-decoration: underline; text-underline-offset: 0.18em;
  }
  a, button, slot::slotted(a) {
    min-inline-size: 44px; min-block-size: 44px; max-inline-size: 100%;
    display: inline-flex; align-items: center; padding-inline: var(--mu-space-2); overflow-wrap: anywhere;
  }
  button {
    justify-content: center; font: inherit; color: inherit; cursor: pointer;
    background: var(--mu-color-surface); border: 1px solid var(--mu-color-border); border-radius: var(--mu-radius-2);
  }
  slot::slotted(a:focus-visible) { outline: 2px solid var(--mu-color-focus); outline-offset: 2px; }
  @media (forced-colors: active) {
    a, slot::slotted(a) { color: LinkText; }
    button:not(:disabled) { color: ButtonText; background: ButtonFace; border-color: ButtonText; }
    slot::slotted(a:focus-visible) { outline-color: Highlight; }
  }
`);

const PHONE = '(max-width: 30rem)';

export class MuAppShell extends MuElement {
  static component = 'app-shell';
  static styles = [NAVIGATION_CSS, sheet(`
    :host { position: relative; }
    header {
      display: flex; flex-wrap: wrap; align-items: center; justify-content: space-between; gap: var(--mu-space-2);
      padding: var(--mu-space-3) var(--mu-space-4); background: var(--mu-color-surface);
      border-block-end: 1px solid var(--mu-color-border); overflow-wrap: anywhere;
    }
    .layout { display: grid; grid-template-columns: minmax(0, 15rem) minmax(0, 1fr); }
    nav { display: flex; flex-direction: column; align-items: start; gap: var(--mu-space-2); }
    .layout > nav {
      padding: var(--mu-space-4); border-inline-end: 1px solid var(--mu-color-border);
    }
    main { min-inline-size: 0; padding: var(--mu-space-4); overflow-wrap: anywhere; }
    main:focus-visible { outline-offset: -2px; }
    [part="skip"] {
      position: absolute; z-index: 1; inset-block-start: var(--mu-space-2); inset-inline-start: var(--mu-space-2);
      background: var(--mu-color-surface);
    }
    [part="skip"]:not(:focus) { clip-path: inset(50%); }
    [part="menu"] { display: none; }
    @media ${PHONE} {
      .layout { grid-template-columns: minmax(0, 1fr); }
      [part="menu"] { display: inline-flex; }
    }
    @media (forced-colors: active) {
      header, [part="skip"] { background: Canvas; }
      header, .layout > nav { border-color: CanvasText; }
    }
  `)];

  #phone = matchMedia(PHONE);
  #drawer = null;
  #placeSide = null;
  #resize = () => this.#placeSide?.();

  connectedCallback() {
    this.#phone.addEventListener('change', this.#resize);
    this.#resize();
  }

  disconnectedCallback() {
    this.#phone.removeEventListener('change', this.#resize);
    this.#drawer?.close();
  }

  refuse(errors) {
    this.#drawer?.close();
    this.#drawer = null;
    this.#placeSide = null;
    super.refuse(errors);
  }

  check(data) {
    return Object.keys(data).filter((field) => !data[field].trim()).map((field) => 'data.' + field + ': give text');
  }

  render({ nav_label, menu_label, close_label, skip_label }) {
    this.#drawer?.close();
    const main = el('main', { id: 'main', part: 'main', tabindex: -1 }, el('slot'));
    const skip = el('a', { part: 'skip' }, skip_label);
    skip.setAttribute('href', '#main');
    skip.addEventListener('click', (event) => {
      event.preventDefault();
      main.focus();
    });
    const menu = el('button', { type: 'button', part: 'menu', 'aria-haspopup': 'dialog',
      'aria-expanded': 'false', 'aria-controls': 'navigation-drawer' }, menu_label);
    const top = el('header', { part: 'top' }, el('slot', { name: 'top' }), menu);
    const side = el('nav', { part: 'side', 'aria-label': nav_label }, el('slot', { name: 'side' }));
    const layout = el('div', { class: 'layout' }, main);
    const drawer = document.createElement('mu-drawer');
    drawer.id = 'navigation-drawer';
    drawer.fill = fill('drawer', { title: nav_label, close_label });

    menu.addEventListener('click', () => {
      menu.focus();
      drawer.open();
    });
    drawer.addEventListener('mu-dialog-open', (event) => {
      if (event.target === drawer) menu.setAttribute('aria-expanded', 'true');
    });
    drawer.addEventListener('mu-dialog-close', (event) => {
      if (event.target !== drawer || drawer.dataset.state === 'open') return;
      menu.setAttribute('aria-expanded', 'false');
      if (drawer === this.#drawer && this.isConnected && this.#phone.matches) menu.focus();
    });
    side.addEventListener('click', (event) => {
      if (drawer.dataset.state === 'open' && event.composedPath().some((node) => node.matches?.('a[href]'))) drawer.close();
    });
    this.#drawer = drawer;
    this.#placeSide = () => {
      if (this.#phone.matches) {
        if (side.parentNode !== drawer) drawer.append(side);
      } else {
        drawer.close();
        if (side.parentNode !== layout) layout.prepend(side);
      }
    };
    this.#resize();
    return [skip, top, layout, drawer];
  }
}

export class MuBreadcrumbs extends MuElement {
  static component = 'breadcrumbs';
  static styles = [NAVIGATION_CSS, sheet(`
    ol { display: flex; flex-wrap: wrap; gap: var(--mu-space-2); list-style: none; margin: 0; padding: 0; }
    li { display: flex; align-items: center; max-inline-size: 100%; overflow-wrap: anywhere; }
    [part="separator"] { margin-inline-end: var(--mu-space-2); }
    [aria-current="page"] { font-weight: bold; }
  `)];

  check({ label, items }) {
    const errors = label.trim() ? [] : ['data.label: give text'];
    for (const [index, item] of items.entries()) {
      if (!item.label.trim()) errors.push(`data.items[${index}].label: give text`);
    }
    return errors;
  }

  render({ label, items }) {
    const list = el('ol', { part: 'list', role: 'list' });
    for (const [index, item] of items.entries()) {
      const separator = index ? el('span', { part: 'separator', 'aria-hidden': 'true' }, '/') : null;
      const content = index === items.length - 1
        ? el('span', { part: 'current', 'aria-current': 'page' }, item.label)
        : el('a', { part: 'link' }, item.label);
      if (content.localName === 'a') content.setAttribute('href', navigationHref(item.href));
      list.append(el('li', {}, separator, content));
    }
    return [el('nav', { part: 'nav', 'aria-label': label }, list)];
  }
}

function visiblePages(page, count) {
  if (count <= 7) return Array.from({ length: count }, (_, index) => index + 1);
  if (page <= 4) return [1, 2, 3, 4, 5, null, count];
  if (page >= count - 3) return [1, null, count - 4, count - 3, count - 2, count - 1, count];
  return [1, null, page - 1, page, page + 1, null, count];
}

export class MuPagination extends MuElement {
  static component = 'pagination';
  static styles = [NAVIGATION_CSS, sheet(`
    nav { display: flex; flex-wrap: wrap; align-items: center; gap: var(--mu-space-2); }
    button[aria-current="page"] { border-block-end: 3px solid var(--mu-color-accent); font-weight: bold; }
    @media (forced-colors: active) {
      button[aria-current="page"] { border-block-end-color: Highlight; }
    }
  `)];

  #focusAfterRender = null;

  check({ label }) {
    return label.trim() ? [] : ['data.label: give text'];
  }

  render(data) {
    const { label, page, page_count } = data;
    const active = this.shadowRoot.activeElement;
    const request = (button, target) => {
      button.disabled = target < 1 || target > page_count;
      button.addEventListener('click', () => {
        if (this.fill?.data !== data || button.disabled || target === page) return;
        this.dispatchEvent(new CustomEvent('mu-page-change', { bubbles: true, composed: true, detail: { page: target } }));
      });
      return button;
    };
    const previous = request(el('button', { type: 'button', part: 'previous-page', id: 'previous-page',
      'aria-label': 'Previous page' }, 'Previous'), page - 1);
    const next = request(el('button', { type: 'button', part: 'next-page', id: 'next-page',
      'aria-label': 'Next page' }, 'Next'), page + 1);
    const numbers = visiblePages(page, page_count).map((number) => number === null
      ? el('span', { part: 'gap', 'aria-hidden': 'true' }, '…')
      : request(el('button', { type: 'button', part: 'page', id: 'page-' + number,
        'aria-label': 'Page ' + number, 'aria-current': number === page ? 'page' : null }, number), number));
    const controls = [previous, ...numbers, next];
    if (active) {
      this.#focusAfterRender = controls.find((node) => node.id === active.id && !node.disabled)
        ?? numbers.find((node) => node.id === 'page-' + page);
    }
    return [el('nav', { part: 'nav', 'aria-label': label }, ...controls)];
  }

  rendered() {
    this.#focusAfterRender?.focus();
    this.#focusAfterRender = null;
  }
}

export class MuSteps extends MuElement {
  static component = 'steps';
  static styles = [NAVIGATION_CSS, sheet(`
    ol { display: flex; flex-wrap: wrap; gap: var(--mu-space-2); list-style: none; margin: 0; padding: 0; }
    li { display: flex; align-items: center; min-block-size: 44px; max-inline-size: 100%; overflow-wrap: anywhere; }
    li > span { padding-inline: var(--mu-space-2); }
    [aria-current="step"] { font-weight: bold; }
    button { display: block; text-align: start; }
  `)];

  render(data) {
    const { label, labels, current } = data;
    const list = el('ol', { part: 'list', role: 'list', 'aria-label': label });
    for (const [index, title] of labels.entries()) {
      const done = index < current;
      const position = el('span', { part: 'position' }, `Step ${index + 1} of ${labels.length}`);
      const state = el('span', { part: 'state' }, done ? 'Done' : index === current ? 'Current' : 'Next');
      const content = [position, '. ', title, '. ', state];
      const item = el('li', { part: 'step', 'aria-current': index === current ? 'step' : null });
      if (done) {
        const back = el('button', { part: 'back', type: 'button' }, ...content);
        back.addEventListener('click', () => {
          if (this.fill?.data !== data) return;
          this.dispatchEvent(new CustomEvent('mu-step-change', { bubbles: true, composed: true, detail: { index } }));
        });
        item.append(back);
      } else {
        item.append(el('span', {}, ...content));
      }
      list.append(item);
    }
    return [list];
  }
}
