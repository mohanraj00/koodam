import { MuElement } from '../primitives/base.js';
import { el, sheet } from '../render/dom.js';

export class MuProgress extends MuElement {
  static component = 'progress';
  static styles = [sheet(`
    :host { display: block; min-inline-size: 0; color: var(--mu-color-text); font: var(--mu-type-body); }
    .caption {
      display: flex; flex-wrap: wrap; align-items: baseline; justify-content: space-between;
      gap: var(--mu-space-1) var(--mu-space-2); margin-block-end: var(--mu-space-1); overflow-wrap: anywhere;
    }
    [part="bar"] {
      --fill-color: var(--mu-color-accent);
      display: block; appearance: none; overflow: hidden; inline-size: 100%; block-size: 0.75rem;
      background: var(--mu-color-surface); border: 1px solid var(--mu-color-border); border-radius: var(--mu-radius-2);
    }
    progress::-webkit-progress-bar { background: transparent; }
    progress::-webkit-progress-value { background: var(--fill-color); }
    progress::-moz-progress-bar { background: var(--fill-color); }
    [part="indicator"] {
      display: block; position: relative; inset-inline-start: 0;
      inline-size: 30%; block-size: 100%; background: var(--fill-color);
      animation: progress-sweep 1.6s ease-in-out infinite alternate;
    }
    @keyframes progress-sweep { to { inset-inline-start: 70%; } }
    @media (forced-colors: active) {
      [part="bar"] {
        --fill-color: Highlight;
        background: Canvas; border-color: CanvasText; forced-color-adjust: none;
      }
      progress::-webkit-progress-value, [part="indicator"] { forced-color-adjust: none; }
      progress::-moz-progress-bar { forced-color-adjust: none; }
    }
  `)];

  state({ value }) {
    return value === undefined ? 'loading' : 'default';
  }

  render({ label, maximum, value }) {
    const range = { part: 'bar', 'aria-labelledby': 'label', 'aria-valuemin': 0, 'aria-valuemax': maximum };
    const unknown = value === undefined;
    const bar = unknown
      ? el('div', { ...range, role: 'progressbar' }, el('span', { part: 'indicator', 'aria-hidden': 'true' }))
      : el('progress', { ...range, 'aria-valuenow': value });
    if (!unknown) {
      bar.setAttribute('max', String(maximum));
      bar.setAttribute('value', String(value));
    }
    const caption = el('div', { class: 'caption' }, el('span', { id: 'label', part: 'label' }, label),
      el('span', { part: 'value' }, unknown ? 'Unknown' : `${value} of ${maximum}`));
    return [caption, bar];
  }
}
