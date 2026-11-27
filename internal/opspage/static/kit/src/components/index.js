// The catalog components: card, action button, editable form, form card, card grid, citation and excerpt.
import { MuElement } from '../primitives/base.js';
import { APPROVAL_MARK_CSS, approvalMark, badgeLabel } from '../primitives/badge.js';
import { ReadMoreElement, READ_MORE_CSS } from '../primitives/read-more.js';
import { define as definePrimitives, FIELDS } from '../primitives/index.js';
import { ActionState, bindingErrors, copyActionValue, isActionValue, isEditType } from '../actions/action.js';
import { PATIENCE, patienceOf, ReceiptTimer } from './pending.js';
import { COMPACT_CONTROL_CSS, CONTROL_DISPLAY, EDGE, FLOATING_TOOLTIP_CSS, LIFT, compactControls, controlIcon,
  el, floatingTooltip, icon, setDisclosure, sheet } from '../render/dom.js';
import { ACTION_RESULT_CSS, REFERENCE_CSS, showReference } from './receipt.js';
import { sameRichTextValue } from '../render/rich-text.js';
import { fillErrors } from '../render/validate.js';
import { CONTRACTS, registeredTag } from '../render/registry.js';
import { MuTable } from './table.js';
export { MuTable } from './table.js';
import { MuDialog, MuDrawer } from './dialog.js';
export { MuDialog, MuDrawer } from './dialog.js';
import { MuMenu, MuTooltip, MuToast } from './popovers.js';
export { MuMenu, MuTooltip, MuToast } from './popovers.js';
import { MuAppShell, MuBreadcrumbs, MuPagination, MuSteps } from './navigation.js';
export { MuAppShell, MuBreadcrumbs, MuPagination, MuSteps } from './navigation.js';
import { MuRunSteps, MuPlan } from './run-steps.js';
export { MuRunSteps, MuPlan } from './run-steps.js';
import { MuProgress } from './progress.js';
export { MuProgress } from './progress.js';
import { MuEmptyState, MuSkeleton, MuBadge, MuAvatar } from './page-states.js';
export { MuEmptyState, MuSkeleton, MuBadge, MuAvatar } from './page-states.js';
import { MuAccordion, MuTabs } from './panels.js';
export { MuAccordion, MuTabs } from './panels.js';
import { MuStatus } from './status.js';
export { MuStatus } from './status.js';
import { MuImage } from './image.js';
export { MuImage } from './image.js';
import { MuChart } from './chart.js';
export { MuChart } from './chart.js';
import { MuRichText } from './rich-text.js';
export { MuRichText } from './rich-text.js';
import { MuDataGrid } from './data-grid.js';
export { MuDataGrid } from './data-grid.js';
import { MuUpload } from './upload.js';
export { MuUpload } from './upload.js';

const FIELD_NAME = /^[a-z][a-z0-9]*(_[a-z0-9]+)*$/;
const boundError = (name) => `data.sections: the read-only field ${name} does not show the bound value`;
const DIFF_EMPTY_PART = 'No changes. You do not need to review this part.';
const DIFF_EMPTY = 'No changes. You do not need to review these versions.';
// The thread gives this context before it sets the fill. The bound actor still checks the author claim.
export const CARD_MESSAGE_ACTOR = Symbol('card message actor');
const UPDATE_CARD_DISPLAY = Symbol('update card display');
const COLLAPSE_CSS = sheet(`
  .collapse-controls { display: flex; flex-wrap: wrap; gap: var(--mu-space-2); margin-block: var(--mu-space-2) var(--mu-space-3); }
  .collapse-toggle, .collapse-controls button {
    font: var(--mu-type-body); min-height: 2.75rem; min-width: 2.75rem; padding: var(--mu-space-2) var(--mu-space-3);
    border: 1px solid var(--mu-color-border); border-radius: var(--mu-radius-2);
    background: transparent; color: var(--mu-color-text); cursor: pointer;
  }
  .collapse-toggle { display: inline-flex; align-items: center; gap: var(--mu-space-2); flex: none; }
  /* The chevron points down on a collapsed card and up on an expanded card. */
  .collapse-toggle[aria-expanded="true"] .icon { transform: rotate(180deg); }
  .card-header { display: grid; grid-template-columns: minmax(0, 1fr) auto; align-items: center; gap: var(--mu-space-2); }
  .card-header .title { min-width: 0; overflow-wrap: anywhere; }
  .card-body { display: flex; flex-direction: column; gap: var(--mu-space-2); }
  [hidden] { display: none !important; }
  @media (forced-colors: active) {
    .collapse-toggle, .collapse-controls button { color: ButtonText; border-color: ButtonText; background: ButtonFace; }
  }
`);
const CARD_CSS = sheet(`
  :host { display: block; }
  article, form {
    background: var(--mu-color-surface); color: var(--mu-color-text); font: var(--mu-type-body);
    border: 1px solid ${EDGE}; border-radius: var(--mu-radius-2); padding: var(--mu-space-4);
    box-shadow: ${LIFT}; display: flex; flex-direction: column; gap: var(--mu-space-2); height: 100%;
  }
  .title { font-weight: var(--mu-type-weight-strong); margin: 0; }
  article p { margin: 0; }
  .muted, dt { font: var(--mu-type-caption); color: var(--mu-color-text-muted); }
  dl { margin: 0; display: grid; grid-template-columns: max-content 1fr; gap: var(--mu-space-2) var(--mu-space-3); }
  dd { margin: 0; }
  .actions { display: flex; flex-wrap: wrap; gap: var(--mu-space-2); }
  .provenance p { margin: 0; overflow-wrap: anywhere; }
  .provenance button {
    font: var(--mu-type-caption); min-height: 2.75rem; padding: 0 var(--mu-space-2); cursor: pointer;
    border: 1px solid var(--mu-color-border); border-radius: var(--mu-radius-2); background: transparent; color: var(--mu-color-text);
  }
  @media (forced-colors: active) {
    article, form { border-color: CanvasText; } .muted, dt { color: CanvasText; }
    .provenance button { color: ButtonText; border-color: ButtonText; }
  }
`);
const ACTION_CSS = sheet(`
  :host { display: inline-flex; flex-direction: column; gap: var(--mu-space-1); max-inline-size: 100%; }
  button {
    font: var(--mu-type-body); min-height: max(44px, 2.75rem); min-width: max(44px, 2.75rem); padding: var(--mu-space-2) var(--mu-space-3);
    border-radius: var(--mu-radius-2); border: 1px solid var(--mu-color-accent);
    background: var(--mu-color-accent); color: var(--mu-color-on-accent); cursor: pointer;
  }
  .with-icon { display: inline-flex; align-items: center; justify-content: center; gap: var(--mu-space-2); }
  .icon-control { display: flex; align-items: flex-start; }
  .icon-control button {
    min-inline-size: max(44px, 2.75rem); min-block-size: max(44px, 2.75rem); padding: var(--mu-space-2);
  }
  .status { display: block; font: var(--mu-type-caption); color: var(--mu-color-text-muted); min-height: 1lh; overflow-wrap: anywhere; }
  .status.done { color: var(--mu-color-success); }
  .status.error { color: var(--mu-color-danger); }
  .result { display: flex; flex-direction: column; gap: var(--mu-space-1); }
  .copy { border-radius: var(--mu-radius-2); }
  .step { display: flex; flex-wrap: wrap; align-items: center; gap: var(--mu-space-2); }
  .step p { flex-basis: 100%; min-inline-size: 0; margin: 0; overflow-wrap: anywhere; }
  .step button[part="cancel"] { background: transparent; color: var(--mu-color-accent); }
  @media (forced-colors: active) {
    button { background: ButtonFace; color: ButtonText; border-color: ButtonText; forced-color-adjust: none; }
    .step button[part="cancel"] { background: ButtonFace; color: ButtonText; }
    .status, .status.done, .status.error { color: CanvasText; }
  }
`);

// The filled slots of a fill, as text (catalog/schema/README.md). The locator opens only after a person clicks
// the button. The element first sends a cancelable mu-open event, so that the application can open the locator.
export function provenance(slots, authorLine) {
  const { authorship, citation } = slots ?? {};
  if (!authorship && !citation) return null;
  const parts = [];
  if (authorship) {
    const name = authorship.name ? ` (${authorship.name})` : '';
    parts.push(el('p', { class: 'muted', part: 'authorship' },
      ...(authorLine ?? [`Author: ${authorship.type} ${authorship.id}${name}`])));
  }
  if (citation) {
    const open = el('button', { type: 'button', part: 'open', 'aria-label': `Open source: ${citation.source}` }, 'Open source');
    open.addEventListener('click', () => openLocator(open, citation.locator));
    parts.push(el('p', { class: 'muted', part: 'citation' }, `Source: ${citation.source}`),
      el('p', { class: 'muted', part: 'locator' }, citation.locator), el('div', {}, open));
  }
  return el('div', { class: 'provenance', part: 'provenance' }, ...parts);
}

function cardAuthorLine({ type, id, name }, messageActor) {
  const identity = `${type}, ${id}`;
  if (type === messageActor?.type && id === messageActor.id) {
    const fullName = name ?? messageActor.name;
    const fullLine = fullName ? `Author: ${fullName} (${identity})` : `Author: ${identity}`;
    return [el('span', { 'aria-hidden': 'true' }, 'Author: same as the message'),
      el('span', { class: 'full-author' }, fullLine)];
  }
  return name ? ['Author: ', el('span', { class: 'author-name' }, name), ` (${identity})`] : [`Author: ${identity}`];
}

// The validator accepts only an https: locator with no user name. Check it again before the browser opens it.
function openLocator(source, locator) {
  let url;
  try {
    url = new URL(locator);
  } catch {
    return;
  }
  if (url.protocol !== 'https:' || url.username || url.password) return;
  const event = new CustomEvent('mu-open', { bubbles: true, composed: true, cancelable: true, detail: { locator: url.href } });
  if (source.dispatchEvent(event)) window.open(url.href, '_blank', 'noopener,noreferrer');
}

const STALE = 'Saved the values from before your last edit. Save again to send the edit.';
// The text of each state. A pending action never shows as done.
const STATUS = { default: '', disabled: '', confirm: '', pending: 'Pending', done: 'Done', error: 'Not done' };
const APPROVAL_CONTROL = Symbol('approval control');
const BLOCK_ACTION = Symbol('block action');

// Shared code for a component with one action. The application binds the tool with the action property
// and gives the receipt with receipt(). The element sends a mu-action event with the request.
class ActionElement extends MuElement {
  #action = new ActionState();
  #status = null;
  #control = null;
  // The area of the confirm step, and the step while it shows.
  #area = null;
  #asking = null;
  #reference = null;
  // The exits of a pending action that shows now: queued or stalled. The timer of a try with no receipt.
  #exit = '';
  #timer = new ReceiptTimer();
  #patience = PATIENCE;
  #blocked = false;

  // The state of the action can only be read. Only bind(), start() and receipt() change it.
  get action() {
    return this.#action;
  }

  set action(binding) {
    if (this.constructor.component !== 'action-button' && binding && (Object.hasOwn(binding, 'icon') || Object.hasOwn(binding, 'display'))) {
      throw new TypeError('action: only an action button accepts icon and display');
    }
    const errors = this.#action.bind(binding);
    if (errors.length) throw new TypeError('action: ' + errors.join('; '));
    this.#close();
    this.rebound();
    this.#update();
  }

