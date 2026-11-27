import { MuElement } from '../primitives/base.js';
import { BADGE_LABEL_CSS, badgeLabel } from '../primitives/badge.js';
import { el, sheet } from '../render/dom.js';

export class MuEmptyState extends MuElement {
  static component = 'empty-state';
  static styles = [sheet(`
    :host { display: block; min-inline-size: 0; color: var(--mu-color-text); font: var(--mu-type-body); }
    .empty {
      display: grid; justify-items: start; gap: var(--mu-space-2); padding: var(--mu-space-4);
      border: 1px solid var(--mu-color-border); border-radius: var(--mu-radius-2);
      background: var(--mu-color-surface); overflow-wrap: anywhere;
    }
    h2 { margin: 0; font: var(--mu-type-heading-3); }
    p { margin: 0; }
    slot { display: contents; }
    ::slotted(:is(button, a)) {
      display: inline-flex; align-items: center; min-inline-size: 44px; min-block-size: 44px; max-inline-size: 100%;
    }
    ::slotted(:is(button, a):focus-visible) { outline: 2px solid var(--mu-color-focus); outline-offset: 2px; }
    @media (forced-colors: active) {
      .empty { color: CanvasText; background: Canvas; border-color: CanvasText; }
      ::slotted(:is(button, a):focus-visible) { outline-color: Highlight; }
    }
  `)];

  state() {
    return 'empty';
  }

  render({ title, text }) {
    return [el('div', { class: 'empty', role: 'group', 'aria-labelledby': 'title' },
      el('h2', { id: 'title', part: 'title' }, title),
      el('p', { part: 'text' }, text),
      el('slot', { name: 'action', part: 'action' }))];
  }
}

export class MuSkeleton extends MuElement {
  static component = 'skeleton';
  static styles = [sheet(`
    :host { display: block; min-inline-size: 0; }
    [part="status"] { display: grid; gap: var(--mu-space-2); }
    [part="line"] {
      display: block; inline-size: 100%; block-size: 1rem; background: var(--mu-color-border);
      border-radius: var(--mu-radius-2); animation: skeleton-pulse 1.6s ease-in-out infinite alternate;
    }
    [part="line"]:nth-child(even) { inline-size: 80%; }
    @keyframes skeleton-pulse { to { opacity: 0.55; } }
    @media (forced-colors: active) {
      [part="line"] { background: GrayText; forced-color-adjust: none; animation: none; }
    }
  `)];

  state() {
    return 'loading';
  }

  render({ label, lines }) {
    return [el('div', { part: 'status', role: 'status', 'aria-label': label },
      ...Array.from({ length: lines }, () => el('span', { part: 'line', 'aria-hidden': 'true' })))];
  }
}

export class MuBadge extends MuElement {
  static component = 'badge';
  static styles = [BADGE_LABEL_CSS, sheet(`
    :host { display: inline-flex; max-inline-size: 100%; }
  `)];

  render({ text, tone }) {
    return [badgeLabel(text, tone, { part: 'label' })];
  }
}

function nameInitials(name) {
  const letters = name.normalize('NFC').split(/\s+/u)
    .map((word) => word.match(/\p{L}/u)?.[0]).filter(Boolean);
  if (!letters.length) return '?';
  const initials = letters.length === 1 ? letters : [letters[0], letters.at(-1)];
  return initials.map((letter) => {
    const upper = letter.toUpperCase();
    return [...upper].length === 1 ? upper : letter;
  }).join('');
}

export class MuAvatar extends MuElement {
  static component = 'avatar';
  static styles = [sheet(`
    :host { display: inline-flex; }
    [part="initials"] {
      display: inline-grid; place-items: center; min-inline-size: 2.25rem; min-block-size: 2.25rem;
      padding: var(--mu-space-1); border: 1px solid var(--mu-color-border); border-radius: 50%;
      color: var(--mu-color-text); background: var(--mu-color-surface);
      font: var(--mu-type-caption); font-weight: var(--mu-type-weight-strong);
    }
    @media (forced-colors: active) {
      [part="initials"] { color: CanvasText; background: Canvas; border-color: CanvasText; }
    }
  `)];

  render({ name }) {
    return [el('span', { part: 'initials', role: 'img', 'aria-label': name }, nameInitials(name))];
  }
}
