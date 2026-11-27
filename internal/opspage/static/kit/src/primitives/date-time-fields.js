import { el, sheet } from '../render/dom.js';
import { dateRangeProblem, isLocalDateTime, timeProblem } from '../render/validate.js';
import { FieldElement } from './fields.js';

const DATE_TIME_CSS = sheet(`
  input:is([type="date"], [type="time"], [type="datetime-local"]) {
    min-inline-size: 44px; min-block-size: 44px; max-inline-size: 100%;
  }
  .dates { min-inline-size: 0; }
  .with-zone { display: flex; flex-wrap: wrap; align-items: center; gap: var(--mu-space-2); }
  [part="time-zone"], [part="label"] { overflow-wrap: anywhere; }
`);
const STYLES = [...FieldElement.styles, DATE_TIME_CSS];

function nativeInput(field, id, type, value, data) {
  const input = el('input', { id, type });
  input.value = value;
  input.required = true;
  if (data.earliest !== undefined) input.min = data.earliest;
  if (data.latest !== undefined) input.max = data.latest;
  if (data.step !== undefined) {
    // HTML uses seconds. A step of one day or more has the same effect within a day.
    input.step = String(Math.min(data.step, 1440) * 60);
  }
  input.addEventListener('input', () => field.changedValue());
  return input;
}

class NativeDateTimeField extends FieldElement {
  static styles = STYLES;
  static submitOnEnter = false;

  #input;

  read() {
    return this.#input.value;
  }

  control(data, id) {
    const input = nativeInput(this, id, this.constructor.inputType, data.value, data);
    input.setAttribute('part', 'input');
    this.#input = input;
    return { node: input, targets: [input] };
  }
}

export class MuDateRangeField extends FieldElement {
  static component = 'date-range-field';
  static styles = STYLES;
  static submitOnEnter = false;

  #inputs = [];

  read() {
    return this.#inputs.map((input) => input.value);
  }

  get changed() {
    const value = this.value;
    return value !== null && value.some((date, index) => date !== this.initial[index]);
  }

  reason(value, data) {
    return dateRangeProblem(value, data)?.message ?? null;
  }

  problemTarget() {
    return this.#inputs[dateRangeProblem(this.read(), this.fill.data)?.index ?? 1];
  }

  control(data, id) {
    const start = nativeInput(this, id + '-start', 'date', data.value[0], data);
    const end = nativeInput(this, id + '-end', 'date', data.value[1], data);
    start.setAttribute('part', 'start');
    end.setAttribute('part', 'end');
    this.#inputs = [start, end];
    const label = el('span', { id: id + '-label', part: 'label' }, data.label);
    const group = el('div', { class: 'box dates', role: 'group', 'aria-labelledby': label.id, part: 'group' },
      el('label', { for: start.id, part: 'date-label' }, 'Start date'), start,
      el('label', { for: end.id, part: 'date-label' }, 'End date'), end);
    return { node: el('div', { class: 'box' }, label, group), targets: this.#inputs, labelled: true };
  }
}

export class MuTimeField extends NativeDateTimeField {
  static component = 'time-field';
  static inputType = 'time';

  reason(value, data) {
    const problem = timeProblem(value, data);
    if (problem === 'the time is outside the range') {
      if (data.earliest && value < data.earliest) return 'Give a time at or after ' + data.earliest + '.';
      return 'Give a time at or before ' + data.latest + '.';
    }
    if (problem === 'the time is off a step') return `Use steps of ${data.step} minutes from ${data.earliest ?? '00:00'}.`;
    return problem ? 'Give a time that exists, as HH:MM.' : null;
  }
}

export class MuDateTimeField extends NativeDateTimeField {
  static component = 'date-time-field';
  static inputType = 'datetime-local';

  control(data, id) {
    const { node: input, targets } = super.control(data, id);
    const zone = el('span', { id: id + '-zone', class: 'hint', part: 'time-zone' }, 'Time zone ' + data.time_zone);
    return { node: el('div', { class: 'with-zone' }, input, zone), targets, descriptionIds: [zone.id] };
  }

  reason(value) {
    return isLocalDateTime(value) ? null : 'Give a date and time that exist.';
  }
}
