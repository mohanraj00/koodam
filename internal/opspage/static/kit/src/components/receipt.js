import { copyControl, el, sheet } from '../render/dom.js';

export const REFERENCE_CSS = sheet(`
  .reference-row {
    display: flex; flex-wrap: wrap; align-items: center; gap: var(--mu-space-1) var(--mu-space-2);
    font: var(--mu-type-caption); color: var(--mu-color-text-muted);
  }
  .reference-row:empty { display: none; }
  .reference-row [part="reference"] { min-inline-size: 0; overflow-wrap: anywhere; }
  .reference-row .copy {
    min-inline-size: 44px; min-block-size: 44px;
    border-color: var(--mu-color-border); background: transparent; color: var(--mu-color-text);
  }
  @media (forced-colors: active) {
    .reference-row { color: CanvasText; }
    .reference-row .copy { border-color: ButtonText; background: ButtonFace; color: ButtonText; }
  }
`);

export const ACTION_RESULT_CSS = sheet(`
  button[data-state="done"], button[data-state="disabled"] {
    opacity: 1; background: var(--mu-color-surface); transition-duration: 0ms;
  }
  button[data-state="done"] { color: var(--mu-color-success); border-color: var(--mu-color-success); }
  button[data-state="disabled"] { color: var(--mu-color-text-muted); border-color: var(--mu-color-border); }
  @media (forced-colors: active) {
    button[data-state="done"] { background: ButtonFace; color: ButtonText; border-color: ButtonText; }
    button[data-state="disabled"] { background: ButtonFace; color: GrayText; border-color: GrayText; }
  }
`);

// Keep the copy control while the reference stays the same.
export function showReference(row, reference, nameControl) {
  const words = reference ? 'Reference: ' + reference : '';
  if ((row.firstElementChild?.textContent ?? '') === words) return;
  if (!words) {
    row.replaceChildren();
    return;
  }
  const copy = copyControl(reference, 'reference');
  nameControl?.(copy.firstElementChild, 'Copy reference');
  row.replaceChildren(el('span', { part: 'reference' }, words), copy);
}
