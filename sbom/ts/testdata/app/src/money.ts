export const CURRENCY = "EUR";

export function format(cents: number): string {
  return `${(cents / 100).toFixed(2)} ${CURRENCY}`;
}
