import { COMPACT_CONTROL_CSS, CONTROL_DISPLAY, FLOATING_TOOLTIP_CSS, compactControls, el, formatIcon, icon,
  sheet, text } from '../render/dom.js';
import { parseLinkURL } from '../render/validate.js';
import { RICH_TEXT_EDIT_LIMIT, richNode, richTextEditProblem, richTextLinkSchemes, richTextValueErrors, sameRichTextValue,
  watchRichTextLinkSchemes } from '../render/rich-text.js';
import { FieldElement } from './fields.js';
import { INLINE_MARKS, readRichTransfer, readRichTree, richSpan, richValue } from './rich-text-field-model.js';
import { applyBlock, applyMark, caretAt, editLines, lineText, pasteLines, replaceRuns, richLines, samePoint,
  selectedLines, selectedRuns, splitLine, valueFromLines } from './rich-text-field-edit.js';

const FORMATS = [
  ['paragraph', 'Paragraph', null, 'P'], ['heading-1', 'Heading 1', null, 'H1'],
  ['heading-2', 'Heading 2', null, 'H2'], ['heading-3', 'Heading 3', null, 'H3'],
  ['bullets', 'Bulleted list'], ['numbers', 'Numbered list'], ['quote', 'Quote'], ['code-block', 'Code block'],
  ['strong', 'Strong', 'B'], ['emphasis', 'Emphasis', 'I'], ['code', 'Inline code', 'E'], ['link', 'Link', 'K'],
];
const SHORTCUTS = new Map(FORMATS.filter(([, , key]) => key).map(([format, , key]) => [key.toLowerCase(), format]));
const NATIVE_FORMAT = new Map([['formatBold', 'strong'], ['formatItalic', 'emphasis'],
  ['insertOrderedList', 'numbers'], ['insertUnorderedList', 'bullets'], ['insertLink', 'link']]);
const ROWS = ':scope > :is(p,h1,h2,h3,pre), :scope > :is(ul,ol) > li, :scope > blockquote > p';

export class MuRichTextField extends FieldElement {
  static component = 'rich-text-field';
  static observedAttributes = [CONTROL_DISPLAY];
  static submitOnEnter = false;
  static styles = [...FieldElement.styles, sheet(`
    :host { min-inline-size: 0; overflow-wrap: anywhere; }
    .toolbar, .link-editor { display: flex; flex-wrap: wrap; }
    .toolbar { gap: var(--mu-space-1); }
    [hidden] { display: none !important; }
    button, .link-editor input, .editor {
      min-block-size: 44px; min-inline-size: 44px; max-inline-size: 100%;
      background: var(--mu-color-surface); color: var(--mu-color-text);
      border: 1px solid var(--mu-color-border); border-radius: var(--mu-radius-1);
      font: var(--mu-type-body); padding: var(--mu-space-1) var(--mu-space-2);
    }
    .toolbar button, .link-editor button { cursor: pointer; }
    .toolbar button {
      display: inline-flex; align-items: center; justify-content: center;
      box-sizing: border-box; inline-size: 44px; block-size: 44px;
      padding: 0; font-weight: var(--mu-type-weight-strong); white-space: nowrap;
    }
    .toolbar .icon { inline-size: 16px; block-size: 16px; stroke-width: 1.5; }
    .toolbar [aria-pressed="true"] {
      color: var(--mu-color-on-accent); background: var(--mu-color-accent); border-color: var(--mu-color-accent);
    }
    .tooltip-space {
      display: flex; align-items: flex-start; flex: 0 0 100%; min-inline-size: 0; font: var(--mu-type-caption);
      min-block-size: calc(1lh + 2 * var(--mu-space-1) + 2px);
    }
    /* The format toolbar tooltip uses this class in its reserved space. The link tooltip floats (FLOATING_TOOLTIP_CSS). */
    .action-tooltip {
      max-inline-size: 100%; overflow-wrap: anywhere; padding: var(--mu-space-1) var(--mu-space-2);
      font: var(--mu-type-caption); color: var(--mu-color-text); background: var(--mu-color-surface);
      border: 1px solid var(--mu-color-border); border-radius: var(--mu-radius-1);
    }
    .link-editor { align-items: end; gap: var(--mu-space-2); }
    .link-label { display: flex; flex: 1 1 12rem; min-inline-size: 0; flex-direction: column; }
    .editor { min-block-size: 8rem; padding: var(--mu-space-3); white-space: pre-wrap; }
    .editor :is(p,h1,h2,h3,ul,ol,pre,blockquote) { margin: 0; }
    .editor :is(h1,h2,h3) { font-weight: var(--mu-type-weight-strong); }
    .editor h1 { font-size: 1.25em; }
    .editor h2 { font-size: 1.125em; }
    .editor h3 { font-size: 1em; }
    .editor :is(ul,ol) { padding-inline-start: var(--mu-space-4); }
    .editor blockquote {
      border-inline-start: 3px solid var(--mu-color-border); padding-inline-start: var(--mu-space-3);
    }
    .editor pre { white-space: inherit; }
    .editor a { color: var(--mu-color-accent); text-decoration: underline; }
    .editor[aria-disabled="true"] { opacity: 0.6; }
    .editor[aria-invalid="true"] { border-color: var(--mu-color-danger); }
    .status { min-block-size: 1lh; font: var(--mu-type-caption); }
    .description { position: absolute; clip-path: inset(50%); width: 1px; height: 1px; overflow: hidden; }
    @media (forced-colors: active) {
      button { color: ButtonText; background: ButtonFace; border-color: ButtonText; }
      .toolbar [aria-pressed="true"] {
        forced-color-adjust: none; color: HighlightText; background: Highlight; border-color: Highlight;
      }
      .action-tooltip { background: Canvas; color: CanvasText; border-color: CanvasText; }
      .editor { background: Canvas; }
      .editor, .editor[aria-invalid="true"], .editor blockquote { color: CanvasText; border-color: CanvasText; }
      .editor[aria-disabled="true"] { color: GrayText; border-color: GrayText; opacity: 1; }
      .editor a { color: LinkText; }
      .status { color: CanvasText; }
    }
  `), COMPACT_CONTROL_CSS, FLOATING_TOOLTIP_CSS];

