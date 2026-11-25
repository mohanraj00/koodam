// The operations page reads the spend API of the same origin. It keeps the bearer token only in
// a variable of this module. It never writes the token to storage, the DOM, the URL or the console.
import { define } from './kit/src/components/index.js';
import { createKit, fill } from './kit/src/kit.js';

const POLL_MS = 30000;
// A kit table holds up to 500 rows, so read up to 5 pages of 100 items.
const MAX_PAGES = 5;
const RUN_COLUMNS = ['Namespace', 'Name', 'Phase', 'Models', 'Budget', 'Spend', 'Remaining', 'Start', 'End'];
const REFUSALS = {
  401: 'The server did not accept the token.',
  403: 'The token cannot list Agents in a namespace of this server.',
};

define();
// The data on the page comes from the spend API, so the spend API is the author of each fill.
const kit = createKit({ actor: { type: 'system', id: 'spend-api', name: 'Spend API' } });

const byId = (id) => document.getElementById(id);
let token = null;
let timer = null;
let runs = [];
// Each refresh gets a number. A refresh shows its data only if no later refresh or forget started.
let generation = 0;

const state = kit.mount('status', { state: 'unknown', text: 'Give a token to read the data.' }, { parent: byId('state') });
const runTable = kit.mount('table', { label: 'Runs', columns: RUN_COLUMNS, rows: [] }, { parent: byId('runs') });
const labelTable = kit.mount('table', { label: 'Spend by label', columns: ['Label value', 'Spend'], rows: [] },
  { parent: byId('by-label') });
const namespaceTable = kit.mount('table', { label: 'Spend by namespace', columns: ['Namespace', 'Spend'], rows: [] },
  { parent: byId('by-namespace') });

function show(element, name, data) {
  element.fill = kit.outer(name, data);
}

function setState(kind, text) {
  show(state, 'status', { state: kind, text });
}

const money = (n) => (typeof n === 'number' && Number.isFinite(n)
  ? n.toLocaleString('en-US', { minimumFractionDigits: 2, maximumFractionDigits: 4 }) : 'Unknown');
const time = (t) => (t ? new Date(t).toISOString().replace('.000Z', 'Z') : '');

function todayUTC() {
  return new Date().toISOString().slice(0, 10);
}

class Refusal extends Error {}

async function read(path) {
  const response = await fetch(path, {
    headers: { Authorization: `Bearer ${token}`, Accept: 'application/json' },
    cache: 'no-store', credentials: 'omit', redirect: 'error', referrerPolicy: 'no-referrer',
  });
  if (REFUSALS[response.status]) throw new Refusal(REFUSALS[response.status]);
  if (!response.ok) throw new Error(`The spend API gave status ${response.status}.`);
  return response.json();
}

// readAll follows nextCursor. It gives the items and true if more items are on the server.
async function readAll(path) {
  const items = [];
  let cursor = '';
  for (let page = 0; page < MAX_PAGES; page += 1) {
    const separator = path.includes('?') ? '&' : '?';
    const body = await read(cursor ? `${path}${separator}cursor=${encodeURIComponent(cursor)}` : path);
    items.push(...body.items);
    cursor = body.nextCursor ?? '';
    if (!cursor) return [items, false];
  }
  return [items, true];
}

function periodQuery(groupBy) {
  const q = new URLSearchParams({ from: byId('from').value, to: byId('to').value, groupBy });
  return `v1/spend?${q}`;
}

function showRuns(items) {
  runs = items;
  show(runTable, 'table', {
    label: 'Runs', columns: RUN_COLUMNS,
    rows: items.map((r) => fill('table-row', {
      cells: [r.namespace, r.name, r.phase ?? 'Unknown', (r.models ?? []).join(', '), money(r.budget),
        money(r.spend), money(r.remainingBudget), time(r.startedAt), time(r.completedAt)],
    })),
  });
  const select = byId('run');
  const chosen = select.value;
  select.replaceChildren(new Option('Select a run', ''),
    ...items.map((r) => new Option(`${r.namespace}/${r.name}`, r.id)));
  select.value = items.some((r) => r.id === chosen) ? chosen : '';
  showDetail();
}

function showDetail() {
  const run = runs.find((r) => r.id === byId('run').value);
  const parent = byId('detail');
  if (!run) {
    parent.replaceChildren();
    return;
  }
  // A kit card holds up to 6 facts. The summary gives the other fields.
  const facts = [
    ['Phase', run.phase ?? 'Unknown'], ['Budget', money(run.budget)], ['Spend', money(run.spend)],
    ['Remaining budget', money(run.remainingBudget)], ['Start', time(run.startedAt) || 'Not started'],
    ['End', time(run.completedAt) || 'Not ended'],
  ].map(([label, value]) => fill('fact', { label, value }));
  const models = (run.models ?? []).join(', ') || 'none';
  const summary = `Namespace ${run.namespace}. Run ID ${run.id}. Models: ${models}. Created ${time(run.createdAt)}.`;
  const data = { title: run.name, summary, facts };
  const card = parent.firstElementChild;
  if (card) show(card, 'card', data);
  else kit.mount('card', data, { parent });
}

function spendRows(items, empty) {
  return items.map((g) => fill('table-row', { cells: [g.labelValue || empty, money(g.spend)] }));
}

function clearData() {
  showRuns([]);
  show(labelTable, 'table', { label: 'Spend by label', columns: ['Label value', 'Spend'], rows: [] });
  show(namespaceTable, 'table', { label: 'Spend by namespace', columns: ['Namespace', 'Spend'], rows: [] });
}

async function refresh() {
  clearTimeout(timer);
  const current = ++generation;
  if (!token) return;
  const label = byId('label').value.trim();
  try {
    const [[runItems, moreRuns], [labelItems, moreLabels], [namespaceItems, moreNamespaces]] = await Promise.all([
      readAll('v1/runs'), readAll(periodQuery(`label:${label}`)), readAll(periodQuery('namespace')),
    ]);
    if (current !== generation) return;
    showRuns(runItems);
    show(labelTable, 'table', {
      label: `Spend by ${label}`, columns: ['Label value', 'Spend'], rows: spendRows(labelItems, 'No value'),
    });
    show(namespaceTable, 'table', {
      label: 'Spend by namespace', columns: ['Namespace', 'Spend'], rows: spendRows(namespaceItems, ''),
    });
    const more = moreRuns || moreLabels || moreNamespaces ? ' The tables show the first 500 items.' : '';
    setState('passed', `Updated at ${time(new Date().toISOString())}.${more}`);
  } catch (error) {
    if (current !== generation) return;
    if (error instanceof Refusal) {
      forget();
      setState('failed', error.message);
      return;
    }
    setState('failed', error instanceof Error && error.message.startsWith('The spend API')
      ? error.message : 'The spend API is not available.');
  }
  if (token) timer = setTimeout(refresh, POLL_MS);
}

function forget() {
  generation += 1;
  token = null;
  clearTimeout(timer);
  clearData();
  setState('unknown', 'Give a token to read the data.');
}

byId('token-form').addEventListener('submit', (event) => {
  event.preventDefault();
  const input = byId('token');
  token = input.value.trim() || null;
  input.value = '';
  setState('pending', 'Reading the data.');
  refresh();
});
byId('forget').addEventListener('click', forget);
byId('run').addEventListener('change', showDetail);
byId('period').addEventListener('submit', (event) => {
  event.preventDefault();
  if (token) refresh();
});

clearData();
byId('to').value = todayUTC();
byId('from').value = `${todayUTC().slice(0, 8)}01`;
