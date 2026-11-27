// One state from the application or service, with short text (catalog/status.json).
import { MuElement } from '../primitives/base.js';
import { el, sheet } from '../render/dom.js';

const STATES = Object.freeze({
  pending: { label: 'Pending', className: 'label pending' },
  passed: { label: 'Passed', className: 'label passed' },
  failed: { label: 'Failed', className: 'label failed' },
  unknown: { label: 'Unknown', className: 'label unknown' },
});

export class MuStatus extends MuElement {
  static component = 'status';
  static styles = [sheet(`
    :host { display: block; }
    .status {
      color: var(--mu-color-text); font: var(--mu-type-body);
      display: flex; flex-wrap: wrap; align-items: center; gap: var(--mu-space-2);
    }
    .label {
      border: 3px solid var(--mu-color-border); border-radius: var(--mu-radius-2);
      padding: var(--mu-space-1) var(--mu-space-3); font-weight: var(--mu-type-weight-strong);
      background-clip: padding-box;
    }
    .pending { border-style: dashed; border-color: var(--mu-color-warning);
      background-color: var(--mu-color-warning); color: var(--mu-color-on-warning); }
    .passed { border-color: var(--mu-color-success);
      background-color: var(--mu-color-success); color: var(--mu-color-on-success); }
    .failed { border-style: double; }
    .unknown { border-style: dotted; }
    .text { overflow-wrap: anywhere; white-space: pre-wrap; min-width: 0; }
    .authorship {
      flex-basis: 100%; margin: 0; overflow-wrap: anywhere;
      font: var(--mu-type-caption); color: var(--mu-color-text-muted);
    }
    @media (forced-colors: active) {
      .status, .authorship { color: CanvasText; }
      .label { color: CanvasText; background: Canvas; border-color: CanvasText; }
    }
  `)];

  render(data) {
    const state = STATES[data.state];
    const author = this.fill.slots.authorship;
    const name = author.name ? ` (${author.name})` : '';
    // Validation accepts only the four fixed states. Text and author values make only text nodes.
    return [el('div', { class: 'status', role: 'group', 'aria-label': data.text, part: 'status' },
      el('span', { class: state.className, part: 'label' }, state.label),
      el('span', { class: 'text', part: 'text' }, data.text),
      el('p', { class: 'authorship', part: 'authorship' }, `Author: ${author.type} ${author.id}${name}`))];
  }
}
