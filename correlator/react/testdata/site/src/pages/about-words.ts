// Imports and declarations only, reached through the about page's dynamic
// import.
import { PRICES } from "../cart";

export function aboutWords(): string {
  return PRICES.tea > 0 ? "About us" : "";
}