  #editor;
  #buttons = [];
  #dismissHoverTooltip;
  #linkControls;
  #linkPanel;
  #address;
  #status;
  #model = richValue([]);
  #urls = new WeakMap();
  #selection = caretAt(0, 0);
  #pendingMark;
  #pendingCode = false;
  #policy;
  #unwatch;
  #composition = false;

  static rules(data) {
    return data.value ? richTextValueErrors(data.value).map((error) => 'data.' + error) : [];
  }

  get value() {
    if (!this.fill) return null;
    this.#refreshLinks();
    return structuredClone(this.#model);
  }

  get changed() {
    return Boolean(this.fill) && !sameRichTextValue(this.#model, this.initial);
  }

  get problem() {
    if (this.#editable && this.fill.data.required && !richLines(this.#model).some((row) => lineText(row).trim())) {
      return 'Enter text.';
    }
    return null;
  }

  get #editable() {
    return this.fill && !this.fill.data.disabled && !this.fill.data.read_only;
  }

  focus(options) {
    if (!this.disabled && this.#editor && this.fill) this.#editor.focus(options);
  }

  connectedCallback() {
    this.#linkControls?.render();
    this.ownerDocument.addEventListener('selectionchange', this.#selectionChanged);
    // Escape can close a tooltip when the focus is outside the toolbar.
    this.ownerDocument.addEventListener('keydown', this.#tooltipEscape, true);
    this.#unwatch = watchRichTextLinkSchemes(() => this.#refreshLinks());
    this.#refreshLinks();
  }

  disconnectedCallback() {
    this.#linkControls?.dismiss();
    this.ownerDocument.removeEventListener('selectionchange', this.#selectionChanged);
    this.ownerDocument.removeEventListener('keydown', this.#tooltipEscape, true);
    this.#unwatch?.();
  }

  refuse(errors) {
    this.#linkControls?.dismiss();
    this.#linkControls = null;
    super.refuse(errors);
  }

  attributeChangedCallback() {
    this.#linkControls?.render();
  }

  #tooltipEscape = (event) => {
    if (event.key === 'Escape' && !event.isComposing) this.#dismissHoverTooltip?.(event);
  };

  control(data, id) {
    this.#linkControls?.dismiss();
    this.#linkControls = null;
    this.#model = data.value ? structuredClone(data.value) : richValue([]);
    this.#policy = richTextLinkSchemes();
    this.#selection = caretAt(0, 0);
    this.#pendingMark = undefined;
    this.#pendingCode = false;
    this.#composition = false;
    this.#status = el('span', { part: 'status', class: 'status', role: 'status' });
    const label = el('span', { part: 'label', id: id + '-label' }, data.label);
    this.#editor = el('div', { id, part: 'input', class: 'editor', role: 'textbox', 'aria-multiline': 'true',
      'aria-labelledby': label.id, 'aria-required': data.required ? 'true' : null,
      'aria-readonly': data.read_only ? 'true' : null, 'aria-disabled': data.disabled ? 'true' : null,
      tabindex: data.disabled ? -1 : 0 });
    this.#editor.contentEditable = this.#editable ? 'true' : 'false';
    this.#editor.replaceChildren(...this.#renderValue(this.#model));
    this.#editor.addEventListener('beforeinput', (event) => this.#beforeInput(event));
    this.#editor.addEventListener('input', () => this.#input());
    this.#editor.addEventListener('compositionstart', () => { this.#composition = true; });
    this.#editor.addEventListener('compositionend', () => { this.#composition = false; });
    this.#editor.addEventListener('keydown', (event) => this.#shortcut(event));
    this.#editor.addEventListener('paste', (event) => this.#transfer(event, event.clipboardData));
    this.#editor.addEventListener('dragover', (event) => event.preventDefault());
    this.#editor.addEventListener('drop', (event) => this.#transfer(event, event.dataTransfer));
    this.#editor.addEventListener('click', (event) => {
      if (event.target.closest?.('a')) event.preventDefault();
    });
    this.#editor.addEventListener('focus', (event) => {
      if (event.relatedTarget && this.#buttons.includes(event.relatedTarget)) this.#restoreSelection();
    });

    this.#buttons = [];
    const toolbar = this.#editable ? this.#toolbar(id) : null;
    const panel = this.#editable ? this.#linkEditor(id) : null;
    this.#pressed();
    return { node: el('div', { class: 'box' }, label, toolbar, this.#editor, panel, this.#status),
      targets: [this.#editor], labelled: true };
  }

  #toolbar(id) {
    const help = [];
    const tooltip = el('span', { class: 'action-tooltip', part: 'tooltip', 'aria-hidden': 'true' });
    tooltip.hidden = true;
    let hovered;
    let focused;
    const showTooltip = (label) => {
      tooltip.textContent = label ?? '';
      tooltip.hidden = !label;
    };
    const dismissTooltip = () => {
      if (tooltip.hidden || !tooltip.isConnected) return;
      hovered = undefined;
      focused = undefined;
      showTooltip();
    };
    this.#buttons = FORMATS.map(([format, label, key, glyph], index) => {
      const description = key ? id + '-shortcut-' + format : null;
      const name = glyph && format !== 'paragraph' ? `${glyph} ${label}` : label;
      if (description) help.push(el('span', { id: description, class: 'description' }, 'Control or Command + ' + key));
      const button = el('button', { part: 'format', type: 'button', tabindex: index === 0 ? 0 : -1,
        'aria-label': name, 'aria-pressed': 'false', 'aria-describedby': description }, glyph ?? formatIcon(format));
      if (key) button.setAttribute('aria-keyshortcuts', 'Control+' + key + ' Meta+' + key);
      button.addEventListener('mousedown', (event) => {
        // Keep the text selection when a pointer presses a format.
        if (event.button === 0 && this.shadowRoot.activeElement === this.#editor) {
          this.#rememberSelection();
          event.preventDefault();
        }
      });
      button.addEventListener('pointerenter', () => {
        hovered = label;
        showTooltip(hovered);
      });
      button.addEventListener('focus', () => {
        this.#tabStop(index);
        focused = label;
        showTooltip(focused);
      });
      button.addEventListener('blur', () => {
        focused = undefined;
        showTooltip(hovered);
      });
      button.addEventListener('click', () => {
        this.#tabStop(index);
        this.#format(format);
      });
      return button;
    });
    const toolbar = el('div', { part: 'toolbar', class: 'toolbar', role: 'toolbar', 'aria-label': 'Text formats' },
      ...this.#buttons, el('div', { class: 'tooltip-space' }, tooltip), ...help);
    this.#dismissHoverTooltip = (event) => {
      if (!event.composedPath().includes(toolbar)) dismissTooltip();
    };
    toolbar.addEventListener('pointerleave', () => {
      hovered = undefined;
      showTooltip(focused);
    });
    toolbar.addEventListener('keydown', (event) => {
      if (event.key === 'Escape') {
        if (tooltip.hidden) return;
        dismissTooltip();
        event.preventDefault();
        event.stopPropagation();
        return;
      }
      if (event.altKey || event.ctrlKey || event.metaKey || event.shiftKey) return;
      let index = this.#buttons.indexOf(event.target);
      if (index === -1) return;
      if (event.key === 'Home') index = 0;
      else if (event.key === 'End') index = this.#buttons.length - 1;
      else if (['ArrowRight', 'ArrowDown'].includes(event.key)) index += 1;
      else if (['ArrowLeft', 'ArrowUp'].includes(event.key)) index -= 1;
      else return;
      event.preventDefault();
      this.#buttons[(index + this.#buttons.length) % this.#buttons.length].focus();
    });
    return toolbar;
  }

