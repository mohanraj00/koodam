// SVG charts use fixed shapes and numeric coordinates. Each label stays text.
import { MuElement } from '../primitives/base.js';
import { EDGE, el, sheet, text } from '../render/dom.js';
import { MuTable } from './table.js';

const SERIES = ['series-1', 'series-2', 'series-3', 'series-4', 'series-5', 'series-6'];
const svg = (tag) => document.createElementNS('http://www.w3.org/2000/svg', tag);
const number = (value) => {
  if (!Number.isFinite(value)) throw new TypeError('chart: a coordinate must be finite');
  return String(value);
};
function label(value, x, y) {
  const node = svg('text');
  node.setAttribute('x', number(x));
  node.setAttribute('y', number(y));
  node.append(text(value));
  return node;
}
function line(x1, y1, x2, y2) {
  const node = svg('line');
  node.setAttribute('x1', number(x1));
  node.setAttribute('y1', number(y1));
  node.setAttribute('x2', number(x2));
  node.setAttribute('y2', number(y2));
  return node;
}
function bar(x, y, width, height) {
  const node = svg('rect');
  node.setAttribute('x', number(x));
  node.setAttribute('y', number(y));
  node.setAttribute('width', number(width));
  node.setAttribute('height', number(height));
  node.setAttribute('part', 'bar');
  return node;
}
function marker(x, y) {
  const node = svg('circle');
  node.setAttribute('cx', number(x));
  node.setAttribute('cy', number(y));
  node.setAttribute('r', '5');
  return node;
}

export class MuChart extends MuElement {
  static component = 'chart';
  static styles = [sheet(`
    :host { display: block; min-width: 0; }
    figure { margin: 0; padding: var(--mu-space-3); color: var(--mu-color-text);
      background: var(--mu-color-surface); font: var(--mu-type-body); border: 1px solid ${EDGE}; }
    h3 { margin: 0; font: inherit; font-weight: var(--mu-type-weight-strong); }
    p { margin: var(--mu-space-2) 0; }
    h3, p, li { overflow-wrap: anywhere; }
    .plot { overflow-x: auto; }
    svg { display: block; color: var(--mu-color-text); }
    svg text { fill: var(--mu-color-text); font: var(--mu-type-caption); text-anchor: middle; }
    line, rect, circle { stroke: var(--mu-color-text); stroke-width: 2; fill: var(--mu-color-surface); }
    .axis { stroke: var(--mu-color-border); }
    .series-1 { stroke-dasharray: none; }
    .series-2 { stroke-dasharray: 8 3; }
    .series-3 { stroke-dasharray: 2 3; }
    .series-4 { stroke-dasharray: 10 3 2 3; }
    .series-5 { stroke-dasharray: 5 2 2 2; }
    .series-6 { stroke-dasharray: 12 4 4 4; }
    [part="point"]:focus { outline: 2px solid var(--mu-color-focus); outline-offset: 3px; }
    button { font: inherit; color: inherit; background: var(--mu-color-surface); border: 1px solid var(--mu-color-border);
      padding: var(--mu-space-2); min-height: 2.75rem; cursor: pointer; }
    .muted { color: var(--mu-color-text-muted); font: var(--mu-type-caption); }
    @media (forced-colors: active) {
      figure, button { color: CanvasText; background: Canvas; border-color: CanvasText; }
      .muted { color: CanvasText; }
      svg text { fill: CanvasText; }
      line, rect, circle { stroke: CanvasText; fill: Canvas; }
      [part="point"]:focus { outline-color: Highlight; }
    }
  `)];

  // The contract format cannot compare the sizes of two lists.
  check(data) {
    if (!data.categories.length) return ['data.categories: at least one category is required'];
    if (!data.series.length) return ['data.series: at least one series is required'];
    return data.series.flatMap((series, index) => series.data.values.length === data.categories.length ? []
      : [`data.series[${index}].data.values: the value count must equal the category count`]);
  }

