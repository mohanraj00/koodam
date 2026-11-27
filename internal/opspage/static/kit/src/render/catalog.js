// The contracts that the kit knows, in the full form. CI validates each one with tools/catalog.py.
// A contract file has the short form. resolveContract gives its full form (catalog/schema/README.md).
import { resolveContract } from './resolve.js';
import appShell from '../../catalog/app-shell.json' with { type: 'json' };
import breadcrumbs from '../../catalog/breadcrumbs.json' with { type: 'json' };
import pagination from '../../catalog/pagination.json' with { type: 'json' };
import progress from '../../catalog/progress.json' with { type: 'json' };
import steps from '../../catalog/steps.json' with { type: 'json' };
import runSteps from '../../catalog/run-steps.json' with { type: 'json' };
import approvalQueue from '../../catalog/approval-queue.json' with { type: 'json' };
import plan from '../../catalog/plan.json' with { type: 'json' };
import emptyState from '../../catalog/empty-state.json' with { type: 'json' };
import skeleton from '../../catalog/skeleton.json' with { type: 'json' };
import badge from '../../catalog/badge.json' with { type: 'json' };
import avatar from '../../catalog/avatar.json' with { type: 'json' };
import accordion from '../../catalog/accordion.json' with { type: 'json' };
import tabs from '../../catalog/tabs.json' with { type: 'json' };
import actionButton from '../../catalog/action-button.json' with { type: 'json' };
import actionQueue from '../../catalog/action-queue.json' with { type: 'json' };
import button from '../../catalog/button.json' with { type: 'json' };
import card from '../../catalog/card.json' with { type: 'json' };
import cardGrid from '../../catalog/card-grid.json' with { type: 'json' };
import choiceField from '../../catalog/choice-field.json' with { type: 'json' };
import citation from '../../catalog/citation.json' with { type: 'json' };
import dateField from '../../catalog/date-field.json' with { type: 'json' };
import dateRangeField from '../../catalog/date-range-field.json' with { type: 'json' };
import dateTimeField from '../../catalog/date-time-field.json' with { type: 'json' };
import timeField from '../../catalog/time-field.json' with { type: 'json' };
import diff from '../../catalog/diff.json' with { type: 'json' };
import diffChange from '../../catalog/diff-change.json' with { type: 'json' };
import diffPart from '../../catalog/diff-part.json' with { type: 'json' };
import dialog from '../../catalog/dialog.json' with { type: 'json' };
import drawer from '../../catalog/drawer.json' with { type: 'json' };
import editableForm from '../../catalog/editable-form.json' with { type: 'json' };
import excerpt from '../../catalog/excerpt.json' with { type: 'json' };
import fact from '../../catalog/fact.json' with { type: 'json' };
import field from '../../catalog/field.json' with { type: 'json' };
import formCard from '../../catalog/form-card.json' with { type: 'json' };
import formLine from '../../catalog/form-line.json' with { type: 'json' };
import formSection from '../../catalog/form-section.json' with { type: 'json' };
import grid from '../../catalog/grid.json' with { type: 'json' };
import longTextField from '../../catalog/long-text-field.json' with { type: 'json' };
import numberField from '../../catalog/number-field.json' with { type: 'json' };
import image from '../../catalog/image.json' with { type: 'json' };
import menu from '../../catalog/menu.json' with { type: 'json' };
import tooltip from '../../catalog/tooltip.json' with { type: 'json' };
import toast from '../../catalog/toast.json' with { type: 'json' };
import sourceStatus from '../../catalog/source-status.json' with { type: 'json' };
import stack from '../../catalog/stack.json' with { type: 'json' };
import status from '../../catalog/status.json' with { type: 'json' };
import rangeField from '../../catalog/range-field.json' with { type: 'json' };
import multiChoiceField from '../../catalog/multi-choice-field.json' with { type: 'json' };
import selectField from '../../catalog/select-field.json' with { type: 'json' };
import toggleField from '../../catalog/toggle-field.json' with { type: 'json' };
import text from '../../catalog/text.json' with { type: 'json' };
import table from '../../catalog/table.json' with { type: 'json' };
import tableRow from '../../catalog/table-row.json' with { type: 'json' };
import chart from '../../catalog/chart.json' with { type: 'json' };
import chartSeries from '../../catalog/chart-series.json' with { type: 'json' };
import richTextField from '../../catalog/rich-text-field.json' with { type: 'json' };
import richText from '../../catalog/rich-text.json' with { type: 'json' };
import richParagraph from '../../catalog/rich-paragraph.json' with { type: 'json' };
import richHeading from '../../catalog/rich-heading.json' with { type: 'json' };
import richList from '../../catalog/rich-list.json' with { type: 'json' };
import richCode from '../../catalog/rich-code.json' with { type: 'json' };
import richQuote from '../../catalog/rich-quote.json' with { type: 'json' };
import richSpan from '../../catalog/rich-span.json' with { type: 'json' };
import richLink from '../../catalog/rich-link.json' with { type: 'json' };
import dataGrid from '../../catalog/data-grid.json' with { type: 'json' };
import dataGridColumn from '../../catalog/data-grid-column.json' with { type: 'json' };
import dataGridRow from '../../catalog/data-grid-row.json' with { type: 'json' };
import textCell from '../../catalog/text-cell.json' with { type: 'json' };
import numberCell from '../../catalog/number-cell.json' with { type: 'json' };
import dateCell from '../../catalog/date-cell.json' with { type: 'json' };
import choiceCell from '../../catalog/choice-cell.json' with { type: 'json' };
import upload from '../../catalog/upload.json' with { type: 'json' };
import queuedAction from '../../catalog/queued-action.json' with { type: 'json' };

