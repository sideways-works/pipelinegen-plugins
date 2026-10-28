import { format } from "./money";
import "./cart.css";

export interface Line {
  name: string;
  cents: number;
}

export function total(lines: Line[]): number {
  return lines.reduce((sum, l) => sum + l.cents, 0);
}

export function Cart({ lines }: { lines: Line[] }) {
  return <p>{format(total(lines))}</p>;
}
