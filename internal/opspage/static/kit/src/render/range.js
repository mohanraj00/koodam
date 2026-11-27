// A pair is valid only when both of its entries are finite numbers.
export function isNumberPair(value) {
  return Array.isArray(value) && value.length === 2 && Object.hasOwn(value, 0) && Object.hasOwn(value, 1)
    && Number.isFinite(value[0]) && Number.isFinite(value[1]);
}

export function isOrderedPair(value) {
  return isNumberPair(value) && value[0] <= value[1];
}

export function copyPair(value) {
  return Object.freeze([value[0], value[1]]);
}

export function sameValue(first, second) {
  return first === second || (isNumberPair(first) && isNumberPair(second)
    && first[0] === second[0] && first[1] === second[1]);
}

function decimal(value) {
  const [digits, power = '0'] = String(value).split('e');
  const point = digits.indexOf('.');
  const exponent = Number(power) - (point < 0 ? 0 : digits.length - point - 1);
  return { units: BigInt(digits.replace('.', '')), exponent };
}

// Use integers at the same decimal place. Binary remainders can reject a valid step such as 0.3 with step 0.1.
function align(values) {
  const exponent = Math.min(...values.map((value) => value.exponent));
  const units = values.map((value) => value.units * 10n ** BigInt(value.exponent - exponent));
  return { units, exponent };
}

export function stepGrid({ minimum, maximum, step }) {
  const start = decimal(minimum);
  const stride = decimal(step);
  const { units: [first, last, size], exponent } = align([start, decimal(maximum), stride]);
  const count = (last - first) / size;
  const at = (index) => Number(`${first + index * size}e${exponent}`);
  function distance(value) {
    const { units: [point, origin, stepSize] } = align([decimal(value), start, stride]);
    return [point - origin, stepSize];
  }
  return {
    last: at(count),
    onStep(value) {
      const [offset, stepSize] = distance(value);
      return offset % stepSize === 0n;
    },
    move(value, steps) {
      const [offset, stepSize] = distance(value);
      // Round a position between two steps up if it is halfway or more.
      const nearest = (2n * offset + stepSize) / (2n * stepSize);
      const index = nearest + steps;
      return at(index < 0n ? 0n : index > count ? count : index);
    },
  };
}

export function rangeEditProblem(value, { minimum, maximum, step }, grid, [min, max]) {
  if (!Number.isFinite(value)) return 'Give a number.';
  if (value < minimum || value > maximum) return `The number is outside the range ${minimum} to ${maximum}.`;
  if (!grid.onStep(value)) return `Step size is ${step}. Start at ${minimum}.`;
  if (value < min || value > max) return 'The low value must be at most the high value.';
  return null;
}

// Call this after the fill has passed the checks for required fields and their types.
export function rangeErrors(data, prefix) {
  const { minimum, maximum, step, value } = data;
  if (maximum < minimum) return [prefix + 'maximum: must be at least the minimum'];
  if (step <= 0) return [prefix + 'step: must be more than zero'];
  const grid = stepGrid(data);
  const pair = Array.isArray(value);
  const values = pair ? value : [value];
  for (const [index, number] of values.entries()) {
    const path = prefix + (pair ? `value[${index}]` : 'value');
    if (number < minimum || number > maximum) return [path + ': the number is outside the range'];
    if (!grid.onStep(number)) return [path + ': the number is off a step'];
  }
  return pair && value[0] > value[1] ? [prefix + 'value: the low value is more than the high value'] : [];
}
