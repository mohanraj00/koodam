// An edit uses positions in the text of a block, list item or quote paragraph.
import { CATALOG } from '../render/catalog.js';
import { richNode } from '../render/rich-text.js';
import { richParagraph, richSpan, richValue } from './rich-text-field-model.js';

const SPAN_SIZE = CATALOG['rich-span'].data.text.max_length;
const line = (kind, content, group = null) => ({ kind, content, group });

export function richLines(value) {
  return value.data.blocks.flatMap((block) => {
    const { component, data } = block;
    if (component === 'rich-heading') return line('heading-' + data.level, data.content);
    if (component === 'rich-list') return data.items.map(({ data: item }) => line(data.ordered ? 'numbers' : 'bullets', item.content, block));
    if (component === 'rich-quote') return data.paragraphs.map(({ data: item }) => line('quote', item.content, block));
    if (component === 'rich-code') return line('code-block', [richSpan(data.text)]);
    return line('paragraph', data.content);
  });
}

export const lineText = ({ content }) => content.map(({ data }) => data.text).join('');
export const samePoint = (a, b) => a.line === b.line && a.offset === b.offset;
export const caretAt = (line, offset) => ({ start: { line, offset }, end: { line, offset } });

export function editLines(value) {
  const lines = richLines(value);
  return lines.length ? lines : [line('paragraph', [])];
}

export function valueFromLines(lines, old) {
  const blocks = [];
  let lastGroup;
  for (const { kind, content, group } of lines) {
    if (['bullets', 'numbers', 'quote'].includes(kind)) {
      const key = kind === 'quote' ? 'paragraphs' : 'items';
      if (lastGroup !== group || !blocks.at(-1)?.data[key]) {
        blocks.push(kind === 'quote' ? richNode('rich-quote', { paragraphs: [] })
          : richNode('rich-list', kind === 'numbers' ? { ordered: true, items: [] } : { items: [] }));
      }
      blocks.at(-1).data[key].push(richParagraph(content));
    } else if (kind.startsWith('heading-')) {
      blocks.push(richNode('rich-heading', { level: Number(kind.at(-1)), content }));
    } else if (kind === 'code-block' && content.length) {
      blocks.push(richNode('rich-code', { text: lineText({ content }) }));
    } else {
      blocks.push(richParagraph(content));
    }
    lastGroup = group;
  }
  const value = richValue(blocks);
  if (old.data.clamp !== undefined) value.data.clamp = old.data.clamp;
  return value;
}

function slice(content, start, end = Infinity) {
  const result = [];
  let offset = 0;
  for (const node of content) {
    const next = offset + node.data.text.length;
    if (start < next && end > offset) {
      const text = node.data.text.slice(Math.max(0, start - offset), end - offset);
      if (text) result.push(text === node.data.text ? node : richNode(node.component, { ...node.data, text }));
    }
    offset = next;
    if (offset >= end) break;
  }
  return result;
}

function join(...runs) {
  const result = [];
  for (const node of runs.flat()) {
    const last = result.at(-1);
    if (last?.component === 'rich-span' && node.component === 'rich-span' && last.data.mark === node.data.mark) {
      const text = last.data.text + node.data.text;
      if (text.length <= SPAN_SIZE || (text.length <= SPAN_SIZE * 2 && [...text].length <= SPAN_SIZE)) {
        result[result.length - 1] = richSpan(text, node.data.mark);
        continue;
      }
    }
    result.push(node);
  }
  return result;
}

export function selectedLines(lines, { start, end }) {
  const last = end.line > start.line && end.offset === 0 ? end.line - 1 : end.line;
  return lines.slice(start.line, last + 1);
}

export function selectedRuns(lines, { start, end }) {
  if (samePoint(start, end)) {
    const { content } = lines[start.line];
    let offset = 0;
    for (const node of content) {
      offset += node.data.text.length;
      if (start.offset <= offset) return [node];
    }
    return [];
  }
  return lines.slice(start.line, end.line + 1).flatMap(({ content }, index) => {
    const row = start.line + index;
    return slice(content, row === start.line ? start.offset : 0, row === end.line ? end.offset : Infinity);
  });
}

export function replaceRuns(lines, { start, end }, content) {
  const first = lines[start.line];
  const last = lines[end.line];
  const before = slice(first.content, 0, start.offset);
  const after = slice(last.content, end.offset);
  lines.splice(start.line, end.line - start.line + 1, { ...first, content: join(before, content, after) });
  return caretAt(start.line, start.offset + lineText({ content }).length);
}

export function applyMark(lines, { start, end }, mark) {
  for (let index = start.line; index <= end.line; index += 1) {
    const row = lines[index];
    if (row.kind === 'code-block') continue;
    const low = index === start.line ? start.offset : 0;
    const high = index === end.line ? end.offset : Infinity;
    const format = slice(row.content, low, high).map((node) => node.component === 'rich-span' ? richSpan(node.data.text, mark) : node);
    lines[index] = { ...row, content: join(slice(row.content, 0, low), format, slice(row.content, high)) };
  }
}

export function applyBlock(lines, selection, kind) {
  const selected = selectedLines(lines, selection);
  const group = {};
  const converted = selected.map((row) => {
    const content = kind === 'code-block' && row.content.length ? [richSpan(lineText(row))] : row.content;
    return line(kind, content, group);
  });
  lines.splice(selection.start.line, selected.length, ...converted);
}

export function splitLine(lines, selection) {
  const { start } = replaceRuns(lines, selection, []);
  const row = lines[start.line];
  const before = slice(row.content, 0, start.offset);
  const after = slice(row.content, start.offset);
  const grouped = ['bullets', 'numbers', 'quote'].includes(row.kind);
  if (grouped && !row.content.length) {
    lines[start.line] = line('paragraph', []);
    return caretAt(start.line, 0);
  }
  const next = grouped ? { ...row, content: after } : line('paragraph', after);
  lines.splice(start.line, 1, { ...row, content: before }, next);
  return caretAt(start.line + 1, 0);
}

export function pasteLines(lines, { start, end }, incoming) {
  const first = lines[start.line];
  const last = lines[end.line];
  if (first.kind === 'code-block') {
    const text = incoming.map(lineText).join('\n');
    return replaceRuns(lines, { start, end }, text ? [richSpan(text)] : []);
  }
  if (incoming.length === 1 && incoming[0].kind === 'paragraph') {
    return replaceRuns(lines, { start, end }, incoming[0].content);
  }

  const before = slice(first.content, 0, start.offset);
  const after = slice(last.content, end.offset);
  if (incoming[0].kind === 'paragraph') incoming[0] = { ...first, content: join(before, incoming[0].content) };
  else if (before.length) incoming.unshift({ ...first, content: before });
  const line = incoming.length - 1;
  const offset = lineText(incoming[line]).length;
  if (after.length) {
    if (incoming[line].kind === 'paragraph') incoming[line].content = join(incoming[line].content, after);
    else incoming.push({ ...last, content: after });
  }
  lines.splice(start.line, end.line - start.line + 1, ...incoming);
  return caretAt(start.line + line, offset);
}
