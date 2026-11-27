// The only way the kit makes DOM nodes. Data goes into text nodes and into a short list of attributes.
// No function here parses a string as markup. tests/test_sinks.py refuses markup sinks in src/.
const ATTRIBUTES = new Set(['id', 'class', 'part', 'role', 'type', 'for', 'name', 'autocomplete', 'tabindex',
  'aria-label', 'aria-describedby', 'aria-invalid', 'aria-live', 'aria-disabled', 'data-state', 'scope', 'aria-sort', 'aria-hidden',
  'aria-labelledby', 'aria-modal', 'aria-expanded', 'aria-haspopup', 'aria-controls', 'aria-readonly', 'aria-autocomplete',
  'aria-selected', 'aria-pressed', 'aria-multiline', 'aria-required', 'aria-multiselectable', 'aria-current', 'aria-valuemin', 'aria-valuemax', 'aria-valuenow']);

export function text(value) {
  return document.createTextNode(String(value));
}

export function el(tag, attributes = {}, ...children) {
  const node = document.createElement(tag);
  for (const [name, value] of Object.entries(attributes)) {
    if (!ATTRIBUTES.has(name)) throw new Error('attribute not allowed: ' + name);
    if (value === false || value === null || value === undefined) continue;
    node.setAttribute(name, value === true ? '' : String(value));
  }
  for (const child of children) {
    if (child === null || child === undefined || child === false) continue;
    node.append(child instanceof Node ? child : text(child));
  }
  return node;
}

export function sheet(css) {
  const result = new CSSStyleSheet();
  result.replaceSync(css);
  return result;
}

export function setDisclosure(button, panel, expanded) {
  button.setAttribute('aria-expanded', String(expanded));
  panel.hidden = !expanded;
}

export function positionPopup(panel, anchor) {
  const owner = anchor.ownerDocument;
  const view = owner.defaultView;
  const viewport = owner.documentElement;
  const events = new AbortController();
  // Use the space below the control when the panel fits. Otherwise use the side with more space.
  const update = () => {
    const target = anchor.getBoundingClientRect();
    const box = panel.getBoundingClientRect();
    const inset = 8;
    const height = Math.max(box.height, panel.scrollHeight + panel.offsetHeight - panel.clientHeight);
    const belowStart = Math.max(inset, target.bottom);
    const aboveEnd = Math.min(target.top, viewport.clientHeight - inset);
    const below = Math.max(0, viewport.clientHeight - belowStart - inset);
    const above = Math.max(0, aboveEnd - inset);
    const useBelow = height <= below || below >= above;
    const available = useBelow ? below : above;
    const start = view.getComputedStyle(anchor).direction === 'rtl' ? target.right - box.width : target.left;
    const left = Math.max(inset, Math.min(start, viewport.clientWidth - box.width - inset));
    const top = useBelow ? belowStart : aboveEnd - Math.min(height, available);
    panel.style.setProperty('--popup-left', left + 'px');
    panel.style.setProperty('--popup-top', top + 'px');
    panel.style.setProperty('--popup-height', available + 'px');
  };
  update();
  const observer = new ResizeObserver(update);
  observer.observe(anchor);
  observer.observe(panel);
  // A scroll event in a shadow root does not reach its host.
  const scroll = { capture: true, signal: events.signal };
  for (let node = anchor; node; node = node.assignedSlot ?? node.parentNode ?? node.host) {
    if (node.host) node.addEventListener('scroll', update, scroll);
  }
  owner.addEventListener('scroll', update, scroll);
  view.addEventListener('resize', update, { signal: events.signal });
  return () => { events.abort(); observer.disconnect(); };
}

export const FLOATING_TOOLTIP_CSS = sheet(`
  .floating-tooltip {
    position: fixed; inset: auto; left: var(--popup-left, 8px); top: var(--popup-top, 8px); margin: 0;
    inline-size: max-content; max-inline-size: calc(100% - 16px);
    max-block-size: var(--popup-height, calc(100% - 16px)); overflow: auto; overflow-wrap: anywhere;
    padding: var(--mu-space-1) var(--mu-space-2);
    font: var(--mu-type-caption); color: var(--mu-color-text); background: var(--mu-color-surface);
    border: 1px solid var(--mu-color-border); border-radius: var(--mu-radius-2);
  }
  .floating-tooltip[hidden] { display: none !important; }
  .tooltip-fallback { position: fixed; inset: 0; pointer-events: none; z-index: 1; }
  .tooltip-fallback > .floating-tooltip { position: absolute; pointer-events: auto; }
  @media (forced-colors: active) {
    .floating-tooltip { background: Canvas; color: CanvasText; border-color: CanvasText; }
  }
`);