const FILES = {
  'action-button': actionButton, 'action-queue': actionQueue, 'queued-action': queuedAction, button, card, 'card-grid': cardGrid, 'choice-field': choiceField, citation, 'date-field': dateField,
  diff, 'diff-change': diffChange, 'diff-part': diffPart, dialog, drawer, 'editable-form': editableForm, excerpt, fact, field,
  'form-card': formCard, 'form-line': formLine, 'form-section': formSection, grid, image,
  menu, tooltip, toast, tabs, accordion, 'app-shell': appShell, breadcrumbs, pagination, progress, steps,
  'empty-state': emptyState, skeleton, badge, avatar, 'run-steps': runSteps, plan, 'approval-queue': approvalQueue,
  'long-text-field': longTextField, 'number-field': numberField, 'source-status': sourceStatus, stack, status, text, table, 'table-row': tableRow,
  'toggle-field': toggleField,
  'range-field': rangeField,
  'multi-choice-field': multiChoiceField,
  'date-range-field': dateRangeField, 'time-field': timeField, 'date-time-field': dateTimeField,
  'select-field': selectField,
  chart, 'chart-series': chartSeries,
  'rich-text': richText, 'rich-text-field': richTextField, 'rich-paragraph': richParagraph, 'rich-heading': richHeading, 'rich-list': richList,
  'rich-code': richCode, 'rich-quote': richQuote, 'rich-span': richSpan, 'rich-link': richLink,
  'data-grid': dataGrid, 'data-grid-column': dataGridColumn, 'data-grid-row': dataGridRow,
  'text-cell': textCell, 'number-cell': numberCell, 'date-cell': dateCell, 'choice-cell': choiceCell,
  upload,
};

function resolved(name, file) {
  const { contract, errors } = resolveContract(file, { checked: false });
  if (errors.length) throw new Error(`catalog: the contract ${name} is not valid: ${errors.join('; ')}`);
  return contract;
}

export const CATALOG = Object.freeze(Object.assign(Object.create(null),
  Object.fromEntries(Object.entries(FILES).map(([name, file]) => [name, resolved(name, file)]))));