  receipt(value) {
    const accepted = this.#action.receipt(value);
    // A receipt confirms only the values of its request. If the data changed after the request, the
    // element does not show done. It asks for a new request.
    if (accepted && this.#action.state === 'done' && this.stale()) this.#action.reopen(STALE);
    if (accepted) this.#update();
    return accepted;
  }

  noReceipt(id) {
    if (this.#action.noReceipt(id)) this.#update();
  }

  // The time in milliseconds that a try waits for its receipt. After that time the action is stalled, and the
  // element offers a retry and a cancel. The action stays pending, and a later receipt is still valid.
  get patience() {
    return this.#patience;
  }

  set patience(value) {
    this.#patience = patienceOf(value);
  }

  // Sets the detail of the pending action: { key, wait, tries, message }, where wait is sent, queued or stalled
  // (docs/contracts/offline-queue.md). The offline queue calls it. The state stays pending.
  hold(update) {
    const before = this.#action.wait + this.#action.message;
    const accepted = this.#action.hold(update);
    // The same detail again changes nothing, so the status does not say its text a second time.
    if (accepted && this.#action.wait + this.#action.message !== before) this.#update();
    return accepted;
  }

  // Cancels the pending action with this key, if it waits for a connection or got no receipt. The owner of the
  // request calls it, for example the offline queue. Returns true if the action is not pending now.
  cancel(key) {
    if (!key || key !== this.#action.key || !this.#action.cancel()) return false;
    this.canceled();
    this.#update();
    return true;
  }

  // A subclass can forget the values of the canceled request.
  canceled() {}

  // The name of the data field that shows the label of the action.
  static labelField = 'label';

  // The application gives the label of the action. A fill cannot show a different meaning for the bound tool.
  check(data) {
    const label = this.#action.label;
    const field = this.constructor.labelField;
    if (label !== null && data[field] !== label) return [`data.${field}: the label is not the label of the application`];
    return [];
  }

  // A new binding can refuse a fill that the element shows now.
  rebound() {
    if (this.fill && this.check(this.fill.data).length) this.fill = this.fill;
  }

  // A subclass can change its controls after each change of the state.
  updated() {}

  // A subclass returns true if its data changed after the last request.
  stale() {
    return false;
  }

  // An edit after a receipt makes the data new. Then the action can start again. An edit also closes
  // the confirm step, because the person did not confirm the new data.
  reopen() {
    this.#close();
    if (this.#action.reopen()) this.#update();
  }

  state(data) {
    return data.disabled ? 'disabled' : this.#action.state;
  }

  // Returns the confirm area and the receipt text. Each subclass puts them in its shadow root.
  attach(control, status) {
    this.#control = control;
    this.#status = status;
    this.#asking = null;
    this.#reference = el('span', { class: 'reference-row' });
    this.#area = el('div', { class: 'confirm-area' });
    this.#exit = '';
    this.#update();
    return [this.#area, el('div', { class: 'result' }, status, this.#reference)];
  }

  // The started function runs only for an accepted request, before the element sends the event.
  // If the binding has a confirm step, the element shows it first and sends the action only after a person confirms.
  send(extra, started, edits) {
    if (this.dataset.state === 'refused' || !this.fill || this.#asking || this.#blocked) return;
    const state = this.#action.state;
    if (state !== 'default' && state !== 'error') return;
    const confirm = this.#action.confirm;
    if (!confirm) {
      this.#start(extra, started, false, edits);
      return;
    }
    const cancel = el('button', { type: 'button', part: 'cancel' }, 'Cancel');
    const accept = el('button', { type: 'button', part: 'accept' }, confirm.label);
    const nameControl = this[APPROVAL_CONTROL]?.nameControl;
    nameControl?.(cancel, 'Cancel');
    nameControl?.(accept, confirm.label);
    const step = el('div', { class: 'step', role: 'group', 'aria-label': confirm.message, part: 'confirm' },
      el('p', {}, confirm.message), cancel, accept);
    cancel.addEventListener('click', () => this.#close());
    accept.addEventListener('click', () => {
      this.#close(false);
      this.#start(extra, started, true, edits);
    });
    step.addEventListener('keydown', (event) => {
      if (event.key !== 'Escape') return;
      event.preventDefault();
      event.stopPropagation();
      this.#close();
    });
    this.#asking = step;
    this.#area.replaceChildren(step);
    this.#update();
    cancel.focus();
  }

  // Closes the confirm step. If the focus was in the step, it goes back to the action control.
  #close(update = true) {
    if (!this.#asking) return;
    const focused = this.#asking.contains(this.shadowRoot?.activeElement);
    this.#asking = null;
    this.#area.replaceChildren();
    if (update) this.#update();
    if (focused) this.#control.focus();
  }

  #start(extra, started, confirmed, edits) {
    const request = this.#action.start(extra, confirmed, edits);
    if (!request) {
      this.#update();
      return;
    }
    started?.(request);
    this.#update();
    this.dispatchEvent(new CustomEvent('mu-action', { bubbles: true, composed: true, detail: request }));
  }

  // A new try of a stalled action: the same key and a new id.
  #retry() {
    const request = this.#action.retry();
    if (!request) return;
    this.#update();
    this.dispatchEvent(new CustomEvent('mu-action', { bubbles: true, composed: true, detail: request }));
  }

  // A listener that owns the request, for example the offline queue, cancels the action itself or prevents the
  // event. With no such listener, the element cancels the action.
  #cancel() {
    const key = this.#action.key;
    if (!key) return;
    const event = new CustomEvent('mu-action-cancel', { bubbles: true, composed: true, cancelable: true, detail: Object.freeze({ key }) });
    if (this.dispatchEvent(event)) this.cancel(key);
  }

  // The exits of a pending action: a cancel while it waits for a connection, a retry and a cancel after a try
  // with no receipt. The group stays the same node while the detail stays the same, so the focus stays.
  #exits(wait) {
    const kind = wait === 'queued' || wait === 'stalled' ? wait : '';
    if (kind === this.#exit) return;
    const focused = Boolean(this.#exit) && this.#area.contains(this.shadowRoot.activeElement);
    this.#exit = kind;
    if (this.#asking) return;
    if (!kind) {
      this.#area.replaceChildren();
      if (focused) this.#control.focus();
      return;
    }
    const name = this.fill.data[this.constructor.labelField];
    const retry = kind === 'stalled' ? el('button', { type: 'button', part: 'retry', 'aria-label': `Retry ${name}` }, 'Retry') : null;
    const cancel = el('button', { type: 'button', part: 'cancel', 'aria-label': `Cancel ${name}` }, 'Cancel');
    const nameControl = this[APPROVAL_CONTROL]?.nameControl;
    if (retry) nameControl?.(retry, 'Retry ' + name);
    nameControl?.(cancel, 'Cancel ' + name);
    retry?.addEventListener('click', () => this.#retry());
    cancel.addEventListener('click', () => this.#cancel());
    this.#area.replaceChildren(el('div', { class: 'step', role: 'group', 'aria-label': `Pending: ${name}`, part: 'exit' }, retry, cancel));
    if (focused) (retry ?? cancel).focus();
  }

  #update() {
    if (!this.fill || !this.#control) return;
    const state = this.#asking ? 'confirm' : this.#action.state;
    this.dataset.state = state;
    // The detail of a pending action: sent, queued or stalled. A stalled or queued action has an exit.
    const wait = this.#action.wait;
    if (wait) this.dataset.wait = wait;
    else delete this.dataset.wait;
    this.#exits(wait);
    this.#timer.watch(wait, this.#patience, () => this.hold({ key: this.#action.key, wait: 'stalled' }));
    this.#updateControl(state);
    const words = [STATUS[state], state === 'confirm' ? '' : this.#action.message].filter(Boolean).join(': ');
    if (this.#status) {
      this.#status.className = 'status ' + state;
      this.#status.textContent = words;
    }
    showReference(this.#reference, state === 'confirm' ? '' : this.#action.reference, this[APPROVAL_CONTROL]?.nameControl);
    this.updated(state, words);
  }

  [BLOCK_ACTION](blocked) {
    this.#blocked = blocked;
    this.#updateControl(this.dataset.state);
  }

  #updateControl(state) {
    if (!this.#control) return;
    const enabled = !this.#blocked && (state === 'default' || state === 'error');
    this.#control.dataset.state = this.#blocked ? 'disabled' : state;
    this.#control.setAttribute('aria-disabled', enabled ? 'false' : 'true');
    // A pending action is busy until its receipt comes back. The control accepts no second press.
    if (state === 'pending') this.#control.setAttribute('aria-busy', 'true');
    else this.#control.removeAttribute('aria-busy');
  }
}

export class MuActionButton extends ActionElement {
  static component = 'action-button';
  static focus = true;
  static styles = [ACTION_CSS, REFERENCE_CSS, ACTION_RESULT_CSS, FLOATING_TOOLTIP_CSS];
  #graphic = null;
  #doneGraphic = null;
  #dismissTooltip = null;

  disconnectedCallback() {
    this.#dismissTooltip?.();
  }

  refuse(errors) {
    this.#dismissTooltip?.();
    this.#dismissTooltip = null;
    super.refuse(errors);
  }

  // The application gives the description, icon and display of the bound action. The fill must agree with the binding.
  check(data) {
    const errors = super.check(data);
    if (errors.length) return errors;
    const { description, icon, display } = this.action;
    if (description === null) return [];
    if ((data.description ?? '') !== description) {
      return ['data.description: the description is not the description of the application'];
    }
    if (data.icon !== icon) return ['data.icon: the icon is not the icon of the application'];
    if (data.display !== display) return ['data.display: the display is not the display of the application'];
    return [];
  }

  render(data) {
    this.#dismissTooltip?.();
    this.#dismissTooltip = null;
    const approval = this[APPROVAL_CONTROL];
    const status = approval ? null : el('span', { class: 'status', role: 'status', 'aria-live': 'polite', part: 'status' });
    const display = data.icon ? data.display ?? 'text' : 'text';
    const iconOnly = display === 'icon';
    this.#graphic = display === 'text' ? null : icon(data.icon);
    this.#doneGraphic = this.#graphic ? icon('check') : null;
    const button = el('button', { type: 'button', part: 'button', class: this.#graphic ? 'with-icon' : null,
      'aria-label': iconOnly ? data.label : null }, this.#graphic, iconOnly ? null : data.label);
    approval?.nameControl(button, data.label);
    button.addEventListener('click', () => this.send());
    return [approval?.title, iconOnly ? this.#withTooltip(button, data.label) : button,
      data.description ? el('span', { class: 'muted' }, data.description) : null, ...this.attach(button, status)];
  }

  updated(state, words) {
    this[APPROVAL_CONTROL]?.changed(state, words);
    if (!this.#graphic) return;
    if (state === 'done') this.#graphic.replaceWith(this.#doneGraphic);
    else this.#doneGraphic.replaceWith(this.#graphic);
  }

  #withTooltip(button, label) {
    // The button already has this name. The tooltip gives the same text to a person who can see it.
    const tooltip = el('span', { part: 'tooltip', 'aria-hidden': 'true' }, label);
    const control = el('div', { class: 'icon-control' }, button);
    const layer = floatingTooltip(control, tooltip);
    const shown = { hover: false, focus: false };
    const show = (source, value) => {
      shown[source] = value;
      if (shown.hover || shown.focus) layer.show(button);
      else layer.hide();
    };
    const dismiss = () => {
      shown.hover = false;
      show('focus', false);
    };
    this.#dismissTooltip = dismiss;
    control.addEventListener('pointerenter', (event) => {
      if (event.pointerType !== 'touch') show('hover', true);
    });
    control.addEventListener('pointerleave', () => show('hover', false));
    button.addEventListener('focus', () => show('focus', true));
    button.addEventListener('blur', () => show('focus', false));
    button.addEventListener('keydown', (event) => {
      if (event.key !== 'Escape' || tooltip.hidden) return;
      event.preventDefault();
      event.stopPropagation();
      dismiss();
    });
    return control;
  }
}

const DECISIONS = Object.freeze({
  approve: { label: 'Approve', confirm: 'Confirm approval', result: 'Approved', state: 'approved' },
  reject: { label: 'Reject', confirm: 'Confirm rejection', result: 'Rejected', state: 'rejected' },
});

function approvalAuthor({ type, id, name }) {
  return 'Author: ' + (name ? `${name} (${type}, ${id})` : `${type}, ${id}`);
}

export class MuApprovalQueue extends MuElement {
  static component = 'approval-queue';
  static styles = [APPROVAL_MARK_CSS, sheet(`
    :host { display: block; min-inline-size: 0; color: var(--mu-color-text); font: var(--mu-type-body); overflow-wrap: anywhere; }
    p, ul { margin: 0; }
    [part="label"], [part="title"] { font-weight: var(--mu-type-weight-strong); }
    [part="authorship"], [part="state"], .bound-context { font: var(--mu-type-caption); }
    [part="authorship"] { margin-block-end: var(--mu-space-1); }
    ul { list-style: none; padding: 0; }
    li { border-block-start: 1px solid var(--mu-color-border); padding: var(--mu-space-2); }
    [part="state"] { margin-block: var(--mu-space-1); }
    [part="state"] > [data-badge] { display: inline-block; max-inline-size: 100%; box-sizing: border-box; }
    .decisions { display: flex; flex-wrap: wrap; align-items: start; gap: var(--mu-space-2); }
    [part="announcement"] {
      position: absolute; inline-size: 1px; block-size: 1px; overflow: hidden; clip-path: inset(50%); white-space: nowrap;
    }
    @media (forced-colors: active) {
      li { border-color: CanvasText; }
    }
  `)];

  #view = null;
  #rows = new Map();

  // Bind only proposals that the application accepts. Each control uses a separate action binding.
  bind(id, decisions) {
    const entry = this.#rows.get(id);
    if (!this.fill || !entry || entry.bound) throw new TypeError('bind: give the id of an item with no binding');
    const bindings = Object.entries(DECISIONS).map(([kind, { label }]) => {
      const binding = { ...decisions?.[kind], label };
      if (!binding.confirm) throw new TypeError('bind: give a confirm step for ' + label);
      if (binding.icon !== undefined || binding.display !== undefined || binding.description !== undefined) {
        throw new TypeError('bind: the queue gives the text of the control');
      }
      const errors = bindingErrors(binding);
      if (errors.length) throw new TypeError('bind: ' + errors.join('; '));
      return [kind, binding];
    });
    for (const [kind, binding] of bindings) entry.actions[kind].action = binding;
    entry.bound = this.#snapshot(entry.proposal);
    entry.row.dataset.state = 'default';
    entry.state.replaceChildren(badgeLabel('Awaiting decision'));
  }

  // Each action checks the id and key of its own request.
  receipt(value) {
    for (const { actions } of this.#rows.values()) {
      if (actions.approve.receipt(value) || actions.reject.receipt(value)) return true;
    }
    return false;
  }

  state({ items }) {
    if (items.length || [...this.#rows.values()].some(({ row }) => row.dataset.state === 'pending')) return 'default';
    return 'empty';
  }

  refuse(errors) {
    // Keep decisions that were pending at the first refusal until the next valid fill.
    if (this.fill) {
      for (const [id, entry] of this.#rows) {
        if (entry.row.dataset.state === 'pending') continue;
        this.#remove(entry);
        this.#rows.delete(id);
      }
    }
    if (this.#rows.size === 0) this.#view = null;
    super.refuse(errors);
    const announcement = this.#view?.announcement;
    if (this.dataset.state === 'refused' && announcement && !announcement.parentNode) {
      this.shadowRoot.append(announcement);
    }
  }

  render({ label, items }) {
    this.#view ??= {
      label: el('p', { part: 'label' }),
      byline: el('p', { part: 'authorship' }),
      list: el('ul', { part: 'list', role: 'list' }),
      empty: el('p', { part: 'empty' }, 'No items need a decision. Check again later.'),
      announcement: el('div', { part: 'announcement', role: 'status', 'aria-live': 'polite' }),
    };
    const view = this.#view;
    const author = approvalAuthor(this.actor);
    if (view.byline.textContent !== author) view.byline.textContent = author;
    if (view.label.textContent !== label) {
      view.label.textContent = label;
      view.list.setAttribute('aria-label', label);
    }
    const next = new Map();
    for (const item of items) {
      const entry = this.#rows.get(item.id) ?? this.#newItem();
      entry.proposal = { ...item, label };
      this.#showProposal(entry);
      next.set(item.id, entry);
    }
    for (const [id, entry] of this.#rows) {
      if (next.has(id)) continue;
      if (entry.row.dataset.state === 'pending') {
        entry.proposal = { ...entry.proposal, label };
        this.#showProposal(entry);
        next.set(id, entry);
      } else {
        this.#remove(entry);
      }
    }
    view.empty.hidden = next.size > 0;
    const active = this.shadowRoot.activeElement;
    let before = null;
    for (const { row } of [...next.values()].reverse()) {
      // Move other rows around the focused row to keep focus in its action.
      if ((!active || !row.contains(active)) && (row.parentNode !== view.list || row.nextElementSibling !== before)) {
        view.list.insertBefore(row, before);
      }
      before = row;
    }
    this.#rows = next;
    return view.list.parentNode === this.shadowRoot ? null : [view.label, view.byline, view.list, view.empty, view.announcement];
  }

  #snapshot({ label, title, summary }) {
    // Add each new proposal value that the queue shows to this snapshot.
    return { label, title, summary, actor: this.actor };
  }

  #showProposal(entry) {
    const { bound, context } = entry;
    const current = this.#snapshot(entry.proposal);
    if (entry.row.dataset.state === 'pending') {
      // The list header shows the current label and actor. Keep the bound context on this row.
      const author = approvalAuthor(bound.actor);
      const words = [
        bound.label !== current.label ? bound.label : '',
        author !== approvalAuthor(current.actor) ? author : '',
      ].filter(Boolean).join('. ');
      if (context.textContent !== words) context.textContent = words;
      context.hidden = !words;
      return;
    }
    if (!context.hidden) {
      context.hidden = true;
      context.textContent = '';
    }
    const { title, summary } = current;
    if (bound && JSON.stringify(bound) !== JSON.stringify(current)) {
      this.#unbind(entry);
    }
    if (entry.title.textContent !== title) {
      entry.title.textContent = title;
      for (const name of entry.names) name.textContent = title;
    }
    if (entry.summary.textContent !== summary) entry.summary.textContent = summary;
  }

  #newItem() {
    const context = el('p', { class: 'bound-context' });
    context.hidden = true;
    const title = el('p', { part: 'title' });
    const summary = el('p', { part: 'summary' });
    const state = el('p', { part: 'state' }, badgeLabel('Unavailable'));
    const controls = el('div', { class: 'decisions' });
    const row = el('li', { part: 'item', 'data-state': 'disabled' }, context, title, summary, state, controls);
    const entry = { row, context, title, summary, state, actions: {}, views: {}, names: [], bound: null, proposal: null };
    for (const [kind, { label }] of Object.entries(DECISIONS)) {
      const child = document.createElement('mu-action-button');
      child.setAttribute('exportparts', 'button: action-button-button, accept: action-button-accept, cancel: action-button-cancel, confirm: action-button-confirm, copy: action-button-copy, copy-status: action-button-copy-status, exit: action-button-exit, reference: action-button-reference, retry: action-button-retry');
      const name = el('span', { id: 'proposal-title' });
      name.hidden = true;
      child[APPROVAL_CONTROL] = {
        title: name,
        // Start the name with the visible button label, then add the proposal title.
        nameControl(control, label) {
          control.id = 'decision-' + control.getAttribute('part');
          control.setAttribute('aria-label', label + ':');
          control.setAttribute('aria-labelledby', control.id + ' ' + name.id);
        },
        changed: (state, words) => this.#changed(entry, kind, state, words),
      };
      child.fill = { component: 'action-button', version: '0.1.0', data: { label } };
      entry.actions[kind] = child;
      entry.names.push(name);
      controls.append(child);
    }
    return entry;
  }

  #changed(entry, kind, state, words) {
    entry.views[kind] = { state, words };
    if (!entry.bound) return;
    const other = kind === 'approve' ? 'reject' : 'approve';
    entry.actions[other][BLOCK_ACTION](state === 'confirm' || state === 'pending' || state === 'done');
    if (state === 'default' && !words && entry.views[other].state === 'error') {
      ({ state, words } = entry.views[other]);
    }
    if (state === 'confirm') words = DECISIONS[kind].confirm;
    if (state === 'done') words = DECISIONS[kind].result + words.slice(STATUS.done.length);
    if (!words) words = 'Awaiting decision';
    if (entry.row.dataset.state === state && entry.state.textContent === words) return;
    const resolved = entry.row.dataset.state === 'pending' && state !== 'pending';
    entry.row.dataset.state = state;
    entry.state.replaceChildren(state === 'done' ? approvalMark({ state: DECISIONS[kind].state }, words)
      : badgeLabel(words, state === 'error' ? 'danger' : undefined));
    const { announcement } = this.#view;
    // A receipt can come back in a refusal event listener, before refuse() puts the status back.
    if (this.dataset.state === 'refused' && !announcement.parentNode) this.shadowRoot.append(announcement);
    announcement.textContent = `${entry.bound.title}: ${words}`;
    if (resolved) this.#showProposal(entry);
  }

  #unbind(entry) {
    if (!entry.bound) return;
    entry.bound = null;
    for (const action of Object.values(entry.actions)) {
      action.action = null;
      action[BLOCK_ACTION](false);
    }
    entry.row.dataset.state = 'disabled';
    entry.state.replaceChildren(badgeLabel('Unavailable'));
  }

  #remove(entry) {
    this.#unbind(entry);
    entry.row.remove();
  }
}

export class MuCard extends MuElement {
  static component = 'card';
  static observedAttributes = [CONTROL_DISPLAY];
  static styles = [CARD_CSS, COLLAPSE_CSS, COMPACT_CONTROL_CSS, FLOATING_TOOLTIP_CSS, sheet(`
    .thread-card { background: var(--mu-color-surface-subtle); box-shadow: none; }
    [part="facts"] { align-items: baseline; }
    .author-name { color: var(--mu-color-text); }
    .full-author { position: absolute; width: 1px; height: 1px; overflow: hidden;
      clip-path: inset(50%); white-space: nowrap; }
    @media (forced-colors: none) {
      .thread-card { border-color: transparent; }
    }
    @media (forced-colors: active) {
      .author-name { color: CanvasText; }
    }
  `)];

  #collapsed = false;
  #hasRendered = false;
  #changed = false;
  #toggle = null;
  #toggleLabel;
  #controls;
  #body = null;
  #summary = null;
  #observer = new MutationObserver(() => this.#watchActions());

  constructor() {
    super();
    // Expand before the action event reaches application code. The request still needs its receipt.
    for (const name of ['mu-action', 'mu-upload']) this.addEventListener(name, () => this.#watchActions());
  }

  connectedCallback() {
    this[UPDATE_CARD_DISPLAY]();
    this.#watchActions();
  }

  disconnectedCallback() {
    this.#observer.disconnect();
    this.#controls?.dismiss();
  }

  refuse(errors) {
    this.#controls?.dismiss();
    this.#controls = null;
    super.refuse(errors);
  }

  attributeChangedCallback() {
    this[UPDATE_CARD_DISPLAY]();
  }

  [UPDATE_CARD_DISPLAY]() {
    this.#controls?.render();
  }

  get collapsed() {
    return this.fill !== null && this.#collapsed;
  }

  set collapsed(value) {
    if (typeof value !== 'boolean') throw new TypeError('collapsed: must be a boolean');
    if (!this.fill?.data.collapsible) return;
    this.#setCollapsed(value && !this.#mustStayOpen());
  }

  // Inspect light DOM and open shadow roots, including nested forms and offline pending markers.
  #actionRoots() {
    const roots = [this];
    for (let i = 0; i < roots.length; i += 1) {
      for (const node of roots[i].querySelectorAll('*')) if (node.shadowRoot) roots.push(node.shadowRoot);
    }
    return roots;
  }

  #mustStayOpen() {
    return this.#actionRoots().some((root) => [...root.querySelectorAll('*')].some((node) =>
      ['pending', 'error'].includes(node.action?.state) || ['pending', 'confirm', 'error'].includes(node.dataset?.state)));
  }

  #watchActions() {
    this.#observer.disconnect();
    if (!this.fill?.data.collapsible) return;
    if (this.isConnected) {
      for (const root of this.#actionRoots()) {
        this.#observer.observe(root, { subtree: true, childList: true, attributes: true, attributeFilter: ['data-state'] });
      }
    }
    if (this.#mustStayOpen()) this.#setCollapsed(false);
  }

  #setCollapsed(value) {
    if (this.#collapsed === value) return;
    // Do not move focus when a grid button changes this card. Return only focus inside hidden content.
    const focused = value && (this.#body?.contains(this.shadowRoot.activeElement) || this.contains(document.activeElement));
    this.#collapsed = value;
    this.dataset.state = value ? 'collapsed' : 'expanded';
    this.#sync();
    if (focused) this.#toggle?.focus();
    this.#emit();
  }

  #sync() {
    if (!this.#toggle) return;
    setDisclosure(this.#toggle, this.#body, !this.#collapsed);
    this.#toggle.setAttribute('aria-label', `${this.#collapsed ? 'Expand' : 'Collapse'} ${this.fill.data.title}`);
    this.#toggleLabel.textContent = this.#collapsed ? 'Expand' : 'Collapse';
    this.#controls.refresh();
    if (this.#summary) this.#summary.hidden = !this.#collapsed;
  }

  #emit() {
    this.dispatchEvent(new CustomEvent('mu-collapse', { bubbles: true, composed: true, detail: { collapsed: this.#collapsed } }));
  }

  state(data) {
    return data.collapsible ? (data.collapsed && !this.#mustStayOpen() ? 'collapsed' : 'expanded') : 'default';
  }

  render(data) {
    this.#observer.disconnect();
    this.#controls?.dismiss();
    this.#controls = null;
    const before = this.#collapsed;
    this.#collapsed = Boolean(data.collapsible && data.collapsed && !this.#mustStayOpen());
    this.#toggle = null;
    this.#body = null;
    this.#summary = null;
    const date = data.date ? el('p', { class: 'muted' }, `${data.date_label ?? 'Date'}: ${data.date}`) : null;
    const facts = (data.facts ?? []).length ? el('dl', { part: 'facts' }, ...data.facts.flatMap((item) => [
      el('dt', {}, item.data.label), el('dd', {}, item.data.value)])) : null;
    // The application puts action buttons in the actions slot. A fill cannot add one.
    const actions = el('div', { class: 'actions', part: 'actions' });
    const slot = document.createElement('slot');
    slot.name = 'actions';
    slot.addEventListener('slotchange', () => this.#watchActions());
    actions.append(slot);
    const title = el('p', { class: 'title', part: 'title' }, data.title);
    const status = data.status ? el('p', { class: 'muted', part: 'status', role: 'status' }, data.status) : null;
    const messageActor = this[CARD_MESSAGE_ACTOR];
    const attribution = provenance(this.fill.slots, cardAuthorLine(this.fill.slots.authorship, messageActor));
    const content = [data.summary ? el('p', {}, data.summary) : null, date, facts];
    let parts = [title, status, ...content, attribution, actions];
    if (data.collapsible) {
      this.#body = el('div', { class: 'card-body', part: 'body', id: 'card-body' }, ...content, actions);
      this.#summary = data.collapsed_summary ? el('p', { part: 'collapsed-summary' }, data.collapsed_summary) : null;
      this.#toggle = el('button', { type: 'button', class: 'collapse-toggle', part: 'toggle', 'aria-controls': 'card-body' });
      this.#toggleLabel = el('span');
      const header = el('header', { class: 'card-header' }, title, this.#toggle);
      const tooltip = el('span', { part: 'tooltip', 'aria-hidden': 'true' });
      this.#controls = compactControls(this, header, tooltip, [{ button: this.#toggle, label: this.#toggleLabel,
        graphic: icon('chevron'), keepIcon: true }]);
      this.#toggle.addEventListener('click', () => { this.collapsed = !this.#collapsed; });
      this.#sync();
      parts = [header, status, this.#summary, attribution, this.#body];
    }
    const article = el('article', { 'aria-label': data.title, part: 'card', class: messageActor ? 'thread-card' : null }, ...parts);
    this.#changed = this.#hasRendered && before !== this.#collapsed;
    this.#hasRendered = true;
    return [article];
  }

  rendered() {
    this.#watchActions();
    const changed = this.#changed;
    this.#changed = false;
    if (changed) this.#emit();
  }
}

export class MuCardGrid extends MuElement {
  static component = 'card-grid';
  static observedAttributes = [CONTROL_DISPLAY];
  static styles = [COLLAPSE_CSS, COMPACT_CONTROL_CSS, FLOATING_TOOLTIP_CSS, sheet(`
    :host { display: block; }
    ul { list-style: none; margin: 0; padding: 0; display: grid; gap: var(--mu-space-4);
      grid-template-columns: repeat(auto-fill, minmax(min(100%, var(--mu-size-column-medium)), 1fr)); }
    .empty { font: var(--mu-type-caption); color: var(--mu-color-text-muted); }
    @media (forced-colors: active) { .empty { color: CanvasText; } }
  `)];

  #controls;

  connectedCallback() {
    this.#controls?.render();
  }

  disconnectedCallback() {
    this.#controls?.dismiss();
  }

  refuse(errors) {
    this.#controls?.dismiss();
    this.#controls = null;
    super.refuse(errors);
  }

  attributeChangedCallback() {
    this.#controls?.render();
    this.rendered();
  }

  rendered() {
    for (const card of this.shadowRoot.querySelectorAll('mu-card')) card[UPDATE_CARD_DISPLAY]?.();
  }

  state(data) {
    return data.cards.length ? 'default' : 'empty';
  }

  // An empty grid shows only the empty text, and no empty list.
  render(data) {
    this.#controls?.dismiss();
    this.#controls = null;
    if (data.cards.length === 0) return [el('p', { class: 'empty', part: 'empty' }, 'No cards. Change the request, or try again later.')];
    const list = el('ul', { 'aria-label': data.label, part: 'list' });
    // A nested card takes the slots and the actor of the grid, so each card shows them.
    for (const item of data.cards) {
      const card = document.createElement('mu-card');
      card[CARD_MESSAGE_ACTOR] = this[CARD_MESSAGE_ACTOR];
      card.setAttribute('exportparts', 'title: card-title, status: card-status, toggle: card-toggle, tooltip: card-tooltip');
      card.actor = this.actor;
      card.fill = this.fill.slots ? { ...item, slots: this.fill.slots } : item;
      list.append(el('li', {}, card));
    }
    if (!data.collapse_controls) return [list];
    const controls = el('div', { class: 'collapse-controls', role: 'group', 'aria-label': `${data.label}: card controls` });
    const expand = el('button', { type: 'button', part: 'expand-all', 'aria-label': 'Expand all' });
    const collapse = el('button', { type: 'button', part: 'collapse-all', 'aria-label': 'Collapse all' });
    const setCollapsed = (value) => {
      for (const card of list.querySelectorAll('mu-card')) card.collapsed = value;
    };
    expand.addEventListener('click', () => setCollapsed(false));
    collapse.addEventListener('click', () => setCollapsed(true));
    controls.append(expand, collapse);
    const tooltip = el('span', { part: 'tooltip', 'aria-hidden': 'true' });
    this.#controls = compactControls(this, controls, tooltip, [
      { button: expand, label: 'Expand all', graphic: controlIcon('expand-all') },
      { button: collapse, label: 'Collapse all', graphic: controlIcon('collapse-all') },
    ]);
    return [controls, list];
  }
}

function fieldTag(name) {
  return registeredTag(name) ?? FIELDS[name]?.[0];
}

function fieldEnabled({ data, node }) {
  return !data.disabled && !node.disabled;
}

function sameFieldValue(first, second) {
  return first === second || sameRichTextValue(first, second) || (Array.isArray(first) && Array.isArray(second)
    && first.length === second.length && first.every((value, index) => value === second[index]));
}

export class MuEditableForm extends ActionElement {
  static component = 'editable-form';
  static labelField = 'submit_label';
  static styles = [CARD_CSS, ACTION_CSS, REFERENCE_CSS, ACTION_RESULT_CSS, sheet(`
    :host { display: block; }
    form { display: flex; flex-direction: column; gap: var(--mu-space-3); }
    .field-error { display: block; color: var(--mu-color-danger); overflow-wrap: anywhere; }
    @media (forced-colors: active) { .field-error { color: CanvasText; } }
  `)];

  #fields = [];
  #sent = null;
  #error = null;

  // A change of a value or of an edit type after the request makes the data new.
  stale() {
    const values = this.values();
    return Object.values(values).some((value) => !isActionValue(value)) || this.#snapshot(values) !== this.#sent;
  }

  #snapshot(values) {
    const types = this.#fields.filter(fieldEnabled).map(({ node }) => {
      const type = node.editType;
      return isEditType(type) ? type : null;
    });
    return JSON.stringify([values, types]);
  }

  // A form also needs a unique name for each field, so that the values have keys.
  check(data) {
    const errors = super.check(data);
    if (errors.length) return errors;
    // The application gives the title. The title names the target of the bound arguments, so a fill cannot
    // show one target while the request changes a different target.
    const title = this.action.title;
    if (title !== null && data.title !== title) return ['data.title: the title is not the title of the application'];
    // The application labels of the fields tell what the request does. A form with no field that a person can
    // edit sends no such value, because the form does not send a disabled field.
    if (!data.fields.some((item) => !item.data.disabled)) return ['data.fields: the form needs at least one field that a person can edit'];
    // A field can give a value or a range that do not agree. The form refuses the fill, as the field does.
    for (const [index, item] of data.fields.entries()) {
      if (CONTRACTS[item.component]?.field !== true) return [`data.fields[${index}].component: must be a field`];
      const tag = fieldTag(item.component);
      if (!tag) return [`data.fields[${index}].component: must be a field`];
      const element = FIELDS[item.component]?.[1] ?? globalThis.customElements?.get(tag);
      const errors = element?.rules?.(item.data);
      if (errors?.length) return [`data.fields[${index}].${errors[0]}`];
    }
    const names = data.fields.map((item) => item.data.name);
    if (!names.every((name) => typeof name === 'string' && FIELD_NAME.test(name))) return ['data.fields: each field needs a name'];
    if (new Set(names).size !== names.length) return ['data.fields: the field names must be unique'];
    // The application gives the names that a request can carry, and the label of each name.
    // A fill cannot add a different argument or show a different meaning for an argument.
    const editable = this.action.editable;
    if (!editable) return [];
    const other = names.find((name) => !Object.hasOwn(editable, name));
    if (other) return [`data.fields: the application does not accept the field ${other}`];
    const relabeled = data.fields.find((item) => item.data.label !== editable[item.data.name]);
    if (relabeled) return [`data.fields: the label of the field ${relabeled.data.name} is not the label of the application`];
    // The application uses the bound zone to read the local date and time. The fill must show the same zone.
    const timeZones = this.action.timeZones;
    const dateTimes = new Set();
    for (const { data: field } of data.fields) {
      if (!Object.hasOwn(field, 'time_zone')) continue;
      if (field.time_zone !== timeZones[field.name]) {
        return [`data.fields: the time zone of the field ${field.name} is not the time zone of the application`];
      }
      dateTimes.add(field.name);
    }
    const extraZone = Object.keys(timeZones).find((name) => !dateTimes.has(name));
    if (extraZone) return [`data.fields: the time zone of the application for ${extraZone} needs a date and time field`];
    // The application gives the typed names, so a fill cannot remove the type of an edit or add one.
    const typed = this.action.typed;
    const retyped = data.fields.find((item) => Boolean(item.data.typed) !== typed.includes(item.data.name));
    if (retyped) return [`data.fields: the edit type of the field ${retyped.data.name} is not the rule of the application`];
    return [];
  }

  // A person cannot edit a disabled field, so its value does not go in the request.
  values() {
    if (!this.fill) return {};
    return Object.fromEntries(this.#fields.filter(fieldEnabled).map(({ data, node }) => {
      const value = node.value;
      return [data.name, isActionValue(value) ? copyActionValue(value) : value];
    }));
  }

  #showError(message, node, method) {
    this.#error.textContent = '';
    if (method && typeof node[method] === 'function') node[method]();
    else {
      this.#error.textContent = message;
      node?.focus();
    }
  }

  #submit() {
    if (!this.fill || !['default', 'error'].includes(this.action.state)) return;
    const values = this.values();
    if (!Object.keys(values).length) {
      this.#showError('The form needs at least one field that a person can edit.');
      return;
    }
    const edits = this.action.typed.length ? [] : undefined;
    const sent = this.#fields.filter(({ data }) => Object.hasOwn(values, data.name));
    // Each value must be valid before a field asks for an edit type, so the first wrong value takes the focus.
    for (const { node, data } of sent) {
      const message = node.validationMessage;
      if (typeof message === 'string' && message) {
        this.#showError(message, node, 'reportValidity');
        return;
      }
      if (typeof message !== 'string' || !isActionValue(values[data.name])) {
        this.#showError(`${data.label}: the value is not valid for the action.`, node);
        return;
      }
    }
    for (const { node, data, initial, editedAt } of sent) {
      const value = values[data.name];
      if (!data.typed || sameFieldValue(initial, value)) continue;
      if (initial === undefined) {
        this.#showError(`${data.label}: the initial value is not valid for the action.`, node);
        return;
      }
      const type = node.editType;
      if (!isEditType(type)) {
        this.#showError('Choose an edit type.', node, 'needType');
        return;
      }
      edits.push({ field: data.name, type, old_value: initial, time: editedAt ?? new Date().toISOString() });
    }
    this.#error.textContent = '';
    const snapshot = this.#snapshot(values);
    this.send(values, () => { this.#sent = snapshot; }, edits);
  }

  render(data) {
    this.#sent = null;
    this.#fields = data.fields.map((item) => {
      const node = document.createElement(fieldTag(item.component));
      const declared = CONTRACTS[item.component].slots ?? {};
      const slots = Object.fromEntries(Object.entries(this.fill.slots ?? {}).filter(([key]) => Object.hasOwn(declared, key)));
      node.actor = this.actor;
      node.fill = Object.keys(slots).length ? { ...item, slots } : item;
      const field = { node, data: item.data, initial: undefined, editedAt: null };
      if (item.data.typed) {
        const value = node.value;
        field.initial = isActionValue(value) ? copyActionValue(value) : undefined;
        node.addEventListener('mu-edit', () => { field.editedAt = new Date().toISOString(); });
      }
      return field;
    });
    this.#error = el('span', { class: 'field-error', role: 'status', part: 'reason' });
    const status = el('span', { class: 'status', role: 'status', 'aria-live': 'polite', part: 'status' });
    const submit = el('button', { type: 'submit', part: 'button' }, data.submit_label);
    const form = el('form', { 'aria-label': data.title, part: 'form' },
      el('p', { class: 'title' }, data.title), provenance(this.fill.slots), ...this.#fields.map(({ node }) => node),
      el('div', {}, submit), ...this.attach(submit, status), this.#error);
    // An edit after done makes the values new, so the form can save again.
    form.addEventListener('mu-edit', () => {
      this.#error.textContent = '';
      this.reopen();
    });
    form.addEventListener('submit', (event) => {
      event.preventDefault();
      this.#submit();
    });
    // Each input is in the shadow root of its field, so the browser does not connect it to this form.
    // Thus Enter in a single-line text, number or date field submits the form here, as it does in a plain form.
    form.addEventListener('keydown', (event) => {
      if (event.key !== 'Enter' || event.isComposing || event.altKey || event.ctrlKey || event.metaKey || event.shiftKey) return;
      if (!this.#fields.some(({ node }) => node === event.target) || !event.target.constructor.submitOnEnter) return;
      event.preventDefault();
      form.requestSubmit(submit);
    });
    // A new fill has new values. A receipt from before the fill does not confirm them, so the form can save again.
    this.reopen();
    return [form];
  }

  rendered() {
    const index = this.#fields.findIndex(({ node }) => node.dataset.state === 'refused');
    if (index !== -1) this.refuse([`data.fields[${index}]: the field refused its fill`]);
  }
}

const NO_CHANGES = 'No changes to send.';
const OPEN_EDITOR = 'Save or cancel this field first.';

// A long form as labels and values (catalog/form-card.json). A person reads the form first. A click or Enter
// on a value opens the editor of that field. Save keeps the new value, and Cancel or Escape restores the old value.
// The card sends the changed fields as one action, and it shows done only after the receipt.
export class MuFormCard extends ActionElement {
  static component = 'form-card';
  static labelField = 'submit_label';
  static styles = [CARD_CSS, ACTION_CSS, REFERENCE_CSS, ACTION_RESULT_CSS, sheet(`
    :host { display: block; container-type: inline-size; }
    form { gap: var(--mu-space-3); }
    .section { display: flex; flex-direction: column; gap: var(--mu-space-2);
      border-top: 1px solid ${EDGE}; padding-top: var(--mu-space-3); }
    .section-title { margin: 0; font-weight: var(--mu-type-weight-strong); }
    .lines { display: flex; flex-direction: column; gap: var(--mu-space-3); }
    .line { display: grid; grid-template-columns: minmax(6rem, 1fr) 2fr; gap: var(--mu-space-1) var(--mu-space-3); align-items: start; }
    .line dt { font: var(--mu-type-body); color: var(--mu-color-text); padding-top: var(--mu-space-2); }
    .number { color: var(--mu-color-text-muted); margin-inline-end: var(--mu-space-1); }
    .line dd { display: flex; flex-direction: column; align-items: flex-start; gap: var(--mu-space-1); min-width: 0; }
    .line dd > span[part="value"] { padding-top: var(--mu-space-2); overflow-wrap: anywhere; white-space: pre-wrap; }
    button[part="value"] {
      background: transparent; color: var(--mu-color-text); border-color: var(--mu-color-border);
      text-align: start; width: 100%; overflow-wrap: anywhere; white-space: pre-wrap;
    }
    button.secondary { background: transparent; color: var(--mu-color-accent); }
    .changed { font: var(--mu-type-caption); font-weight: var(--mu-type-weight-strong); }
    .hint { font: var(--mu-type-caption); color: var(--mu-color-text-muted); }
    .error { font: var(--mu-type-caption); color: var(--mu-color-danger); }
    .editor { display: flex; flex-direction: column; gap: var(--mu-space-2); width: 100%; }
    .editor > * { width: 100%; }
    .editor .actions > * { width: auto; }
    @container (max-width: 28rem) { .line { grid-template-columns: 1fr; } .line dt { padding-top: 0; } }
    @media (forced-colors: active) {
      .section { border-color: CanvasText; } .line dt, .number, .hint, .error { color: CanvasText; }
      button[part="value"], button.secondary { background: ButtonFace; color: ButtonText; border-color: ButtonText; }
    }
  `)];

  // Each line by its field name: the field fill, the read-only flag, the row, the value area and the open editor.
  #lines = new Map();
  // The values that the application confirmed: the values of the fill, the computed values and the values of a done receipt.
  #base = Object.create(null);
  // The changes that a person saved and the card did not send yet, by field name: { value, type, time }.
  #changes = new Map();
  // The values of the pending request, and the changes when it started.
  #pending = null;
  #sent = null;
  #note = null;

  // A change after the request makes the data new. The card opens no editor while a request is pending.
  stale() {
    return this.#snapshot() !== this.#sent;
  }

  #snapshot() {
    return JSON.stringify([...this.#changes]);
  }

  // The rules of a form, for all lines of all sections. The application gives the title, the editable names,
  // their labels and the typed names. A read-only line shows a value that the application computes.
  check(data) {
    const errors = super.check(data);
    if (errors.length) return errors;
    const title = this.action.title;
    if (title !== null && data.title !== title) return ['data.title: the title is not the title of the application'];
    const lines = data.sections.flatMap((item, at) => item.data.lines.map((entry, place) => ({ line: entry.data,
      path: `data.sections[${at}].data.lines[${place}].data.field.` })));
    for (const { line, path } of lines) {
      if (Object.hasOwn(line.field.data, 'disabled')) return [path + 'data.disabled: a form card uses read_only'];
      const rules = FIELDS[line.field.component][1].rules(line.field.data);
      if (rules.length) return [path + rules[0]];
    }
    const names = lines.map(({ line }) => line.field.data.name);
    if (!names.every((name) => typeof name === 'string' && FIELD_NAME.test(name))) return ['data.sections: each field needs a name'];
    if (new Set(names).size !== names.length) return ['data.sections: the field names must be unique'];
    const open = lines.map(({ line }) => line).filter((line) => !line.read_only);
    if (!open.length) return ['data.sections: the card needs at least one field that a person can edit'];
    const computed = lines.find(({ line }) => line.read_only && line.field.data.typed);
    if (computed) return [`data.sections: the read-only field ${computed.line.field.data.name} cannot have an edit type`];
    // A render shows the values of the fill, so the bound rule checks them.
    const conflict = this.#unbound(lines.filter(({ line }) => line.read_only)
      .map(({ line }) => [line.field.data.name, line.field.data.value ?? '']));
    if (conflict) return [boundError(conflict)];
    const editable = this.action.editable;
    if (!editable) return [];
    const other = open.find((line) => !Object.hasOwn(editable, line.field.data.name));
    if (other) return [`data.sections: the application does not accept the field ${other.field.data.name}`];
    const relabeled = open.find((line) => line.field.data.label !== editable[line.field.data.name]);
    if (relabeled) return [`data.sections: the label of the field ${relabeled.field.data.name} is not the label of the application`];
    const typed = this.action.typed;
    const retyped = open.find((line) => Boolean(line.field.data.typed) !== typed.includes(line.field.data.name));
    if (retyped) return [`data.sections: the edit type of the field ${retyped.field.data.name} is not the rule of the application`];
    return [];
  }

  // The rule of the bound values: the request sends the bound arguments, so a read-only line with the name of a
  // bound argument shows that value. Each render, each computed value, each binding and each send uses this
  // function. It gives the name of the first line that shows a different value, or null.
  #unbound(shown) {
    const bound = this.action.arguments ?? {};
    const found = shown.find(([name, value]) => Object.hasOwn(bound, name) && value !== bound[name]);
    return found ? found[0] : null;
  }

  // The values that the read-only lines show now: the value of the fill or the newest computed value.
  #shown() {
    return [...this.#lines].filter(([, line]) => line.readOnly).map(([name]) => [name, this.#base[name]]);
  }

  // A new binding can give a bound value that a shown value does not agree with. Then the card refuses its fill.
  rebound() {
    const conflict = this.fill ? this.#unbound(this.#shown()) : null;
    if (conflict) this.refuse([boundError(conflict)]);
    else super.rebound();
  }

  // A receipt of a done request makes the sent values the confirmed values. Their changed markers go.
  receipt(value) {
    const sent = this.#pending;
    const accepted = super.receipt(value);
    if (!accepted) return false;
    this.#pending = null;
    if (this.action.state === 'done' && sent) {
      for (const [name, item] of Object.entries(sent)) {
        this.#base[name] = item;
        if (this.#changes.get(name)?.value === item) this.#changes.delete(name);
      }
    }
    for (const [name, line] of this.#lines) if (!line.editor) this.#show(name);
    return true;
  }

  // A canceled request confirms no value. The changes stay, so a person can send them again.
  canceled() {
    this.#pending = null;
    for (const [name, line] of this.#lines) if (!line.editor) this.#show(name);
  }

  // The application gives new computed values of read-only fields, as { name: value }. The card checks each
  // value against the contract of its field and shows the values only if all of them are valid. Returns the errors.
  computed(values) {
    if (!this.fill) return ['the card has no fill'];
    if (values === null || typeof values !== 'object' || Array.isArray(values)) return ['the values must be an object'];
    const errors = [];
    const next = [];
    for (const [name, value] of Object.entries(values)) {
      const line = this.#lines.get(name);
      if (!line) {
        errors.push(`${name}: the card has no field with this name`);
        continue;
      }
      if (!line.readOnly) {
        errors.push(`${name}: the field is not read-only`);
        continue;
      }
      if (this.#unbound([[name, value]])) {
        errors.push(`${name}: the value is not the bound value`);
        continue;
      }
      const data = { ...line.field.data, value };
      const problems = [...fillErrors({ ...line.field, data }, CONTRACTS), ...FIELDS[line.field.component][1].rules(data)];
      if (problems.length) errors.push(`${name}: ${problems[0]}`);
      else next.push([name, value]);
    }
    if (errors.length) return errors;
    for (const [name, value] of next) {
      this.#base[name] = value;
      this.#show(name);
    }
    return [];
  }

  // The card opens no editor while a request is pending or while a person decides on the confirm step.
  updated(state) {
    for (const line of this.#lines.values()) {
      if (!line.readOnly && line.value) line.value.setAttribute('aria-disabled', this.#busy(state) ? 'true' : 'false');
    }
  }

  #busy(state = this.dataset.state) {
    return state === 'pending' || state === 'confirm';
  }

  // The value of a line as text. An empty value shows an empty marker.
  #text(value) {
    return value === '' ? 'No value' : String(value);
  }

  // The read state of a line: the value, a changed marker as text, and the help text.
  #show(name, focus = false) {
    const line = this.#lines.get(name);
    line.editor = null;
    const change = this.#changes.get(name);
    const current = change ? change.value : this.#base[name];
    const text = this.#text(current);
    const shown = current === '' ? 'muted' : '';
    let value;
    const parts = [];
    if (line.readOnly) {
      value = el('span', { part: 'value', class: shown }, text);
      parts.push(value, el('span', { class: 'hint', part: 'read-only' }, 'Read-only'));
    } else {
      value = el('button', { type: 'button', part: 'value', class: shown,
        'aria-label': `Edit ${line.field.data.label}: ${text}${change ? ', changed' : ''}` }, text);
      value.setAttribute('aria-disabled', this.#busy() ? 'true' : 'false');
      value.addEventListener('click', () => this.#open(name));
      parts.push(value);
      if (change) parts.push(el('span', { class: 'changed', part: 'changed' }, 'Changed'));
    }
    if (line.field.data.hint) parts.push(el('span', { class: 'hint' }, line.field.data.hint));
    line.value = value;
    line.slot.replaceChildren(...parts);
    if (focus) value.focus();
  }

  // The editor of a line: the element of its field contract, with the current value, and Save and Cancel.
  #open(name) {
    const line = this.#lines.get(name);
    if (this.dataset.state === 'refused' || this.#busy() || line.readOnly || line.editor) return;
    const change = this.#changes.get(name);
    const current = change ? change.value : this.#base[name];
    const data = { ...line.field.data };
    delete data.value;
    const node = document.createElement(FIELDS[line.field.component][0]);
    node.fill = { ...line.field, data: current === '' ? data : { ...data, value: current } };
    const reason = el('span', { class: 'error', part: 'reason' });
    const save = el('button', { type: 'button', part: 'save' }, 'Save');
    const cancel = el('button', { type: 'button', part: 'cancel', class: 'secondary' }, 'Cancel');
    const editor = el('div', { role: 'group', 'aria-label': `Edit ${line.field.data.label}`, part: 'editor', class: 'editor' },
      node, el('div', { class: 'actions' }, save, cancel), reason);
    save.addEventListener('click', () => this.#save(name));
    cancel.addEventListener('click', () => this.#show(name, true));
    editor.addEventListener('keydown', (event) => {
      if (event.key === 'Escape') {
        event.preventDefault();
        event.stopPropagation();
        this.#show(name, true);
        return;
      }
      if (event.key !== 'Enter' || event.isComposing || event.altKey || event.ctrlKey || event.metaKey || event.shiftKey) return;
      if (event.target !== node || !node.constructor.submitOnEnter) return;
      event.preventDefault();
      this.#save(name);
    });
    line.editor = { node, reason };
    line.slot.replaceChildren(editor);
    node.focus();
  }

  // Save keeps a valid value. A typed field that changed needs an edit type first. The old value again is no change.
  #save(name) {
    const line = this.#lines.get(name);
    const { node } = line.editor;
    if (node.needValue()) return;
    const value = node.value ?? '';
    if (value === this.#base[name]) this.#changes.delete(name);
    else if (node.changed) {
      if (node.needType()) return;
      this.#changes.set(name, { value, type: node.editType, time: node.editedAt });
    }
    this.#note.replaceChildren();
    // An edit after a receipt makes the data new, so the card can send again.
    this.reopen();
    this.#show(name, true);
  }

  // The edits of the typed fields that changed, without the new value and the actor. The action path adds them.
  #edits() {
    return [...this.#changes].filter(([name]) => this.#lines.get(name).field.data.typed)
      .map(([name, change]) => ({ field: name, type: change.type, old_value: this.#base[name], time: change.time }));
  }

  render(data) {
    this.#lines = new Map();
    this.#base = Object.create(null);
    this.#changes = new Map();
    this.#pending = null;
    this.#sent = null;
    const sections = data.sections.map((item) => el('div', { role: 'group', 'aria-label': item.data.title, part: 'section', class: 'section' },
      el('p', { class: 'section-title' }, item.data.title),
      el('dl', { class: 'lines', part: 'lines' }, ...item.data.lines.map((entry) => this.#line(entry.data)))));
    const status = el('span', { class: 'status', role: 'status', 'aria-live': 'polite', part: 'status' });
    this.#note = el('span', { class: 'hint', part: 'note', 'aria-live': 'polite' });
    const submit = el('button', { type: 'submit', part: 'button' }, data.submit_label);
    const form = el('form', { 'aria-label': data.title, part: 'form' },
      el('p', { class: 'title' }, data.title), provenance(this.fill.slots), ...sections, el('div', {}, submit), this.#note, ...this.attach(submit, status));
    form.addEventListener('submit', (event) => {
      event.preventDefault();
      if (this.dataset.state === 'refused' || !this.fill) return;
      if (this.action.state !== 'default' && this.action.state !== 'error') return;
      // A value in an open editor is not saved. The first open editor tells why and takes the focus.
      const open = [...this.#lines.values()].find((line) => line.editor);
      if (open) {
        open.editor.reason.replaceChildren(OPEN_EDITOR);
        open.editor.node.focus();
        return;
      }
      if (!this.#changes.size) {
        this.#note.replaceChildren(NO_CHANGES);
        return;
      }
      // The card never sends while a read-only line shows a value that is not the bound value.
      const conflict = this.#unbound(this.#shown());
      if (conflict) {
        this.refuse([boundError(conflict)]);
        return;
      }
      const values = Object.fromEntries([...this.#changes].map(([name, change]) => [name, change.value]));
      const snapshot = this.#snapshot();
      const edits = this.action.typed?.length ? this.#edits() : undefined;
      // Keep the values only for an accepted request. A refused submit does not change them.
      this.send(values, () => {
        this.#sent = snapshot;
        this.#pending = values;
      }, edits);
    });
    // A new fill has new values. A receipt from before the fill does not confirm them, so the card can send again.
    this.reopen();
    return [form];
  }

  #line(data) {
    const name = data.field.data.name;
    this.#base[name] = data.field.data.value ?? '';
    const slot = el('dd', {});
    const row = el('div', { class: 'line', part: 'line' },
      el('dt', {}, data.line ? el('span', { class: 'number', part: 'line-number' }, data.line) : null,
        el('span', { part: 'label' }, data.field.data.label)),
      slot);
    row.dataset.name = name;
    this.#lines.set(name, { field: data.field, readOnly: Boolean(data.read_only), row, slot, value: null, editor: null });
    this.#show(name);
    return row;
  }
}

// The citation of a claim (catalog/citation.json). The source and the locator come from the citation slot, so the
// locator rule of the slot applies. The validator refuses a missing or bad locator, and the refusal tells why.
export class MuCitation extends MuElement {
  static component = 'citation';
  static styles = [sheet(`
    :host { display: block; }
    figure {
      margin: 0; color: var(--mu-color-text); font: var(--mu-type-body); padding: var(--mu-space-2) var(--mu-space-3);
      border-inline-start: 3px solid var(--mu-color-border); border-radius: var(--mu-radius-2);
      display: flex; flex-direction: column; gap: var(--mu-space-1);
    }
    blockquote { margin: 0; overflow-wrap: anywhere; }
    .muted { font: var(--mu-type-caption); color: var(--mu-color-text-muted); }
    .provenance { display: flex; flex-direction: column; gap: var(--mu-space-1); }
    .provenance p { margin: 0; overflow-wrap: anywhere; }
    .provenance button {
      align-self: start; font: var(--mu-type-caption); min-height: 2.75rem; padding: 0 var(--mu-space-2); cursor: pointer;
      border: 1px solid var(--mu-color-border); border-radius: var(--mu-radius-2); background: transparent; color: var(--mu-color-text);
    }
    @media (forced-colors: active) {
      figure { border-color: CanvasText; } .muted { color: CanvasText; }
      .provenance button { color: ButtonText; border-color: ButtonText; }
    }
  `)];

  state() {
    return this.fill.slots?.citation ? 'default' : 'empty';
  }

  refusal(errors) {
    if (errors[0] === 'slots.citation: missing key locator') return 'The source address is missing.';
    if (errors[0].startsWith('slots.citation.locator:')) return 'The source address is invalid.';
    return super.refusal();
  }

  render(data) {
    const quote = data.quote ? el('blockquote', { part: 'quote' }, data.quote) : null;
    // The authorship slot is required, so the author always shows. With no citation slot, the text tells it.
    const empty = this.fill.slots.citation ? null : el('p', { class: 'muted', part: 'empty' }, 'No source given. Ask for the source before you use this text.');
    return [el('figure', { role: 'group', 'aria-label': 'Citation', part: 'citation' }, quote, empty, provenance(this.fill.slots))];
  }
}

// A passage of a source document with a marked span (catalog/excerpt.json). The passage is document text, so it
// shows only as text nodes. The span starts at span_start and stops before span_end. Both count code points, the
// same as each text limit of the kit. The source and the locator come from the required citation slot.
export class MuExcerpt extends ReadMoreElement {
  static component = 'excerpt';
  static styles = [READ_MORE_CSS, sheet(`
    :host { display: block; }
    figure {
      margin: 0; color: var(--mu-color-text); font: var(--mu-type-body); padding: var(--mu-space-2) var(--mu-space-3);
      border-inline-start: 3px solid var(--mu-color-border); border-radius: var(--mu-radius-2);
      display: flex; flex-direction: column; gap: var(--mu-space-1);
    }
    blockquote { margin: 0; overflow-wrap: anywhere; white-space: pre-wrap; }
    mark {
      background: transparent; color: inherit; font-weight: var(--mu-type-weight-strong);
      text-decoration: underline 2px var(--mu-color-accent); text-underline-offset: 0.2em;
    }
    .muted { font: var(--mu-type-caption); color: var(--mu-color-text-muted); }
    .span { margin: 0; overflow-wrap: anywhere; }
    .provenance { display: flex; flex-direction: column; gap: var(--mu-space-1); }
    .provenance p { margin: 0; overflow-wrap: anywhere; }
    .provenance button {
      align-self: start; font: var(--mu-type-caption); min-height: 2.75rem; padding: 0 var(--mu-space-2); cursor: pointer;
      border: 1px solid var(--mu-color-border); border-radius: var(--mu-radius-2); background: transparent; color: var(--mu-color-text);
    }
    @media (forced-colors: active) {
      figure { border-color: CanvasText; } .muted { color: CanvasText; }
      mark { background: Mark; color: MarkText; text-decoration-color: MarkText; forced-color-adjust: none; }
      .provenance button { color: ButtonText; border-color: ButtonText; }
    }
  `)];

  // The contract format cannot compare two fields, so the element checks the span against the passage.
  check(data) {
    if (data.span_start >= data.span_end) return ['data.span_start: must be less than span_end'];
    if (data.span_end > [...data.passage].length) return ['data.span_end: outside the passage'];
    return [];
  }

  refusal(errors) {
    const [error] = errors;
    if (error === 'data.span_end: outside the passage') return 'The marked text is outside the passage.';
    if (error === 'data.span_start: must be less than span_end') return 'The marked text is empty.';
    if (error === 'slots.citation: a required slot is missing') return 'The source and its address are missing.';
    if (error === 'slots.citation: missing key locator') return 'The source address is missing.';
    if (error.startsWith('slots.citation.locator:')) return 'The source address is invalid.';
    return super.refusal();
  }

  render(data) {
    const points = [...data.passage];
    const span = points.slice(data.span_start, data.span_end).join('');
    const passage = el('blockquote', { part: 'passage' }, points.slice(0, data.span_start).join(''),
      el('mark', { part: 'mark' }, span), points.slice(data.span_end).join(''));
    // The span shows again as text, so that the mark does not depend on its style only.
    const marked = el('p', { class: 'span muted', part: 'span' }, `Marked text: ${span}`);
    return [el('figure', { role: 'group', 'aria-label': 'Source excerpt', part: 'excerpt' },
      ...this.readMore(passage, data.clamp), marked, provenance(this.fill.slots))];
  }
}

// The words of each state of an offline data source. The state shows as text, so it does not depend on color.
const SOURCE_STATES = Object.freeze({ ready: 'Ready', stale: 'Stale', empty: 'Empty', error: 'Error' });

// The state of an offline data source of the application (docs/contracts/data-source.md). The application fills
// it from a result of read in src/offline/source.js. The thread does not show it.
export class MuSourceStatus extends MuElement {
  static component = 'source-status';
  static styles = [sheet(`
    :host { display: block; }
    .status {
      color: var(--mu-color-text); font: var(--mu-type-body); padding: var(--mu-space-1) var(--mu-space-2);
      border: 1px solid var(--mu-color-border); border-radius: var(--mu-radius-2);
      display: flex; flex-wrap: wrap; align-items: baseline; gap: var(--mu-space-1) var(--mu-space-2);
    }
    .status[data-state="error"] { border-color: var(--mu-color-danger); }
    p { margin: 0; overflow-wrap: anywhere; }
    .state { font-weight: var(--mu-type-weight-strong); }
    .muted { font: var(--mu-type-caption); color: var(--mu-color-text-muted); }
    @media (forced-colors: active) { .status, .status[data-state="error"] { border-color: CanvasText; } .muted { color: CanvasText; } }
  `)];

  // The contract format cannot make one field depend on another, so the element checks the time and the reason.
  check(data) {
    if ((data.state === 'ready' || data.state === 'stale') && !data.checked) return [`data.checked: a ${data.state} source needs the time of its last read`];
    if (data.state === 'error' && !data.reason) return ['data.reason: a source with an error needs a reason'];
    return [];
  }

  state(data) {
    return { ready: 'default', stale: 'default', empty: 'empty', error: 'error' }[data.state];
  }

  render(data) {
    const time = data.checked ? el('p', { class: 'muted', part: 'checked' }, `${data.state === 'stale' ? 'Last checked' : 'Checked'}: ${data.checked}`) : null;
    const empty = data.state === 'empty' ? el('p', { part: 'empty' }, 'No records. Add a record, or try again later.') : null;
    const reason = data.reason ? el('p', { part: 'reason' }, data.reason) : null;
    return [el('div', { class: 'status', role: 'status', 'aria-label': data.label, part: 'source', 'data-state': data.state },
      el('p', { class: 'muted', part: 'label' }, data.label), el('p', { class: 'state', part: 'state' }, SOURCE_STATES[data.state]),
      empty, reason, time)];
  }
}

// The words of each state of an action in the offline queue. No state shows as done.
const QUEUE_STATES = Object.freeze({ queued: 'Pending', sent: 'Pending', stalled: 'Pending', rejected: 'Not done', unreadable: 'Unreadable' });
const QUEUE_NOTES = Object.freeze({ queued: 'Waiting for a connection.', sent: '', stalled: 'No receipt came back.', rejected: 'The service rejected the action.' });
// The actions of the offline queue of the application (docs/contracts/offline-queue.md). The application fills it
// with queueFill in src/offline/queue.js after each change of its queue. The thread does not show it. A button
// sends a mu-queue-step event, and the queue does the step.
export class MuActionQueue extends MuElement {
  static component = 'action-queue';
  // The steps that a person can do for each state, with the word of each button.
  static #steps = Object.freeze({
    queued: [[{ part: 'cancel' }, 'Cancel']],
    sent: [],
    stalled: [[{ part: 'retry' }, 'Retry'], [{ part: 'cancel' }, 'Cancel']],
    rejected: [[{ part: 'dismiss' }, 'Dismiss']],
    unreadable: [[{ part: 'dismiss' }, 'Dismiss']],
  });
  static styles = [sheet(`
    :host { display: block; }
    .queue {
      color: var(--mu-color-text); font: var(--mu-type-body); padding: var(--mu-space-2) var(--mu-space-3);
      border: 1px solid var(--mu-color-border); border-radius: var(--mu-radius-2);
      display: flex; flex-direction: column; gap: var(--mu-space-2);
    }
    p, ul { margin: 0; padding: 0; }
    ul { list-style: none; display: flex; flex-direction: column; gap: var(--mu-space-2); }
    li { display: flex; flex-wrap: wrap; align-items: center; gap: var(--mu-space-1) var(--mu-space-2); overflow-wrap: anywhere; }
    .name { font-weight: var(--mu-type-weight-strong); }
    .muted { font: var(--mu-type-caption); color: var(--mu-color-text-muted); }
    li:is([data-state="rejected"], [data-state="unreadable"]) .state { color: var(--mu-color-danger); }
    button {
      font: var(--mu-type-body); min-height: 2.75rem; min-width: 2.75rem; padding: var(--mu-space-1) var(--mu-space-3);
      border-radius: var(--mu-radius-2); border: 1px solid var(--mu-color-accent); background: transparent;
      color: var(--mu-color-accent); cursor: pointer;
    }
    @media (forced-colors: active) {
      .queue { border-color: CanvasText; }
      .muted, li:is([data-state="rejected"], [data-state="unreadable"]) .state { color: CanvasText; }
      button { background: ButtonFace; color: ButtonText; border-color: ButtonText; forced-color-adjust: none; }
    }
  `)];

  // The step that had the focus before a new fill, as [key, step].
  #focused = null;

  // Each action has a different key, so a step names one action.
  check(data) {
    if (data.recovery?.some((item) => item.data.state !== 'unreadable')) {
      return ['data.recovery: each entry must be unreadable'];
    }
    const keys = [...data.actions, ...(data.recovery ?? [])].map((item) => item.data.key);
    return new Set(keys).size === keys.length ? [] : ['data.actions: two actions have the same key'];
  }

  state(data) {
    const states = [...data.actions, ...(data.recovery ?? [])].map((item) => item.data.state);
    if (states.includes('rejected') || states.includes('unreadable')) return 'error';
    return states.length ? 'pending' : 'empty';
  }

  render(data) {
    // The nodes of the fill before this one are still in the shadow root.
    const active = this.shadowRoot.activeElement;
    this.#focused = active?.dataset.step ? [active.dataset.key, active.dataset.step] : null;
    const entries = [...data.actions, ...(data.recovery ?? [])];
    const rejected = entries.filter((item) => item.data.state === 'rejected').length;
    const unreadable = entries.filter((item) => item.data.state === 'unreadable').length;
    const waiting = entries.length - rejected - unreadable;
    const summary = entries.length === 0 ? 'No pending actions.'
      : [waiting ? `${waiting} pending` : '', rejected ? `${rejected} not done` : '',
        unreadable ? `${unreadable} unreadable` : ''].filter(Boolean).join(', ') + '.';
    const items = entries.map(({ data: action }) => {
      const note = action.message ?? QUEUE_NOTES[action.state];
      const buttons = MuActionQueue.#steps[action.state].map(([attributes, word]) => {
        const step = attributes.part;
        const button = el('button', { type: 'button', ...attributes, 'aria-label': `${word} ${action.label}` }, word);
        button.dataset.key = action.key;
        button.dataset.step = step;
        button.addEventListener('click', () => this.dispatchEvent(new CustomEvent('mu-queue-step',
          { bubbles: true, composed: true, detail: Object.freeze({ key: action.key, step }) })));
        return button;
      });
      return el('li', { part: 'action', 'data-state': action.state },
        el('span', { class: 'name', part: 'name' }, action.label),
        el('span', { class: 'state', part: 'state' }, [QUEUE_STATES[action.state], note].filter(Boolean).join(': ')),
        action.queued ? el('span', { class: 'muted', part: 'queued' }, `Queued: ${action.queued}`) : null, ...buttons);
    });
    return [el('div', { class: 'queue', role: 'group', 'aria-label': data.label, part: 'queue' },
      el('p', { class: 'muted', part: 'label' }, data.label),
      el('p', { part: 'summary', role: 'status', tabindex: '-1' }, summary),
      items.length ? el('ul', { part: 'actions', 'aria-label': 'Actions' }, ...items) : null)];
  }

  // A new fill makes new nodes. The focus goes back to the same step, or to the summary if the step is not there now.
  rendered() {
    const focused = this.#focused;
    this.#focused = null;
    if (!focused || !this.fill) return;
    const same = [...this.shadowRoot.querySelectorAll('button')].find((button) => button.dataset.key === focused[0] && button.dataset.step === focused[1]);
    (same ?? this.shadowRoot.querySelector('[part="summary"]'))?.focus();
  }
}

// The words of each kind of change. The kind shows as text, so it does not depend on color.
const KINDS = Object.freeze({ added: 'Added', removed: 'Removed', changed: 'Changed', unchanged: 'Unchanged' });
// A diff with more changes than this starts with each part closed.
const OPEN_LIMIT = 20;

function counts(changes) {
  const found = changes.map((item) => item.data.change);
  const words = Object.keys(KINDS).map((kind) => [kind, found.filter((value) => value === kind).length])
    .filter(([, count]) => count > 0).map(([kind, count]) => `${count} ${kind}`);
  return words.length ? words.join(', ') : 'no changes';
}

export class MuDiff extends MuElement {
  static component = 'diff';
  static styles = [sheet(`
    :host { display: block; }
    .diff {
      color: var(--mu-color-text); font: var(--mu-type-body); padding: var(--mu-space-2) var(--mu-space-3);
      border: 1px solid ${EDGE}; border-radius: var(--mu-radius-2);
      display: flex; flex-direction: column; gap: var(--mu-space-2);
    }
    p { margin: 0; overflow-wrap: anywhere; }
    .title { font-weight: var(--mu-type-weight-strong); }
    .muted { font: var(--mu-type-caption); color: var(--mu-color-text-muted); }
    details[data-state] { border-block-start: 1px solid ${EDGE}; padding-block-start: var(--mu-space-2); }
    summary[part="summary"] { cursor: pointer; min-height: 2.75rem; display: flex; flex-wrap: wrap; align-items: center; gap: var(--mu-space-1) var(--mu-space-2); }
    summary[part="summary"] .name { font-weight: var(--mu-type-weight-strong); }
    ul { list-style: none; margin: 0; padding: 0; display: flex; flex-direction: column; gap: var(--mu-space-2); }
    li { display: flex; flex-direction: column; gap: var(--mu-space-1); padding-inline-start: var(--mu-space-2);
      border-inline-start: 3px solid var(--mu-color-border); }
    li[data-state="added"] { border-inline-start-color: var(--mu-color-accent); }
    li[data-state="removed"] { border-inline-start-color: var(--mu-color-danger); }
    li[data-state="changed"] { border-inline-start-style: double; }
    .kind { font: var(--mu-type-caption); font-weight: var(--mu-type-weight-strong); }
    .head { display: flex; flex-wrap: wrap; gap: var(--mu-space-1) var(--mu-space-2); align-items: baseline; }
    .value { white-space: pre-wrap; }
    del { text-decoration: line-through; }
    ins { text-decoration: underline; }
    .provenance { display: flex; flex-direction: column; gap: var(--mu-space-1); }
    @media (forced-colors: active) {
      .diff, details[data-state], li, li[data-state] { border-color: CanvasText; } .muted { color: CanvasText; }
    }
  `)];

  // The contract format cannot compare two fields, so the element checks the values of each change.
  check(data) {
    for (const [p, part] of data.parts.entries()) {
      for (const [c, item] of part.data.changes.entries()) {
        const { change, before, after } = item.data;
        const at = `data.parts[${p}].data.changes[${c}]`;
        const has = [before !== undefined, after !== undefined];
        if (change === 'added' && (has[0] || !has[1])) return [`${at}: added needs after and no before`];
        if (change === 'removed' && (!has[0] || has[1])) return [`${at}: removed needs before and no after`];
        if (change === 'changed' && !(has[0] && has[1] && before !== after)) return [`${at}: changed needs a before and a different after`];
        if (change === 'unchanged' && !(has[0] && has[1] && before === after)) return [`${at}: unchanged needs the same before and after`];
      }
    }
    return [];
  }

  state(data) {
    return data.parts.some((part) => part.data.changes.length) ? 'default' : 'empty';
  }

  render(data) {
    const before = data.before_label ?? 'Before';
    const after = data.after_label ?? 'After';
    const all = data.parts.flatMap((part) => part.data.changes);
    const head = [el('p', { class: 'title', part: 'title' }, data.title),
      el('p', { class: 'muted', part: 'versions' }, `${before} to ${after}`)];
    if (!all.length) {
      head.push(el('p', { class: 'muted', part: 'empty' }, DIFF_EMPTY));
      return [el('div', { class: 'diff', role: 'group', 'aria-label': data.title, part: 'diff' }, ...head, provenance(this.fill.slots))];
    }
    head.push(el('p', { class: 'muted', part: 'counts' }, `In all: ${counts(all)}.`));
    const parts = data.parts.map((part) => this.#part(part.data, before, after, all.length <= OPEN_LIMIT));
    return [el('div', { class: 'diff', role: 'group', 'aria-label': data.title, part: 'diff' }, ...head, ...parts, provenance(this.fill.slots))];
  }

  // A part is a details element, so Enter and Space on its summary open and close it.
  #part(data, before, after, open) {
    const summary = el('summary', { part: 'summary' }, el('span', { class: 'name' }, data.title),
      el('span', { class: 'muted', part: 'counts' }, counts(data.changes)));
    const node = el('details', { part: 'part', 'data-state': data.changes.length ? 'default' : 'empty' }, summary);
    // A part with no difference starts closed.
    node.open = open && data.changes.some((item) => item.data.change !== 'unchanged');
    if (!data.changes.length) {
      node.append(el('p', { class: 'muted', part: 'empty' }, DIFF_EMPTY_PART));
      return node;
    }
    const list = el('ul', { part: 'changes' });
    for (const { data: item } of data.changes) list.append(this.#change(item, before, after));
    node.append(list);
    return node;
  }

  #change(item, before, after) {
    const shown = (value) => (value === '' ? el('span', { class: 'muted' }, 'No value') : value);
    const row = el('li', { part: 'change', 'data-state': item.change },
      el('p', { class: 'head' }, el('span', { class: 'kind', part: 'kind' }, KINDS[item.change]),
        el('span', { part: 'label' }, item.label)));
    if (item.change === 'unchanged') {
      row.append(el('p', { class: 'value', part: 'value' }, shown(item.before)));
      return row;
    }
    if (item.before !== undefined) {
      row.append(el('p', { class: 'value' }, el('span', { class: 'muted', part: 'version' }, `${before}: `),
        el('del', { part: 'before' }, shown(item.before))));
    }
    if (item.after !== undefined) {
      row.append(el('p', { class: 'value' }, el('span', { class: 'muted', part: 'version' }, `${after}: `),
        el('ins', { part: 'after' }, shown(item.after))));
    }
    return row;
  }
}

export function define(registry = customElements) {
  definePrimitives(registry);
  for (const [name, element] of [['mu-action-button', MuActionButton], ['mu-card', MuCard],
    ['mu-card-grid', MuCardGrid], ['mu-editable-form', MuEditableForm], ['mu-form-card', MuFormCard], ['mu-citation', MuCitation], ['mu-diff', MuDiff],
    ['mu-dialog', MuDialog], ['mu-drawer', MuDrawer],
    ['mu-menu', MuMenu], ['mu-tooltip', MuTooltip], ['mu-toast', MuToast],
    ['mu-tabs', MuTabs], ['mu-accordion', MuAccordion],
    ['mu-app-shell', MuAppShell], ['mu-breadcrumbs', MuBreadcrumbs], ['mu-pagination', MuPagination],
    ['mu-steps', MuSteps], ['mu-progress', MuProgress],
    ['mu-run-steps', MuRunSteps], ['mu-plan', MuPlan], ['mu-approval-queue', MuApprovalQueue],
    ['mu-empty-state', MuEmptyState], ['mu-skeleton', MuSkeleton], ['mu-badge', MuBadge], ['mu-avatar', MuAvatar],
    ['mu-excerpt', MuExcerpt], ['mu-table', MuTable], ['mu-status', MuStatus], ['mu-source-status', MuSourceStatus], ['mu-action-queue', MuActionQueue], ['mu-image', MuImage], ['mu-chart', MuChart],
    ['mu-rich-text', MuRichText], ['mu-data-grid', MuDataGrid], ['mu-upload', MuUpload]]) {
    if (!registry.get(name)) registry.define(name, element);
  }
}

// An application builds its own element on MuElement and registers it with its contract.
export { MuElement } from '../primitives/base.js';
export { register } from '../render/registry.js';
