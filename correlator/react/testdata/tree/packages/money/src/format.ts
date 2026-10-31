import { RATES } from "./rates";

export function format(cents: number, currency: string): string {
  return `${((cents / 100) * (RATES[currency] ?? 1)).toFixed(2)} ${currency}`;
}
