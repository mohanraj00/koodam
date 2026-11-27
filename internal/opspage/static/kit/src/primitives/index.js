// The primitives: text, button, the fields of src/primitives/fields.js, stack and grid. Each one has a contract in catalog/.
import { MuElement } from './base.js';
import { ReadMoreElement, READ_MORE_CSS } from './read-more.js';
import { el, sheet } from '../render/dom.js';
import { FieldElement, MuChoiceField, MuDateField, MuField, MuLongTextField, MuNumberField, MuToggleField } from './fields.js';
import { MuDateRangeField, MuDateTimeField, MuTimeField } from './date-time-fields.js';
import { MuMultiChoiceField } from './multi-choice-field.js';
import { MuRangeField } from './range-field.js';
import { MuRichTextField } from './rich-text-field.js';
import { MuSelectField } from './select-field.js';

export { FieldElement, MuChoiceField, MuDateField, MuField, MuLongTextField, MuMultiChoiceField, MuNumberField, MuRangeField, MuRichTextField, MuSelectField, MuToggleField };
export { MuDateRangeField, MuDateTimeField, MuTimeField };

export const FIELDS = Object.freeze({
  field: ['mu-field', MuField],
  'rich-text-field': ['mu-rich-text-field', MuRichTextField],
  'long-text-field': ['mu-long-text-field', MuLongTextField],
  'number-field': ['mu-number-field', MuNumberField],
  'date-field': ['mu-date-field', MuDateField],
  'choice-field': ['mu-choice-field', MuChoiceField],
  'toggle-field': ['mu-toggle-field', MuToggleField],
  'range-field': ['mu-range-field', MuRangeField],
  'select-field': ['mu-select-field', MuSelectField],
  'multi-choice-field': ['mu-multi-choice-field', MuMultiChoiceField],
  'date-range-field': ['mu-date-range-field', MuDateRangeField],
  'time-field': ['mu-time-field', MuTimeField],
  'date-time-field': ['mu-date-time-field', MuDateTimeField],
});

const GAPS = { small: 'var(--mu-space-1)', medium: 'var(--mu-space-2)', large: 'var(--mu-space-4)' };
const COLUMNS = { narrow: 'var(--mu-size-column-narrow)', medium: 'var(--mu-size-column-medium)', wide: 'var(--mu-size-column-wide)' };

export class MuText extends ReadMoreElement {
  static component = 'text';
  static styles = [READ_MORE_CSS, sheet(`
    :host { display: inline; color: var(--mu-color-text); font: var(--mu-type-body); }
    .strong { font-weight: var(--mu-type-weight-strong); }
    .caption { font: var(--mu-type-caption); color: var(--mu-color-text-muted); }
    @media (forced-colors: active) { :host, .caption { color: CanvasText; } }
  `)];

  state(data) {
    return data.text === '' ? 'empty' : 'default';
  }

  render(data) {
    return this.readMore(el('span', { part: 'text', class: data.variant ?? 'body' }, data.text), data.clamp);
  }
}

export class MuButton extends MuElement {
  static component = 'button';
  static focus = true;
  static styles = [sheet(`
    :host { display: inline-block; }
    button {
      font: var(--mu-type-body); min-height: 2.75rem; min-width: 2.75rem;
      padding: var(--mu-space-2) var(--mu-space-3); border-radius: var(--mu-radius-2);
      border: 1px solid var(--mu-color-accent); background: var(--mu-color-accent); color: var(--mu-color-on-accent);
      cursor: pointer;
    }
    button.secondary { background: var(--mu-color-surface); color: var(--mu-color-accent); border-color: var(--mu-color-border); }
    @media (forced-colors: active) {
      button, button.secondary { background: ButtonFace; color: ButtonText; border-color: ButtonText; forced-color-adjust: none; }
    }
  `)];

  state(data) {
    return data.disabled ? 'disabled' : 'default';
  }

  render(data) {
    const button = el('button', { type: 'button', part: 'button', class: data.emphasis ?? 'primary' }, data.label);
    button.disabled = Boolean(data.disabled);
    // The button only tells the code that contains it. It never sends an action.
    button.addEventListener('click', () => {
      this.dispatchEvent(new CustomEvent('mu-press', { bubbles: true, composed: true }));
    });
    return [button];
  }
}

export class MuStack extends MuElement {
  static component = 'stack';
  static styles = [sheet(`
    :host { display: block; }
    .layout { display: flex; flex-direction: column; }
    .layout.row { flex-direction: row; flex-wrap: wrap; }
  `)];

  render(data) {
    const layout = el('div', { role: 'group', 'aria-label': data.label, part: 'layout', class: 'layout ' + (data.direction ?? 'column') },
      document.createElement('slot'));
    layout.style.gap = GAPS[data.gap ?? 'medium'];
    return [layout];
  }
}

export class MuGrid extends MuElement {
  static component = 'grid';
  static styles = [sheet(`
    :host { display: block; }
    .layout { display: grid; }
  `)];

  render(data) {
    const layout = el('div', { role: 'group', 'aria-label': data.label, part: 'layout', class: 'layout' },
      document.createElement('slot'));
    layout.style.gap = GAPS[data.gap ?? 'medium'];
    layout.style.gridTemplateColumns = `repeat(auto-fill, minmax(min(100%, ${COLUMNS[data.min_column ?? 'medium']}), 1fr))`;
    return [layout];
  }
}

export function define(registry = customElements) {
  for (const [name, element] of [['mu-text', MuText], ['mu-button', MuButton], ...Object.values(FIELDS),
    ['mu-stack', MuStack], ['mu-grid', MuGrid]]) {
    if (!registry.get(name)) registry.define(name, element);
  }
}