export function floatingTooltip(area, tooltip) {
  const native = typeof tooltip.showPopover === 'function';
  tooltip.setAttribute('class', 'floating-tooltip');
  if (native) tooltip.popover = 'manual';
  // The fixed wrapper keeps the absolute tooltip outside the normal flow and scroll clip.
  const surface = native ? tooltip : el('span', { class: 'tooltip-fallback' }, tooltip);
  tooltip.hidden = surface.hidden = true;
  area.append(surface);
  let stop;
  let target;
  const hide = () => {
    stop?.();
    stop = null;
    target = null;
    if (native && tooltip.matches(':popover-open')) tooltip.hidePopover();
    tooltip.hidden = surface.hidden = true;
  };
  const show = (anchor) => {
    if (anchor === target || !tooltip.isConnected) return;
    stop?.();
    target = anchor;
    tooltip.hidden = surface.hidden = false;
    if (native && !tooltip.matches(':popover-open')) tooltip.showPopover({ source: anchor });
    stop = positionPopup(tooltip, anchor);
  };
  return { show, hide };
}

// The edge and the small shadow of a container that is not a control: a card, a form or a message. They come
// from the border token, so each theme and each contract that declares color.border has them. A control keeps
// the full border color, because its edge needs a contrast of 3:1. Forced colors replaces the two.
export const EDGE = 'color-mix(in srgb, var(--mu-color-border) 45%, transparent)';
export const LIFT = '0 1px 2px color-mix(in srgb, var(--mu-color-border) 25%, transparent)';

// The kit draws each icon as an inline SVG. Each path is a constant of the kit.
// A fill can only select an icon name. The contract enum and the binding check limit the name.
// The icon function refuses a name that is not a key of ICONS.
const ICONS = Object.freeze({
  chevron: 'M3.5 6 8 10.5 12.5 6',
  check: 'M2.5 8.5 6.5 12 13.5 4',
  cross: 'M4 4 12 12 M12 4 4 12',
  edit: 'M3 10 10.5 2.5 13.5 5.5 6 13 2.5 13.5Z M9 4 12 7',
  delete: 'M2.5 4.5h11 M6 4.5V2.5h4v2 M4 4.5l.75 9h6.5l.75-9 M8 7v4',
  send: 'M2 6 14 2 10 14 7 9 2 6Z M7 9 14 2',
  retry: 'M2.5 3v4h4 M2.8 7a5.5 5.5 0 1 1 .6 4',
});
// Only the format toolbar uses these paths.
const FORMAT_ICONS = Object.freeze({
  bullets: 'M6 4h8 M6 8h8 M6 12h8 M2.5 4h.1 M2.5 8h.1 M2.5 12h.1',
  numbers: 'M2 3l1-1v5 M2 7h2 M2 10c0-1.4 2.5-1.4 2.5 0 0 1-1.5 2-2.5 3h2.5 M7 4h7 M7 11h7',
  quote: 'M2.5 8H6v4H2.5V8c0-2 1-3.5 3-4 M9.5 8H13v4H9.5V8c0-2 1-3.5 3-4',
  'code-block': 'M2 3h12v10H2z M5 6l2 2-2 2 M9 10h2',
  strong: 'M4 13.5v-11h4.5a2.75 2.75 0 0 1 0 5.5H4 M8.5 8h1a2.75 2.75 0 0 1 0 5.5H4',
  emphasis: 'M7 2.5h6 M3 13.5h6 M10 2.5 6 13.5',
  code: 'M5 5 2 8l3 3 M11 5l3 3-3 3 M9.5 3l-3 10',
  link: 'M6 9.5 10 5.5 M5.5 6.5l-2 2a2.8 2.8 0 0 0 4 4l2-2 M6.5 5.5l2-2a2.8 2.8 0 0 1 4 4l-2 2',
});
// Only the grid controls use these paths.
const CONTROL_ICONS = Object.freeze({
  'expand-all': 'M3.5 3.5 8 7 12.5 3.5 M3.5 8.5 8 12 12.5 8.5',
  'collapse-all': 'M3.5 7.5 8 4 12.5 7.5 M3.5 12.5 8 9 12.5 12.5',
});
const SVG = 'http://www.w3.org/2000/svg';

// An icon is decoration. The control that holds it keeps the accessible name.
export function icon(name) {
  if (!Object.hasOwn(ICONS, name)) throw new Error('icon not known: ' + name);
  const { node, path } = svgIcon();
  node.setAttribute('part', 'icon');
  path.setAttribute('d', ICONS[name]);
  return node;
}