  render(data) {
    const series = data.series.map((item) => item.data);
    const stacked = data.type === 'stacked_bar';
    const step = Math.max(140, series.length * 62);
    const width = Math.max(640, data.categories.length * step + 140);
    const left = 100;
    const right = width - 40;
    const slot = (right - left) / data.categories.length;
    const top = 40;
    const bottom = 310;
    const positive = data.categories.map(() => 0);
    const negative = data.categories.map(() => 0);
    const ranges = series.map((item) => item.values.map((value, index) => {
      const start = stacked ? (value < 0 ? negative[index] : positive[index]) : 0;
      const end = start + value;
      if (stacked) {
        if (value < 0) negative[index] = end;
        else positive[index] = end;
      }
      return { start, end };
    }));
    const ends = ranges.flatMap((items) => items.map(({ end }) => end));
    const low = Math.min(0, ...ends);
    const high = Math.max(0, ...ends);
    // Divide before multiplication so that very small finite values stay finite.
    const span = high - low || 1;
    const y = (value) => bottom - ((value - low) / span) * (bottom - top);
    const x = (index) => left + slot * (index + 0.5);
    const plot = svg('svg');
    plot.setAttribute('viewBox', `0 0 ${number(width)} 500`);
    plot.setAttribute('width', number(width));
    plot.setAttribute('height', '500');
    const axis = line(left, y(0), right, y(0));
    axis.setAttribute('class', 'axis');
    plot.append(axis, line(left, top, left, bottom));
    // Endpoint labels and the zero line state the scale. Values stay in their original units.
    plot.append(label(String(low), 45, bottom), label(String(high === low ? 1 : high), 45, top));
    if (low < 0 && high > 0) plot.append(label('0', 45, y(0)));
    data.categories.forEach((category, index) => {
      // Short lines keep long category labels inside their category column.
      const chars = [...category];
      for (let offset = 0; offset < chars.length; offset += 12) {
        plot.append(label(chars.slice(offset, offset + 12).join(''), x(index), 345 + (offset / 12) * 20));
      }
    });

    const points = [];
    const descriptions = [];
    const valueText = el('p', { part: 'value', role: 'status', 'aria-live': 'polite' });
    series.forEach((item, seriesIndex) => {
      const group = svg('g');
      item.values.forEach((value, index) => {
        const { start, end } = ranges[seriesIndex][index];
        const center = data.type === 'bar' ? x(index) - slot * 0.4 + (seriesIndex + 0.5) * slot * 0.8 / series.length : x(index);
        if (data.type === 'line' && index > 0) {
          const segment = line(x(index - 1), y(item.values[index - 1]), center, y(value));
          segment.setAttribute('class', SERIES[seriesIndex]);
          segment.setAttribute('part', 'segment');
          group.append(segment);
        }
        const point = svg('g');
        const title = svg('title');
        const id = `point-${points.length}`;
        const description = `${item.label}, ${data.categories[index]}: ${value} ${data.units}`;
        title.setAttribute('id', id);
        title.append(text(description));
        point.setAttribute('role', 'img');
        point.setAttribute('aria-labelledby', id);
        point.setAttribute('tabindex', points.length === 0 ? '0' : '-1');
        point.setAttribute('part', 'point');
        const shape = data.type === 'line' ? marker(center, y(value))
          : bar(center - (stacked ? slot * 0.3 : slot * 0.35 / series.length), Math.min(y(start), y(end)),
            stacked ? slot * 0.6 : slot * 0.7 / series.length, Math.abs(y(end) - y(start)));
        shape.setAttribute('class', SERIES[seriesIndex]);
        point.append(title, shape, label(`${seriesIndex + 1}: ${value}`, center,
          Math.max(18, y(end) - 9 - (stacked ? seriesIndex * 16 : 0))));
        const position = points.length;
        point.addEventListener('focus', () => {
          points.forEach((node, i) => node.setAttribute('tabindex', i === position ? '0' : '-1'));
          valueText.textContent = description;
        });
        point.addEventListener('click', () => point.focus());
        point.addEventListener('keydown', (event) => {
          const delta = event.key === 'ArrowRight' || event.key === 'ArrowDown' ? 1
            : event.key === 'ArrowLeft' || event.key === 'ArrowUp' ? -1 : undefined;
          if (delta === undefined) return;
          event.preventDefault();
          points[(position + delta + points.length) % points.length].focus();
        });
        points.push(point);
        descriptions.push(description);
        group.append(point);
      });
      plot.append(group);
    });
    valueText.textContent = descriptions[0];

    // The table is the existing component. It gets the same trusted actor and provenance.
    const table = document.createElement('mu-table');
    if (!(table instanceof MuTable)) throw new TypeError('chart: define mu-table before the chart');
    table.id = 'chart-table';
    table.actor = this.actor;
    table.fill = { component: 'table', version: '0.1.0', slots: this.fill.slots, data: {
      label: data.title, columns: [data.x_label, ...series.map((item) => item.label)],
      rows: data.categories.map((category, index) => ({ component: 'table-row', version: '0.1.0',
        data: { cells: [category, ...series.map((item) => `${item.values[index]} ${data.units}`)] } })),
    } };
    table.hidden = true;
    const toggle = el('button', { type: 'button', 'aria-expanded': 'false', 'aria-controls': 'chart-table' }, 'Show data table');
    toggle.addEventListener('click', () => {
      table.hidden = !table.hidden;
      toggle.setAttribute('aria-expanded', String(!table.hidden));
      toggle.textContent = table.hidden ? 'Show data table' : 'Hide data table';
    });
    const author = this.fill.slots.authorship;
    return [el('figure', { role: 'group', 'aria-labelledby': 'chart-title', part: 'chart' },
      el('h3', { id: 'chart-title' }, data.title),
      el('p', {}, `${data.y_label} (${data.units})`),
      el('ul', {}, ...series.map((item, index) => el('li', {}, `${index + 1}. ${item.label}`))),
      el('p', { class: 'muted' }, 'Use arrow keys to move between points. Each value starts with its series number.'),
      el('div', { class: 'plot' }, plot), el('p', {}, data.x_label), valueText, toggle, table,
      el('p', { class: 'muted', part: 'authorship' }, `Author: ${author.type} ${author.id}${author.name ? ` (${author.name})` : ''}`))];
  }
}
