// A table of text. The application or the service gives each row status.
import { MuElement } from '../primitives/base.js';
import { EDGE, el, sheet } from '../render/dom.js';

export class MuTable extends MuElement {
  static component = 'table';
  static styles = [sheet(`
    :host { display: block; min-width: 0; }
    /* A long table scrolls in this area, and the column headers stay in view. */
    .table { overflow: auto; max-block-size: 30rem; color: var(--mu-color-text); background: var(--mu-color-surface); font: var(--mu-type-body); }
    table { border-collapse: separate; border-spacing: 0; width: 100%; min-width: 24rem; table-layout: fixed; }
    caption { text-align: start; font-weight: var(--mu-type-weight-strong); padding: var(--mu-space-2) var(--mu-space-3); }
    th, td { text-align: start; vertical-align: top; overflow-wrap: anywhere;
      padding: var(--mu-space-2) var(--mu-space-3); border-bottom: 1px solid ${EDGE}; }
    th { font-weight: var(--mu-type-weight-strong); }
    thead th { position: sticky; inset-block-start: 0; z-index: 1; background: var(--mu-color-surface); }
    button { font: inherit; color: inherit; background: transparent; border: 1px solid var(--mu-color-border);
      min-height: 2.75rem; min-width: 2.75rem; padding: var(--mu-space-2); cursor: pointer; overflow-wrap: anywhere; max-width: 100%; }
    p { margin: var(--mu-space-2) var(--mu-space-3); overflow-wrap: anywhere; }
    .muted { font: var(--mu-type-caption); color: var(--mu-color-text-muted); }
    /* The author stays in view at the bottom of the scroll area. */
    [part="authorship"] { position: sticky; inset-block-end: 0; inset-inline-start: 0; margin: 0;
      padding: var(--mu-space-2) var(--mu-space-3); background: var(--mu-color-surface); }
    @media (forced-colors: active) {
      .table, .muted { color: CanvasText; background: Canvas; }
      th, td { border-color: CanvasText; }
      thead th { background: Canvas; }
      button { color: ButtonText; border-color: ButtonText; background: ButtonFace; }
    }
  `)];

  // The contract format cannot compare a row length, a status or a sort key with another field.
  check(data) {
    if (!data.columns.length) return ['data.columns: at least one column is required'];
    if (data.sort_key !== undefined && data.sort_key >= data.columns.length) return ['data.sort_key: the column does not exist'];
    if (data.sort_direction !== undefined && data.sort_key === undefined) return ['data.sort_direction: a sort key is required'];
    const statuses = new Set(data.status_values ?? []);
    const errors = [];
    data.rows.forEach((row, index) => {
      if (row.data.cells.length !== data.columns.length) {
        errors.push(`data.rows[${index}].data.cells: the cell count must equal the column count`);
      }
      if (row.data.status !== undefined && !statuses.has(row.data.status)) {
        errors.push(`data.rows[${index}].data.status: the status is not in status_values`);
      }
    });
    return errors;
  }

  state(data) {
    return data.rows.length ? 'default' : 'empty';
  }

  render(data) {
    let key = data.sort_key;
    let direction = data.sort_direction ?? 'ascending';
    const hasStatus = Boolean(data.status_values?.length);
    const body = el('tbody');
    const announcement = el('p', { class: 'muted', role: 'status', 'aria-live': 'polite', part: 'sort-status' });
    const headers = data.columns.map((label, index) => {
      const button = el('button', { type: 'button' }, label);
      button.addEventListener('click', () => {
        direction = key === index && direction === 'ascending' ? 'descending' : 'ascending';
        key = index;
        update();
      });
      return el('th', { scope: 'col' }, button);
    });
    // A sort changes only the rows and the sort attributes. The focused header button stays in place.
    const update = () => {
      headers.forEach((header, index) => {
        if (index === key) header.setAttribute('aria-sort', direction);
        else header.removeAttribute('aria-sort');
      });
      const rows = data.rows.map((row, index) => ({ row, index }));
      if (key !== undefined) rows.sort((a, b) => {
        const left = a.row.data.cells[key];
        const right = b.row.data.cells[key];
        const order = left < right ? -1 : left > right ? 1 : 0;
        return (direction === 'ascending' ? order : -order) || a.index - b.index;
      });
      body.replaceChildren(...rows.map(({ row }) => el('tr', {},
        ...row.data.cells.map((value) => el('td', {}, value)),
        hasStatus ? el('td', {}, row.data.status ?? '') : null)));
      announcement.textContent = key === undefined ? '' : `Sorted by ${data.columns[key]}: ${direction}.`;
    };
    update();
    const author = this.fill.slots.authorship;
    const name = author.name ? ` (${author.name})` : '';
    return [el('div', { class: 'table', role: 'group', 'aria-label': data.label, tabindex: '0', part: 'table' },
      el('table', {}, el('caption', {}, data.label),
        el('thead', {}, el('tr', {}, ...headers, hasStatus ? el('th', { scope: 'col' }, 'Status') : null)), body),
      data.rows.length ? null : el('p', { part: 'empty' }, 'No rows. Change the request, or try again later.'), announcement,
      el('p', { class: 'muted', part: 'authorship' }, `Author: ${author.type} ${author.id}${name}`))];
  }
}
