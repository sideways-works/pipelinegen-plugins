// A module of imports and declarations only: loading it runs nothing of its
// own, but its init (the modules it loads) is evaluated with the page.
import { PRICES } from "./cart";

export function fmt(n: number): string {
  return `#${n}/${PRICES.tea}`;
}