  #tabStop(current) {
    for (const [index, button] of this.#buttons.entries()) button.tabIndex = index === current ? 0 : -1;
  }

  #linkEditor(id) {
    const label = el('label', { class: 'link-label', for: id + '-address' }, 'Link address');
    this.#address = el('input', { id: id + '-address', type: 'text', autocomplete: 'off', 'aria-describedby': id + '-link-status' });
    label.append(this.#address);
    const apply = el('button', { type: 'button', part: 'link-apply', 'aria-label': 'Apply link' });
    const cancel = el('button', { type: 'button', part: 'link-cancel', 'aria-label': 'Cancel' });
    const feedback = el('span', { id: id + '-link-status', class: 'error', role: 'status' });
    apply.addEventListener('click', () => {
      const address = this.#address.value.trim();
      const url = parseLinkURL(address, richTextLinkSchemes());
      if (!url) {
        feedback.textContent = 'Enter a link address that this page permits.';
        this.#address.setAttribute('aria-invalid', 'true');
        this.#address.focus();
        return;
      }
      const lines = editLines(this.#model);
      const label = selectedRuns(lines, this.#selection).map(({ data }) => data.text).join('');
      const selected = !samePoint(this.#selection.start, this.#selection.end);
      const selection = replaceRuns(lines, this.#selection, [richNode('rich-link', { text: selected ? label : address, url: address })]);
      if (this.#commit(valueFromLines(lines, this.#model), selection)) this.#closeLink();
      else {
        feedback.textContent = RICH_TEXT_EDIT_LIMIT;
        this.#address.setAttribute('aria-invalid', 'true');
        this.#address.focus();
      }
    });
    cancel.addEventListener('click', () => this.#closeLink());
    this.#address.addEventListener('input', () => {
      feedback.textContent = '';
      this.#address.removeAttribute('aria-invalid');
    });
    this.#linkPanel = el('div', { part: 'link-editor', class: 'link-editor', role: 'group', 'aria-label': 'Link address controls' },
      label, apply, cancel, feedback);
    const tooltip = el('span', { part: 'link-tooltip', 'aria-hidden': 'true' });
    this.#linkControls = compactControls(this, this.#linkPanel, tooltip, [
      { button: apply, label: 'Apply link', graphic: icon('check') },
      { button: cancel, label: 'Cancel', graphic: icon('cross') },
    ]);
    this.#linkPanel.hidden = true;
    this.#linkPanel.addEventListener('keydown', (event) => {
      if (event.isComposing) return;
      if (event.key === 'Escape' || (event.key === 'Enter' && event.target === this.#address)) {
        event.preventDefault();
        event.stopPropagation();
        if (event.key === 'Escape') this.#closeLink();
        else apply.click();
      }
    });
    return this.#linkPanel;
  }

