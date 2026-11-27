// A native slider and number box for each value. The range field contract sets the step and the limits.
import { FieldElement } from './fields.js';
import { el, sheet } from '../render/dom.js';
import { copyPair, rangeEditProblem, sameValue, stepGrid } from '../render/range.js';

const MOVES = Object.freeze({
  ArrowUp: 1n, ArrowRight: 1n, ArrowDown: -1n, ArrowLeft: -1n, PageUp: 10n, PageDown: -10n,
});

const RANGE_CSS = sheet(`
  :host { min-inline-size: 0; }
  .range { display: grid; gap: var(--mu-space-1); overflow-wrap: anywhere; }
  .controls {
    display: grid; grid-template-columns: minmax(44px, 1fr) minmax(44px, 8ch);
    align-items: center; gap: var(--mu-space-2);
  }
  .controls input { inline-size: 100%; min-inline-size: 44px; min-block-size: 44px; margin: 0; }
  .controls input[type="range"] {
    accent-color: var(--mu-color-accent); background: transparent; border: 0; padding: 0; cursor: pointer;
  }
  .controls input[type="number"] { padding-inline: var(--mu-space-1); }
  .controls input:disabled { cursor: not-allowed; }
`);

export class MuRangeField extends FieldElement {
  static component = 'range-field';
  static styles = [...FieldElement.styles, RANGE_CSS];
  static submitOnEnter = false;

  #grid;
  #values = [];
  #rows = [];

  read() {
    return this.#values.length === 2 ? copyPair(this.#values) : this.#values[0];
  }

  get changed() {
    const value = this.value;
    return value !== null && !sameValue(value, this.initial);
  }

  get problem() {
    if (!this.fill || this.fill.data.disabled) return null;
    return this.#rows.find((row) => row.problem)?.problem ?? null;
  }

  problemTarget() {
    return this.#rows.find((row) => row.problem)?.number;
  }

  control(data, id) {
    this.#grid = stepGrid(data);
    this.#values = Array.isArray(data.value) ? [...data.value] : [data.value];
    const pair = Array.isArray(data.value);
    const labelId = id + '-label';
    const label = el('label', { id: labelId, for: id, part: 'label' }, data.label);
    this.#rows = (pair ? ['Minimum', 'Maximum'] : ['']).map((name, index) => {
      const handleId = id + '-handle-' + index;
      const slider = el('input', { id: index === 0 ? id : null, type: 'range', part: 'slider',
        'aria-labelledby': pair ? labelId + ' ' + handleId : null });
      const numberName = [data.label, name, 'number'].filter(Boolean).join(' ');
      const number = el('input', { type: 'number', part: 'number', 'aria-label': numberName });
      slider.step = number.step = String(data.step);
      const row = { slider, number, index, problem: null };
      const handle = name ? el('span', { id: handleId, part: 'handle-label' }, name) : null;
      row.node = el('div', {}, handle, el('div', { class: 'controls', part: 'controls' }, slider, number));
      slider.addEventListener('keydown', (event) => this.#key(event, row));
      slider.addEventListener('input', () => {
        if (!slider.disabled) this.#fromSlider(row, this.#grid.move(slider.valueAsNumber, 0n));
      });
      number.addEventListener('input', () => this.#fromNumber(row));
      const finish = () => {
        if (!row.problem) return;
        number.value = String(this.#values[index]);
        this.showProblem();
      };
      number.addEventListener('change', finish);
      number.addEventListener('blur', finish);
      number.addEventListener('keydown', (event) => { if (event.key === 'Enter' && !event.isComposing) finish(); });
      return row;
    });
    this.#sync();
    return {
      node: el('div', { class: 'range', part: pair ? 'group' : null, role: pair ? 'group' : null,
        'aria-labelledby': pair ? labelId : null }, label, ...this.#rows.map((row) => row.node)),
      targets: this.#rows.flatMap(({ slider, number }) => [slider, number]),
      labelled: true,
    };
  }

  #bounds(index) {
    return [this.#values[index - 1] ?? this.fill.data.minimum, this.#values[index + 1] ?? this.#grid.last];
  }

  #sync(activeNumber = null) {
    for (const { slider, number, index } of this.#rows) {
      const [min, max] = this.#bounds(index);
      slider.min = number.min = String(min);
      slider.max = number.max = String(max);
      const value = this.#values[index];
      slider.value = String(value);
      // Keep the text a person is typing, for example the zero after a decimal point.
      if (number !== activeNumber) number.value = String(value);
    }
  }

  #accept(row, value, activeNumber = null) {
    const changed = value !== this.#values[row.index];
    const hadProblem = Boolean(row.problem);
    row.problem = null;
    this.#values[row.index] = value;
    this.#sync(activeNumber);
    if (changed) this.changedValue();
    else if (hadProblem) this.showProblem();
  }

  #fromSlider(row, value) {
    const [min, max] = this.#bounds(row.index);
    const next = Math.max(min, Math.min(max, value));
    // A decimal step too small for a Number can round to a value off the grid.
    if (this.#grid.onStep(next)) this.#accept(row, next);
    else this.#sync();
  }

  #fromNumber(row) {
    if (row.number.disabled) return;
    const value = row.number.valueAsNumber;
    const problem = rangeEditProblem(value, this.fill.data, this.#grid, this.#bounds(row.index));
    if (problem) row.problem = problem;
    else this.#accept(row, value, row.number);
  }

  #key(event, row) {
    if (row.slider.disabled || event.altKey || event.ctrlKey || event.metaKey) return;
    const [min, max] = this.#bounds(row.index);
    let value;
    if (event.key === 'Home') value = min;
    else if (event.key === 'End') value = max;
    else if (Object.hasOwn(MOVES, event.key)) value = this.#grid.move(this.#values[row.index], MOVES[event.key]);
    else return;
    event.preventDefault();
    this.#fromSlider(row, value);
  }
}
