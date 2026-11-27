// The base class of each primitive. It validates a fill against the contract before it renders.
import { actorErrors, authorErrors, fillErrors } from '../render/validate.js';
import { BRAND, CONTRACTS } from '../render/registry.js';
import { BASE, REFUSED_NEXT, REFUSED_REASON, el, refusalDetails } from '../render/dom.js';

export class MuElement extends HTMLElement {
  // register (src/render/registry.js) reads this key to accept only a subclass of MuElement.
  static [BRAND] = true;
  static component = '';
  static styles = [];
  static focus = false;

  #fill = null;
  #actor = null;
  // The fill that a script gave before the upgrade, until the element replays it.
  #early = null;

  constructor() {
    super();
    const root = this.attachShadow({ mode: 'open', delegatesFocus: this.constructor.focus });
    root.adoptedStyleSheets = [BASE, ...this.constructor.styles];
    // Each instance gets fill and actor as own accessors that it cannot change. Thus a class field of a
    // subclass, for example fill = null, cannot replace the validation: its definition throws a TypeError.
    const early = {};
    for (const key of ['actor', 'fill']) {
      if (Object.hasOwn(this, key)) early[key] = this[key];
      const { get, set } = Object.getOwnPropertyDescriptor(MuElement.prototype, key);
      Object.defineProperty(this, key, { get, set, enumerable: false, configurable: false });
    }
    // An actor that a script gave before the upgrade goes through the setter now. With no fill, the setter
    // calls no method of the subclass. An early fill goes through the setter after the constructor of the
    // subclass completes, because check and render can read its fields. A newer fill replaces it.
    if (Object.hasOwn(early, 'actor')) this.actor = early.actor;
    if (!Object.hasOwn(early, 'fill')) return;
    this.#early = { fill: early.fill };
    queueMicrotask(() => {
      const values = this.#early;
      this.#early = null;
      if (values && Object.hasOwn(values, 'fill')) this.fill = values.fill;
    });
  }

  get fill() {
    return this.#fill;
  }

  // The application or the thread gives the actor of the content, for example the actor of the envelope.
  // A fill cannot set it. The element refuses a fill whose authorship slot names a different actor.
  get actor() {
    return this.#actor;
  }

  set actor(value) {
    const errors = actorErrors(value);
    if (errors.length) throw new TypeError('actor: ' + errors.join('; '));
    this.#actor = value === null ? null
      : Object.freeze({ type: value.type, id: value.id, ...(Object.hasOwn(value, 'name') ? { name: value.name } : {}) });
    if (this.#fill) this.fill = this.#fill;
  }

  // A fill is data only. If it has an error, the element shows a refusal as text and keeps no data.
  set fill(value) {
    if (this.#early) delete this.#early.fill;
    const name = this.constructor.component;
    // A fill of a different component is refused for that reason first.
    const other = value !== null && typeof value === 'object' && !Array.isArray(value) && value.component !== name;
    const errors = other ? [`component: must be ${name}`] : fillErrors(value, CONTRACTS);
    if (errors.length === 0) errors.push(...authorErrors(value, this.#actor), ...this.check(value.data));
    if (errors.length) {
      this.refuse(errors);
      return;
    }
    this.#fill = structuredClone(value);
    this.dataset.state = this.state(this.#fill.data);
    // A render can give null for an optional part. Such a part shows nothing.
    // A render can give null instead of a list when it updates the existing DOM.
    const parts = this.render(this.#fill.data);
    if (parts !== null) this.shadowRoot.replaceChildren(...parts.filter((part) => part !== null && part !== undefined));
    this.rendered();
  }

  // The element keeps no data and shows the refusal as text. A subclass also calls it when its shown data stops
  // agreeing with a rule, for example after a new binding.
  refuse(errors) {
    this.#fill = null;
    this.dataset.state = 'refused';
    // The status gives the reason, then what the person does next.
    this.shadowRoot.replaceChildren(el('div', { class: 'refused' },
      el('div', { role: 'status' }, el('span', { part: 'reason' }, this.refusal(errors)), ' ',
        el('span', { part: 'next' }, REFUSED_NEXT)),
      refusalDetails(this.constructor.component, errors)));
    this.dispatchEvent(new CustomEvent('mu-refused', { bubbles: true, composed: true, detail: { errors } }));
  }

  // A subclass can give a specific reason in plain words.
  refusal() {
    return REFUSED_REASON;
  }

  // A subclass can refuse a fill for a rule that the contract format cannot give.
  check() {
    return [];
  }

  state() {
    return 'default';
  }

  render() {
    return [];
  }

  // A component can update its controls and send state events after its new nodes are in the DOM.
  rendered() {}
}
