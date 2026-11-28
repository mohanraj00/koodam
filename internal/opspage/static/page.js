// The operations page reads the spend API of the same origin. It keeps the bearer token only in
// a variable of this module. It never writes the token to storage, the DOM, the URL or the console.
import { define } from './kit/src/components/index.js';
import { createKit, fill } from './kit/src/kit.js';

const POLL_MS = 30000;
// A kit table holds up to 500 rows, so read up to 5 pages of 100 items.
const MAX_PAGES = 5;
const RUN_COLUMNS = ['Namespace', 'Name', 'Phase', 'Models', 'Budget', 'Spend', 'Remaining', 'Start', 'End'];
const BUDGET_COLUMNS = ['Namespace', 'Period', 'Period end', 'Limit', 'Spend', 'Reserved', 'Remaining', 'Waiting runs',
  'Exceeded'];
const CONDITION_COLUMNS = ['Object', 'Type', 'Status', 'Reason', 'Message', 'Last change'];
// The kit table of v0.7.0 gives each column the same width, so the pod table joins some fields into one cell.
const POD_COLUMNS = ['Pod', 'Pod phase', 'Pod start', 'Container', 'Ready', 'Restarts', 'State'];
// The log tail has the last LOG_LINES lines of the run.
const LOG_LINES = 200;
// The watch rules of the API: the first wait before a new connection, and a ping every 15 s. With no
// bytes for WATCH_IDLE_MS, the page closes the connection and connects again.
const WATCH_RETRY_MS = 5000;
const WATCH_IDLE_MS = 45000;
// The limits of a wait before a new connection, and of the text of one event.
const MIN_WAIT_MS = 1000;
const MAX_WAIT_MS = 300000;
const MAX_EVENT = 1 << 20;
// The server is busy or a read upstream failed. The page connects again after the wait.
const RETRY_STATUS = new Set([502, 503, 504]);
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
// view is the run of the run detail. Each new run detail and each forget makes a new view, so a read or a
// watch of an old view cannot change the page.
let view = null;

const state = kit.mount('status', { state: 'unknown', text: 'Give a token to read the data.' }, { parent: byId('state') });
const runTable = kit.mount('table', { label: 'Runs', columns: RUN_COLUMNS, rows: [] }, { parent: byId('runs') });
const labelTable = kit.mount('table', { label: 'Spend by label', columns: ['Label value', 'Spend'], rows: [] },
  { parent: byId('by-label') });
const namespaceTable = kit.mount('table', { label: 'Spend by namespace', columns: ['Namespace', 'Spend'], rows: [] },
  { parent: byId('by-namespace') });
const budgetTable = kit.mount('table', { label: 'Period budgets', columns: BUDGET_COLUMNS, rows: [] },
  { parent: byId('budgets') });
const watchState = kit.mount('status', { state: 'unknown', text: 'No live watch.' }, { parent: byId('watch-state') });
const runState = kit.mount('status', { state: 'unknown', text: 'No run detail.' }, { parent: byId('run-state') });
const logState = kit.mount('status', { state: 'unknown', text: 'No log.' }, { parent: byId('log-state') });
const jobCard = kit.mount('card', { title: 'Job', summary: 'No data.' }, { parent: byId('job') });
const podTable = kit.mount('table', { label: 'Pods and containers', columns: POD_COLUMNS, rows: [] },
  { parent: byId('pods') });
const conditionTable = kit.mount('table', { label: 'Status conditions', columns: CONDITION_COLUMNS, rows: [] },
  { parent: byId('conditions') });

function show(element, name, data) {
  element.fill = kit.outer(name, data);
}

function setState(kind, text) {
  show(state, 'status', { state: kind, text });
}

const money = (n) => (typeof n === 'number' && Number.isFinite(n)
  ? n.toLocaleString('en-US', { minimumFractionDigits: 2, maximumFractionDigits: 4 }) : 'Unknown');
