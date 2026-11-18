export const RATES: Record<string, number> = { EUR: 1, CHF: 0.95 };
export function rate(c: string) {
  return RATES[c];
}
