// The only way the kit makes DOM nodes. Data goes into text nodes and into a short list of attributes.
// No function here parses a string as markup. tests/test_sinks.py refuses markup sinks in src/.
const ATTRIBUTES = new Set(['id', 'class', 'part', 'role', 'type', 'for', 'name', 'autocomplete', 'tabindex',
  'aria-label', 'aria-describedby', 'aria-invalid', 'aria-live', 'aria-disabled', 'data-state', 'scope', 'aria-sort', 'aria-hidden',
  'aria-labelledby', 'aria-expanded', 'aria-controls', 'aria-readonly']);

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

// The edge and the small shadow of a container that is not a control: a card, a form or a message. They come
// from the border token, so each theme and each contract that declares color.border has them. A control keeps
// the full border color, because its edge needs a contrast of 3:1. Forced colors replaces the two.
export const EDGE = 'color-mix(in srgb, var(--mu-color-border) 45%, transparent)';
export const LIFT = '0 1px 2px color-mix(in srgb, var(--mu-color-border) 25%, transparent)';

// The path of each icon of the kit. An icon is an inline SVG that the kit draws. No data goes into it.
const ICONS = Object.freeze({ chevron: 'M3.5 6 8 10.5 12.5 6' });
const SVG = 'http://www.w3.org/2000/svg';

// An icon is decoration. The control that holds it keeps the accessible name.
export function icon(name) {
  if (!Object.hasOwn(ICONS, name)) throw new Error('icon not known: ' + name);
  const node = document.createElementNS(SVG, 'svg');
  for (const [key, value] of [['class', 'icon'], ['part', 'icon'], ['viewBox', '0 0 16 16'], ['aria-hidden', 'true'], ['focusable', 'false']]) {
    node.setAttribute(key, value);
  }
  const path = document.createElementNS(SVG, 'path');
  path.setAttribute('d', ICONS[name]);
  node.append(path);
  return node;
}

// What a person does after a refusal. Each refused state shows this text after the reason.
export const REFUSED_NEXT = 'Ask for this content again. If the refusal shows again, tell the owner of the application.';

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