// clip bounds a text to a kit length limit, in code points. The kit refuses a fill with a longer text.
const clip = (text, max) => {
  const chars = Array.from(text);
  return chars.length > max ? `${chars.slice(0, max - 1).join('')}\u2026` : text;
};
// The kit limits: a table cell, a table name or a card title, a card summary, and a status text or a fact value.
const CELL = 500;
const TITLE = 120;
const SUMMARY = 1000;
const STATUS = 200;
const time = (t) => (t ? new Date(t).toISOString().replace('.000Z', 'Z') : '');

function todayUTC() {
  return new Date().toISOString().slice(0, 10);
}

class Refusal extends Error {}

// request is the one network read of the page. It sends a GET request to a relative /v1/ path, with the token in
// the Authorization header and never in the URL.
function request(path, accept, signal) {
  return fetch(path, {
    headers: { Authorization: `Bearer ${token}`, Accept: accept },
    cache: 'no-store', credentials: 'omit', redirect: 'error', referrerPolicy: 'no-referrer', signal,
  });
}

async function read(path) {
  const response = await request(path, 'application/json');
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
      cells: [r.namespace, r.name, r.phase ?? 'Unknown', clip((r.models ?? []).join(', '), CELL), money(r.budget),
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

const runPath = (run) => `v1/runs/${encodeURIComponent(run.namespace)}/${encodeURIComponent(run.name)}`;

// showDetail shows the run that the viewer selected. A new run starts a new view. If the live watch is not open,
// each refresh reads the run and its log again. If the watch is open, a refresh reads the log again only after a
// log error, for example when the pod or the container of a new run is not ready.
function showDetail() {
  const run = runs.find((r) => r.id === byId('run').value);
  if (!run) {
    closeView();
    byId('detail').replaceChildren();
    return;
  }
  if (view?.id !== run.id) openView(run);
  else if (!view.open) readView(view);
  else if (view.logFailed) readLog(view);
  showCard();
}

function showCard() {
  const run = runs.find((r) => r.id === view?.id);
  const parent = byId('detail');
  if (!run) return;
  // The status of the view is newer than the list, so the card shows it in place of the list values.
  const s = { ...run, ...(view.status ?? {}) };
  // A kit card holds up to 6 facts. The summary gives the other fields.
  const facts = [
    ['Phase', s.phase ?? 'Unknown'], ['Budget', money(run.budget)], ['Spend', money(run.spend)],
    ['Remaining budget', money(run.remainingBudget)], ['Start', time(s.startedAt) || 'Not started'],
    ['End', time(s.completedAt) || 'Not ended'],
  ].map(([label, value]) => fill('fact', { label, value: clip(value, STATUS) }));
  const models = (run.models ?? []).join(', ') || 'none';
  const summary = `Namespace ${run.namespace}. Run ID ${run.id}. Models: ${models}. Created ${time(run.createdAt)}.`;
  const data = { title: clip(run.name, TITLE), summary: clip(summary, SUMMARY), facts };
  const card = parent.firstElementChild;
  if (card) show(card, 'card', data);
  else kit.mount('card', data, { parent });
}

function openView(run) {
  closeView();
  view = {
    id: run.id, path: runPath(run), status: null, job: null, events: 0, detailReads: 0, logReads: 0, logFailed: false,
    retry: WATCH_RETRY_MS, open: false, connection: null, timer: null, idle: null,
  };
  byId('run-view').hidden = false;
  readView(view);
  connect(view);
}

// closeView stops the watch of the view with its AbortController and clears the run view.
function closeView() {
  const v = view;
  view = null;
  if (v) {
    clearTimeout(v.timer);
    clearTimeout(v.idle);
    v.connection?.abort();
  }
  byId('run-view').hidden = true;
  setStatus(watchState, 'unknown', 'No live watch.');
  setStatus(runState, 'unknown', 'No run detail.');
  setStatus(logState, 'unknown', 'No log.');
  show(jobCard, 'card', { title: 'Job', summary: 'No data.' });
  showRows(podTable, 'Pods and containers', POD_COLUMNS, []);
  showRows(conditionTable, 'Status conditions', CONDITION_COLUMNS, []);
  byId('log').textContent = '';
}

function setStatus(element, kind, text) {
  show(element, 'status', { state: kind, text: clip(text, STATUS) });
}

function showRows(table, label, columns, rows) {
  show(table, 'table', {
    label, columns, rows: rows.map((cells) => fill('table-row', { cells: cells.map((c) => clip(String(c ?? ''), CELL)) })),
  });
}

function readView(v) {
  readDetail(v);
  readLog(v);
}

// problemText gives the error text of the API, for example the text of the time limit or of the line limit.
async function problemText(response) {
  let text = '';
  try {
    const body = await response.json();
    if (typeof body?.error === 'string') text = body.error;
  } catch {
    // The response has no problem body.
  }
  return text ? `${text} (status ${response.status})` : `The spend API gave status ${response.status}.`;
}

// readJSON reads a path of a view. It gives the body, or the error text. If the server did not accept the token,
// the page forgets the token and readJSON gives no error text.
async function readJSON(v, path) {
  try {
    const response = await request(path, 'application/json');
    if (v !== view) return [null, null];
    if (response.status === 401) {
      forget();
      setState('failed', REFUSALS[401]);
      return [null, null];
    }
    if (!response.ok) return [null, await problemText(response)];
    return [await response.json(), null];
  } catch {
    return [null, 'The spend API is not available.'];
  }
}

async function readDetail(v) {
  const read = ++v.detailReads;
  const events = v.events;
  const [detail, error] = await readJSON(v, v.path);
  if (v !== view || read !== v.detailReads || !(detail || error)) return;
  if (error) {
    setStatus(runState, 'failed', error);
    return;
  }
  v.job = detail.job ?? null;
  // A status event that came during the read is newer than the read, so the page keeps it.
  if (events === v.events) {
    v.status = { phase: detail.phase, startedAt: detail.startedAt, completedAt: detail.completedAt,
      conditions: detail.conditions ?? [] };
  }
  setStatus(runState, 'passed', `Read the run at ${time(new Date().toISOString())}.`);
  showCard();
  showJob(v.job, detail.pods ?? []);
  showConditions(v);
}

function showJob(job, pods) {
  if (job) {
    show(jobCard, 'card', {
      title: clip(job.name, TITLE),
      facts: [['Active', job.active], ['Succeeded', job.succeeded], ['Failed', job.failed],
        ['Start', time(job.startedAt) || 'Not started'], ['End', time(job.completedAt) || 'Not ended']]
        .map(([label, value]) => fill('fact', { label, value: clip(String(value), STATUS) })),
    });
  } else {
    show(jobCard, 'card', { title: 'No Job', summary: 'The Agent has no run Job.' });
  }
  // One row for each container. The kit table holds up to 500 rows, and the API gives up to 10 pods.
  const state = (c) => [c.state, c.reason, c.exitCode === undefined ? '' : `exit code ${c.exitCode}`]
    .filter((part) => part !== undefined && part !== '').join(', ');
  const rows = pods.flatMap((p) => (p.containers?.length ? p.containers : [{}]).map((c) => [
    p.name, p.phase, time(p.startedAt), c.name ? `${c.name}${c.init ? ' (init)' : ''}` : '',
    c.name ? (c.ready ? 'Yes' : 'No') : '', c.restartCount, state(c),
  ])).slice(0, 500);
  showRows(podTable, 'Pods and containers', POD_COLUMNS, rows);
}

// showConditions shows the redacted conditions of the Agent and of the Job as the server gives them.
function showConditions(v) {
  const rows = (object, list) => (list ?? []).map((c) => [object, c.type, c.status, c.reason, c.message,
    time(c.lastTransitionTime)]);
  showRows(conditionTable, 'Status conditions', CONDITION_COLUMNS,
    [...rows('Agent', v.status?.conditions), ...rows('Job', v.job?.conditions)].slice(0, 500));
}

// readLog shows the redacted log tail as text. The page never parses the log as HTML.
async function readLog(v) {
  const read = ++v.logReads;
  const [log, error] = await readJSON(v, `${v.path}/log?lines=${LOG_LINES}`);
  if (v !== view || read !== v.logReads || !(log || error)) return;
  v.logFailed = Boolean(error);
  if (error) {
    byId('log').textContent = '';
    setStatus(logState, 'failed', `${error} The page reads the log again in ${seconds(POLL_MS)} s.`);
    return;
  }
  const lines = Array.isArray(log.lines) ? log.lines : [];
  byId('log').textContent = lines.join('\n');
  const more = log.truncated ? ` The log has more lines. The page shows the last ${LOG_LINES} lines.` : '';
  setStatus(logState, 'passed',
    `${lines.length} lines of the container ${log.container} of the pod ${log.pod}.${more}`);
}

const seconds = (ms) => Math.ceil(ms / 1000);
const bound = (ms) => Math.min(MAX_WAIT_MS, Math.max(MIN_WAIT_MS, ms));

// retryAfter gives the wait of a Retry-After header, in seconds or as a date.
function retryAfter(value, fallback) {
  if (/^\d+$/.test(value ?? '')) return bound(Number(value) * 1000);
  const at = Date.parse(value ?? '');
  return Number.isNaN(at) ? fallback : bound(at - Date.now());
}

// connect reads the watch of a view with fetch and a stream reader. EventSource cannot send the
// Authorization header. After an end event or a network error, the page connects again after the retry
// time. If the watch is not available, the page reads the run again at each refresh.
async function connect(v) {
  if (v !== view) return;
  const connection = new AbortController();
  v.connection = connection;
  let wait = v.retry;
  try {
    const response = await request(`${v.path}/watch`, 'text/event-stream', connection.signal);
    if (v !== view) return;
    const stream = response.ok && response.body &&
      (response.headers.get('Content-Type') ?? '').startsWith('text/event-stream');
    if (response.status === 401) {
      forget();
      setState('failed', REFUSALS[401]);
      return;
    }
    if (response.status === 429) {
      wait = retryAfter(response.headers.get('Retry-After'), v.retry);
      setStatus(watchState, 'pending', `${await problemText(response)} The page connects again in ${seconds(wait)} s.`);
    } else if (RETRY_STATUS.has(response.status)) {
      setStatus(watchState, 'pending', `${await problemText(response)} The page connects again in ${seconds(wait)} s.`);
    } else if (!stream) {
      const why = response.ok ? 'The server did not send an event stream.' : await problemText(response);
      setStatus(watchState, 'unknown',
        `The live watch is not available. ${why} The page reads the run again every ${seconds(POLL_MS)} s.`);
      return;
    } else {
      v.open = true;
      setStatus(watchState, 'passed', 'The live watch is open.');
      const result = await readEvents(v, response.body, connection);
      v.open = false;
      if (v !== view) return;
      if (result.deleted) {
        setStatus(watchState, 'unknown', 'The Agent is deleted. The live watch stopped.');
        return;
      }
      wait = v.retry;
      const reason = result.reason ? ` (${result.reason})` : '';
      setStatus(watchState, 'pending', `The live watch ended${reason}. The page connects again in ${seconds(wait)} s.`);
      readView(v);
    }
  } catch {
    if (v !== view) return;
    v.open = false;
    setStatus(watchState, 'pending', `The live watch is not connected. The page connects again in ${seconds(wait)} s.`);
  }
  v.timer = setTimeout(() => connect(v), wait);
}

// readEvents parses the server-sent events of a watch: the retry field, the events status, deleted and end, and
// the comment lines, for example ": ping". It gives the result when the stream ends.
async function readEvents(v, body, connection) {
  const reader = body.pipeThrough(new TextDecoderStream()).getReader();
  const idle = () => {
    clearTimeout(v.idle);
    v.idle = setTimeout(() => connection.abort(), WATCH_IDLE_MS);
  };
  let buffer = '';
  let name = '';
  let data = [];
  let size = 0;
  idle();
  try {
    for (;;) {
      const { value, done } = await reader.read();
      if (done) return {};
      idle();
      buffer += value;
      for (let at = buffer.indexOf('\n'); at >= 0; at = buffer.indexOf('\n')) {
        const line = buffer.slice(0, at).replace(/\r$/, '');
        buffer = buffer.slice(at + 1);
        if (line === '') {
          const result = data.length ? dispatch(v, name || 'message', data.join('\n')) : null;
          name = '';
          data = [];
          size = 0;
          if (result) return result;
        } else if (!line.startsWith(':')) {
          // A line that starts with a colon is a comment, for example ": ping". The ping only keeps the
          // connection open.
          const colon = line.indexOf(':');
          const field = colon < 0 ? line : line.slice(0, colon);
          const text = colon < 0 ? '' : line.slice(colon + 1).replace(/^ /, '');
          if (field === 'event') name = text;
          else if (field === 'data') {
            data.push(text);
            size += text.length;
          } else if (field === 'retry' && /^\d+$/.test(text)) v.retry = bound(Number(text));
        }
      }
      if (buffer.length + size > MAX_EVENT) throw new Error('The event is too long.');
    }
  } finally {
    clearTimeout(v.idle);
    reader.cancel().catch(() => {});
  }
}

// dispatch acts on one event. It gives a result if the stream must stop.
function dispatch(v, name, data) {
  if (v !== view) return { reason: '' };
  let body;
  try {
    body = JSON.parse(data);
  } catch {
    return null;
  }
  if (!body || typeof body !== 'object') return null;
  if (name === 'status' || name === 'deleted') onStatus(v, body);
  if (name === 'deleted') return { deleted: true };
  if (name === 'end') return { reason: typeof body.reason === 'string' ? clip(body.reason, 40) : '' };
  return null;
}

// onStatus shows the status of a status event at once. If the phase changed, the page reads the run path and the
// log again, because the event has no Job and no pods.
function onStatus(v, s) {
  const before = v.status;
  v.events += 1;
  v.status = { phase: s.phase, startedAt: s.startedAt, completedAt: s.completedAt,
    conditions: Array.isArray(s.conditions) ? s.conditions : [] };
  showCard();
  showConditions(v);
  if (before && before.phase !== s.phase) readView(v);
}

function spendRows(items, empty) {
  return items.map((g) => fill('table-row', { cells: [clip(g.labelValue || empty, CELL), money(g.spend)] }));
}

// showBudgets shows the SpendBudget ledger of each namespace. The list has one item for each namespace at most.
function showBudgets(items) {
  show(budgetTable, 'table', {
    label: 'Period budgets', columns: BUDGET_COLUMNS,
    rows: items.map((b) => fill('table-row', {
      cells: [b.namespace, b.period, time(b.periodEnd), money(b.limit), money(b.spend), money(b.reserved),
        money(b.remaining), String(b.waitingRuns), b.exceeded ? 'Yes' : 'No'],
    })),
  });
}

function clearData() {
  showRuns([]);
  showBudgets([]);
  show(labelTable, 'table', { label: 'Spend by label', columns: ['Label value', 'Spend'], rows: [] });
  show(namespaceTable, 'table', { label: 'Spend by namespace', columns: ['Namespace', 'Spend'], rows: [] });
}

async function refresh() {
  clearTimeout(timer);
  const current = ++generation;
  if (!token) return;
  const label = byId('label').value.trim();
  try {
    const [[runItems, moreRuns], [labelItems, moreLabels], [namespaceItems, moreNamespaces], budgets] = await Promise.all([
      readAll('v1/runs'), readAll(periodQuery(`label:${label}`)), readAll(periodQuery('namespace')), read('v1/budgets'),
    ]);
    if (current !== generation) return;
    showRuns(runItems);
    showBudgets(budgets.items);
    show(labelTable, 'table', {
      label: clip(`Spend by ${label}`, TITLE), columns: ['Label value', 'Spend'], rows: spendRows(labelItems, 'No value'),
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
  closeView();
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