export function formatIcon(name) {
  if (!Object.hasOwn(FORMAT_ICONS, name)) throw new Error('format icon not known: ' + name);
  const { node, path } = svgIcon();
  node.setAttribute('part', 'icon');
  path.setAttribute('d', FORMAT_ICONS[name]);
  return node;
}

export function controlIcon(name) {
  if (!Object.hasOwn(CONTROL_ICONS, name)) throw new Error('control icon not known: ' + name);
  const { node, path } = svgIcon();
  node.setAttribute('part', 'icon');
  path.setAttribute('d', CONTROL_ICONS[name]);
  return node;
}

function svgIcon() {
  const node = document.createElementNS(SVG, 'svg');
  for (const [key, value] of [['class', 'icon'], ['viewBox', '0 0 16 16'], ['aria-hidden', 'true'], ['focusable', 'false']]) {
    node.setAttribute(key, value);
  }
  const path = document.createElementNS(SVG, 'path');
  node.append(path);
  return { node, path };
}

export const CONTROL_DISPLAY = 'control-display';
export const COMPACT_CONTROL_CSS = sheet(`
  button[data-control-display="icon"] {
    display: inline-flex; align-items: center; justify-content: center; flex: none;
    inline-size: 44px; block-size: 44px; min-inline-size: 44px; min-block-size: 44px; padding: 0;
  }
  [data-control-display="icon"] .icon { inline-size: 16px; block-size: 16px; }
`);

const openControlTooltip = new WeakMap();

export function compactControls(host, area, tooltip, controls) {
  const layer = floatingTooltip(area, tooltip);
  let textDisplay;
  let hovered;
  let focused;
  let shown;
  let activeDocument;
  const hide = () => {
    if (!activeDocument) return;
    if (openControlTooltip.get(activeDocument) === dismiss) openControlTooltip.delete(activeDocument);
    activeDocument.removeEventListener('keydown', escape, true);
    activeDocument = null;
    shown = null;
    layer.hide();
    tooltip.textContent = '';
  };
  const dismiss = () => {
    hovered = focused = null;
    hide();
  };
  const escape = (event) => {
    if (event.key !== 'Escape' || event.isComposing) return;
    const visible = tooltip.isConnected && tooltip.getClientRects().length > 0;
    dismiss();
    // Stop Escape only when it comes from the controls. Escape from other content still reaches its own handler.
    if (visible && event.composedPath().includes(area)) {
      event.preventDefault();
      event.stopPropagation();
    }
  };
  const refresh = () => {
    if (shown) tooltip.textContent = shown.getAttribute('aria-label');
  };
  const show = (button) => {
    if (!button || textDisplay || !tooltip.isConnected) {
      hide();
      return;
    }
    if (!activeDocument) {
      activeDocument = tooltip.ownerDocument;
      openControlTooltip.get(activeDocument)?.();
      openControlTooltip.set(activeDocument, dismiss);
      activeDocument.addEventListener('keydown', escape, true);
    }
    shown = button;
    refresh();
    layer.show(button);
  };
  // Keep the tooltip open while the pointer moves from the button to its text.
  area.addEventListener('pointerleave', () => {
    hovered = null;
    show(focused);
  });
  for (const { button } of controls) {
    button.addEventListener('pointerenter', (event) => {
      if (event.pointerType === 'touch') return;
      hovered = button;
      show(button);
    });
    button.addEventListener('focus', () => {
      if (!button.matches(':focus-visible')) return;
      focused = button;
      show(button);
    });
    button.addEventListener('blur', () => {
      focused = null;
      show(hovered);
    });
  }
  const render = () => {
    let display = null;
    for (let node = host; node && display === null; node = node.assignedSlot ?? node.parentNode ?? node.host) {
      display = node.getAttribute?.(CONTROL_DISPLAY) ?? null;
    }
    const next = display === 'text';
    if (next === textDisplay) return;
    textDisplay = next;
    for (const { button, label, graphic, keepIcon } of controls) {
      button.setAttribute('data-control-display', textDisplay ? 'text' : 'icon');
      const content = textDisplay ? (keepIcon ? [label, graphic] : [label]) : [graphic];
      button.replaceChildren(...content);
    }
    show(focused ?? hovered);
  };
  render();
  return { render, dismiss, refresh };
}

export const REFUSED_REASON = 'This content did not show because it did not match the rules of the application.';
export const REFUSED_NEXT = 'Ask for this content again. If the refusal shows again, tell the owner of the application.';

export function refusalDetails(component, errors) {
  const body = el('div');
  body.textContent = (component ? component + ': ' : '') + errors.join('\n');
  return el('details', { part: 'details' }, el('summary', {}, 'Technical details'), body,
    copyControl(body.textContent, 'technical details'));
}

// The time in milliseconds that a copy control shows Copied.
export const COPIED_TIME = 2000;

