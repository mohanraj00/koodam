import { CONTRACTS } from '../render/registry.js';
import { FILL_ACTIVITY, FILL_FORMAT, MAX_ID, MAX_NAME, actorCopy, actorErrors, cut, isId } from './envelope.js';

const contract = CONTRACTS['run-steps'];
const { label: labelField, steps: stepsField } = contract.data;
const fields = stepsField.items.fields;
const MAX_ROWS = 100;
const MESSAGE_SUFFIX = '/steps';

function nameOf(value, limit) {
  return typeof value === 'string' && value.trim() ? cut(value, limit) : '';
}

function hasOptionalId(event, key) {
  return !Object.hasOwn(event, key) || isId(event[key]);
}

export function createRunSteps({ actor, label = 'Run steps', toolText = false } = {}) {
  const errors = actorErrors(actor, 'actor');
  if (errors.length) throw new TypeError(errors.join('. '));
  const bound = actorCopy(actor);
  const title = nameOf(label, labelField.max_length) || 'Run steps';
  let run = null;
  let longIds = 0;

  function rowId(preferred) {
    const id = cut(preferred, fields.id.max_length);
    if (!id.trim() || id.startsWith('#row-') || run.rows.some(({ row }) => row.id === id)
      || run.steps.some(({ row }) => row.id === id)) return `#row-${run.total + 1}`;
    return id;
  }

  function addRow(entry) {
    const at = run.rows.findIndex((other) => other.group > entry.group);
    run.rows.splice(at < 0 ? run.rows.length : at, 0, entry);
    if (run.rows.length > MAX_ROWS) run.rows.shift();
    run.total += 1;
  }

  function endRun(detail) {
    for (const entry of run.rows) {
      if (entry.row.state === 'running') entry.row = { ...entry.row, state: 'failed', detail };
    }
    run = null;
  }

  function findTool(event) {
    if (!isId(event.toolCallId)) return undefined;
    return run.rows.find(({ callId }) => callId === event.toolCallId);
  }

  function endTool(event, result) {
    const entry = findTool(event);
    if (!entry) return;
    const { row } = entry;
    if (row.state === 'done' && (result === undefined || result === row.tool_result)) return;
    entry.row = { ...row, state: 'done', ...(result === undefined ? {} : { tool_result: result }) };
  }

  const handlers = {
    RUN_FINISHED(event) {
      if (event.runId === run.runId && event.threadId === run.threadId) {
        endRun('The run ended before this step finished.');
      }
    },
    RUN_ERROR() {
      endRun('The run stopped with an error.');
    },
    STEP_STARTED(event) {
      const key = nameOf(event.stepName, MAX_ID);
      if (!key) return;
      const name = cut(key, Math.min(MAX_NAME, fields.name.max_length));
      const occurrence = (run.steps.findLast((step) => step.key === key)?.occurrence ?? 0) + 1;
      const suffix = occurrence > 1 ? `#${occurrence}` : '';
      const id = rowId(cut(key, fields.id.max_length - suffix.length) + suffix);
      const entry = { group: run.total, row: { id, name, state: 'running' }, key, occurrence };
      addRow(entry);
      run.steps.push(entry);
      if (run.steps.length > MAX_ROWS) run.steps.shift();
    },
    STEP_FINISHED(event) {
      const key = nameOf(event.stepName, MAX_ID);
      const entry = run.steps.findLast((step) => step.key === key && step.row.state === 'running');
      if (entry) entry.row = { ...entry.row, state: 'done' };
    },
    TOOL_CALL_START(event) {
      if (!isId(event.toolCallId) || !hasOptionalId(event, 'parentMessageId') || findTool(event)) return;
      const toolName = nameOf(event.toolCallName, Math.min(MAX_NAME, fields.tool_name.max_length));
      if (!toolName) return;
      const step = run.steps.findLast(({ row }) => row.state === 'running');
      const { toolCallId: callId } = event;
      const id = rowId(step ? `${step.row.id}/${callId}` : callId);
      const row = { id, name: toolName, state: 'running', tool_name: toolName };
      addRow({ group: step?.group ?? run.total, row, callId });
    },
    TOOL_CALL_ARGS(event) {
      if (toolText !== true || typeof event.delta !== 'string' || !event.delta) return;
      const entry = findTool(event);
      if (!entry || entry.row.state !== 'running' || entry.argsCut) return;
      const max = fields.tool_arguments.max_length;
      const joined = (entry.row.tool_arguments ?? '') + cut(event.delta, max + 3);
      const text = cut(joined, max);
      entry.argsCut = text !== joined;
      if (text !== entry.row.tool_arguments) entry.row = { ...entry.row, tool_arguments: text };
    },
    TOOL_CALL_END(event) {
      endTool(event);
    },
    TOOL_CALL_RESULT(event) {
      if (!hasOptionalId(event, 'messageId')) return;
      const result = toolText === true && typeof event.content === 'string'
        ? cut(event.content, fields.tool_result.max_length) : undefined;
      endTool(event, result);
    },
  };

  return {
    push(event) {
      if (!event || typeof event !== 'object' || Array.isArray(event) || typeof event.type !== 'string') return [];
      if (event.type === 'RUN_STARTED') {
        if (!isId(event.runId) || !isId(event.threadId)) return [];
        let messageId = event.runId + MESSAGE_SUFFIX;
        if (!isId(messageId)) {
          const suffix = `${MESSAGE_SUFFIX}#${++longIds}`;
          messageId = cut(event.runId, MAX_ID - suffix.length) + suffix;
        }
        run = { runId: event.runId, threadId: event.threadId, messageId, rows: [], steps: [], total: 0 };
        return [];
      }
      if (!run || !Object.hasOwn(handlers, event.type)) return [];
      const current = run;
      const visible = () => current.rows.slice(-stepsField.max_items).map(({ row }) => row);
      const before = visible();
      const total = current.total;
      handlers[event.type](event);
      const rows = visible();
      if (total === current.total && rows.every((row, index) => row === before[index])) return [];
      const omitted = current.total - rows.length;
      const suffix = omitted ? ` (${omitted} earlier ${omitted === 1 ? 'step' : 'steps'} not shown)` : '';
      const data = { label: cut(title, labelField.max_length - suffix.length) + suffix,
        steps: rows.map((row) => ({ ...row })) };
      return [{ type: 'ACTIVITY_SNAPSHOT', activityType: FILL_ACTIVITY, messageId: current.messageId, replace: true,
        metadata: { mugham: { actor: actorCopy(bound) } },
        content: { format: FILL_FORMAT, fill: { component: contract.name, version: contract.version, data,
          slots: { authorship: actorCopy(bound) } } } }];
    },
  };
}