  #closeLink() {
    this.#linkControls.dismiss();
    this.#linkPanel.hidden = true;
    this.focus();
    this.#restoreSelection();
  }

  #renderValue(value) {
    const content = (runs) => runs.length ? runs.map(({ component, data }) => {
      if (component === 'rich-link') {
        // An address never becomes an attribute of the editable tree.
        const link = el('a', {}, text(data.text));
        this.#urls.set(link, data.url);
        return link;
      }
      return data.mark ? el(INLINE_MARKS[data.mark], {}, text(data.text)) : text(data.text);
    }) : [el('br')];
    const nodes = value.data.blocks.map(({ component, data }) => {
      if (component === 'rich-heading') return el('h' + data.level, {}, ...content(data.content));
      if (component === 'rich-list') return el(data.ordered ? 'ol' : 'ul', {},
        ...data.items.map(({ data: item }) => el('li', {}, ...content(item.content))));
      if (component === 'rich-quote') return el('blockquote', {},
        ...data.paragraphs.map(({ data: item }) => el('p', {}, ...content(item.content))));
      if (component === 'rich-code') return el('pre', {}, el('code', {}, text(data.text)));
      return el('p', {}, ...content(data.content));
    });
    if (!richLines(value).length) nodes.push(el('p', {}, el('br')));
    return nodes;
  }

  #refreshLinks() {
    const policy = richTextLinkSchemes();
    if (!this.fill || this.#policy === policy) return;
    this.#policy = policy;
    const value = structuredClone(this.#model);
    let changed = false;
    for (const { content } of richLines(value)) {
      for (const [index, node] of content.entries()) {
        if (node.component !== 'rich-link' || parseLinkURL(node.data.url, policy)) continue;
        content[index] = richSpan(node.data.text);
        changed = true;
      }
    }
    if (changed) this.#commit(value, this.#selection, false);
  }

  #domSelection() {
    return this.shadowRoot.getSelection?.() ?? this.ownerDocument.getSelection?.();
  }

  #range() {
    const selection = this.#domSelection();
    if (!selection?.rangeCount) return null;
    const range = selection.getRangeAt(0);
    if (this.#editor.contains(range.startContainer) && this.#editor.contains(range.endContainer)) return range;
    return selection.getComposedRanges?.({ shadowRoots: [this.shadowRoot] }).find((item) =>
      this.#editor.contains(item.startContainer) && this.#editor.contains(item.endContainer)) ?? null;
  }

  #capture(lines, ignored = []) {
    const selected = this.#range();
    if (!selected) return null;
    const rows = [...this.#editor.querySelectorAll(ROWS)];
    const range = this.ownerDocument.createRange();
    const offsetIn = (root, container, offset) => {
      range.setStart(root, 0);
      range.setEnd(container, offset);
      let size = range.toString().length;
      for (const node of ignored) {
        if (node === container) size -= offset;
        else if (root.contains(node) && range.comparePoint(node, node.length) === 0) size -= node.length;
      }
      return size;
    };
    const point = (container, offset) => {
      const index = rows.findIndex((row) => row.contains(container));
      if (index >= 0 && rows.length === lines.length) {
        return { line: index, offset: Math.min(offsetIn(rows[index], container, offset), lineText(lines[index]).length) };
      }
      let position = offsetIn(this.#editor, container, offset);
      for (const [line, row] of lines.entries()) {
        const length = lineText(row).length;
        if (length >= position || line === lines.length - 1) return { line, offset: Math.min(position, length) };
        position -= length;
      }
    };
    return { start: point(selected.startContainer, selected.startOffset), end: point(selected.endContainer, selected.endOffset) };
  }

  #restoreSelection() {
    if (this.shadowRoot.activeElement !== this.#editor) return;
    const selection = this.#domSelection();
    if (!selection) return;
    const rows = this.#editor.querySelectorAll(ROWS);
    const range = this.ownerDocument.createRange();
    const place = ({ line, offset }, set) => {
      const row = rows[line] ?? rows[rows.length - 1];
      if (!row) return range[set](this.#editor, 0);
      const walker = this.ownerDocument.createTreeWalker(row, 4);
      while (walker.nextNode()) {
        const node = walker.currentNode;
        if (offset <= node.length) return range[set](node, offset);
        offset -= node.length;
      }
      range[set](row, row.childNodes.length ? row.childNodes.length - 1 : 0);
    };
    place(this.#selection.start, 'setStart');
    place(this.#selection.end, 'setEnd');
    selection.removeAllRanges();
    selection.addRange(range);
  }

  #rememberSelection() {
    const current = this.#capture(editLines(this.#model));
    if (!current) return;
    if (!samePoint(current.start, this.#selection.start) || !samePoint(current.end, this.#selection.end)) {
      this.#pendingMark = undefined;
      this.#pendingCode = false;
    }
    this.#selection = current;
  }

  #selectionChanged = () => {
    if (this.shadowRoot.activeElement !== this.#editor || !this.#editable || this.#composition) return;
    this.#rememberSelection();
    this.#pressed();
  };

  #pressed() {
    if (!this.#buttons.length) return;
    const lines = editLines(this.#model);
    const rows = selectedLines(lines, this.#selection);
    const runs = selectedRuns(lines, this.#selection);
    const hasMark = (mark) => this.#pendingMark !== undefined ? this.#pendingMark === mark
      : rows.every(({ kind }) => kind !== 'code-block') && runs.length && runs.every(({ data }) => data.mark === mark);
    for (const [index, [format]] of FORMATS.entries()) {
      const active = format === 'link' ? runs.length && runs.every(({ component }) => component === 'rich-link')
        : Object.hasOwn(INLINE_MARKS, format) ? hasMark(format)
          : format === 'code-block' && this.#pendingCode || rows.every(({ kind }) => kind === format);
      this.#buttons[index].setAttribute('aria-pressed', String(Boolean(active)));
    }
  }

  #commit(value, selection, notify = true) {
    const problem = richTextEditProblem(value);
    if (problem) {
      this.#status.textContent = problem;
      return false;
    }
    const changed = !sameRichTextValue(this.#model, value);
    this.#model = value;
    this.#selection = selection;
    this.#editor.replaceChildren(...this.#renderValue(value));
    this.#restoreSelection();
    this.#pressed();
    this.#status.textContent = '';
    if (notify && changed) this.changedValue();
    return true;
  }

  #format(format) {
    if (!this.#editable) return;
    if (this.shadowRoot.activeElement === this.#editor) {
      this.#rememberSelection();
      this.#pressed();
    }
    const lines = editLines(this.#model);
    const selection = this.#selection;
    const { start, end } = selection;
    const collapsed = samePoint(start, end);
    const pressed = this.#buttons[FORMATS.findIndex(([name]) => name === format)].getAttribute('aria-pressed') === 'true';
    if (format === 'link') {
      if (start.line !== end.line || lines[start.line].kind === 'code-block') {
        this.#status.textContent = 'Select text in one paragraph, heading or list item to add a link.';
      } else if (pressed) {
        let range = selection;
        if (collapsed) {
          let offset = 0;
          for (const node of lines[start.line].content) {
            const next = offset + node.data.text.length;
            if (start.offset <= next) {
              range = { start: { line: start.line, offset }, end: { line: start.line, offset: next } };
              break;
            }
            offset = next;
          }
        }
        const content = selectedRuns(lines, range).map(({ data }) => richSpan(data.text));
        replaceRuns(lines, range, content);
        this.#commit(valueFromLines(lines, this.#model), selection);
      } else {
        this.#address.value = '';
        this.#address.dispatchEvent(new Event('input'));
        this.#linkPanel.hidden = false;
        this.#address.focus();
      }
    } else if (Object.hasOwn(INLINE_MARKS, format)) {
      if (selectedLines(lines, selection).every(({ kind }) => kind === 'code-block')) return;
      const mark = pressed ? null : format;
      if (collapsed) {
        this.#pendingMark = mark;
        this.#pressed();
      } else {
        applyMark(lines, selection, mark);
        this.#commit(valueFromLines(lines, this.#model), selection);
      }
    } else if (format === 'code-block' && collapsed && !lines[start.line].content.length) {
      this.#pendingCode = !this.#pendingCode;
      this.#pressed();
    } else {
      this.#pendingMark = undefined;
      this.#pendingCode = false;
      applyBlock(lines, selection, pressed ? 'paragraph' : format);
      this.#commit(valueFromLines(lines, this.#model), selection);
    }
  }

  #shortcut(event) {
    if (event.isComposing || event.altKey || event.shiftKey || (!event.ctrlKey && !event.metaKey)) return;
    const format = SHORTCUTS.get(event.key.toLowerCase());
    if (!format) return;
    event.preventDefault();
    this.#format(format);
  }

  #beforeInput(event) {
    if (!this.#editable) {
      event.preventDefault();
      return;
    }
    const kind = event.inputType ?? '';
    const format = NATIVE_FORMAT.get(kind);
    if (format) {
      event.preventDefault();
      this.#format(format);
      return;
    }
    if (kind === 'insertCompositionText' || kind === 'deleteCompositionText') return;
    if (kind.startsWith('delete') || kind === 'historyUndo' || kind === 'historyRedo') return;
    this.#rememberSelection();
    const ordinary = kind === 'insertText' && this.#pendingMark === undefined && !this.#pendingCode;
    if (ordinary) return;
    event.preventDefault();
    if (!event.defaultPrevented) return;
    const lines = editLines(this.#model);
    const row = lines[this.#selection.start.line];
    let selection;
    if (kind === 'insertParagraph' && row.kind !== 'code-block' && !this.#pendingCode) {
      selection = splitLine(lines, this.#selection);
    } else {
      const words = ['insertParagraph', 'insertLineBreak'].includes(kind) ? '\n'
        : ['insertText', 'insertReplacementText'].includes(kind) ? event.data : '';
      if (!words) return;
      if (this.#pendingCode) row.kind = 'code-block';
      selection = replaceRuns(lines, this.#selection, [richSpan(words, this.#pendingMark)]);
    }
    if (this.#commit(valueFromLines(lines, this.#model), selection)) this.#pendingCode = false;
  }

  #input() {
    if (!this.fill) return;
    const ignored = [];
    const result = this.#editable
      ? readRichTree(this.#editor, { editorLink: (node) => this.#urls.get(node), ignoreText: (node) => ignored.push(node) })
      : {};
    if (!result.value) {
      this.#editor.replaceChildren(...this.#renderValue(this.#model));
      this.#restoreSelection();
      this.#status.textContent = result.problem ?? '';
      return;
    }
    const value = result.value;
    if (this.#model.data.clamp !== undefined) value.data.clamp = this.#model.data.clamp;
    const selection = this.#capture(editLines(value), ignored) ?? caretAt(0, 0);
    const current = value.data.blocks.length === 1 && !this.#model.data.blocks.length
      && value.data.blocks[0].component === 'rich-paragraph' && !value.data.blocks[0].data.content.length ? this.#model : value;
    const nodes = this.#renderValue(current);
    const live = this.#editor.childNodes;
    if (nodes.length !== live.length || nodes.some((node, index) => !node.isEqualNode(live[index]))) {
      this.#commit(current, selection);
    } else {
      const changed = !sameRichTextValue(this.#model, current);
      this.#model = current;
      this.#selection = selection;
      this.#status.textContent = '';
      this.#pressed();
      if (changed) this.changedValue();
    }
  }

  #transfer(event, transfer) {
    event.preventDefault();
    if (!this.#editable) return;
    const result = readRichTransfer(transfer);
    if (result.problem) {
      this.#status.textContent = result.problem;
      return;
    }
    const incoming = richLines(result.value);
    if (!incoming.length) return;
    this.#rememberSelection();
    if (event.type === 'drop') {
      const document = this.ownerDocument;
      const position = document.caretPositionFromPoint?.(event.clientX, event.clientY, { shadowRoots: [this.shadowRoot] });
      const range = position ? null : document.caretRangeFromPoint?.(event.clientX, event.clientY);
      const node = position?.offsetNode ?? range?.startContainer;
      if (node && this.#editor.contains(node)) {
        const at = document.createRange();
        at.setStart(node, position?.offset ?? range.startOffset);
        at.collapse(true);
        this.focus();
        const selection = this.#domSelection();
        selection?.removeAllRanges();
        selection?.addRange(at);
        this.#rememberSelection();
      }
    }
    const lines = editLines(this.#model);
    const selection = pasteLines(lines, this.#selection, incoming);
    this.#commit(valueFromLines(lines, this.#model), selection);
  }
}