// A control that copies a text to the clipboard after a person presses it. The text is data: it goes only to
// the clipboard. The control shows Copied only after the browser accepts the text, and a status gives the
// result to a screen reader. name completes the accessible name, for example Copy reference.
export function copyControl(value, name) {
  const button = el('button', { type: 'button', class: 'copy', part: 'copy', 'aria-label': 'Copy ' + name }, 'Copy');
  const status = el('span', { class: 'copy-status', role: 'status', 'aria-live': 'polite', part: 'copy-status' });
  let timer = null;
  const show = (label, words) => {
    clearTimeout(timer);
    button.textContent = label;
    status.textContent = words;
    timer = setTimeout(() => {
      button.textContent = 'Copy';
      status.textContent = '';
    }, COPIED_TIME);
  };
  button.addEventListener('click', async () => {
    try {
      await navigator.clipboard.writeText(String(value));
      show('Copied', 'Copied');
    } catch {
      show('Not copied', 'Not copied. Select the text and copy it.');
    }
  });
  return el('span', { class: 'copy-box' }, button, status);
}

// Shared rules for each primitive: the focus ring, the states of each control, forced colors and reduced motion.
// A control shows hover and pressed with a layer of its own text color, so each theme gives the two states.
export const BASE = sheet(`
  :host { box-sizing: border-box; }
  :host([hidden]) { display: none; }
  *, *::before, *::after { box-sizing: inherit; }
  :focus-visible { outline: 2px solid var(--mu-color-focus); outline-offset: 2px; }
  button, summary, input, textarea, select {
    transition: background-color var(--mu-motion-fast), border-color var(--mu-motion-fast),
      box-shadow var(--mu-motion-fast), color var(--mu-motion-fast);
  }
  button:hover:not(:disabled, [aria-disabled="true"]), summary:hover {
    box-shadow: inset 0 0 0 100vmax color-mix(in srgb, currentColor 8%, transparent);
  }
  button:active:not(:disabled, [aria-disabled="true"]), summary:active {
    box-shadow: inset 0 0 0 100vmax color-mix(in srgb, currentColor 14%, transparent);
  }
  button:disabled, button[aria-disabled="true"] { cursor: not-allowed; opacity: 0.6; }
  button[aria-busy="true"] { cursor: progress; }
  :is(input, textarea, select):hover:not(:disabled, [aria-invalid="true"]) { border-color: currentColor; }
  :is(input, textarea, select):disabled { cursor: not-allowed; opacity: 0.6; }
  .icon { inline-size: 1em; block-size: 1em; flex: none; fill: none; stroke: currentColor; stroke-width: 2;
    stroke-linecap: round; stroke-linejoin: round; transition: transform var(--mu-motion-fast); }
  .copy-box { display: inline-flex; align-items: center; }
  .copy {
    font: var(--mu-type-caption); min-height: 2.75rem; min-width: 2.75rem; padding: 0 var(--mu-space-2); cursor: pointer;
    border: 1px solid currentColor; background: transparent; color: inherit;
  }
  .copy-status { position: absolute; width: 1px; height: 1px; overflow: hidden; clip-path: inset(50%); white-space: nowrap; }
  .refused { font: var(--mu-type-caption); color: var(--mu-color-danger); }
  .refused [part="next"] { display: block; }
  details[part="details"] > summary { min-height: 2.75rem; min-width: 2.75rem; padding-block: 0.625rem; cursor: pointer; }
  details[part="details"]:not([open]) > :not(summary) { display: none; }
  details[part="details"] > div { white-space: pre-wrap; overflow-wrap: anywhere; }
  @media (prefers-reduced-motion: reduce) {
    *, *::before, *::after { transition: none !important; animation: none !important; }
  }
  @media (forced-colors: active) {
    :focus-visible { outline-color: Highlight; }
    button:hover:not(:disabled, [aria-disabled="true"]) { border-color: Highlight; }
    /* A summary has no border, so its hover shows as an outline. The focus ring has priority. */
    summary:hover:not(:focus-visible) { outline: 1px solid Highlight; }
    button:active:not(:disabled, [aria-disabled="true"]), summary:active {
      background: Highlight; color: HighlightText; border-color: Highlight; forced-color-adjust: none;
    }
    button:disabled, button[aria-disabled="true"], :is(input, textarea, select):disabled { color: GrayText; border-color: GrayText; opacity: 1; }
    :is(input, textarea, select):hover:not(:disabled, [aria-invalid="true"]) { border-color: Highlight; }
    .icon { stroke: currentColor; }
    .copy { color: ButtonText; border-color: ButtonText; background: ButtonFace; }
    .refused { color: CanvasText; }
  }
`);
