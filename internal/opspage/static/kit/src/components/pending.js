// The time that a try waits for its receipt before the element offers a retry and a cancel.
export const PATIENCE = 30000;

export function patienceOf(value) {
  if (!(Number.isFinite(value) && value >= 1 && value <= 3600000)) throw new TypeError('patience: a number of milliseconds from 1 to 3600000');
  return value;
}

export class ReceiptTimer {
  #id = null;

  clear() {
    clearTimeout(this.#id);
    this.#id = null;
  }

  // The timer runs only while a try is in flight. An update of the same wait does not restart it.
  watch(wait, patience, stalled) {
    if (wait !== 'sent') this.clear();
    else if (this.#id === null) {
      this.#id = setTimeout(() => {
        this.#id = null;
        stalled();
      }, patience);
    }
  }
}
